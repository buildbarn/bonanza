package evaluation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"iter"
	"maps"
	"slices"
	"sync"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/protobuf/proto"
)

// RecursiveComputer can be used to compute values, taking dependencies
// between keys into account.
//
// Whenever the computation function requests the value for a key that
// has not been computed before, the key of the dependency is placed in
// a queue. Once the values of all previously missing dependencies are
// available, computation of the original key is restarted. This process
// repeates itself until all requested keys are exhausted.
type RecursiveComputer[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	base          Computer[TReference, TMetadata]
	objectManager model_core.ObjectManager[TReference, TMetadata]

	lock sync.Mutex

	// Map of all keys that have been requested.
	keys map[[sha256.Size]byte]*KeyState[TReference, TMetadata]

	// Keys on which currently one or more other keys are blocked.
	blockingKeys map[*KeyState[TReference, TMetadata]]struct{}

	// The number of keys that are currently being evaluated, which
	// is at most equal to the number of goroutines calling
	// ProcessNextQueuedKey().
	currentlyEvaluatingKeysCount uint

	// List of keys for which evaluation should be attempted.
	firstQueuedKey *KeyState[TReference, TMetadata]
	lastQueuedKey  **KeyState[TReference, TMetadata]
	queuedKeysWait chan struct{}
}

// NewRecursiveComputer creates a new RecursiveComputer that is in the
// initial state (i.e., having no queued or evaluated keys).
func NewRecursiveComputer[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	base Computer[TReference, TMetadata],
	objectManager model_core.ObjectManager[TReference, TMetadata],
) *RecursiveComputer[TReference, TMetadata] {
	rc := &RecursiveComputer[TReference, TMetadata]{
		base:          base,
		objectManager: objectManager,

		keys:         map[[sha256.Size]byte]*KeyState[TReference, TMetadata]{},
		blockingKeys: map[*KeyState[TReference, TMetadata]]struct{}{},
	}
	rc.lastQueuedKey = &rc.firstQueuedKey
	return rc
}

func (rc *RecursiveComputer[TReference, TMetadata]) ProcessNextQueuedKey(ctx context.Context) bool {
	for {
		rc.lock.Lock()
		if rc.firstQueuedKey != nil {
			// One or more keys are available for evaluation.
			break
		}

		if rc.currentlyEvaluatingKeysCount == 0 {
			// If there are no keys queued for evaluation,
			// we are currently evaluating none of them, but
			// there are keys blocking others, it means we
			// have one or more cycles.
			for ks := range rc.blockingKeys {
				rc.propagateFailure(ks, errors.New("cyclic dependency detected"))
			}
		}

		if rc.queuedKeysWait == nil {
			rc.queuedKeysWait = make(chan struct{})
		}
		queuedKeysWait := rc.queuedKeysWait
		rc.lock.Unlock()

		select {
		case <-ctx.Done():
			return false
		case <-queuedKeysWait:
		}
	}

	// Extract the first queued key.
	ks := rc.firstQueuedKey
	rc.firstQueuedKey = ks.nextQueuedKey
	ks.nextQueuedKey = nil
	if rc.firstQueuedKey == nil {
		rc.lastQueuedKey = &rc.firstQueuedKey
	}
	rc.currentlyEvaluatingKeysCount++
	rc.lock.Unlock()

	e := recursivelyComputingEnvironment[TReference, TMetadata]{
		computer:     rc,
		keyState:     ks,
		dependencies: map[*KeyState[TReference, TMetadata]]struct{}{},
	}
	err := ks.value.compute(ctx, &e)
	dependencies := slices.Collect(maps.Keys(e.dependencies))

	rc.lock.Lock()
	rc.currentlyEvaluatingKeysCount--
	if ks.err == (errKeyNotEvaluated{}) {
		if err == nil {
			ks.err = nil
			for _, ksBlocked := range ks.blocking {
				ksBlocked.blockedCount--
				if ksBlocked.blockedCount == 0 && ksBlocked.err == (errKeyNotEvaluated{}) {
					rc.enqueue(ksBlocked)
				}
			}
			rc.completeKeyState(ks)
		} else if errors.Is(err, ErrMissingDependency) {
			if len(dependencies) == 0 {
				panic("no dependencies")
			}
			if ks.blockedCount != 0 {
				panic("key that is currently being evaluated cannot be blocked")
			}
			restartImmediately := true
			for _, ksDep := range dependencies {
				if err := ksDep.err; err == (errKeyNotEvaluated{}) {
					if len(ksDep.blocking) == 0 {
						rc.blockingKeys[ksDep] = struct{}{}
					}
					ksDep.blocking = append(ksDep.blocking, ks)
					ks.blockedCount++
					restartImmediately = false
				} else if err != nil {
					rc.failKeyState(ks, NestedError[TReference]{
						Key: ksDep.key,
						Err: err,
					})
					restartImmediately = false
				}
			}
			if restartImmediately {
				rc.enqueue(ks)
			}
		} else {
			rc.failKeyState(ks, err)
		}
	}
	ks.dependencies = dependencies
	rc.lock.Unlock()
	return true
}

