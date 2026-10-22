package evaluation_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"sync"
	"testing"
	"time"

	model_core "bonanza.build/pkg/model/core"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_evaluation "bonanza.build/pkg/model/evaluation"
	model_parser "bonanza.build/pkg/model/parser"
	model_evaluation_pb "bonanza.build/pkg/proto/model/evaluation"
	model_evaluation_cache_pb "bonanza.build/pkg/proto/model/evaluation/cache"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/clock"
	"github.com/buildbarn/bb-storage/pkg/program"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// cacheTestStorage is a simple in-memory implementation of an object
// store and tag store. It is used to test that results uploaded into
// the cache by one build can be used by subsequent builds.
type cacheTestStorage struct {
	lock    sync.Mutex
	objects map[object.LocalReference]*object.Contents
	tags    map[[sha256.Size]byte]object.LocalReference
}

func newCacheTestStorage() *cacheTestStorage {
	return &cacheTestStorage{
		objects: map[object.LocalReference]*object.Contents{},
		tags:    map[[sha256.Size]byte]object.LocalReference{},
	}
}

type cacheTestMetadata struct{}

func (cacheTestMetadata) Discard() {}

func (s *cacheTestStorage) CaptureCreatedObject(ctx context.Context, createdObject model_core.CreatedObject[model_core.ReferenceMetadata]) (model_core.ReferenceMetadata, error) {
	s.lock.Lock()
	s.objects[createdObject.Contents.GetLocalReference()] = createdObject.Contents
	s.lock.Unlock()
	return cacheTestMetadata{}, nil
}

func (cacheTestStorage) CaptureExistingObject(reference object.LocalReference) model_core.ReferenceMetadata {
	return cacheTestMetadata{}
}

func (cacheTestStorage) ReferenceObject(capturedObject model_core.MetadataEntry[model_core.ReferenceMetadata]) object.LocalReference {
	return capturedObject.LocalReference
}

func (s *cacheTestStorage) ReadObject(ctx context.Context, reference object.LocalReference) (model_core.Message[[]byte, object.LocalReference], error) {
	s.lock.Lock()
	contents, ok := s.objects[reference]
	s.lock.Unlock()
	if !ok {
		return model_core.Message[[]byte, object.LocalReference]{}, status.Error(codes.NotFound, "Object not found")
	}
	return model_core.NewRawMessage(contents), nil
}

func (s *cacheTestStorage) ResolveTag(ctx context.Context, keyHash [sha256.Size]byte) (object.LocalReference, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	reference, ok := s.tags[keyHash]
	if !ok {
		return object.LocalReference{}, status.Error(codes.NotFound, "Tag not found")
	}
	return reference, nil
}

func (s *cacheTestStorage) UpdateTag(ctx context.Context, keyHash [sha256.Size]byte, reference object.LocalReference) error {
	s.lock.Lock()
	s.tags[keyHash] = reference
	s.lock.Unlock()
	return nil
}

// cacheTestClock is a clock whose time can be adjusted by tests.
type cacheTestClock struct {
	clock.Clock
	now time.Time
}

func (c *cacheTestClock) Now() time.Time {
	return c.now
}

// cacheTestComputer is a Computer whose keys are UInt32Values, and
// whose values are UInt32Values as well. The value of each key is
// computed by calling into a function provided by the test. It keeps
// track of the number of times each key is evaluated.
//
// In addition to that, it supports a single StringValue key that
// returns a native value. It returns the value of the key that is
// requested by the test. As native values are never cached, this key
// is evaluated during every build, allowing tests to observe the
// value of the requested key, regardless of whether it was obtained
// from the cache.
type cacheTestComputer struct {
	functions map[uint32]func(get func(uint32) (uint32, bool)) (uint32, bool)
	root      uint32

	lock        sync.Mutex
	evaluations map[uint32]int
	rootValue   uint32
}

var cacheTestStringValueTypeURL = "type.googleapis.com/google.protobuf.StringValue"

func (cacheTestComputer) getValue(e model_evaluation.Environment[object.LocalReference, model_core.ReferenceMetadata], key uint32) (uint32, bool) {
	value := e.GetMessageValue(
		model_core.NewSimplePatchedMessage[model_core.ReferenceMetadata, proto.Message](
			&wrapperspb.UInt32Value{Value: key},
		),
	)
	if !value.IsSet() {
		return 0, false
	}
	return value.Message.(*wrapperspb.UInt32Value).Value, true
}

