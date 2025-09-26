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
	"time"

	model_core "bonanza.build/pkg/model/core"
	model_evaluation_pb "bonanza.build/pkg/proto/model/evaluation"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/clock"
	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// RecursiveComputerQueue represents a queue of evaluation keys that are
// currently not blocked and are ready to be evaluated.
//
// Instances of RecursiveComputer can make use of multiple queues. This
// can be used to enforce that different types of keys are evaluated
// with different amounts of concurrency. For example, keys that are CPU
// intensive to evaluate can be executed with a concurrency proportional
// to the number of locally available CPU cores, while keys that perform
// long-running network requests can use a higher amount of concurrency.
type RecursiveComputerQueue[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	queuedKeys     keyStateList[TReference, TMetadata]
	queuedKeysWait chan struct{}
}

// NewRecursiveComputerQueue creates a new RecursiveComputerQueue that
// does not have any queues keys.
func NewRecursiveComputerQueue[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata]() *RecursiveComputerQueue[TReference, TMetadata] {
	var rcq RecursiveComputerQueue[TReference, TMetadata]
	rcq.queuedKeys.init()
	return &rcq
}

// RecursiveComputerQueuePicker is used by RecursiveComputer to pick a
// RecursiveComputerQueue to which a given key should be assigned.
type RecursiveComputerQueuePicker[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] interface {
	PickQueue(model_core.Message[proto.Message, TReference]) *RecursiveComputerQueue[TReference, TMetadata]
}

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
	queuePicker   RecursiveComputerQueuePicker[TReference, TMetadata]
	objectManager model_core.ObjectManager[TReference, TMetadata]
	clock         clock.Clock

	lock sync.Mutex

	// Map of all keys that have been requested.
	keys map[[sha256.Size]byte]*KeyState[TReference, TMetadata]

	// Keys on which currently one or more other keys are blocked.
	blockingKeys map[*KeyState[TReference, TMetadata]]struct{}

	// Keys which are currently blocked on one or more other keys.
	blockedKeys keyStateList[TReference, TMetadata]

	// Keys that are currently being evaluated, which is at most
	// equal to the number of goroutines calling
	// ProcessNextQueuedKey().
	evaluatingKeys keyStateList[TReference, TMetadata]

	// Total number of keys for which evaluation should be attempted.
	totalQueuedKeysCount uint64

	completedKeys keyStateList[TReference, TMetadata]
}

// NewRecursiveComputer creates a new RecursiveComputer that is in the
// initial state (i.e., having no queued or evaluated keys).
func NewRecursiveComputer[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	base Computer[TReference, TMetadata],
	queuePicker RecursiveComputerQueuePicker[TReference, TMetadata],
	objectManager model_core.ObjectManager[TReference, TMetadata],
	clock clock.Clock,
) *RecursiveComputer[TReference, TMetadata] {
	rc := &RecursiveComputer[TReference, TMetadata]{
		base:          base,
		queuePicker:   queuePicker,
		objectManager: objectManager,
		clock:         clock,

		keys:         map[[sha256.Size]byte]*KeyState[TReference, TMetadata]{},
		blockingKeys: map[*KeyState[TReference, TMetadata]]struct{}{},
	}
	rc.blockedKeys.init()
	rc.evaluatingKeys.init()
	rc.completedKeys.init()
	return rc
}