func getKeyHash[TReference object.BasicReference](key model_core.TopLevelMessage[proto.Message, TReference]) ([sha256.Size]byte, error) {
	anyKey, err := model_core.MarshalTopLevelAny(key)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	marshaledKey, err := model_core.MarshalTopLevelMessage(anyKey)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(marshaledKey), nil
}

func (rc *RecursiveComputer[TReference, TMetadata]) propagateFailure(ks *KeyState[TReference, TMetadata], err error) {
	for _, ksBlocked := range ks.blocking {
		rc.failKeyState(ksBlocked, NestedError[TReference]{
			Key: ks.key,
			Err: err,
		})
	}
}

func (rc *RecursiveComputer[TReference, TMetadata]) failKeyState(ks *KeyState[TReference, TMetadata], err error) {
	if ks.err == (errKeyNotEvaluated{}) {
		ks.err = err
		rc.propagateFailure(ks, err)
		rc.completeKeyState(ks)
	}
}

func (rc *RecursiveComputer[TReference, TMetadata]) completeKeyState(ks *KeyState[TReference, TMetadata]) {
	ks.blocking = nil
	delete(rc.blockingKeys, ks)
	if ks.completionWait != nil {
		close(ks.completionWait)
		ks.completionWait = nil
	}
}

func (rc *RecursiveComputer[TReference, TMetadata]) getOrCreateKeyStateLocked(key model_core.TopLevelMessage[proto.Message, TReference], keyHash [sha256.Size]byte, initialValueState valueState[TReference, TMetadata]) *KeyState[TReference, TMetadata] {
	ks, ok := rc.keys[keyHash]
	if !ok {
		ks = &KeyState[TReference, TMetadata]{
			key:     key,
			keyHash: keyHash,
			value:   initialValueState,
			err:     errKeyNotEvaluated{},
		}
		rc.keys[keyHash] = ks
		rc.enqueue(ks)
	}
	return ks
}

func (rc *RecursiveComputer[TReference, TMetadata]) GetOrCreateKeyState(key model_core.TopLevelMessage[proto.Message, TReference]) (*KeyState[TReference, TMetadata], error) {
	keyHash, err := getKeyHash(key)
	if err != nil {
		return nil, err
	}

	rc.lock.Lock()
	ks := rc.getOrCreateKeyStateLocked(key, keyHash, &messageValueState[TReference, TMetadata]{})
	rc.lock.Unlock()
	return ks, nil
}

func (rc *RecursiveComputer[TReference, TMetadata]) WaitForMessageValue(ctx context.Context, ks *KeyState[TReference, TMetadata]) (model_core.Message[proto.Message, TReference], error) {
	rc.lock.Lock()
	if ks.err == (errKeyNotEvaluated{}) {
		// Key has not finished evaluating. Wait for its
		// completion. As we only tend to wait on a very small
		// number of keys, a channel is not created by default.
		if ks.completionWait == nil {
			ks.completionWait = make(chan struct{})
		}
		completionWait := ks.completionWait
		rc.lock.Unlock()
		select {
		case <-completionWait:
		case <-ctx.Done():
			return model_core.Message[proto.Message, TReference]{}, util.StatusFromContext(ctx)
		}
	} else {
		rc.lock.Unlock()
	}

	if err := ks.err; err != nil {
		return model_core.Message[proto.Message, TReference]{}, err
	}
	return ks.value.(*messageValueState[TReference, TMetadata]).value, nil
}