func (c *cacheTestComputer) ComputeMessageValue(ctx context.Context, key model_core.Message[proto.Message, object.LocalReference], e model_evaluation.Environment[object.LocalReference, model_core.ReferenceMetadata]) (model_core.PatchedMessage[proto.Message, model_core.ReferenceMetadata], error) {
	n := key.Message.(*wrapperspb.UInt32Value).Value
	c.lock.Lock()
	c.evaluations[n]++
	c.lock.Unlock()

	v, ok := c.functions[n](func(dependency uint32) (uint32, bool) {
		return c.getValue(e, dependency)
	})
	if !ok {
		return model_core.PatchedMessage[proto.Message, model_core.ReferenceMetadata]{}, model_evaluation.ErrMissingDependency
	}
	return model_core.NewSimplePatchedMessage[model_core.ReferenceMetadata, proto.Message](
		&wrapperspb.UInt32Value{Value: v},
	), nil
}

func (c *cacheTestComputer) ComputeNativeValue(ctx context.Context, key model_core.Message[proto.Message, object.LocalReference], e model_evaluation.Environment[object.LocalReference, model_core.ReferenceMetadata]) (any, error) {
	v, ok := c.getValue(e, c.root)
	if !ok {
		return nil, model_evaluation.ErrMissingDependency
	}
	c.lock.Lock()
	c.rootValue = v
	c.lock.Unlock()
	return v, nil
}

func (cacheTestComputer) IsLookup(typeURL string) bool {
	return false
}

func (cacheTestComputer) ReturnsNativeValue(typeURL string) bool {
	return typeURL == cacheTestStringValueTypeURL
}

// cacheTestHarness can be used to perform successive builds that
// share the same cache.
type cacheTestHarness struct {
	storage                   *cacheTestStorage
	clock                     *cacheTestClock
	evaluationReader          model_parser.MessageObjectReader[object.LocalReference, *model_evaluation_pb.Evaluation]
	lookupResultReader        model_parser.MessageObjectReader[object.LocalReference, *model_evaluation_cache_pb.LookupResult]
	keysReader                model_parser.MessageObjectReader[object.LocalReference, []*model_evaluation_pb.Keys]
	cacheDeterministicEncoder model_encoding.DeterministicBinaryEncoder
	cacheKeyedEncoder         model_encoding.KeyedBinaryEncoder
}

const cacheTestIndirectionMaximumAge = time.Hour

func newCacheTestHarness(t *testing.T) *cacheTestHarness {
	storage := newCacheTestStorage()
	cacheDeterministicEncoder := util.Must(model_encoding.NewEncryptingDeterministicBinaryEncoder(
		bytes.Repeat([]byte{0x42}, 32),
		nil,
	))
	cacheKeyedEncoder := util.Must(model_encoding.NewEncryptingKeyedBinaryEncoder(
		bytes.Repeat([]byte{0x43}, 32),
		nil,
	))
	return &cacheTestHarness{
		storage: storage,
		clock: &cacheTestClock{
			Clock: clock.SystemClock,
			now:   time.Unix(1700000000, 0),
		},
		evaluationReader: model_parser.NewParsedObjectReader(
			storage,
			model_parser.NewChainedObjectParser(
				model_parser.NewEncodedObjectParser[object.LocalReference](cacheDeterministicEncoder),
				model_parser.NewProtoObjectParser[object.LocalReference, model_evaluation_pb.Evaluation](),
			),
		),
		lookupResultReader: model_parser.NewParsedObjectReader(
			storage,
			model_parser.NewChainedObjectParser(
				model_parser.NewEncodedObjectParser[object.LocalReference](cacheKeyedEncoder),
				model_parser.NewProtoObjectParser[object.LocalReference, model_evaluation_cache_pb.LookupResult](),
			),
		),
		keysReader: model_parser.NewParsedObjectReader(
			storage,
			model_parser.NewChainedObjectParser(
				model_parser.NewEncodedObjectParser[object.LocalReference](cacheDeterministicEncoder),
				model_parser.NewProtoListObjectParser[object.LocalReference, model_evaluation_pb.Keys](),
			),
		),
		cacheDeterministicEncoder: cacheDeterministicEncoder,
		cacheKeyedEncoder:         cacheKeyedEncoder,
	}
}

