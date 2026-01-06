package evaluation

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"bonanza.build/pkg/crypto/lthash"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	"bonanza.build/pkg/model/core/inlinedtree"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_parser "bonanza.build/pkg/model/parser"
	model_tag "bonanza.build/pkg/model/tag"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_evaluation_pb "bonanza.build/pkg/proto/model/evaluation"
	model_evaluation_cache_pb "bonanza.build/pkg/proto/model/evaluation/cache"
	"bonanza.build/pkg/storage/object"
	bonanza_sync "bonanza.build/pkg/sync"

	"github.com/buildbarn/bb-storage/pkg/clock"
	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// RecursiveComputerEvaluationQueue represents a queue of evaluation
// keys that are currently not blocked and are ready to be evaluated.
//
// Instances of RecursiveComputer can make use of multiple evaluation
// queues. This can be used to enforce that different types of keys are
// evaluated with different amounts of concurrency. For example, keys
// that are CPU intensive to evaluate can be executed with a concurrency
// proportional to the number of locally available CPU cores, while keys
// that perform long-running network requests can use a higher amount of
// concurrency.
type RecursiveComputerEvaluationQueue[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	queuedKeys     keyStateList[TReference, TMetadata]
	queuedKeysWait bonanza_sync.ConditionVariable
}

// NewRecursiveComputerEvaluationQueue creates a new
// RecursiveComputerEvaluationQueue that does not have any queues keys.
func NewRecursiveComputerEvaluationQueue[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata]() *RecursiveComputerEvaluationQueue[TReference, TMetadata] {
	var rceq RecursiveComputerEvaluationQueue[TReference, TMetadata]
	rceq.queuedKeys.init()
	return &rceq
}

// RecursiveComputerEvaluationQueuePicker is used by RecursiveComputer to pick a
// RecursiveComputerEvaluationQueue to which a given key should be assigned.
type RecursiveComputerEvaluationQueuePicker[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] interface {
	PickQueue(typeURL string) *RecursiveComputerEvaluationQueue[TReference, TMetadata]
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
	base                      Computer[TReference, TMetadata]
	evaluationQueuePicker     RecursiveComputerEvaluationQueuePicker[TReference, TMetadata]
	referenceFormat           object.ReferenceFormat
	objectManager             model_core.ObjectManager[TReference, TMetadata]
	tagResolver               model_tag.BoundResolver[TReference]
	actionTagKeyReference     object.LocalReference
	lookupResultReader        model_parser.MessageObjectReader[TReference, *model_evaluation_cache_pb.LookupResult]
	keysReader                model_parser.MessageObjectReader[TReference, []*model_evaluation_pb.Keys]
	cacheDeterministicEncoder model_encoding.DeterministicBinaryEncoder
	cacheKeyedEncoder         model_encoding.KeyedBinaryEncoder
	clock                     clock.Clock

	lock sync.RWMutex

	// Map of all keys that have been requested.
	keys map[object.LocalReference]*KeyState[TReference, TMetadata]

	// Keys which are currently blocked on one or more other keys.
	blockedKeys keyStateList[TReference, TMetadata]

	// Keys that are currently being evaluated, which is at most
	// equal to the number of goroutines calling
	// ProcessNextEvaluatableKey().
	evaluatingKeys keyStateList[TReference, TMetadata]

	// Total number of keys for which evaluation should be attempted.
	evaluatableKeysCount uint64

	evaluatedKeys       keyStateList[TReference, TMetadata]
	shouldStopUploading bool
	evaluatedKeysWait   bonanza_sync.ConditionVariable
	uploadingKeys       keyStateList[TReference, TMetadata]
	completedKeys       keyStateList[TReference, TMetadata]
}

// NewRecursiveComputer creates a new RecursiveComputer that is in the
// initial state (i.e., having no queued or evaluated keys).
func NewRecursiveComputer[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	base Computer[TReference, TMetadata],
	evaluationQueuePicker RecursiveComputerEvaluationQueuePicker[TReference, TMetadata],
	referenceFormat object.ReferenceFormat,
	objectManager model_core.ObjectManager[TReference, TMetadata],
	tagResolver model_tag.BoundResolver[TReference],
	actionTagKeyReference object.LocalReference,
	lookupResultReader model_parser.MessageObjectReader[TReference, *model_evaluation_cache_pb.LookupResult],
	keysReader model_parser.MessageObjectReader[TReference, []*model_evaluation_pb.Keys],
	cacheDeterministicEncoder model_encoding.DeterministicBinaryEncoder,
	cacheKeyedEncoder model_encoding.KeyedBinaryEncoder,
	clock clock.Clock,
) *RecursiveComputer[TReference, TMetadata] {
	rc := &RecursiveComputer[TReference, TMetadata]{
		base:                      base,
		evaluationQueuePicker:     evaluationQueuePicker,
		referenceFormat:           referenceFormat,
		objectManager:             objectManager,
		tagResolver:               tagResolver,
		actionTagKeyReference:     actionTagKeyReference,
		lookupResultReader:        lookupResultReader,
		keysReader:                keysReader,
		cacheDeterministicEncoder: cacheDeterministicEncoder,
		cacheKeyedEncoder:         cacheKeyedEncoder,
		clock:                     clock,

		keys: map[object.LocalReference]*KeyState[TReference, TMetadata]{},
	}
	rc.blockedKeys.init()
	rc.evaluatingKeys.init()
	rc.evaluatedKeys.init()
	rc.uploadingKeys.init()
	rc.completedKeys.init()
	return rc
}