func (rc *RecursiveComputer[TReference, TMetadata]) ProcessNextQueuedKey(ctx context.Context, rcq *RecursiveComputerQueue[TReference, TMetadata]) bool {
	for {
		rc.lock.Lock()
		if !rcq.queuedKeys.empty() {
			// One or more keys are available for evaluation.
			break
		}

		if rc.totalQueuedKeysCount == 0 && rc.evaluatingKeys.empty() {
			// If there are no keys queued for evaluation,
			// we are currently evaluating none of them, but
			// there are keys blocking others, it means we
			// have one or more cycles.
			for ks := range rc.blockingKeys {
				rc.propagateFailure(ks, errors.New("cyclic dependency detected"))
			}
		}

		if rcq.queuedKeysWait == nil {
			rcq.queuedKeysWait = make(chan struct{})
		}
		queuedKeysWait := rcq.queuedKeysWait
		rc.lock.Unlock()

		select {
		case <-ctx.Done():
			return false
		case <-queuedKeysWait:
		}
	}

	// Extract the first queued key.
	ks := rcq.queuedKeys.popFirst()
	rc.totalQueuedKeysCount--
	rc.evaluatingKeys.pushLast(ks)
	ks.currentEvaluationStart = rc.clock.Now()
	if ks.restarts == 0 {
		ks.firstEvaluationStart = ks.currentEvaluationStart
	}
	rc.lock.Unlock()

	e := recursivelyComputingEnvironment[TReference, TMetadata]{
		computer:     rc,
		keyState:     ks,
		dependencies: map[*KeyState[TReference, TMetadata]]struct{}{},
	}
	err := ks.value.compute(ctx, &e)
	dependencies := slices.Collect(maps.Keys(e.dependencies))

	rc.lock.Lock()
	rc.evaluatingKeys.remove(ks)
	if ks.err == (errKeyNotEvaluated{}) {
		if err == nil {
			ks.err = nil
			for _, ksBlocked := range ks.blocking {
				if ksBlocked.blockedCount > 0 {
					ksBlocked.blockedCount--
					if ksBlocked.blockedCount == 0 {
						rc.blockedKeys.remove(ksBlocked)
						rc.enqueue(ksBlocked)
					}
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
			ks.restarts++
			restartImmediately := true
			for _, ksDep := range dependencies {
				if err := ksDep.err; err != nil && err != (errKeyNotEvaluated{}) {
					rc.failKeyState(ks, NestedError[TReference]{
						Key: ksDep.key,
						Err: err,
					})
					restartImmediately = false
					break
				}
			}
			if restartImmediately {
				for _, ksDep := range dependencies {
					if ksDep.err == (errKeyNotEvaluated{}) {
						if len(ksDep.blocking) == 0 {
							rc.blockingKeys[ksDep] = struct{}{}
						}
						ksDep.blocking = append(ksDep.blocking, ks)
						if ks.blockedCount == 0 {
							rc.blockedKeys.pushLast(ks)
						}
						ks.blockedCount++
						restartImmediately = false
					}
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
		if ksBlocked.blockedCount > 0 {
			rc.blockedKeys.remove(ksBlocked)
			ksBlocked.blockedCount = 0
			rc.failKeyState(ksBlocked, NestedError[TReference]{
				Key: ks.key,
				Err: err,
			})
		}
	}
}

func (rc *RecursiveComputer[TReference, TMetadata]) failKeyState(ks *KeyState[TReference, TMetadata], err error) {
	ks.err = err
	rc.propagateFailure(ks, err)
	rc.completeKeyState(ks)
}

func (rc *RecursiveComputer[TReference, TMetadata]) completeKeyState(ks *KeyState[TReference, TMetadata]) {
	ks.blocking = nil
	delete(rc.blockingKeys, ks)
	if ks.completionWait != nil {
		close(ks.completionWait)
		ks.completionWait = nil
	}
	rc.completedKeys.pushLast(ks)
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

func (rc *RecursiveComputer[TReference, TMetadata]) InjectKeyState(key model_core.TopLevelMessage[proto.Message, TReference], value model_core.Message[proto.Message, TReference]) error {
	keyHash, err := getKeyHash(key)
	if err != nil {
		return err
	}

	rc.lock.Lock()
	if _, ok := rc.keys[keyHash]; !ok {
		rc.keys[keyHash] = &KeyState[TReference, TMetadata]{
			key:     key,
			keyHash: keyHash,
			value: &messageValueState[TReference, TMetadata]{
				value: value,
			},
		}
	}
	rc.lock.Unlock()
	return nil
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

func (rc *RecursiveComputer[TReference, TMetadata]) GetProgress() (model_core.PatchedMessage[*model_evaluation_pb.Progress, TMetadata], error) {
	rc.lock.Lock()
	defer rc.lock.Unlock()

	return model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (*model_evaluation_pb.Progress, error) {
		// TODO: Set additional_evaluating_keys_count if we have
		// too many keys to fit in a message.
		evaluatingKeys := make([]*model_evaluation_pb.Progress_EvaluatingKey, 0, rc.evaluatingKeys.count)
		for ks := rc.evaluatingKeys.head.nextKey; ks != &rc.evaluatingKeys.head; ks = ks.nextKey {
			anyKey, err := model_core.MarshalAny(model_core.Patch(rc.objectManager, ks.key.Decay()))
			if err != nil {
				return nil, err
			}
			evaluatingKeys = append(evaluatingKeys, &model_evaluation_pb.Progress_EvaluatingKey{
				Key:                    anyKey.Merge(patcher),
				FirstEvaluationStart:   timestamppb.New(ks.firstEvaluationStart),
				CurrentEvaluationStart: timestamppb.New(ks.currentEvaluationStart),
				Restarts:               ks.restarts,
			})
		}
		return &model_evaluation_pb.Progress{
			CompletedKeysCount:   rc.completedKeys.count,
			OldestEvaluatingKeys: evaluatingKeys,
			QueuedKeysCount:      rc.totalQueuedKeysCount,
			BlockedKeysCount:     rc.blockedKeys.count,
		}, nil
	})
}

func (rc *RecursiveComputer[TReference, TMetadata]) enqueue(ks *KeyState[TReference, TMetadata]) {
	rcq := rc.queuePicker.PickQueue(ks.key.Decay())
	rcq.queuedKeys.pushLast(ks)
	rc.totalQueuedKeysCount++
	// TODO: This wakes up all threads.
	if rcq.queuedKeysWait != nil {
		close(rcq.queuedKeysWait)
		rcq.queuedKeysWait = nil
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

func (e *recursivelyComputingEnvironment[TReference, TMetadata]) CaptureCreatedObject(ctx context.Context, createdObject model_core.CreatedObject[TMetadata]) (TMetadata, error) {
	return e.computer.objectManager.CaptureCreatedObject(ctx, createdObject)
}

func (e *recursivelyComputingEnvironment[TReference, TMetadata]) CaptureExistingObject(reference TReference) TMetadata {
	return e.computer.objectManager.CaptureExistingObject(reference)
}

func (e *recursivelyComputingEnvironment[TReference, TMetadata]) ReferenceObject(capturedObject model_core.MetadataEntry[TMetadata]) TReference {
	return e.computer.objectManager.ReferenceObject(capturedObject)
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
	mvs, ok := vs.(*messageValueState[TReference, TMetadata])
	if !ok {
		panic("TODO: Mark current key as broken")
	}
	return mvs.value
}

func (e *recursivelyComputingEnvironment[TReference, TMetadata]) GetNativeValue(patchedKey model_core.PatchedMessage[proto.Message, TMetadata]) (any, bool) {
	vs := e.getValueState(patchedKey, &nativeValueState[TReference, TMetadata]{})
	if vs == nil {
		return nil, false
	}
	nvs, ok := vs.(*nativeValueState[TReference, TMetadata])
	if !ok {
		panic("TODO: Mark current key as broken")
	}
	return nvs.value, true
}

type keyStateList[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	head  KeyState[TReference, TMetadata]
	count uint64
}

func (ksl *keyStateList[TReference, TMetadata]) init() {
	ksl.head.previousKey = &ksl.head
	ksl.head.nextKey = &ksl.head
}

func (ksl *keyStateList[TReference, TMetadata]) empty() bool {
	return ksl.head.nextKey == &ksl.head
}

func (ksl *keyStateList[TReference, TMetadata]) popFirst() *KeyState[TReference, TMetadata] {
	ks := ksl.head.nextKey
	ksl.remove(ks)
	return ks
}

func (ksl *keyStateList[TReference, TMetadata]) pushLast(ks *KeyState[TReference, TMetadata]) {
	if ks.previousKey != nil || ks.nextKey != nil {
		panic("element is already in a list")
	}

	ks.previousKey = ksl.head.previousKey
	ks.nextKey = &ksl.head
	ks.previousKey.nextKey = ks
	ks.nextKey.previousKey = ks
	ksl.count++
}

func (ksl *keyStateList[TReference, TMetadata]) remove(ks *KeyState[TReference, TMetadata]) {
	if ksl.count == 0 {
		panic("invalid list element count")
	}

	ks.previousKey.nextKey = ks.nextKey
	ks.nextKey.previousKey = ks.previousKey
	ks.previousKey = nil
	ks.nextKey = nil
	ksl.count--
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

	// Pointers to siblings in the keyStateList.
	previousKey *KeyState[TReference, TMetadata]
	nextKey     *KeyState[TReference, TMetadata]

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

	firstEvaluationStart   time.Time
	currentEvaluationStart time.Time
	restarts               uint32
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