func cacheTestKey(n uint32) model_core.TopLevelMessage[*anypb.Any, object.LocalReference] {
	return util.Must(
		model_core.MarshalTopLevelAny(
			model_core.NewSimpleTopLevelMessage[object.LocalReference, proto.Message](
				&wrapperspb.UInt32Value{Value: n},
			),
		),
	)
}

func cacheTestKeyReference(n uint32) object.LocalReference {
	return util.Must(model_core.ComputeTopLevelMessageReference(cacheTestKey(n), object.SHA256V1ReferenceFormat))
}

// build performs a single build, using the provided values for keys
// for which overrides are present. It returns the value of the root
// key, and the number of times each key was evaluated.
func (h *cacheTestHarness) build(
	ctx context.Context,
	t *testing.T,
	functions map[uint32]func(get func(uint32) (uint32, bool)) (uint32, bool),
	overrides map[uint32]uint32,
	root uint32,
) (uint32, map[uint32]int) {
	computer := &cacheTestComputer{
		functions:   functions,
		root:        root,
		evaluations: map[uint32]int{},
	}
	queues := model_evaluation.NewSimpleRecursiveComputerEvaluationQueuesFactory[object.LocalReference, model_core.ReferenceMetadata](4).NewQueues()
	recursiveComputer := model_evaluation.NewRecursiveComputer(
		computer,
		queues,
		object.SHA256V1ReferenceFormat,
		h.storage,
		h.storage,
		/* actionTagKeyReference = */ object.MustNewSHA256V1LocalReference("9aa6b4ec3b3a1b6c3c4d7b0b07dcb1f3ee5e2e2a59a4b2e5e1f4e0de8d0e5c1a", 100, 0, 0, 0),
		h.evaluationReader,
		h.lookupResultReader,
		h.keysReader,
		h.cacheDeterministicEncoder,
		h.cacheKeyedEncoder,
		h.clock,
		cacheTestIndirectionMaximumAge,
	)

	for key, value := range overrides {
		require.NoError(t, recursiveComputer.OverrideKeyState(cacheTestKeyReference(key), cacheTestKey(value)))
	}

	probeKeyState, err := recursiveComputer.GetOrCreateKeyState(
		util.Must(
			model_core.MarshalTopLevelAny(
				model_core.NewSimpleTopLevelMessage[object.LocalReference, proto.Message](
					&wrapperspb.StringValue{Value: "probe"},
				),
			),
		),
	)
	require.NoError(t, err)

	require.NoError(t, program.RunLocal(ctx, func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
		queues.ProcessAllEvaluatableKeys(dependenciesGroup, recursiveComputer)
		return recursiveComputer.WaitForEvaluation(ctx, probeKeyState)
	}))

	// Upload all results into the cache.
	recursiveComputer.GracefullyStopUploading()
	for {
		shouldContinue, err := recursiveComputer.ProcessNextUploadableKey(ctx)
		require.NoError(t, err)
		if !shouldContinue {
			break
		}
	}
	return computer.rootValue, computer.evaluations
}