// ProcessNextEvaluatableKey blocks until one or more keys are queued for
// evaluation. After that it will attempt to evaluate it.
func (rc *RecursiveComputer[TReference, TMetadata]) ProcessNextEvaluatableKey(ctx context.Context, rceq *RecursiveComputerEvaluationQueue[TReference, TMetadata]) bool {
	rc.lock.Lock()
	for {
		if !rceq.queuedKeys.empty() {
			// One or more keys are available for evaluation.
			break
		}

		if rc.evaluatableKeysCount == 0 && rc.evaluatingKeys.empty() && !rc.blockedKeys.empty() {
			// If there are no keys queued for evaluation,
			// we are currently evaluating none of them, but
			// there are keys blocking others, it means we
			// have one or more cycles.
			//
			// Due to the way cache lookups are performed,
			// there may be dependencies between keys that
			// after a cache miss and evaluation turn out to
			// be non-existent.
			blockedOn := make(map[*KeyState[TReference, TMetadata]][]*KeyState[TReference, TMetadata], rc.blockedKeys.count)
			for ks := rc.blockedKeys.head.nextKey; ks != &rc.blockedKeys.head; ks = ks.nextKey {
				for _, ksBlocked := range ks.blocking {
					blockedOn[ksBlocked] = append(blockedOn[ksBlocked], ks)
				}
			}

			blockedCyclic := make(map[*KeyState[TReference, TMetadata]]bool, rc.blockedKeys.count)
			disabledCacheLookupOnAKey := false
			for ks := rc.blockedKeys.head.nextKey; ks != &rc.blockedKeys.head; ks = ks.nextKey {
				if ks.isBlockedCyclic(blockedOn, blockedCyclic) && ks.value.disableCacheLookup() {
					rc.forceUnblockKeyState(ks, blockedOn[ks])
					rc.enqueueForEvaluation(ks)
					disabledCacheLookupOnAKey = true
				}
			}

			if !disabledCacheLookupOnAKey {
				ksCyclic := rc.blockedKeys.head.nextKey
				for !ksCyclic.isBlockedCyclic(blockedOn, blockedCyclic) {
					ksCyclic = ksCyclic.nextKey
					if ksCyclic == &rc.blockedKeys.head {
						panic("no cyclic blocked keys found")
					}
				}
				for _, ksBlocked := range ksCyclic.blocking {
					rc.forceUnblockKeyState(ksBlocked, blockedOn[ksBlocked])
					rc.failKeyState(ksBlocked, NestedError[TReference, TMetadata]{
						KeyState: ksCyclic,
						Err:      errors.New("cyclic dependency detected"),
					})
				}
			}
		}

		if err := rceq.queuedKeysWait.Wait(ctx, &rc.lock); err != nil {
			return false
		}
	}

	// Extract the first queued key.
	ks := rceq.queuedKeys.popFirst()
	rc.evaluatableKeysCount--
	rc.evaluatingKeys.pushLast(ks)
	ks.currentEvaluationStart = rc.clock.Now()
	if ks.restarts == 0 {
		ks.firstEvaluationStart = ks.currentEvaluationStart
	}
	rc.lock.Unlock()

	missingDependencies, err := ks.value.compute(ctx, rc, ks)

	rc.lock.Lock()
	rc.evaluatingKeys.remove(ks)
	if ks.err == (errKeyNotEvaluated{}) {
		if err != nil {
			rc.failKeyState(ks, err)
		} else if len(missingDependencies) == 0 {
			ks.err = nil
			rc.evaluatedKeyState(ks)
		} else {
			if len(missingDependencies) == 0 {
				panic("computation stopped because of missing dependencies, but no missing dependencies were returned")
			}
			if ks.blockedCount != 0 {
				panic("key that is currently being evaluated cannot be blocked")
			}
			ks.restarts++
			restartImmediately := true
			for _, ksDep := range missingDependencies {
				if ksDep.err == (errKeyNotEvaluated{}) {
					ksDep.blocking = append(ksDep.blocking, ks)
					if ks.blockedCount == 0 {
						rc.blockedKeys.pushLast(ks)
					}
					ks.blockedCount++
					restartImmediately = false
				}
			}
			if restartImmediately {
				rc.enqueueForEvaluation(ks)
			}
		}
	}
	rc.lock.Unlock()
	return true
}

// ProcessNextUploadableKey processes one of the recently evaluated keys
// and uploads its results into storage, so that subsequent builds can
// reuse cached results.
func (rc *RecursiveComputer[TReference, TMetadata]) ProcessNextUploadableKey(ctx context.Context) (bool, error) {
	rc.lock.Lock()
	for {
		if !rc.evaluatedKeys.empty() {
			// One or more keys are available for uploading.
			break
		}
		if rc.shouldStopUploading {
			rc.lock.Unlock()
			return false, nil
		}

		if err := rc.evaluatedKeysWait.Wait(ctx, &rc.lock); err != nil {
			return false, err
		}
	}

	ks := rc.evaluatedKeys.popFirst()
	rc.uploadingKeys.pushLast(ks)
	rc.lock.Unlock()

	if err := ks.value.upload(ctx, rc, ks); err != nil {
		return false, err
	}

	rc.lock.Lock()
	rc.uploadingKeys.remove(ks)
	rc.completedKeys.pushLast(ks)
	rc.lock.Unlock()
	return true, nil
}

// GracefullyStopUploading can be used to ensure that calls to
// ProcessNextUploadableKey() no longer block, but immediately return if
// no keys need to be uploaded.
func (rc *RecursiveComputer[TReference, TMetadata]) GracefullyStopUploading() {
	rc.lock.Lock()
	rc.shouldStopUploading = true
	rc.evaluatedKeysWait.Broadcast()
	rc.lock.Unlock()
}

func (rc *RecursiveComputer[TReference, TMetadata]) evaluatedKeyState(ks *KeyState[TReference, TMetadata]) {
	for _, ksBlocked := range ks.blocking {
		if ksBlocked.blockedCount == 0 {
			panic("blocked key has invalid blocked count")
		}
		ksBlocked.blockedCount--
		if ksBlocked.blockedCount == 0 {
			rc.blockedKeys.remove(ksBlocked)
			rc.enqueueForEvaluation(ksBlocked)
		}
	}
	ks.blocking = nil

	if ks.evaluatedWait != nil {
		close(ks.evaluatedWait)
		ks.evaluatedWait = nil
	}

	rc.evaluatedKeys.pushLast(ks)
	rc.evaluatedKeysWait.Broadcast()
}

func (rc *RecursiveComputer[TReference, TMetadata]) forceUnblockKeyState(ks *KeyState[TReference, TMetadata], blockedOn []*KeyState[TReference, TMetadata]) {
	if ks.blockedCount != uint(len(blockedOn)) {
		panic("key blocked count does not match the provided number of blockers")
	}
	for _, ksBlocker := range blockedOn {
		ksBlocker.blocking[slices.Index(ksBlocker.blocking, ks)] = ksBlocker.blocking[len(ksBlocker.blocking)-1]
		ksBlocker.blocking[len(ksBlocker.blocking)-1] = nil
		ksBlocker.blocking = ksBlocker.blocking[:len(ksBlocker.blocking)-1]
	}
	ks.blockedCount = 0
	rc.blockedKeys.remove(ks)
}

func (rc *RecursiveComputer[TReference, TMetadata]) failKeyState(ks *KeyState[TReference, TMetadata], err error) {
	ks.value = nil
	ks.err = err
	rc.evaluatedKeyState(ks)
}

func (rc *RecursiveComputer[TReference, TMetadata]) getOrCreateKeyStateLocked(keyReference object.LocalReference, keyMessageFetcher messageFetcher[TReference, TMetadata], initialValueState valueState[TReference, TMetadata], evaluationQueue *RecursiveComputerEvaluationQueue[TReference, TMetadata]) *KeyState[TReference, TMetadata] {
	ks, ok := rc.keys[keyReference]
	if !ok {
		ks = &KeyState[TReference, TMetadata]{
			keyReference:      keyReference,
			keyMessageFetcher: keyMessageFetcher,
			value:             initialValueState,
			evaluationQueue:   evaluationQueue,
			err:               errKeyNotEvaluated{},
		}
		rc.keys[keyReference] = ks
		rc.enqueueForEvaluation(ks)
	}
	return ks
}

