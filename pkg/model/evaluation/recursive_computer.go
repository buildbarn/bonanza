package evaluation

import (
	"bytes"
	"context"
	"encoding"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"bonanza.build/pkg/crypto/lthash"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
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
	tagStore                  model_tag.BoundStore[TReference]
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
	tagStore model_tag.BoundStore[TReference],
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
		tagStore:                  tagStore,
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
				if ks.isBlockedCyclic(blockedOn, blockedCyclic) {
					if newValueState := ks.valueState.disableCacheLookup(); newValueState != nil {
						ks.valueState = newValueState
						rc.forceUnblockKeyState(ks, blockedOn[ks])
						rc.enqueueForEvaluation(ks)
						disabledCacheLookupOnAKey = true
					}
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
					ksBlocked.valueState = ksBlocked.valueState.gotFailedDependency(
						NestedError[TReference, TMetadata]{
							KeyState: ksCyclic,
							Err:      errors.New("cyclic dependency detected"),
						},
					)
					rc.evaluatedKeyState(ksBlocked)
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
	valueState := ks.valueState
	rc.lock.Unlock()

	newValueState, missingDependencies := valueState.evaluate(ctx, rc, ks)

	rc.lock.Lock()
	rc.evaluatingKeys.remove(ks)
	if oldValueState := ks.valueState; !oldValueState.isEvaluated() {
		ks.valueState = newValueState
		if len(missingDependencies) == 0 {
			// Successful evaluation.
			rc.evaluatedKeyState(ks)
		} else {
			if ks.blockedCount != 0 {
				panic("key that is currently being evaluated cannot be blocked")
			}
			ks.restarts++
			restartImmediately := true
			for _, ksDep := range missingDependencies {
				if !ksDep.valueState.isEvaluated() {
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
	valueState := ks.valueState
	rc.lock.Unlock()

	newValueState, err := valueState.upload(ctx, rc, ks)
	if err != nil {
		return false, err
	}

	rc.lock.Lock()
	ks.valueState = newValueState
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
	if !ks.valueState.isEvaluated() {
		panic("value state does not indicate the current key is evaluated")
	}

	// TODO: If ks.valueState.getError() returns a non-nil value,
	// we should recursively ksBlocked.gotFailedDependency(). That
	// way we report build failures more quickly.
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

func (rc *RecursiveComputer[TReference, TMetadata]) getOrCreateKeyStateLocked(keyReference object.LocalReference, keyMessageFetcher messageFetcher[TReference, TMetadata], initialValueState valueState[TReference, TMetadata], evaluationQueue *RecursiveComputerEvaluationQueue[TReference, TMetadata]) *KeyState[TReference, TMetadata] {
	ks, ok := rc.keys[keyReference]
	if !ok {
		ks = &KeyState[TReference, TMetadata]{
			keyReference:      keyReference,
			keyMessageFetcher: keyMessageFetcher,
			valueState:        initialValueState,
			evaluationQueue:   evaluationQueue,
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

		missingDependencies:        map[*KeyState[TReference, TMetadata]]struct{}{},
		directVariableDependencies: map[*KeyState[TReference, TMetadata]]struct{}{},
	}
}

func (rc *RecursiveComputer[TReference, TMetadata]) newInitialValueState(typeURL string) valueState[TReference, TMetadata] {
	if rc.base.ReturnsNativeValue(typeURL) {
		return &computingNativeValueState[TReference, TMetadata]{}
	}
	return initialMessageValueState[TReference, TMetadata]{}
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
	ks := rc.getOrCreateKeyStateLocked(keyReference, keyMessageFetcher, rc.newInitialValueState(key.Message.TypeUrl), evaluationQueue)
	rc.lock.Unlock()
	return ks, nil
}

// GetKeyStateKeyMessage returns the message of the key that is
// associated with the provided KeyState. This can, for example, be used
// to generate proper stack traces using the KeyState instances
// referenced by NestedError.
func (rc *RecursiveComputer[TReference, TMetadata]) GetKeyStateKeyMessage(ctx context.Context, ks *KeyState[TReference, TMetadata]) (model_core.TopLevelMessage[*anypb.Any, TReference], error) {
	return ks.keyMessageFetcher.getAny(ctx, rc)
}

func (rc *RecursiveComputer[TReference, TMetadata]) computeDependenciesHashRecordReferenceForMessage(keyReference object.LocalReference, value model_core.TopLevelMessage[*anypb.Any, TReference]) object.LocalReference {
	dependenciesHashRecord, _ := model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.NoopReferenceMetadata]) *model_evaluation_cache_pb.DependenciesHashRecord {
		return &model_evaluation_cache_pb.DependenciesHashRecord{
			KeyReference: patcher.AddReference(model_core.MetadataEntry[model_core.NoopReferenceMetadata]{
				LocalReference: keyReference,
			}),
			Value: &model_evaluation_cache_pb.DependenciesHashRecord_MessageValue{
				MessageValue: model_core.Patch(
					model_core.NewDiscardingObjectCapturer[TReference](),
					model_core.WrapTopLevelAny(value).Decay(),
				).Merge(patcher),
			},
		}
	}).SortAndSetReferences()
	return util.Must(model_core.ComputeTopLevelMessageReference(dependenciesHashRecord, rc.referenceFormat))
}

// OverrideKeyState overrides the value for a given key. This prevents
// the key from getting evaluated, and causes evaluation of keys that
// depend on it to receive the injected value.
func (rc *RecursiveComputer[TReference, TMetadata]) OverrideKeyState(keyReference object.LocalReference, value model_core.TopLevelMessage[*anypb.Any, TReference]) error {
	dependenciesHashRecordReference := rc.computeDependenciesHashRecordReferenceForMessage(keyReference, value)
	rc.lock.Lock()
	if _, ok := rc.keys[keyReference]; !ok {
		rc.keys[keyReference] = &KeyState[TReference, TMetadata]{
			keyReference: keyReference,
			valueState: &overriddenMessageValueState[TReference, TMetadata]{
				isVariableDependencyValueState: isVariableDependencyValueState[TReference, TMetadata]{
					dependenciesHashRecordReference: dependenciesHashRecordReference,
				},
				value: value,
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
	if !ks.valueState.isEvaluated() {
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
		rc.lock.Lock()
	}
	valueState := ks.valueState
	rc.lock.Unlock()
	return valueState.getError()
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
			evaluatingKeysMessages = append(evaluatingKeysMessages, &model_evaluation_pb.Progress_EvaluatingKey{
				Key:                    model_core.Patch(rc.objectManager, model_core.WrapTopLevelAny(key).Decay()).Merge(patcher),
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
		evaluation, err := model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (*model_evaluation_pb.Evaluations, error) {
			newValueState, graphlet, err := ks.valueState.getGraphlet(ctx, rc)
			ks.valueState = newValueState
			if err != nil {
				return nil, err
			}
			return &model_evaluation_pb.Evaluations{
				Level: &model_evaluation_pb.Evaluations_Leaf_{
					Leaf: &model_evaluation_pb.Evaluations_Leaf{
						KeyReference: ks.keyReference.GetRawReference(),
						Graphlet:     model_core.Patch(rc.objectManager, graphlet).Merge(patcher),
					},
				},
			}, nil
		})
		if err != nil {
			return model_core.PatchedMessage[[]*model_evaluation_pb.Evaluations, TMetadata]{}, err
		}
		if err := evaluationsBuilder.PushChild(evaluation); err != nil {
			return model_core.PatchedMessage[[]*model_evaluation_pb.Evaluations, TMetadata]{}, err
		}
	}

	return evaluationsBuilder.FinalizeList()
}

// GetEvaluations returns a B-tree of evaluations, including graphlets
// for all provided KeyStates, including all of their transitive
// dependencies.
func (rc *RecursiveComputer[TReference, TMetadata]) GetEvaluations(ctx context.Context, keyStates []*KeyState[TReference, TMetadata]) (model_core.PatchedMessage[[]*model_evaluation_pb.Evaluations, TMetadata], error) {
	rc.lock.Lock()
	defer rc.lock.Unlock()

	topLevelKeyStates := make(map[*KeyState[TReference, TMetadata]]struct{}, len(keyStates))
	// TODO: Gather nested top-level key states.
	for _, ks := range keyStates {
		if ks.valueState.isVariableDependency() {
			topLevelKeyStates[ks] = struct{}{}
		}
	}

	return rc.getEvaluationsForSortedList(ctx, sortedKeyStates(topLevelKeyStates))
}

func (rc *RecursiveComputer[TReference, TMetadata]) getCacheLookupTagKeyHash(ks *KeyState[TReference, TMetadata], subsequentLookup *model_evaluation_cache_pb.LookupTagKeyData_SubsequentLookup) (model_tag.DecodableKeyHash, error) {
	tagKeyData, _ := model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.NoopReferenceMetadata]) *model_evaluation_cache_pb.LookupTagKeyData {
		result := &model_evaluation_cache_pb.LookupTagKeyData{
			ActionTagKeyReference: patcher.AddReference(model_core.MetadataEntry[model_core.NoopReferenceMetadata]{
				LocalReference: rc.actionTagKeyReference,
			}),
			EvaluationKeyReference: patcher.AddReference(model_core.MetadataEntry[model_core.NoopReferenceMetadata]{
				LocalReference: ks.keyReference,
			}),
			SubsequentLookup: subsequentLookup,
		}
		return result
	}).SortAndSetReferences()
	return model_tag.NewDecodableKeyHashFromMessage(tagKeyData, rc.lookupResultReader.GetDecodingParametersSizeBytes())
}

type recursivelyComputingEnvironment[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	// Constant fields.
	computer *RecursiveComputer[TReference, TMetadata]
	context  context.Context
	keyState *KeyState[TReference, TMetadata]

	err                        error
	missingDependencies        map[*KeyState[TReference, TMetadata]]struct{}
	directVariableDependencies map[*KeyState[TReference, TMetadata]]struct{}
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
	if !ks.valueState.isEvaluated() {
		e.missingDependencies[ks] = struct{}{}
		return nil
	}
	if err := ks.valueState.getError(); err != nil {
		e.setError(NestedError[TReference, TMetadata]{
			KeyState: ks,
			Err:      err,
		})
		return nil
	}
	if ks.valueState.isVariableDependency() {
		e.directVariableDependencies[ks] = struct{}{}
	}
	return ks.valueState
}

func (e *recursivelyComputingEnvironment[TReference, TMetadata]) GetMessageValue(patchedKey model_core.PatchedMessage[proto.Message, TMetadata]) model_core.Message[proto.Message, TReference] {
	vs := e.getValueState(patchedKey, initialMessageValueState[TReference, TMetadata]{})
	if vs == nil {
		return model_core.Message[proto.Message, TReference]{}
	}
	anyValue, err := vs.getMessageValue(e.context, e.computer)
	if err != nil {
		e.setError(err)
		return model_core.Message[proto.Message, TReference]{}
	}
	value, err := model_core.UnmarshalTopLevelAnyNew(anyValue)
	if err != nil {
		e.setError(err)
		return model_core.Message[proto.Message, TReference]{}
	}
	return value.Decay()
}

func (e *recursivelyComputingEnvironment[TReference, TMetadata]) GetNativeValue(patchedKey model_core.PatchedMessage[proto.Message, TMetadata]) (any, bool) {
	vs := e.getValueState(patchedKey, &computingNativeValueState[TReference, TMetadata]{})
	if vs == nil {
		return nil, false
	}
	v, err := vs.getNativeValue()
	if err != nil {
		e.setError(err)
		return model_core.Message[proto.Message, TReference]{}, false
	}
	return v, true
}

func (e *recursivelyComputingEnvironment[TReference, TMetadata]) getVariableDependenciesValueState() (variableDependenciesValueState[TReference, TMetadata], bool) {
	if len(e.directVariableDependencies) == 0 {
		// This key does not have any direct or transitive
		// dependencies on keys for which overrides are in
		// place. For these keys we don't construct graphlets.
		return variableDependenciesValueState[TReference, TMetadata]{}, false
	}

	panic("TODO")
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
	valueState      valueState[TReference, TMetadata]

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

// valueState contains state regarding the value that's associated with
// a key.
//
// Instances of valueState are expected to be immutable. This permits
// operations to be performed against them without having any locks
// held. This is why many of the methods return a new instance of
// valueState, which RecursiveComputer should use later on.
type valueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] interface {
	// Attempt to evaluate the key, and capture the value that
	// evaluation yields. When evaluation completes,
	// missingDependencies is empty. When missingDependencies is
	// non-empty, evaluation is re-attempted when the value of the
	// dependency has become available.
	evaluate(
		ctx context.Context,
		rc *RecursiveComputer[TReference, TMetadata],
		ks *KeyState[TReference, TMetadata],
	) (
		newValueState valueState[TReference, TMetadata],
		missingDependencies []*KeyState[TReference, TMetadata],
	)

	// Attempt to upload the resulting value to the cache, so that
	// subsequent builds can skip computation.
	upload(
		ctx context.Context,
		rc *RecursiveComputer[TReference, TMetadata],
		ks *KeyState[TReference, TMetadata],
	) (valueState[TReference, TMetadata], error)

	// During the next evaluation, do not attempt to perform any
	// cache lookups. This is invoked when potential cyclic
	// dependencies are detected. If the valueState already had
	// cache lookups disabled, or already finished performing cache
	// lookups, this method should return nil.
	disableCacheLookup() valueState[TReference, TMetadata]

	// Report that the evaluation of a dependency on which the
	// current key is blocked has failed. This can be used to
	// propagate evaluation failures more quickly.
	//
	// If the current key was still performing a cache lookup, this
	// method has the same effect as disableCacheLookup().
	gotFailedDependency(err NestedError[TReference, TMetadata]) valueState[TReference, TMetadata]

	// Returns true if the key has been evaluated.
	isEvaluated() bool

	// Get the message value belonging to this key. This method is
	// only called against keys that have been evaluated.
	getMessageValue(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata]) (model_core.TopLevelMessage[*anypb.Any, TReference], error)
	// Get the native value belonging to this key. This method is
	// only called against keys that have been evaluated.
	getNativeValue() (any, error)
	// Only return the error that occurred evaluating this key. This
	// method is identical to calling getMessageValue() or
	// getNativeValue(), except that it returns no value, and works
	// for both value types.
	getError() error

	// Get a graphlet containing the computed value, the list of
	// dependencies that were accessed.
	getGraphlet(
		ctx context.Context,
		rc *RecursiveComputer[TReference, TMetadata],
	) (
		newValueState valueState[TReference, TMetadata],
		graphlet model_core.Message[*model_evaluation_pb.Graphlet, TReference],
		err error,
	)

	// Get a reference of a DependenciesHashRecord message
	// containing the key and value. This reference is necessary
	// when performing cache lookups for keys that depend on this
	// one.
	getDependenciesHashRecordReference() object.LocalReference

	// Returns true if the value state is an
	// injectedMessageValueState, or transitively depends on at
	// least one injectedMessageValueState.
	//
	// If this method returns false, it means that the evaluated
	// value is constant for the current set of injected keys, and
	// it therefore does not make sense to track this key as a true
	// dependency.
	//
	// This method may only be called if evaluation has finished
	// successfully.
	isVariableDependency() bool
}

type unevaluatedValueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct{}

func (unevaluatedValueState[TReference, TMetadata]) isEvaluated() bool {
	return false
}

func (unevaluatedValueState[TReference, TMetadata]) upload(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) (valueState[TReference, TMetadata], error) {
	panic("key has not finished evaluating")
}

func (unevaluatedValueState[TReference, TMetadata]) getMessageValue(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata]) (model_core.TopLevelMessage[*anypb.Any, TReference], error) {
	panic("key has not finished evaluating")
}

func (unevaluatedValueState[TReference, TMetadata]) getNativeValue() (any, error) {
	panic("key has not finished evaluating")
}

func (unevaluatedValueState[TReference, TMetadata]) getError() error {
	panic("key has not finished evaluating")
}

func (unevaluatedValueState[TReference, TMetadata]) getDependenciesHashRecordReference() object.LocalReference {
	panic("key has not finished evaluating")
}

func (unevaluatedValueState[TReference, TMetadata]) isVariableDependency() bool {
	panic("key has not finished evaluating")
}

type evaluatedValueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct{}

func (evaluatedValueState[TReference, TMetadata]) evaluate(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) (valueState[TReference, TMetadata], []*KeyState[TReference, TMetadata]) {
	panic("key already evaluated")
}

func (evaluatedValueState[TReference, TMetadata]) isEvaluated() bool {
	return true
}

func (evaluatedValueState[TReference, TMetadata]) disableCacheLookup() valueState[TReference, TMetadata] {
	panic("key already evaluated")
}

func (evaluatedValueState[TReference, TMetadata]) gotFailedDependency(err NestedError[TReference, TMetadata]) valueState[TReference, TMetadata] {
	panic("key already evaluated")
}

type earlyFailedValueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	evaluatedValueState[TReference, TMetadata]

	err error
}

func (vs *earlyFailedValueState[TReference, TMetadata]) upload(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) (valueState[TReference, TMetadata], error) {
	return vs, nil
}

func (vs *earlyFailedValueState[TReference, TMetadata]) getMessageValue(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata]) (model_core.TopLevelMessage[*anypb.Any, TReference], error) {
	return model_core.TopLevelMessage[*anypb.Any, TReference]{}, vs.err
}