func (rc *RecursiveComputer[TReference, TMetadata]) enqueue(ks *KeyState[TReference, TMetadata]) {
	*rc.lastQueuedKey = ks
	rc.lastQueuedKey = &ks.nextQueuedKey
	// TODO: This wakes up all threads.
	if rc.queuedKeysWait != nil {
		close(rc.queuedKeysWait)
		rc.queuedKeysWait = nil
	}
}

type Evaluation[TReference any] struct {
	Key          model_core.TopLevelMessage[proto.Message, TReference]
	Value        model_core.Message[proto.Message, TReference]
	Dependencies []model_core.TopLevelMessage[proto.Message, TReference]
}

func (rc *RecursiveComputer[TReference, TMetadata]) GetAllEvaluations() iter.Seq[Evaluation[TReference]] {
	return func(yield func(Evaluation[TReference]) bool) {
		rc.lock.Lock()
		defer rc.lock.Unlock()

		for _, key := range slices.SortedFunc(
			maps.Keys(rc.keys),
			func(a, b [sha256.Size]byte) int {
				return bytes.Compare(a[:], b[:])
			},
		) {
			ks := rc.keys[key]

			value := ks.value.getMessageValue()
			dependencies := make([]model_core.TopLevelMessage[proto.Message, TReference], 0, len(ks.dependencies))
			slices.SortFunc(
				ks.dependencies,
				func(a, b *KeyState[TReference, TMetadata]) int {
					return bytes.Compare(a.keyHash[:], b.keyHash[:])
				},
			)
			for _, ksDep := range ks.dependencies {
				dependencies = append(dependencies, ksDep.key)
			}

			// Omit keys for which there's nothing
			// meaningful to report. Those only blow up the
			// size of the data.
			if !value.IsSet() && len(dependencies) == 0 {
				continue
			}

			if !yield(Evaluation[TReference]{
				Key:          ks.key,
				Value:        value,
				Dependencies: dependencies,
			}) {
				break
			}
		}
	}
}

type recursivelyComputingEnvironment[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	// Constant fields.
	computer *RecursiveComputer[TReference, TMetadata]
	keyState *KeyState[TReference, TMetadata]

	dependencies map[*KeyState[TReference, TMetadata]]struct{}
}

func (e *recursivelyComputingEnvironment[TReference, TMetadata]) CaptureCreatedObject(createdObject model_core.CreatedObject[TMetadata]) TMetadata {
	return e.computer.objectManager.CaptureCreatedObject(createdObject)
}

func (e *recursivelyComputingEnvironment[TReference, TMetadata]) CaptureExistingObject(reference TReference) TMetadata {
	return e.computer.objectManager.CaptureExistingObject(reference)
}

func (e *recursivelyComputingEnvironment[TReference, TMetadata]) ReferenceObject(localReference object.LocalReference, metadata TMetadata) TReference {
	return e.computer.objectManager.ReferenceObject(localReference, metadata)
}

func (e *recursivelyComputingEnvironment[TReference, TMetadata]) getValueState(patchedKey model_core.PatchedMessage[proto.Message, TMetadata], initialValueState valueState[TReference, TMetadata]) valueState[TReference, TMetadata] {
	rc := e.computer
	key := model_core.Unpatch(rc.objectManager, patchedKey)
	keyHash, err := getKeyHash(key)
	if err != nil {
		panic("TODO: Mark current key as broken")
	}

	rc.lock.Lock()
	defer rc.lock.Unlock()

	ks := rc.getOrCreateKeyStateLocked(key, keyHash, initialValueState)
	e.dependencies[ks] = struct{}{}
	if ks.err != nil {
		return nil
	}
	return ks.value
}