func (rc *RecursiveComputer[TReference, TMetadata]) newEnvironment(ctx context.Context, ks *KeyState[TReference, TMetadata]) *recursivelyComputingEnvironment[TReference, TMetadata] {
	return &recursivelyComputingEnvironment[TReference, TMetadata]{
		computer: rc,
		context:  ctx,
		keyState: ks,

		missingDependencies: map[*KeyState[TReference, TMetadata]]struct{}{},
		allDependencies:     map[*KeyState[TReference, TMetadata]]struct{}{},
	}
}

// GetOrCreateKeyState looks up the key state for a given key. If the
// key state does not yet exist, it is created.
func (rc *RecursiveComputer[TReference, TMetadata]) GetOrCreateKeyState(key model_core.TopLevelMessage[*anypb.Any, TReference]) (*KeyState[TReference, TMetadata], error) {
	keyReference, err := model_core.ComputeTopLevelMessageReference(key, rc.referenceFormat)
	if err != nil {
		return nil, err
	}
	keyMessageFetcher := &staticMessageFetcher[TReference, TMetadata]{
		message: key,
	}
	evaluationQueue := rc.evaluationQueuePicker.PickQueue(key.Message.TypeUrl)

	rc.lock.Lock()
	ks := rc.getOrCreateKeyStateLocked(keyReference, keyMessageFetcher, &messageValueState[TReference, TMetadata]{}, evaluationQueue)
	rc.lock.Unlock()
	return ks, nil
}

func (rc *RecursiveComputer[TReference, TMetadata]) GetKeyStateKeyMessage(ctx context.Context, ks *KeyState[TReference, TMetadata]) (model_core.TopLevelMessage[*anypb.Any, TReference], error) {
	return ks.keyMessageFetcher.getAny(ctx, rc)
}

// InjectKeyState overrides the value for a given key. This prevents the
// key from getting evaluated, and causes evaluation of keys that depend
// on it to receive the injected value.
func (rc *RecursiveComputer[TReference, TMetadata]) InjectKeyState(keyReference object.LocalReference, value model_core.TopLevelMessage[*anypb.Any, TReference]) error {
	rc.lock.Lock()
	if _, ok := rc.keys[keyReference]; !ok {
		rc.keys[keyReference] = &KeyState[TReference, TMetadata]{
			keyReference: keyReference,
			value: &messageValueState[TReference, TMetadata]{
				isOverride: true,
				valueFetcher: &staticMessageFetcher[TReference, TMetadata]{
					message: value,
				},
			},
		}
	}
	rc.lock.Unlock()
	return nil
}

// WaitForEvaluation blocks until a given key has evaluated. Once
// evaluated, any errors evaluating the key are returned.
func (rc *RecursiveComputer[TReference, TMetadata]) WaitForEvaluation(ctx context.Context, ks *KeyState[TReference, TMetadata]) error {
	rc.lock.Lock()
	if ks.err == (errKeyNotEvaluated{}) {
		// Key has not finished evaluating. Wait for it to
		// finish. As we only tend to wait on a very small
		// number of keys, a channel is not created by default.
		if ks.evaluatedWait == nil {
			ks.evaluatedWait = make(chan struct{})
		}
		evaluatedWait := ks.evaluatedWait
		rc.lock.Unlock()
		select {
		case <-evaluatedWait:
		case <-ctx.Done():
			return util.StatusFromContext(ctx)
		}
	} else {
		rc.lock.Unlock()
	}
	return ks.err
}

// GetProgress returns a Protobuf message containing counters on the
// number of keys that have been evaluated, are currently queued, or are
// currently blocked on other keys. In addition to that, it returns the
// list of keys that are currently being evaluated. This message can be
// returned to clients to display progress.
func (rc *RecursiveComputer[TReference, TMetadata]) GetProgress(ctx context.Context) (model_core.PatchedMessage[*model_evaluation_pb.Progress, TMetadata], error) {
	rc.lock.Lock()
	evaluatingKeys := make([]*KeyState[TReference, TMetadata], 0, rc.evaluatingKeys.count)
	for ks := rc.evaluatingKeys.head.nextKey; ks != &rc.evaluatingKeys.head; ks = ks.nextKey {
		evaluatingKeys = append(evaluatingKeys, ks)
	}
	rc.lock.Unlock()

	return model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (*model_evaluation_pb.Progress, error) {
		// TODO: Set additional_evaluating_keys_count if we have
		// too many keys to fit in a message.
		evaluatingKeysMessages := make([]*model_evaluation_pb.Progress_EvaluatingKey, 0, len(evaluatingKeys))
		for _, ks := range evaluatingKeys {
			key, err := ks.keyMessageFetcher.getAny(ctx, rc)
			if err != nil {
				return nil, err
			}
			anyKey, err := model_core.MarshalAny(model_core.Patch(rc.objectManager, key.Decay()))
			if err != nil {
				return nil, err
			}
			evaluatingKeysMessages = append(evaluatingKeysMessages, &model_evaluation_pb.Progress_EvaluatingKey{
				Key:                    anyKey.Merge(patcher),
				FirstEvaluationStart:   timestamppb.New(ks.firstEvaluationStart),
				CurrentEvaluationStart: timestamppb.New(ks.currentEvaluationStart),
				Restarts:               ks.restarts,
			})
		}
		return &model_evaluation_pb.Progress{
			BlockedKeysCount:     rc.blockedKeys.count,
			EvaluatableKeysCount: rc.evaluatableKeysCount,
			OldestEvaluatingKeys: evaluatingKeysMessages,
			EvaluatedKeysCount:   rc.evaluatedKeys.count,
			UploadingKeysCount:   rc.uploadingKeys.count,
			CompletedKeysCount:   rc.completedKeys.count,
		}, nil
	})
}

func (rc *RecursiveComputer[TReference, TMetadata]) enqueueForEvaluation(ks *KeyState[TReference, TMetadata]) {
	rceq := ks.evaluationQueue
	rceq.queuedKeys.pushLast(ks)
	rc.evaluatableKeysCount++
	rceq.queuedKeysWait.Broadcast()
}