func (vs *earlyFailedValueState[TReference, TMetadata]) getNativeValue() (any, error) {
	return nil, vs.err
}

func (vs *earlyFailedValueState[TReference, TMetadata]) getError() error {
	return vs.err
}

func (earlyFailedValueState[TReference, TMetadata]) getDependenciesHashRecordReference() object.LocalReference {
	panic("key has not evaluated successfully")
}

func (earlyFailedValueState[TReference, TMetadata]) isVariableDependency() bool {
	panic("key has not evaluated successfully")
}

func (vs *earlyFailedValueState[TReference, TMetadata]) getGraphlet(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata]) (valueState[TReference, TMetadata], model_core.Message[*model_evaluation_pb.Graphlet, TReference], error) {
	return vs, model_core.NewSimpleMessage[TReference](&model_evaluation_pb.Graphlet{}), nil
}

type evaluationFailedValueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	evaluatedValueState[TReference, TMetadata]

	err error
}

func (vs *evaluationFailedValueState[TReference, TMetadata]) upload(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) (valueState[TReference, TMetadata], error) {
	return vs, nil
}

func (vs *evaluationFailedValueState[TReference, TMetadata]) getMessageValue(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata]) (model_core.TopLevelMessage[*anypb.Any, TReference], error) {
	return model_core.TopLevelMessage[*anypb.Any, TReference]{}, vs.err
}