func TestRecursiveComputerCache(t *testing.T) {
	ctx := context.Background()

	// Keys for which overrides are provided.
	const keyA, keyB, keyC = 1, 2, 3
	// Key that is requested.
	const keyRoot = 100

	t.Run("StableDependencies", func(t *testing.T) {
		// The root key always depends on the same set of keys.
		// Results for different values of these keys may be
		// stored in the cache simultaneously.
		h := newCacheTestHarness(t)
		functions := map[uint32]func(get func(uint32) (uint32, bool)) (uint32, bool){
			keyRoot: func(get func(uint32) (uint32, bool)) (uint32, bool) {
				a, okA := get(keyA)
				b, okB := get(keyB)
				return a + b, okA && okB
			},
		}

		for _, step := range []struct {
			a, b, value uint32
			expectHit   bool
		}{
			{a: 1, b: 2, value: 3, expectHit: false},
			{a: 1, b: 2, value: 3, expectHit: true},
			{a: 5, b: 2, value: 7, expectHit: false},
			{a: 1, b: 2, value: 3, expectHit: true},
			{a: 5, b: 2, value: 7, expectHit: true},
		} {
			value, evaluations := h.build(ctx, t, functions, map[uint32]uint32{keyA: step.a, keyB: step.b}, keyRoot)
			require.Equal(t, step.value, value)
			if step.expectHit {
				require.Zero(t, evaluations[keyRoot], "a=%d b=%d", step.a, step.b)
			} else {
				require.NotZero(t, evaluations[keyRoot], "a=%d b=%d", step.a, step.b)
			}
		}
	})

	// Key whose set of dependencies depends on the value of A.
	varyingFunctions := map[uint32]func(get func(uint32) (uint32, bool)) (uint32, bool){
		keyRoot: func(get func(uint32) (uint32, bool)) (uint32, bool) {
			a, ok := get(keyA)
			if !ok {
				return 0, false
			}
			if a == 0 {
				return get(keyB)
			}
			return get(keyC)
		},
	}

	t.Run("VaryingDependencies", func(t *testing.T) {
		// Cache entries need to be merged, so that keys whose
		// dependencies vary between evaluations can still be
		// obtained from the cache.
		h := newCacheTestHarness(t)
		for i, step := range []struct {
			a, b, c, value uint32
			expectHit      bool
		}{
			// Initial evaluation depends on {A, B}.
			{a: 0, b: 5, c: 7, value: 5, expectHit: false},
			// Evaluation depending on {A, C}. This reduces
			// the initial lookup result to {A}, causing
			// the previous evaluation to be unreachable.
			{a: 1, b: 5, c: 7, value: 7, expectHit: false},
			{a: 0, b: 5, c: 7, value: 5, expectHit: false},
			// Both variants are now present in the cache.
			{a: 1, b: 5, c: 7, value: 7, expectHit: true},
			{a: 0, b: 5, c: 7, value: 5, expectHit: true},
			// Changing the value of B only affects the
			// variant that depends on it.
			{a: 0, b: 6, c: 7, value: 6, expectHit: false},
			{a: 1, b: 6, c: 7, value: 7, expectHit: true},
			{a: 0, b: 6, c: 7, value: 6, expectHit: true},
			{a: 0, b: 5, c: 7, value: 5, expectHit: true},
		} {
			value, evaluations := h.build(ctx, t, varyingFunctions, map[uint32]uint32{keyA: step.a, keyB: step.b, keyC: step.c}, keyRoot)
			require.Equal(t, step.value, value, "step %d", i)
			if step.expectHit {
				require.Zero(t, evaluations[keyRoot], "step %d", i)
			} else {
				require.NotZero(t, evaluations[keyRoot], "step %d", i)
			}
		}
	})

	t.Run("NestedVaryingDependencies", func(t *testing.T) {
		// Variants that only start to differ after multiple
		// lookups. Lookup results returning missing
		// dependencies need to be reduced to the dependencies
		// that are shared by all variants passing through them.
		const keyD = 4
		functions := map[uint32]func(get func(uint32) (uint32, bool)) (uint32, bool){
			keyRoot: func(get func(uint32) (uint32, bool)) (uint32, bool) {
				a, ok := get(keyA)
				if !ok {
					return 0, false
				}
				if a != 0 {
					return 100, true
				}
				b, ok := get(keyB)
				if !ok {
					return 0, false
				}
				if b == 0 {
					return get(keyC)
				}
				return get(keyD)
			},
		}

		h := newCacheTestHarness(t)
		for i, step := range []struct {
			a, b, value uint32
			expectHit   bool
		}{
			// Depends on {A}.
			{a: 1, b: 0, value: 100, expectHit: false},
			// Depends on {A, B, C}.
			{a: 0, b: 0, value: 30, expectHit: false},
			// Depends on {A, B, D}. The lookup result
			// returning {B, C} is reduced to {B}.
			{a: 0, b: 1, value: 40, expectHit: false},
			// The variant depending on {A, B, C} needs
			// to be re-evaluated, as the lookup result
			// returning {C} is not present yet.
			{a: 0, b: 0, value: 30, expectHit: false},
			// All variants are now present.
			{a: 0, b: 1, value: 40, expectHit: true},
			{a: 0, b: 0, value: 30, expectHit: true},
			{a: 1, b: 0, value: 100, expectHit: true},
		} {
			value, evaluations := h.build(ctx, t, functions, map[uint32]uint32{keyA: step.a, keyB: step.b, keyC: 30, keyD: 40}, keyRoot)
			require.Equal(t, step.value, value, "step %d", i)
			if step.expectHit {
				require.Zero(t, evaluations[keyRoot], "step %d", i)
			} else {
				require.NotZero(t, evaluations[keyRoot], "step %d", i)
			}
		}
	})

	t.Run("IndirectionCollapse", func(t *testing.T) {
		for _, tc := range []struct {
			name           string
			elapsed        time.Duration
			expectFinalHit bool
		}{
			// Lookup results containing indirection that
			// were created recently are merged.
			{name: "Recent", elapsed: cacheTestIndirectionMaximumAge / 2, expectFinalHit: true},
			// Lookup results containing indirection that
			// were created a long time ago are overwritten,
			// causing other variants to be discarded.
			{name: "Expired", elapsed: cacheTestIndirectionMaximumAge * 2, expectFinalHit: false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				h := newCacheTestHarness(t)
				_, evaluations := h.build(ctx, t, varyingFunctions, map[uint32]uint32{keyA: 0, keyB: 5, keyC: 7}, keyRoot)
				require.NotZero(t, evaluations[keyRoot])
				_, evaluations = h.build(ctx, t, varyingFunctions, map[uint32]uint32{keyA: 1, keyB: 5, keyC: 7}, keyRoot)
				require.NotZero(t, evaluations[keyRoot])
				_, evaluations = h.build(ctx, t, varyingFunctions, map[uint32]uint32{keyA: 1, keyB: 5, keyC: 7}, keyRoot)
				require.Zero(t, evaluations[keyRoot])

				h.clock.now = h.clock.now.Add(tc.elapsed)
				_, evaluations = h.build(ctx, t, varyingFunctions, map[uint32]uint32{keyA: 0, keyB: 5, keyC: 7}, keyRoot)
				require.NotZero(t, evaluations[keyRoot])

				value, evaluations := h.build(ctx, t, varyingFunctions, map[uint32]uint32{keyA: 1, keyB: 5, keyC: 7}, keyRoot)
				require.Equal(t, uint32(7), value)
				if tc.expectFinalHit {
					require.Zero(t, evaluations[keyRoot])
				} else {
					require.NotZero(t, evaluations[keyRoot])
				}
			})
		}
	})

	t.Run("ValueFallback", func(t *testing.T) {
		// Let the root key depend on a key M and an override
		// Y. M in turn depends on an override X. If M's key
		// reference is lower than the root's, M is stored
		// inside the root's graphlet. This means that the
		// graphlet depends on {X, Y}, while the value depends
		// on {M, Y}.
		const keyX, keyY = 1, 2
		keyM, keyR := uint32(10), uint32(11)
		if bytes.Compare(cacheTestKeyReference(keyM).GetRawReference(), cacheTestKeyReference(keyR).GetRawReference()) > 0 {
			keyM, keyR = keyR, keyM
		}

		functions := map[uint32]func(get func(uint32) (uint32, bool)) (uint32, bool){
			keyM: func(get func(uint32) (uint32, bool)) (uint32, bool) {
				x, ok := get(keyX)
				return x / 10, ok
			},
			keyR: func(get func(uint32) (uint32, bool)) (uint32, bool) {
				m, okM := get(keyM)
				y, okY := get(keyY)
				return m + y, okM && okY
			},
		}

		h := newCacheTestHarness(t)
		value, evaluations := h.build(ctx, t, functions, map[uint32]uint32{keyX: 1, keyY: 4}, keyR)
		require.Equal(t, uint32(4), value)
		require.NotZero(t, evaluations[keyM])
		require.NotZero(t, evaluations[keyR])

		// Changing X causes the graphlet lookup to fail.
		// However, M yields the same value as before, meaning
		// that the value of the root can be obtained from the
		// cache.
		value, evaluations = h.build(ctx, t, functions, map[uint32]uint32{keyX: 2, keyY: 4}, keyR)
		require.Equal(t, uint32(4), value)
		require.NotZero(t, evaluations[keyM])
		require.Zero(t, evaluations[keyR])

		// The previous build should have uploaded the graphlet
		// corresponding to the new value of X, so that M no
		// longer needs to be evaluated.
		value, evaluations = h.build(ctx, t, functions, map[uint32]uint32{keyX: 2, keyY: 4}, keyR)
		require.Equal(t, uint32(4), value)
		require.Zero(t, evaluations[keyM])
		require.Zero(t, evaluations[keyR])

		// Changing Y invalidates both the graphlet and value.
		value, evaluations = h.build(ctx, t, functions, map[uint32]uint32{keyX: 2, keyY: 5}, keyR)
		require.Equal(t, uint32(5), value)
		require.NotZero(t, evaluations[keyR])
	})
}