func (rc *RecursiveComputer[TReference, TMetadata]) getEvaluationsParentNodeComputer(ctx context.Context) btree.ParentNodeComputer[*model_evaluation_pb.Evaluations, TMetadata] {
	return btree.Capturing(ctx, rc.objectManager, func(createdObject model_core.Decodable[model_core.MetadataEntry[TMetadata]], childNodes model_core.Message[[]*model_evaluation_pb.Evaluations, object.LocalReference]) model_core.PatchedMessage[*model_evaluation_pb.Evaluations, TMetadata] {
		var firstKeyReference []byte
		switch firstEntry := childNodes.Message[0].Level.(type) {
		case *model_evaluation_pb.Evaluations_Leaf_:
			firstKeyReference = firstEntry.Leaf.KeyReference
		case *model_evaluation_pb.Evaluations_Parent_:
			firstKeyReference = firstEntry.Parent.FirstKeyReference
		}
		return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_evaluation_pb.Evaluations {
			return &model_evaluation_pb.Evaluations{
				Level: &model_evaluation_pb.Evaluations_Parent_{
					Parent: &model_evaluation_pb.Evaluations_Parent{
						Reference:         patcher.AddDecodableReference(createdObject),
						FirstKeyReference: firstKeyReference,
					},
				},
			}
		})
	})
}

func (rc *RecursiveComputer[TReference, TMetadata]) getEvaluationsForSortedList(ctx context.Context, sortedKeyStates []*KeyState[TReference, TMetadata]) (model_core.PatchedMessage[[]*model_evaluation_pb.Evaluations, TMetadata], error) {
	evaluationsBuilder := btree.NewHeightAwareBuilder(
		btree.NewProllyChunkerFactory[TMetadata](
			/* minimumSizeBytes = */ 1<<16,
			/* maximumSizeBytes = */ 1<<18,
			/* isParent = */ func(evaluations *model_evaluation_pb.Evaluations) bool {
				return evaluations.GetParent() != nil
			},
		),
		btree.NewObjectCreatingNodeMerger(
			rc.cacheDeterministicEncoder,
			rc.referenceFormat,
			rc.getEvaluationsParentNodeComputer(ctx),
		),
	)
	defer evaluationsBuilder.Discard()

	for _, ks := range sortedKeyStates {
		if ks.fetchGraphlet == nil {
			patchedGraphlet, err := ks.value.buildGraphlet(ctx, rc)
			if err != nil {
				return model_core.PatchedMessage[[]*model_evaluation_pb.Evaluations, TMetadata]{}, err
			}
			graphlet := model_core.Unpatch(rc.objectManager, patchedGraphlet).Decay()
			ks.fetchGraphlet = func() (model_core.Message[*model_evaluation_pb.Graphlet, TReference], error) {
				return graphlet, nil
			}
		}
		graphlet, err := ks.fetchGraphlet()
		if err != nil {
			return model_core.PatchedMessage[[]*model_evaluation_pb.Evaluations, TMetadata]{}, err
		}
		if err := evaluationsBuilder.PushChild(
			model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_evaluation_pb.Evaluations {
				return &model_evaluation_pb.Evaluations{
					Level: &model_evaluation_pb.Evaluations_Leaf_{
						Leaf: &model_evaluation_pb.Evaluations_Leaf{
							KeyReference: ks.keyReference.GetRawReference(),
							Graphlet:     model_core.Patch(rc.objectManager, graphlet).Merge(patcher),
						},
					},
				}
			}),
		); err != nil {
			return model_core.PatchedMessage[[]*model_evaluation_pb.Evaluations, TMetadata]{}, err
		}
	}

	return evaluationsBuilder.FinalizeList()
}

func (rc *RecursiveComputer[TReference, TMetadata]) GetEvaluations(ctx context.Context, keyStates []*KeyState[TReference, TMetadata]) (model_core.PatchedMessage[[]*model_evaluation_pb.Evaluations, TMetadata], error) {
	rc.lock.Lock()
	defer rc.lock.Unlock()

	topLevelKeyStates := make(map[*KeyState[TReference, TMetadata]]struct{}, len(keyStates))
	// TODO: Gather nested top-level key states.
	for _, ks := range keyStates {
		topLevelKeyStates[ks] = struct{}{}
	}

	return rc.getEvaluationsForSortedList(ctx, sortedKeyStates(topLevelKeyStates))
}

type recursivelyComputingEnvironment[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	// Constant fields.
	computer *RecursiveComputer[TReference, TMetadata]
	context  context.Context
	keyState *KeyState[TReference, TMetadata]

	err                 error
	missingDependencies map[*KeyState[TReference, TMetadata]]struct{}
	allDependencies     map[*KeyState[TReference, TMetadata]]struct{}
}

func (e *recursivelyComputingEnvironment[TReference, TMetadata]) setError(err error) {
	if e.err == nil {
		e.err = err
	}
}

func (e *recursivelyComputingEnvironment[TReference, TMetadata]) getMissingDependenciesOrError() ([]*KeyState[TReference, TMetadata], error) {
	if e.err != nil {
		return nil, e.err
	}
	if len(e.missingDependencies) != 0 {
		return slices.Collect(maps.Keys(e.missingDependencies)), nil
	}
	panic("no missing dependencies observed, and no error value is present")
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
	key, err := model_core.MarshalTopLevelAny(model_core.Unpatch(rc.objectManager, patchedKey))
	if err != nil {
		panic(err)
	}
	keyReference, err := model_core.ComputeTopLevelMessageReference(key, rc.referenceFormat)
	if err != nil {
		panic(err)
	}
	keyMessageFetcher := &staticMessageFetcher[TReference, TMetadata]{
		message: key,
	}

	evaluationQueue := rc.evaluationQueuePicker.PickQueue(key.Message.TypeUrl)

	rc.lock.Lock()
	defer rc.lock.Unlock()

	ks := rc.getOrCreateKeyStateLocked(keyReference, keyMessageFetcher, initialValueState, evaluationQueue)
	e.allDependencies[ks] = struct{}{}
	if ks.err != nil {
		if ks.err == (errKeyNotEvaluated{}) {
			e.missingDependencies[ks] = struct{}{}
		} else {
			e.setError(NestedError[TReference, TMetadata]{
				KeyState: ks,
				Err:      ks.err,
			})
		}
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
		e.setError(errors.New("key does not yield a message value"))
		return model_core.Message[proto.Message, TReference]{}
	}
	v, err := mvs.valueFetcher.getNative(e.context, e.computer)
	if err != nil {
		e.setError(err)
		return model_core.Message[proto.Message, TReference]{}
	}
	return v.Decay()
}

func (e *recursivelyComputingEnvironment[TReference, TMetadata]) GetNativeValue(patchedKey model_core.PatchedMessage[proto.Message, TMetadata]) (any, bool) {
	vs := e.getValueState(patchedKey, &nativeValueState[TReference, TMetadata]{})
	if vs == nil {
		return nil, false
	}
	nvs, ok := vs.(*nativeValueState[TReference, TMetadata])
	if !ok {
		e.setError(errors.New("key does not yield a native value"))
		return model_core.Message[proto.Message, TReference]{}, false
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

// messageFetcher is called into to reobtain the Protobuf message
// associated with an evaluation key or value.
//
// In cases where builds experience high cache hit rates, there's no
// need to keep the actual evaluation keys and values in memory, as
// their references (hashes) are sufficient for performing cache
// lookups. However, if keys need to be evaluated or graphlets are
// generated, the full keys need to be known. In those cases
// messageFetcher is invoked.
type messageFetcher[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] interface {
	getAny(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata]) (model_core.TopLevelMessage[*anypb.Any, TReference], error)
	getNative(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata]) (model_core.TopLevelMessage[proto.Message, TReference], error)
}