func (vs *evaluationFailedValueState[TReference, TMetadata]) getNativeValue() (any, error) {
	return nil, vs.err
}

func (vs *evaluationFailedValueState[TReference, TMetadata]) getError() error {
	return vs.err
}

func (evaluationFailedValueState[TReference, TMetadata]) getDependenciesHashRecordReference() object.LocalReference {
	panic("key has not evaluated successfully")
}

func (evaluationFailedValueState[TReference, TMetadata]) isVariableDependency() bool {
	panic("key has not evaluated successfully")
}

func (vs *evaluationFailedValueState[TReference, TMetadata]) getGraphlet(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata]) (valueState[TReference, TMetadata], model_core.Message[*model_evaluation_pb.Graphlet, TReference], error) {
	return vs, model_core.NewSimpleMessage[TReference](&model_evaluation_pb.Graphlet{}), nil
}

type uploadedValueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	evaluatedValueState[TReference, TMetadata]
}

func (uploadedValueState[TReference, TMetadata]) upload(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) (valueState[TReference, TMetadata], error) {
	panic("key already uploaded")
}

type noDependenciesValueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	evaluatedValueState[TReference, TMetadata]
}

func (noDependenciesValueState[TReference, TMetadata]) getGraphlet(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata]) (valueState[TReference, TMetadata], model_core.Message[*model_evaluation_pb.Graphlet, TReference], error) {
	panic("key cannot be embedded into another graphlet, as it is not a variable dependency")
}