func (e *recursivelyComputingEnvironment[TReference, TMetadata]) GetMessageValue(patchedKey model_core.PatchedMessage[proto.Message, TMetadata]) model_core.Message[proto.Message, TReference] {
	vs := e.getValueState(patchedKey, &messageValueState[TReference, TMetadata]{})
	if vs == nil {
		return model_core.Message[proto.Message, TReference]{}
	}
	return vs.(*messageValueState[TReference, TMetadata]).value
}

func (e *recursivelyComputingEnvironment[TReference, TMetadata]) GetNativeValue(patchedKey model_core.PatchedMessage[proto.Message, TMetadata]) (any, bool) {
	vs := e.getValueState(patchedKey, &nativeValueState[TReference, TMetadata]{})
	if vs == nil {
		return nil, false
	}
	return vs.(*nativeValueState[TReference, TMetadata]).value, true
}

// KeyState contains all of the evaluation state of RecursiveComputer
// for a given key. If evaluation has not yet completed, it stores the
// list of keys that are currently blocked on its completion (i.e., its
// reverse dependencies). Upon completion, it stores the value
// associated with the key or any error that occurred computing it.
type KeyState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	// Constant fields.
	key     model_core.TopLevelMessage[proto.Message, TReference]
	keyHash [sha256.Size]byte

	// If the key is queued for evaluation, this field is set to the
	// next queued key, if any.
	nextQueuedKey *KeyState[TReference, TMetadata]

	// The number of keys on which this key depends that have not
	// been computed yet. Keys may not be queued if this field is
	// non-zero.
	blockedCount uint

	// If this key has not been evaluated, this field contains the
	// list of other keys whose execution currently is blocked on
	// the value of this key.
	blocking []*KeyState[TReference, TMetadata]

	// Dependencies of the current key that were derived during the
	// last evaluation of this key.
	dependencies []*KeyState[TReference, TMetadata]

	completionWait chan struct{}
	value          valueState[TReference, TMetadata]
	err            error
}

type valueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] interface {
	compute(ctx context.Context, e *recursivelyComputingEnvironment[TReference, TMetadata]) error
	getMessageValue() model_core.Message[proto.Message, TReference]
}

type messageValueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	value model_core.Message[proto.Message, TReference]
}

func (vs *messageValueState[TReference, TMetadata]) compute(ctx context.Context, e *recursivelyComputingEnvironment[TReference, TMetadata]) error {
	value, err := e.computer.base.ComputeMessageValue(ctx, e.keyState.key.Decay(), e)
	if err != nil {
		return err
	}
	vs.value = model_core.Unpatch(e.computer.objectManager, value).Decay()
	return nil
}

func (vs *messageValueState[TReference, TMetadata]) getMessageValue() model_core.Message[proto.Message, TReference] {
	return vs.value
}

type nativeValueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	value any
}

func (vs *nativeValueState[TReference, TMetadata]) compute(ctx context.Context, e *recursivelyComputingEnvironment[TReference, TMetadata]) error {
	value, err := e.computer.base.ComputeNativeValue(ctx, e.keyState.key.Decay(), e)
	if err != nil {
		return err
	}
	vs.value = value
	return nil
}

func (nativeValueState[TReference, TMetadata]) getMessageValue() model_core.Message[proto.Message, TReference] {
	return model_core.Message[proto.Message, TReference]{}
}

// errKeyNotEvaluated is a placeholder value that is assigned to
// KeyState.err to indicate that evaluation of a given key has not yet
// been completed.
type errKeyNotEvaluated struct{}

func (errKeyNotEvaluated) Error() string {
	panic("attempted to use this type as an actual error object")
}

// NestedError is used to wrap errors that occurred while evaluating a
// dependency of a given key. The key of the dependency is included,
// meaning that repeated unwrapping can be used to obtain a stack trace.
type NestedError[TReference object.BasicReference] struct {
	Key model_core.TopLevelMessage[proto.Message, TReference]
	Err error
}

func (e NestedError[TReference]) Error() string {
	return e.Err.Error()
}

type ObjectManagerForTesting = model_core.ObjectManager[object.LocalReference, model_core.ReferenceMetadata]