type staticMessageFetcher[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	message model_core.TopLevelMessage[*anypb.Any, TReference]
}

func (kf *staticMessageFetcher[TReference, TMetadata]) getAny(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata]) (model_core.TopLevelMessage[*anypb.Any, TReference], error) {
	return kf.message, nil
}

func (kf *staticMessageFetcher[TReference, TMetadata]) getNative(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata]) (model_core.TopLevelMessage[proto.Message, TReference], error) {
	return model_core.UnmarshalTopLevelAnyNew(kf.message)
}

// KeyState contains all of the evaluation state of RecursiveComputer
// for a given key. If evaluation has not yet finished, it stores the
// list of keys that are currently blocked on it (i.e., its reverse
// dependencies). When evaluated, it stores the value associated with
// the key or any error that occurred computing it.
type KeyState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	// Constant fields.
	keyReference      object.LocalReference
	keyMessageFetcher messageFetcher[TReference, TMetadata]

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

	evaluationQueue *RecursiveComputerEvaluationQueue[TReference, TMetadata]
	evaluatedWait   chan struct{}
	value           valueState[TReference, TMetadata]
	err             error
	fetchGraphlet   func() (model_core.Message[*model_evaluation_pb.Graphlet, TReference], error)

	firstEvaluationStart   time.Time
	currentEvaluationStart time.Time
	restarts               uint32
}

func sortedKeyStates[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](keyStates map[*KeyState[TReference, TMetadata]]struct{}) []*KeyState[TReference, TMetadata] {
	return slices.SortedFunc(
		maps.Keys(keyStates),
		func(a, b *KeyState[TReference, TMetadata]) int {
			return bytes.Compare(a.keyReference.GetRawReference(), b.keyReference.GetRawReference())
		},
	)
}

func (ks *KeyState[TReference, TMetadata]) getMessageDependenciesAt(
	dependencies map[*KeyState[TReference, TMetadata]]struct{},
	keysVisited map[*KeyState[TReference, TMetadata]]struct{},
	skipOver skipOverKeyFunc[TReference, TMetadata],
) {
	if _, ok := keysVisited[ks]; !ok {
		keysVisited[ks] = struct{}{}
		ks.value.getMessageDependenciesAt(ks, dependencies, keysVisited, skipOver)
	}
}

func (ks *KeyState[TReference, TMetadata]) isBlockedCyclic(blockedOn map[*KeyState[TReference, TMetadata]][]*KeyState[TReference, TMetadata], cyclic map[*KeyState[TReference, TMetadata]]bool) bool {
	if v, ok := cyclic[ks]; ok {
		return v
	}

	cyclic[ks] = true
	for _, ksBlocker := range blockedOn[ks] {
		if ksBlocker.isBlockedCyclic(blockedOn, cyclic) {
			return true
		}
	}
	cyclic[ks] = false
	return false
}

func (KeyState[TReference, TMetadata]) getMessageDependenciesBelow(
	dependencies map[*KeyState[TReference, TMetadata]]struct{},
	keysVisited map[*KeyState[TReference, TMetadata]]struct{},
	skipOver skipOverKeyFunc[TReference, TMetadata],
) {
	panic("TODO")
}

type valueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] interface {
	compute(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) ([]*KeyState[TReference, TMetadata], error)
	upload(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) error
	getMessageDependenciesAt(
		ks *KeyState[TReference, TMetadata],
		dependencies map[*KeyState[TReference, TMetadata]]struct{},
		keysVisited map[*KeyState[TReference, TMetadata]]struct{},
		skipOver skipOverKeyFunc[TReference, TMetadata],
	)
	disableCacheLookup() bool
	buildGraphlet(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata]) (model_core.PatchedMessage[*model_evaluation_pb.Graphlet, TMetadata], error)
}

type messageValueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	triedCacheLookup bool

	isOverride                      bool
	valueFetcher                    messageFetcher[TReference, TMetadata]
	directDependencies              []*KeyState[TReference, TMetadata]
	dependenciesHashRecordReference object.LocalReference
}