func (noDependenciesValueState[TReference, TMetadata]) getDependenciesHashRecordReference() object.LocalReference {
	panic("key cannot be used as a dependency, as its value is not variable")
}

func (noDependenciesValueState[TReference, TMetadata]) isVariableDependency() bool {
	return false
}

type variableDependenciesValueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	evaluatedValueState[TReference, TMetadata]

	directVariableDependencies []*KeyState[TReference, TMetadata]
}

func (variableDependenciesValueState[TReference, TMetadata]) isVariableDependency() bool {
	return true
}

type isVariableDependencyValueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	dependenciesHashRecordReference object.LocalReference
}

func (vs *isVariableDependencyValueState[TReference, TMetadata]) getDependenciesHashRecordReference() object.LocalReference {
	return vs.dependenciesHashRecordReference
}

func (isVariableDependencyValueState[TReference, TMetadata]) isVariableDependency() bool {
	return true
}

type initialMessageValueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	unevaluatedValueState[TReference, TMetadata]
}

func (vs initialMessageValueState[TReference, TMetadata]) evaluate(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) (valueState[TReference, TMetadata], []*KeyState[TReference, TMetadata]) {
	rootTagKeyHash, err := rc.getCacheLookupTagKeyHash(ks, nil)
	if err != nil {
		return &earlyFailedValueState[TReference, TMetadata]{
			err: err,
		}, nil
	}
	rootLookupResultReference, err := model_tag.ResolveDecodableTag(ctx, rc.tagStore, rootTagKeyHash)
	if err != nil {
		if status.Code(err) != codes.NotFound {
			return &earlyFailedValueState[TReference, TMetadata]{
				err: err,
			}, nil
		}
		return computingMessageValueState[TReference, TMetadata]{}.evaluate(ctx, rc, ks)
	}

	rootLookupResult, err := rc.lookupResultReader.ReadObject(ctx, rootLookupResultReference)
	if err != nil {
		if status.Code(err) != codes.NotFound {
			return &earlyFailedValueState[TReference, TMetadata]{
				err: err,
			}, nil
		}
		return computingMessageValueState[TReference, TMetadata]{}.evaluate(ctx, rc, ks)
	}

	switch r := rootLookupResult.Message.Result.(type) {
	case *model_evaluation_cache_pb.LookupResult_Initial_:
		var dependencyKeyReferences []object.LocalReference
		var errIter error
		for dependency := range btree.AllLeaves(
			ctx,
			rc.keysReader,
			model_core.Nested(rootLookupResult, r.Initial.GraphletVariableDependencyKeys),
			/* traverser = */ func(keys model_core.Message[*model_evaluation_pb.Keys, TReference]) (*model_core_pb.DecodableReference, error) {
				return keys.Message.GetParent().GetReference(), nil
			},
			&errIter,
		) {
			dependencyLeaf, ok := dependency.Message.Level.(*model_evaluation_pb.Keys_Leaf)
			if !ok {
				return computingMessageValueState[TReference, TMetadata]{}.evaluate(ctx, rc, ks)
			}
			dependencyKey, err := model_core.FlattenAny(model_core.Nested(dependency, dependencyLeaf.Leaf))
			if err != nil {
				return computingMessageValueState[TReference, TMetadata]{}.evaluate(ctx, rc, ks)
			}
			dependencyKeyReference, err := model_core.ComputeTopLevelMessageReference(dependencyKey, rc.referenceFormat)
			if err != nil {
				return computingMessageValueState[TReference, TMetadata]{}.evaluate(ctx, rc, ks)
			}
			dependencyKeyReferences = append(dependencyKeyReferences, dependencyKeyReference)
		}
		if errIter != nil {
			return &earlyFailedValueState[TReference, TMetadata]{
				err: err,
			}, nil
		}

		dependenciesHasher := lthash.NewHasher()
		var unevaluatedDependencies []*KeyState[TReference, TMetadata]
		missingDependencyIndices := make([]int, 0, len(dependencyKeyReferences))
		rc.lock.RLock()
		for i, dependencyKeyReference := range dependencyKeyReferences {
			if ksDep, ok := rc.keys[dependencyKeyReference]; ok {
				if vsDep := ksDep.valueState; !vsDep.isEvaluated() {
					unevaluatedDependencies = append(unevaluatedDependencies, ksDep)
				} else if vsDep.getError() != nil || !vsDep.isVariableDependency() {
					rc.lock.RUnlock()
					return computingMessageValueState[TReference, TMetadata]{}.evaluate(ctx, rc, ks)
				} else {
					dependenciesHasher.Add(vsDep.getDependenciesHashRecordReference().GetRawReference())
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
				model_core.Nested(rootLookupResult, r.Initial.GraphletVariableDependencyKeys),
				/* traverser = */ func(keys model_core.Message[*model_evaluation_pb.Keys, TReference]) (*model_core_pb.DecodableReference, error) {
					return keys.Message.GetParent().GetReference(), nil
				},
				&errIter,
			) {
				if i == missingDependencyIndices[0] {
					dependencyLeaf, ok := dependency.Message.Level.(*model_evaluation_pb.Keys_Leaf)
					if !ok {
						return computingMessageValueState[TReference, TMetadata]{}.evaluate(ctx, rc, ks)
					}
					dependencyKey, err := model_core.FlattenAny(model_core.Nested(dependency, dependencyLeaf.Leaf))
					if err != nil {
						return computingMessageValueState[TReference, TMetadata]{}.evaluate(ctx, rc, ks)
					}
					dependencyKeyReference, err := model_core.ComputeTopLevelMessageReference(dependencyKey, rc.referenceFormat)
					if err != nil {
						return computingMessageValueState[TReference, TMetadata]{}.evaluate(ctx, rc, ks)
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
						rc.newInitialValueState(dependencyKey.Message.TypeUrl),
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
				return &earlyFailedValueState[TReference, TMetadata]{
					err: err,
				}, nil
			}
		}

		if len(unevaluatedDependencies) > 0 {
			return vs, unevaluatedDependencies
		}

		// TODO!
		return &earlyFailedValueState[TReference, TMetadata]{
			err: errors.New("got initial result"),
		}, nil
	case *model_evaluation_cache_pb.LookupResult_HitValue:
		// It turns out the current key does not have any
		// dependencies. This means that the initial lookup
		// returned a value immediately.
		return &noDependenciesUploadedMessageValueState[TReference, TMetadata]{
			lookupResultReference: rootLookupResultReference,
		}, nil
	default:
		// Malformed cache entry.
		return computingMessageValueState[TReference, TMetadata]{}.evaluate(ctx, rc, ks)
	}
}

func (initialMessageValueState[TReference, TMetadata]) disableCacheLookup() valueState[TReference, TMetadata] {
	panic("key has never been attempted to be evaluated, meaning that cyclic dependencies may not occur")
}

func (initialMessageValueState[TReference, TMetadata]) gotFailedDependency(err NestedError[TReference, TMetadata]) valueState[TReference, TMetadata] {
	panic("key has never been attempted to be evaluated, meaning that it cannot have any dependencies")
}

func (vs initialMessageValueState[TReference, TMetadata]) getGraphlet(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata]) (valueState[TReference, TMetadata], model_core.Message[*model_evaluation_pb.Graphlet, TReference], error) {
	return vs, model_core.NewSimpleMessage[TReference](&model_evaluation_pb.Graphlet{}), nil
}

type computingMessageValueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	unevaluatedValueState[TReference, TMetadata]
}

func (vs computingMessageValueState[TReference, TMetadata]) evaluate(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) (valueState[TReference, TMetadata], []*KeyState[TReference, TMetadata]) {
	key, err := ks.keyMessageFetcher.getNative(ctx, rc)
	if err != nil {
		return &earlyFailedValueState[TReference, TMetadata]{
			err: err,
		}, nil
	}

	e := rc.newEnvironment(ctx, ks)
	value, err := rc.base.ComputeMessageValue(ctx, key.Decay(), e)
	if err != nil {
		if errors.Is(err, ErrMissingDependency) {
			missingDependencies, err := e.getMissingDependenciesOrError()
			if err != nil {
				return &evaluationFailedValueState[TReference, TMetadata]{
					err: err,
				}, nil
			}
			return vs, missingDependencies
		}
		return &evaluationFailedValueState[TReference, TMetadata]{
			err: err,
		}, nil
	}
	anyValue, err := model_core.MarshalTopLevelAny(model_core.Unpatch(rc.objectManager, value))
	if err != nil {
		return &evaluationFailedValueState[TReference, TMetadata]{
			err: fmt.Errorf("failed to marshal value yielded by evaluation function: %w", err),
		}, nil
	}
	if vsDependencies, ok := e.getVariableDependenciesValueState(); ok {
		return &variableDependenciesComputedMessageValueState[TReference, TMetadata]{
			variableDependenciesValueState: vsDependencies,
			computedMessageValueState: computedMessageValueState[TReference, TMetadata]{
				value: anyValue,
			},
			dependenciesHashRecordReference: rc.computeDependenciesHashRecordReferenceForMessage(ks.keyReference, anyValue),
		}, nil
	}
	return &noDependenciesComputedMessageValueState[TReference, TMetadata]{
		computedMessageValueState: computedMessageValueState[TReference, TMetadata]{
			value: anyValue,
		},
	}, nil
}

func (computingMessageValueState[TReference, TMetadata]) disableCacheLookup() valueState[TReference, TMetadata] {
	return nil
}

func (computingMessageValueState[TReference, TMetadata]) gotFailedDependency(err NestedError[TReference, TMetadata]) valueState[TReference, TMetadata] {
	return &evaluationFailedValueState[TReference, TMetadata]{
		err: err,
	}
}

func (vs computingMessageValueState[TReference, TMetadata]) getGraphlet(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata]) (valueState[TReference, TMetadata], model_core.Message[*model_evaluation_pb.Graphlet, TReference], error) {
	// In principle it would be possible to return a graphlet that
	// contains information on the dependencies gathered up to this
	// point. However, this would lead to non-deterministic results.
	// Return an empty graphlet instead.
	return vs, model_core.NewSimpleMessage[TReference](&model_evaluation_pb.Graphlet{}), nil
}

type computedMessageValueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	value model_core.TopLevelMessage[*anypb.Any, TReference]
}

func (vs *computedMessageValueState[TReference, TMetadata]) getMessageValue(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata]) (model_core.TopLevelMessage[*anypb.Any, TReference], error) {
	return vs.value, nil
}

func (computedMessageValueState[TReference, TMetadata]) getNativeValue() (any, error) {
	return nil, errors.New("key does not yield a native value")
}

func (computedMessageValueState[TReference, TMetadata]) getError() error {
	return nil
}

type noDependenciesComputedMessageValueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	noDependenciesValueState[TReference, TMetadata]
	computedMessageValueState[TReference, TMetadata]
}

func (vs *noDependenciesComputedMessageValueState[TReference, TMetadata]) upload(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) (valueState[TReference, TMetadata], error) {
	rootTagKeyHash, err := rc.getCacheLookupTagKeyHash(ks, nil)
	if err != nil {
		return vs, err
	}

	// Create a root LookupResult message containing the message value.
	createdRootLookupResult, err := model_core.MarshalAndEncodeKeyed(
		model_core.MustBuildPatchedMessage(
			func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) encoding.BinaryMarshaler {
				return model_core.NewProtoBinaryMarshaler(
					&model_evaluation_cache_pb.LookupResult{
						Result: &model_evaluation_cache_pb.LookupResult_HitValue{
							HitValue: model_core.Patch(
								rc.objectManager,
								model_core.WrapTopLevelAny(vs.value).Decay(),
							).Merge(patcher),
						},
					},
				)
			},
		),
		rc.referenceFormat,
		rc.cacheKeyedEncoder,
		rootTagKeyHash.GetDecodingParameters(),
	)
	if err != nil {
		return vs, err
	}
	rootLookupResultMetadataEntry, err := createdRootLookupResult.Capture(ctx, rc.objectManager)
	if err != nil {
		return vs, err
	}
	rootLookupResultReference := rc.objectManager.ReferenceObject(rootLookupResultMetadataEntry)

	// Create a tag that points to the LookupResult message, so that
	// subsequent builds are able to find it again.
	if err := rc.tagStore.UpdateTag(ctx, rootTagKeyHash.Value, rootLookupResultReference); err != nil {
		return vs, err
	}

	// Future accesses to this key can reload the value that's part
	// of the LookupResult. This prevents the need for keeping the
	// value in memory.
	return &noDependenciesUploadedMessageValueState[TReference, TMetadata]{
		lookupResultReference: model_core.CopyDecodable(rootTagKeyHash, rootLookupResultReference),
	}, nil
}

type variableDependenciesComputedMessageValueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	variableDependenciesValueState[TReference, TMetadata]
	computedMessageValueState[TReference, TMetadata]
	dependenciesHashRecordReference object.LocalReference
}

func (vs *variableDependenciesComputedMessageValueState[TReference, TMetadata]) upload(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) (valueState[TReference, TMetadata], error) {
	// TODO: Implement uploading!
	return vs, nil
}

func (variableDependenciesComputedMessageValueState[TReference, TMetadata]) getGraphlet(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata]) (valueState[TReference, TMetadata], model_core.Message[*model_evaluation_pb.Graphlet, TReference], error) {
	panic("TODO")
}

func (vs *variableDependenciesComputedMessageValueState[TReference, TMetadata]) getDependenciesHashRecordReference() object.LocalReference {
	return vs.dependenciesHashRecordReference
}

type noDependenciesUploadedMessageValueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	noDependenciesValueState[TReference, TMetadata]

	lookupResultReference model_core.Decodable[TReference]
}

func (vs *noDependenciesUploadedMessageValueState[TReference, TMetadata]) upload(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) (valueState[TReference, TMetadata], error) {
	return vs, nil
}

func (vs *noDependenciesUploadedMessageValueState[TReference, TMetadata]) getMessageValue(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata]) (model_core.TopLevelMessage[*anypb.Any, TReference], error) {
	rootLookupResult, err := rc.lookupResultReader.ReadObject(ctx, vs.lookupResultReference)
	if err != nil {
		return model_core.TopLevelMessage[*anypb.Any, TReference]{}, err
	}
	return model_core.FlattenAny(
		model_core.Nested(
			rootLookupResult,
			rootLookupResult.Message.Result.(*model_evaluation_cache_pb.LookupResult_HitValue).HitValue,
		),
	)
}

func (noDependenciesUploadedMessageValueState[TReference, TMetadata]) getNativeValue() (any, error) {
	return nil, errors.New("key does not yield a native value")
}

func (noDependenciesUploadedMessageValueState[TReference, TMetadata]) getError() error {
	return nil
}