func (vs *messageValueState[TReference, TMetadata]) compute(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) ([]*KeyState[TReference, TMetadata], error) {
	if !vs.triedCacheLookup {
		tagKeyData, _ := model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.NoopReferenceMetadata]) *model_evaluation_cache_pb.LookupTagKeyData {
			result := &model_evaluation_cache_pb.LookupTagKeyData{
				ActionTagKeyReference: patcher.AddReference(model_core.MetadataEntry[model_core.NoopReferenceMetadata]{
					LocalReference: rc.actionTagKeyReference,
				}),
				EvaluationKeyReference: patcher.AddReference(model_core.MetadataEntry[model_core.NoopReferenceMetadata]{
					LocalReference: ks.keyReference,
				}),
			}
			return result
		}).SortAndSetReferences()
		tagKeyHash, err := model_tag.NewDecodableKeyHashFromMessage(tagKeyData, rc.lookupResultReader.GetDecodingParametersSizeBytes())
		if err != nil {
			return nil, err
		}
		if rootCacheResultReference, err := model_tag.ResolveDecodableTag(ctx, rc.tagResolver, tagKeyHash); err == nil {
			if lookupResult, err := rc.lookupResultReader.ReadObject(ctx, rootCacheResultReference); err == nil {
				switch r := lookupResult.Message.Result.(type) {
				case *model_evaluation_cache_pb.LookupResult_Initial_:
					var dependencyKeyReferences []object.LocalReference
					var errIter error
					for dependency := range btree.AllLeaves(
						ctx,
						rc.keysReader,
						model_core.Nested(lookupResult, r.Initial.GraphletDependencyKeys),
						/* traverser = */ func(keys model_core.Message[*model_evaluation_pb.Keys, TReference]) (*model_core_pb.DecodableReference, error) {
							return keys.Message.GetParent().GetReference(), nil
						},
						&errIter,
					) {
						dependencyLeaf, ok := dependency.Message.Level.(*model_evaluation_pb.Keys_Leaf)
						if !ok {
							return nil, errors.New("dependency is not a valid leaf")
						}
						dependencyKey, err := model_core.FlattenAny(model_core.Nested(dependency, dependencyLeaf.Leaf))
						if err != nil {
							return nil, errors.New("cannot flatten dependency")
						}
						dependencyKeyReference, err := model_core.ComputeTopLevelMessageReference(dependencyKey, rc.referenceFormat)
						if err != nil {
							return nil, errors.New("cannot compute dependency key reference")
						}
						dependencyKeyReferences = append(dependencyKeyReferences, dependencyKeyReference)
					}
					if errIter != nil {
						return nil, err
					}

					dependenciesHasher := lthash.NewHasher()
					var unevaluatedDependencies []*KeyState[TReference, TMetadata]
					missingDependencyIndices := make([]int, 0, len(dependencyKeyReferences))
					rc.lock.RLock()
					for i, dependencyKeyReference := range dependencyKeyReferences {
						if ksDep, ok := rc.keys[dependencyKeyReference]; ok {
							mvsDep, ok := ksDep.value.(*messageValueState[TReference, TMetadata])
							if !ok {
								rc.lock.RUnlock()
								return nil, errors.New("dependency does not yield a message value")
							}
							if mvsDep.valueFetcher == nil {
								unevaluatedDependencies = append(unevaluatedDependencies, ksDep)
							} else {
								dependenciesHasher.Add(mvsDep.dependenciesHashRecordReference.GetRawReference())
							}
						} else {
							missingDependencyIndices = append(missingDependencyIndices, i)
						}
					}
					rc.lock.RUnlock()

					if len(missingDependencyIndices) > 0 {
						var errIter error
						i := 0
						for dependency := range btree.AllLeaves(
							ctx,
							rc.keysReader,
							model_core.Nested(lookupResult, r.Initial.GraphletDependencyKeys),
							/* traverser = */ func(keys model_core.Message[*model_evaluation_pb.Keys, TReference]) (*model_core_pb.DecodableReference, error) {
								return keys.Message.GetParent().GetReference(), nil
							},
							&errIter,
						) {
							if i == missingDependencyIndices[0] {
								dependencyLeaf, ok := dependency.Message.Level.(*model_evaluation_pb.Keys_Leaf)
								if !ok {
									return nil, errors.New("dependency is not a valid leaf")
								}
								dependencyKey, err := model_core.FlattenAny(model_core.Nested(dependency, dependencyLeaf.Leaf))
								if err != nil {
									return nil, errors.New("cannot flatten dependency")
								}
								dependencyKeyReference, err := model_core.ComputeTopLevelMessageReference(dependencyKey, rc.referenceFormat)
								if err != nil {
									return nil, errors.New("cannot compute dependency key reference")
								}
								// TODO: We should have one that reloads the message from storage.
								dependencyKeyMessageFetcher := &staticMessageFetcher[TReference, TMetadata]{
									message: dependencyKey,
								}

								evaluationQueue := rc.evaluationQueuePicker.PickQueue(dependencyKey.Message.TypeUrl)
								rc.lock.Lock()
								ksDep := rc.getOrCreateKeyStateLocked(
									dependencyKeyReference,
									dependencyKeyMessageFetcher,
									// TODO: This might need to use nativeValueState.
									&messageValueState[TReference, TMetadata]{},
									evaluationQueue,
								)
								rc.lock.Unlock()
								unevaluatedDependencies = append(unevaluatedDependencies, ksDep)

								missingDependencyIndices = missingDependencyIndices[1:]
								if len(missingDependencyIndices) == 0 {
									break
								}
							}
							i++
						}
						if errIter != nil {
							return nil, err
						}
					}

					if len(unevaluatedDependencies) > 0 {
						return unevaluatedDependencies, nil
					}

					return nil, errors.New("got initial result")
				case *model_evaluation_cache_pb.LookupResult_HitValue:
					// It turns out the current key does
					// not have any dependencies. This
					// means that the initial lookup
					// returned a value immediately.
					cachedValue, err := model_core.FlattenAny(model_core.Nested(lookupResult, r.HitValue))
					if err != nil {
						return nil, err
					}
					vs.valueFetcher = &staticMessageFetcher[TReference, TMetadata]{
						message: cachedValue,
					}
					if err := vs.setDependenciesHashRecordReference(rc, ks, cachedValue); err != nil {
						return nil, err
					}
					return nil, nil
				}
			} else if status.Code(err) != codes.NotFound {
				return nil, err
			}
		} else if status.Code(err) != codes.NotFound {
			return nil, err
		}
		vs.triedCacheLookup = true
	}

	key, err := ks.keyMessageFetcher.getNative(ctx, rc)
	if err != nil {
		return nil, err
	}

	e := rc.newEnvironment(ctx, ks)
	value, err := rc.base.ComputeMessageValue(ctx, key.Decay(), e)
	if err != nil {
		if errors.Is(err, ErrMissingDependency) {
			return e.getMissingDependenciesOrError()
		}
		return nil, err
	}
	anyValue, err := model_core.MarshalTopLevelAny(model_core.Unpatch(rc.objectManager, value))
	if err != nil {
		return nil, fmt.Errorf("failed to marshal value yielded by evaluation function: %w", err)
	}
	vs.valueFetcher = &staticMessageFetcher[TReference, TMetadata]{
		message: anyValue,
	}
	vs.directDependencies = sortedKeyStates(e.allDependencies)
	if err := vs.setDependenciesHashRecordReference(rc, ks, anyValue); err != nil {
		return nil, err
	}
	return nil, nil
}

func (vs *messageValueState[TReference, TMetadata]) setDependenciesHashRecordReference(rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata], value model_core.TopLevelMessage[*anypb.Any, TReference]) error {
	valueReference, err := model_core.ComputeTopLevelMessageReference(value, rc.referenceFormat)
	if err != nil {
		return err
	}
	dependenciesHashRecord, _ := model_core.MustBuildPatchedMessage(
		func(patcher *model_core.ReferenceMessagePatcher[model_core.NoopReferenceMetadata]) *model_evaluation_cache_pb.DependenciesHashRecord {
			return &model_evaluation_cache_pb.DependenciesHashRecord{
				KeyReference: patcher.AddReference(model_core.MetadataEntry[model_core.NoopReferenceMetadata]{
					LocalReference: ks.keyReference,
				}),
				Value: &model_evaluation_cache_pb.DependenciesHashRecord_MessageValueReference{
					MessageValueReference: patcher.AddReference(model_core.MetadataEntry[model_core.NoopReferenceMetadata]{
						LocalReference: valueReference,
					}),
				},
			}
		},
	).SortAndSetReferences()
	dependenciesHashRecordReference, err := model_core.ComputeTopLevelMessageReference(dependenciesHashRecord, rc.referenceFormat)
	if err != nil {
		return err
	}
	vs.dependenciesHashRecordReference = dependenciesHashRecordReference
	return nil
}