// // cacheHitValueBackedMessageValueState is used for keys whose value was
// // obtained by getting a cache hit for a bare value (not a full
// // graphlet). This means that the value can be obtained by reloading it
// // from storage, but that a graphlet containing the value needs to be
// // reconstructed.
// type cacheHitValueBackedMessageValueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
// 	evaluatedValueState[TReference, TMetadata]
//
// 	lookupResultReference            model_core.Decodable[TReference]
// 	directDependencies              []*KeyState[TReference, TMetadata]
// 	dependenciesHashRecordReference object.LocalReference
// }
//
// func (cacheHitValueBackedMessageValueState[TReference, TMetadata]) getMessageValue(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata]) (model_core.TopLevelMessage[proto.Message, TReference], error)
//
// func (cacheHitValueBackedMessageValueState[TReference, TMetadata]) getNativeValue() (any, error) {
// 	return nil, errors.New("key does not yield a native value")
// }
//
// func (vs *cacheHitValueBackedMessageValueState[TReference, TMetadata]) getGraphlet(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata]) (valueState[TReference, TMetadata], model_core.Message[*model_evaluation_pb.Graphlet, TReference], error) {
// 	inlineCandidates := make(inlinedtree.CandidateList[*model_evaluation_pb.Graphlet, TMetadata], 0, 3)
// 	defer inlineCandidates.Discard()
//
// 	lookupResult, err := rc.lookupResultReader.ReadObject(ctx, vs.lookupResultReference)
// 	if err != nil {
// 		return vs, model_core.Message[*model_evaluation_pb.Graphlet, TReference]{}, err
// 	}
//
// 	// Attach the computed value, if any.
// 	patchedValue := model_core.Patch(
// 		rc.objectManager,
// 		model_core.Nested(lookupResult, lookupResult.Message.Result.(*model_evaluation_cache_pb.LookupResult_HitValue).HitValue),
// 	)
// 	inlineCandidates = append(inlineCandidates, inlinedtree.AlwaysInline(
// 		patchedValue.Patcher,
// 		func(graphlet model_core.PatchedMessage[*model_evaluation_pb.Graphlet, TMetadata]) {
// 			graphlet.Message.Value = patchedValue.Message
// 		},
// 	))
//
// 	// Attach the direct dependencies.
// 	directDependenciesParentNodeComputer := btree.Capturing(ctx, rc.objectManager, func(createdObject model_core.Decodable[model_core.MetadataEntry[TMetadata]], childNodes model_core.Message[[]*model_evaluation_pb.Keys, object.LocalReference]) model_core.PatchedMessage[*model_evaluation_pb.Keys, TMetadata] {
// 		return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_evaluation_pb.Keys {
// 			return &model_evaluation_pb.Keys{
// 				Level: &model_evaluation_pb.Keys_Parent_{
// 					Parent: &model_evaluation_pb.Keys_Parent{
// 						Reference: patcher.AddDecodableReference(createdObject),
// 					},
// 				},
// 			}
// 		})
// 	})
// 	directDependenciesBuilder := btree.NewHeightAwareBuilder(
// 		btree.NewProllyChunkerFactory[TMetadata](
// 			/* minimumSizeBytes = */ 1<<16,
// 			/* maximumSizeBytes = */ 1<<18,
// 			/* isParent = */ func(keys *model_evaluation_pb.Keys) bool {
// 				return keys.GetParent() != nil
// 			},
// 		),
// 		btree.NewObjectCreatingNodeMerger(
// 			rc.cacheDeterministicEncoder,
// 			rc.referenceFormat,
// 			directDependenciesParentNodeComputer,
// 		),
// 	)
// 	defer directDependenciesBuilder.Discard()
//
// 	for _, ksDep := range vs.directDependencies {
// 		directDependency, err := model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (*model_evaluation_pb.Keys, error) {
// 			key, err := ksDep.keyMessageFetcher.getAny(ctx, rc)
// 			if err != nil {
// 				return nil, err
// 			}
// 			return &model_evaluation_pb.Keys{
// 				Level: &model_evaluation_pb.Keys_Leaf{
// 					Leaf: model_core.Patch(
// 						rc.objectManager,
// 						model_core.WrapTopLevelAny(key).Decay(),
// 					).Merge(patcher),
// 				},
// 			}, nil
// 		})
// 		if err != nil {
// 			return vs, model_core.Message[*model_evaluation_pb.Graphlet, TReference]{}, err
// 		}
// 		if err := directDependenciesBuilder.PushChild(directDependency); err != nil {
// 			return vs, model_core.Message[*model_evaluation_pb.Graphlet, TReference]{}, err
// 		}
// 	}
//
// 	directDependenciesList, err := directDependenciesBuilder.FinalizeList()
// 	if err != nil {
// 		return vs, model_core.Message[*model_evaluation_pb.Graphlet, TReference]{}, err
// 	}
// 	inlineCandidates = append(inlineCandidates, inlinedtree.Candidate[*model_evaluation_pb.Graphlet, TMetadata]{
// 		ExternalMessage: model_core.ProtoListToBinaryMarshaler(directDependenciesList),
// 		Encoder:         rc.cacheDeterministicEncoder,
// 		ParentAppender: func(
// 			graphlet model_core.PatchedMessage[*model_evaluation_pb.Graphlet, TMetadata],
// 			externalObject *model_core.Decodable[model_core.CreatedObject[TMetadata]],
// 		) error {
// 			directDependencies, err := btree.MaybeMergeNodes(
// 				directDependenciesList.Message,
// 				externalObject,
// 				graphlet.Patcher,
// 				directDependenciesParentNodeComputer,
// 			)
// 			if err != nil {
// 				return err
// 			}
// 			graphlet.Message.DirectDependencyKeys = directDependencies
// 			return nil
// 		},
// 	})
//
// 	// Attach evaluations of direct and transitive dependencies that
// 	// did not get promoted to the parent.
// 	// TODO: Use the correct set of dependencies!
// 	evaluationsParentNodeComputer := rc.getEvaluationsParentNodeComputer(ctx)
// 	dependencyEvaluationsList, err := rc.getEvaluationsForSortedList(ctx, vs.directDependencies)
// 	inlineCandidates = append(inlineCandidates, inlinedtree.Candidate[*model_evaluation_pb.Graphlet, TMetadata]{
// 		ExternalMessage: model_core.ProtoListToBinaryMarshaler(dependencyEvaluationsList),
// 		Encoder:         rc.cacheDeterministicEncoder,
// 		ParentAppender: func(
// 			graphlet model_core.PatchedMessage[*model_evaluation_pb.Graphlet, TMetadata],
// 			externalObject *model_core.Decodable[model_core.CreatedObject[TMetadata]],
// 		) error {
// 			dependencyEvaluations, err := btree.MaybeMergeNodes(
// 				dependencyEvaluationsList.Message,
// 				externalObject,
// 				graphlet.Patcher,
// 				evaluationsParentNodeComputer,
// 			)
// 			if err != nil {
// 				return err
// 			}
// 			graphlet.Message.DependencyEvaluations = dependencyEvaluations
// 			return nil
// 		},
// 	})
//
// 	patchedGraphlet, err := inlinedtree.Build(
// 		inlineCandidates,
// 		&inlinedtree.Options{
// 			ReferenceFormat:  rc.referenceFormat,
// 			MaximumSizeBytes: 1 << 16,
// 		},
// 	)
// 	if err != nil {
// 		return vs, model_core.Message[*model_evaluation_pb.Graphlet, TReference]{}, err
// 	}
// 	graphlet := model_core.Unpatch(rc.objectManager, patchedGraphlet).Decay()
//
// 	return &inMemoryMessageValueState[TReference, TMetadata]{
// 		graphlet: graphlet,
// 	}, graphlet, nil
// }
//
// func (vs *cacheHitValueBackedMessageValueState[TReference, TMetadata]) getDependenciesHashRecordReference() object.LocalReference {
// 	return vs.dependenciesHashRecordReference
// }

type overriddenMessageValueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	uploadedValueState[TReference, TMetadata]
	isVariableDependencyValueState[TReference, TMetadata]

	value model_core.TopLevelMessage[*anypb.Any, TReference]
}

func (vs *overriddenMessageValueState[TReference, TMetadata]) getMessageValue(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata]) (model_core.TopLevelMessage[*anypb.Any, TReference], error) {
	return vs.value, nil
}

func (overriddenMessageValueState[TReference, TMetadata]) getNativeValue() (any, error) {
	return nil, errors.New("key does not yield a native value")
}