/*
func (messageValueState[TReference, TMetadata]) x(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata], dependencies map[*KeyState[TReference, TMetadata]]struct{}) (inlinedtree.Candidate[*model_evaluation_cache_pb.LookupResult, TMetadata], error) {
	dependencyKeysBuilder := newKeysBTreeBuilder(ctx, rc.objectManager, rc.referenceFormat, rc.cacheDeterministicEncoder)
	defer dependencyKeysBuilder.Discard()

	for _, ksDep := range slices.SortedFunc(
		maps.Keys(dependencies),
		func(a, b *KeyState[TReference, TMetadata]) int {
			return bytes.Compare(a.keyReference.GetRawReference(), b.keyReference.GetRawReference())
		},
	) {
		dependencyKey, err := model_core.BuildPatchedMessage(
			func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (*model_evaluation_pb.Keys, error) {
				keyAny, err := model_core.MarshalAny(model_core.Patch(rc.objectManager, ksDep.key.Decay()))
				if err != nil {
					return nil, err
				}
				return &model_evaluation_pb.Keys{
					Level: &model_evaluation_pb.Keys_Leaf{
						Leaf: keyAny.Merge(patcher),
					},
				}, nil
			},
		)
		if err != nil {
			return inlinedtree.Candidate[*model_evaluation_cache_pb.LookupResult, TMetadata]{}, err
		}
		if err := dependencyKeysBuilder.PushChild(dependencyKey); err != nil {
			return inlinedtree.Candidate[*model_evaluation_cache_pb.LookupResult, TMetadata]{}, err
		}
	}

	dependencyKeysList, err := dependencyKeysBuilder.FinalizeList()
	if err != nil {
		return inlinedtree.Candidate[*model_evaluation_cache_pb.LookupResult, TMetadata]{}, err
	}

	return inlinedtree.Candidate{
		ExternalMessage: xyz,
		Encoder:         rc.blaEncoder,
		ParentAppender: inlinedtree.Capturing(
			ctx,
			objectCapturer,
			func(parent model_core.PatchedMessage[*model_evaluation_cache_pb.LookupResult, TMetadata], externalObject *model_core.Decodable[model_core.MetadataEntry[TMetadata]]) {
			},
		),
	}, nil
}
*/

func (vs *messageValueState[TReference, TMetadata]) upload(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) error {
	key, err := ks.keyMessageFetcher.getNative(ctx, rc)
	if err != nil {
		return err
	}

	// TODO: This should be skipped if the value we obtained
	// actually came from the cache.
	if !vs.isOverride && !rc.base.IsLookup(key.Message) {
		return errors.New("implement uploading")
		/*
			// Gather dependencies of the current node using different
			// levels of granularity.
			skipOverKeyFuncs := []skipOverKeyFunc[TReference, TMetadata]{
				func(ksDep *KeyState[TReference, TMetadata], vsDep *messageValueState[TReference, TMetadata]) bool {
					// Coarse: skip over keys whose reference is
					// higher than the current one, except for
					// ones that we can't skip over.
					return bytes.Compare(ksDep.keyReference.GetRawReference(), ks.keyReference.GetRawReference()) >= 0 &&
						!vsDep.isOverride && !rc.base.IsLookup(ksDep.key.Message)
				},
				func(ksDep *KeyState[TReference, TMetadata], vsDep *messageValueState[TReference, TMetadata]) bool {
					// Fine-grained: only skip over keys that
					// yielded a native value.
					return false
				},
			}
			dependenciesLevels := make([]map[*KeyState[TReference, TMetadata]]struct{}, 0, len(skipOverKeyFuncs))
			for _, skipOver := range skipOverKeyFuncs {
				dependencies := map[*KeyState[TReference, TMetadata]]struct{}{}
				keysVisited := map[*KeyState[TReference, TMetadata]]struct{}{}
				// TODO: This should memoize, so that we don't
				// walk the same graph every time.
				ks.getMessageDependenciesBelow(dependencies, keysVisited, skipOver)
				dependenciesLevels = append(dependenciesLevels, dependencies)
			}

			if vs.gotRootCacheResultReference {
				return errors.New("TODO: implement merging of cache results")
			}

			for _, dependencies := range dependenciesLevels {
			}

			anyKey, _ := model_core.MarshalTopLevelAny(ks.key)
			return fmt.Errorf("TODO: upload message value state with %d dependencies %s", len(dependenciesLevels), protojson.Format(anyKey.Message))
		*/
	}
	return nil
}

type skipOverKeyFunc[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] func(
	ks *KeyState[TReference, TMetadata],
	vs *messageValueState[TReference, TMetadata],
) bool

func (vs *messageValueState[TReference, TMetadata]) getMessageDependenciesAt(
	ks *KeyState[TReference, TMetadata],
	dependencies map[*KeyState[TReference, TMetadata]]struct{},
	keysVisited map[*KeyState[TReference, TMetadata]]struct{},
	skipOver skipOverKeyFunc[TReference, TMetadata],
) {
	if skipOver(ks, vs) {
		ks.getMessageDependenciesBelow(dependencies, keysVisited, skipOver)
	} else {
		dependencies[ks] = struct{}{}
	}
}

func (vs *messageValueState[TReference, TMetadata]) disableCacheLookup() bool {
	if vs.triedCacheLookup {
		return false
	}
	vs.triedCacheLookup = true
	return true
}