func (overriddenMessageValueState[TReference, TMetadata]) getError() error {
	return nil
}

func (vs *overriddenMessageValueState[TReference, TMetadata]) getGraphlet(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata]) (valueState[TReference, TMetadata], model_core.Message[*model_evaluation_pb.Graphlet, TReference], error) {
	wrappedValue := model_core.WrapTopLevelAny(vs.value).Decay()
	return vs, model_core.Nested(
		wrappedValue,
		&model_evaluation_pb.Graphlet{
			Value: wrappedValue.Message,
		},
	), nil
}

type computingNativeValueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	unevaluatedValueState[TReference, TMetadata]
}

func (vs *computingNativeValueState[TReference, TMetadata]) evaluate(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) (valueState[TReference, TMetadata], []*KeyState[TReference, TMetadata]) {
	key, err := ks.keyMessageFetcher.getNative(ctx, rc)
	if err != nil {
		return &earlyFailedValueState[TReference, TMetadata]{
			err: err,
		}, nil
	}

	e := rc.newEnvironment(ctx, ks)
	value, err := rc.base.ComputeNativeValue(ctx, key.Decay(), e)
	if err != nil {
		if errors.Is(err, ErrMissingDependency) {
			missingDependencies, err := e.getMissingDependenciesOrError()
			if err != nil {
				return &evaluationFailedValueState[TReference, TMetadata]{
					err: err,
				}, nil
			}
			return vs, missingDependencies
		}
		return &evaluationFailedValueState[TReference, TMetadata]{
			err: err,
		}, nil
	}

	if vsDependencies, ok := e.getVariableDependenciesValueState(); ok {
		dependenciesHasher := lthash.NewHasher()
		rc.lock.RLock()
		for _, ksDep := range vsDependencies.directVariableDependencies {
			dependenciesHasher.Add(ksDep.valueState.getDependenciesHashRecordReference().GetRawReference())
		}
		rc.lock.RUnlock()
		dependenciesHash := dependenciesHasher.Sum()
		dependenciesHashRecord, _ := model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.NoopReferenceMetadata]) *model_evaluation_cache_pb.DependenciesHashRecord {
			return &model_evaluation_cache_pb.DependenciesHashRecord{
				KeyReference: patcher.AddReference(model_core.MetadataEntry[model_core.NoopReferenceMetadata]{
					LocalReference: ks.keyReference,
				}),
				Value: &model_evaluation_cache_pb.DependenciesHashRecord_NativeValueDependenciesHash{
					NativeValueDependenciesHash: dependenciesHash[:],
				},
			}
		}).SortAndSetReferences()
		dependenciesHashRecordReference := util.Must(model_core.ComputeTopLevelMessageReference(dependenciesHashRecord, rc.referenceFormat))

		return &variableDependenciesComputedNativeValueState[TReference, TMetadata]{
			variableDependenciesValueState: vsDependencies,
			computedNativeValueState: computedNativeValueState[TReference, TMetadata]{
				value: value,
			},
			dependenciesHashRecordReference: dependenciesHashRecordReference,
		}, nil
	}
	return &noDependenciesComputedNativeValueState[TReference, TMetadata]{
		computedNativeValueState: computedNativeValueState[TReference, TMetadata]{
			value: value,
		},
	}, nil
}

func (vs *computingNativeValueState[TReference, TMetadata]) upload(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) (valueState[TReference, TMetadata], error) {
	return vs, nil
}

func (computingNativeValueState[TReference, TMetadata]) disableCacheLookup() valueState[TReference, TMetadata] {
	return nil
}

func (computingNativeValueState[TReference, TMetadata]) gotFailedDependency(err NestedError[TReference, TMetadata]) valueState[TReference, TMetadata] {
	return &evaluationFailedValueState[TReference, TMetadata]{
		err: err,
	}
}

func (vs *computingNativeValueState[TReference, TMetadata]) getGraphlet(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata]) (valueState[TReference, TMetadata], model_core.Message[*model_evaluation_pb.Graphlet, TReference], error) {
	// In principle it would be possible to return a graphlet that
	// contains information on the dependencies gathered up to this
	// point. However, this would lead to non-deterministic results.
	// Return an empty graphlet instead.
	return vs, model_core.NewSimpleMessage[TReference](&model_evaluation_pb.Graphlet{}), nil
}

type computedNativeValueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	value any
}

func (computedNativeValueState[TReference, TMetadata]) getMessageValue(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata]) (model_core.TopLevelMessage[*anypb.Any, TReference], error) {
	return model_core.TopLevelMessage[*anypb.Any, TReference]{}, errors.New("key does not yield a native value")
}

func (vs *computedNativeValueState[TReference, TMetadata]) getNativeValue() (any, error) {
	return vs.value, nil
}

func (computedNativeValueState[TReference, TMetadata]) getError() error {
	return nil
}

type noDependenciesComputedNativeValueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	noDependenciesValueState[TReference, TMetadata]
	computedNativeValueState[TReference, TMetadata]
}

func (vs *noDependenciesComputedNativeValueState[TReference, TMetadata]) upload(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) (valueState[TReference, TMetadata], error) {
	// There is no way native values can be cached.
	return vs, nil
}

type variableDependenciesComputedNativeValueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	variableDependenciesValueState[TReference, TMetadata]
	computedNativeValueState[TReference, TMetadata]
	dependenciesHashRecordReference object.LocalReference
}

func (vs *variableDependenciesComputedNativeValueState[TReference, TMetadata]) upload(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) (valueState[TReference, TMetadata], error) {
	// There is no way native values can be cached.
	return vs, nil
}

func (variableDependenciesComputedNativeValueState[TReference, TMetadata]) getGraphlet(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata]) (valueState[TReference, TMetadata], model_core.Message[*model_evaluation_pb.Graphlet, TReference], error) {
	panic("TODO")
}

func (vs *variableDependenciesComputedNativeValueState[TReference, TMetadata]) getDependenciesHashRecordReference() object.LocalReference {
	return vs.dependenciesHashRecordReference
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
	// BoundStoreForTesting is used to generate mocks that are used
	// by RecursiveComputer's unit tests.
	BoundStoreForTesting = model_tag.BoundStore[object.LocalReference]
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