func (vs *messageValueState[TReference, TMetadata]) buildGraphlet(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata]) (model_core.PatchedMessage[*model_evaluation_pb.Graphlet, TMetadata], error) {
	inlineCandidates := make(inlinedtree.CandidateList[*model_evaluation_pb.Graphlet, TMetadata], 0, 1)
	defer inlineCandidates.Discard()

	// Attach the computed value, if any.
	if vs.valueFetcher != nil {
		value, err := vs.valueFetcher.getAny(ctx, rc)
		if err != nil {
			return model_core.PatchedMessage[*model_evaluation_pb.Graphlet, TMetadata]{}, err
		}
		patchedValue := model_core.Patch(rc.objectManager, model_core.WrapTopLevelAny(value).Decay())
		inlineCandidates = append(inlineCandidates, inlinedtree.AlwaysInline(
			patchedValue.Patcher,
			func(graphlet model_core.PatchedMessage[*model_evaluation_pb.Graphlet, TMetadata]) {
				graphlet.Message.Value = patchedValue.Message
			},
		))
	}

	// Attach the direct dependencies.
	directDependenciesParentNodeComputer := btree.Capturing(ctx, rc.objectManager, func(createdObject model_core.Decodable[model_core.MetadataEntry[TMetadata]], childNodes model_core.Message[[]*model_evaluation_pb.Keys, object.LocalReference]) model_core.PatchedMessage[*model_evaluation_pb.Keys, TMetadata] {
		return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_evaluation_pb.Keys {
			return &model_evaluation_pb.Keys{
				Level: &model_evaluation_pb.Keys_Parent_{
					Parent: &model_evaluation_pb.Keys_Parent{
						Reference: patcher.AddDecodableReference(createdObject),
					},
				},
			}
		})
	})
	directDependenciesBuilder := btree.NewHeightAwareBuilder(
		btree.NewProllyChunkerFactory[TMetadata](
			/* minimumSizeBytes = */ 1<<16,
			/* maximumSizeBytes = */ 1<<18,
			/* isParent = */ func(keys *model_evaluation_pb.Keys) bool {
				return keys.GetParent() != nil
			},
		),
		btree.NewObjectCreatingNodeMerger(
			rc.cacheDeterministicEncoder,
			rc.referenceFormat,
			directDependenciesParentNodeComputer,
		),
	)
	defer directDependenciesBuilder.Discard()

	for _, ksDep := range vs.directDependencies {
		directDependency, err := model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (*model_evaluation_pb.Keys, error) {
			key, err := ksDep.keyMessageFetcher.getAny(ctx, rc)
			if err != nil {
				return nil, err
			}
			return &model_evaluation_pb.Keys{
				Level: &model_evaluation_pb.Keys_Leaf{
					Leaf: model_core.Patch(
						rc.objectManager,
						model_core.WrapTopLevelAny(key).Decay(),
					).Merge(patcher),
				},
			}, nil
		})
		if err != nil {
			return model_core.PatchedMessage[*model_evaluation_pb.Graphlet, TMetadata]{}, err
		}
		if err := directDependenciesBuilder.PushChild(directDependency); err != nil {
			return model_core.PatchedMessage[*model_evaluation_pb.Graphlet, TMetadata]{}, err
		}
	}

	directDependenciesList, err := directDependenciesBuilder.FinalizeList()
	if err != nil {
		return model_core.PatchedMessage[*model_evaluation_pb.Graphlet, TMetadata]{}, err
	}
	inlineCandidates = append(inlineCandidates, inlinedtree.Candidate[*model_evaluation_pb.Graphlet, TMetadata]{
		ExternalMessage: model_core.ProtoListToBinaryMarshaler(directDependenciesList),
		Encoder:         rc.cacheDeterministicEncoder,
		ParentAppender: func(
			graphlet model_core.PatchedMessage[*model_evaluation_pb.Graphlet, TMetadata],
			externalObject *model_core.Decodable[model_core.CreatedObject[TMetadata]],
		) error {
			directDependencies, err := btree.MaybeMergeNodes(
				directDependenciesList.Message,
				externalObject,
				graphlet.Patcher,
				directDependenciesParentNodeComputer,
			)
			if err != nil {
				return err
			}
			graphlet.Message.DirectDependencyKeys = directDependencies
			return nil
		},
	})

	// Attach evaluations of direct and transitive dependencies that
	// did not get promoted to the parent.
	// TODO: Use the correct set of dependencies!
	evaluationsParentNodeComputer := rc.getEvaluationsParentNodeComputer(ctx)
	dependencyEvaluationsList, err := rc.getEvaluationsForSortedList(ctx, vs.directDependencies)
	inlineCandidates = append(inlineCandidates, inlinedtree.Candidate[*model_evaluation_pb.Graphlet, TMetadata]{
		ExternalMessage: model_core.ProtoListToBinaryMarshaler(dependencyEvaluationsList),
		Encoder:         rc.cacheDeterministicEncoder,
		ParentAppender: func(
			graphlet model_core.PatchedMessage[*model_evaluation_pb.Graphlet, TMetadata],
			externalObject *model_core.Decodable[model_core.CreatedObject[TMetadata]],
		) error {
			dependencyEvaluations, err := btree.MaybeMergeNodes(
				dependencyEvaluationsList.Message,
				externalObject,
				graphlet.Patcher,
				evaluationsParentNodeComputer,
			)
			if err != nil {
				return err
			}
			graphlet.Message.DependencyEvaluations = dependencyEvaluations
			return nil
		},
	})

	return inlinedtree.Build(
		inlineCandidates,
		&inlinedtree.Options{
			ReferenceFormat:  rc.referenceFormat,
			MaximumSizeBytes: 1 << 16,
		},
	)
}

type nativeValueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	value any
}

func (vs *nativeValueState[TReference, TMetadata]) compute(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) ([]*KeyState[TReference, TMetadata], error) {
	key, err := ks.keyMessageFetcher.getNative(ctx, rc)
	if err != nil {
		return nil, err
	}

	e := rc.newEnvironment(ctx, ks)
	value, err := rc.base.ComputeNativeValue(ctx, key.Decay(), e)
	if err != nil {
		if errors.Is(err, ErrMissingDependency) {
			return e.getMissingDependenciesOrError()
		}
		return nil, err
	}
	vs.value = value
	return nil, nil
}

func (nativeValueState[TReference, TMetadata]) upload(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) error {
	// There is no way to cache native values.
	return nil
}

func (nativeValueState[TReference, TMetadata]) getMessageDependenciesAt(
	ks *KeyState[TReference, TMetadata],
	dependencies map[*KeyState[TReference, TMetadata]]struct{},
	keysVisited map[*KeyState[TReference, TMetadata]]struct{},
	skipOver skipOverKeyFunc[TReference, TMetadata],
) {
	panic("TODO")
}

func (nativeValueState[TReference, TMetadata]) disableCacheLookup() bool {
	return false
}

func (nativeValueState[TReference, TMetadata]) buildGraphlet(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata]) (model_core.PatchedMessage[*model_evaluation_pb.Graphlet, TMetadata], error) {
	return model_core.PatchedMessage[*model_evaluation_pb.Graphlet, TMetadata]{}, errors.New("TODO: build graphlet for native values")
}

// errKeyNotEvaluated is a placeholder value that is assigned to
// KeyState.err to indicate that evaluation of a given key is not yet
// finished.
type errKeyNotEvaluated struct{}

func (errKeyNotEvaluated) Error() string {
	panic("attempted to use this type as an actual error object")
}

// NestedError is used to wrap errors that occurred while evaluating a
// dependency of a given key. The key of the dependency is included,
// meaning that repeated unwrapping can be used to obtain a stack trace.
type NestedError[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	KeyState *KeyState[TReference, TMetadata]
	Err      error
}

func (e NestedError[TReference, TMetadata]) Error() string {
	return e.Err.Error()
}

type (
	// BoundResolverForTesting is used to generate mocks that are used
	// by RecursiveComputer's unit tests.
	BoundResolverForTesting = model_tag.BoundResolver[object.LocalReference]
	// KeysReaderForTesting is used to generate mocks that
	// are used by RecursiveComputer's unit tests.
	KeysReaderForTesting = model_parser.MessageObjectReader[object.LocalReference, []*model_evaluation_pb.Keys]
	// LookupResultReaderForTesting is used to generate mocks that
	// are used by RecursiveComputer's unit tests.
	LookupResultReaderForTesting = model_parser.MessageObjectReader[object.LocalReference, *model_evaluation_cache_pb.LookupResult]
	// ObjectManagerForTesting is used to generate mocks that are
	// used by RecursiveComputer's unit tests.
	ObjectManagerForTesting = model_core.ObjectManager[object.LocalReference, model_core.ReferenceMetadata]
)
