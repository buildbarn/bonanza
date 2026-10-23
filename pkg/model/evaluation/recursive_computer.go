package evaluation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

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

	"golang.org/x/sync/errgroup"
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
	evaluationReader          model_parser.MessageObjectReader[TReference, *model_evaluation_pb.Evaluation]
	lookupResultReader        model_parser.MessageObjectReader[TReference, *model_evaluation_cache_pb.LookupResult]
	keysReader                model_parser.MessageObjectReader[TReference, []*model_evaluation_pb.Keys]
	cacheDeterministicEncoder model_encoding.DeterministicBinaryEncoder
	cacheKeyedEncoder         model_encoding.KeyedBinaryEncoder
	clock                     clock.Clock

	// The maximum amount of time an initial lookup result may
	// indicate that it contains indirection. After this amount of
	// time has passed, lookup results are no longer merged.
	// Instead, they are overwritten with ones only describing the
	// current evaluation.
	cacheLookupResultIndirectionMaximumAge time.Duration

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
	evaluationReader model_parser.MessageObjectReader[TReference, *model_evaluation_pb.Evaluation],
	lookupResultReader model_parser.MessageObjectReader[TReference, *model_evaluation_cache_pb.LookupResult],
	keysReader model_parser.MessageObjectReader[TReference, []*model_evaluation_pb.Keys],
	cacheDeterministicEncoder model_encoding.DeterministicBinaryEncoder,
	cacheKeyedEncoder model_encoding.KeyedBinaryEncoder,
	clock clock.Clock,
	cacheLookupResultIndirectionMaximumAge time.Duration,
) *RecursiveComputer[TReference, TMetadata] {
	rc := &RecursiveComputer[TReference, TMetadata]{
		base:                      base,
		evaluationQueuePicker:     evaluationQueuePicker,
		referenceFormat:           referenceFormat,
		objectManager:             objectManager,
		tagStore:                  tagStore,
		actionTagKeyReference:     actionTagKeyReference,
		evaluationReader:          evaluationReader,
		lookupResultReader:        lookupResultReader,
		keysReader:                keysReader,
		cacheDeterministicEncoder: cacheDeterministicEncoder,
		cacheKeyedEncoder:         cacheKeyedEncoder,
		clock:                     clock,

		cacheLookupResultIndirectionMaximumAge: cacheLookupResultIndirectionMaximumAge,

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
			for ksIter := &rc.blockedKeys.head.nextKey; *ksIter != &rc.blockedKeys.head; {
				if ks := *ksIter; ks.isBlockedCyclic(blockedOn, blockedCyclic) {
					if newValueState := ks.valueState.disableCacheLookup(); newValueState != nil {
						ks.valueState = newValueState
						rc.forceUnblockKeyState(ks, blockedOn[ks])
						rc.enqueueForEvaluation(ks)
						disabledCacheLookupOnAKey = true
						continue
					}
				}
				ksIter = &(*ksIter).nextKey
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

func (rc *RecursiveComputer[TReference, TMetadata]) getOrCreateKeyStateLocked(keyReference object.LocalReference, keyMessageFetcher messageFetcher[TReference, TMetadata], keyTypeURL string, initialValueState valueState[TReference, TMetadata], evaluationQueue *RecursiveComputerEvaluationQueue[TReference, TMetadata]) *KeyState[TReference, TMetadata] {
	ks, ok := rc.keys[keyReference]
	if !ok {
		// Brand new key.
		ks = &KeyState[TReference, TMetadata]{
			keyReference:      keyReference,
			keyMessageFetcher: keyMessageFetcher,
			isLookup:          rc.base.IsLookup(keyTypeURL),
			valueState:        initialValueState,
			evaluationQueue:   evaluationQueue,
		}
		rc.keys[keyReference] = ks
		rc.enqueueForEvaluation(ks)
	} else if ks.keyMessageFetcher == nil {
		// Key for which an override was created, but the key
		// message itself was up to this point still unknown.
		ks.keyMessageFetcher = keyMessageFetcher
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
	ks := rc.getOrCreateKeyStateLocked(keyReference, keyMessageFetcher, key.Message.TypeUrl, rc.newInitialValueState(key.Message.TypeUrl), evaluationQueue)
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
			isLookup:     true,
			valueState: &overriddenMessageValueState[TReference, TMetadata]{
				variableDependencyValueState: variableDependencyValueState[TReference, TMetadata]{
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
		evaluations, err := model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (*model_evaluation_pb.Evaluations, error) {
			newValueState, graphlet, err := ks.valueState.getGraphlet(ctx, rc, ks)
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
		if err := evaluationsBuilder.PushChild(evaluations); err != nil {
			return model_core.PatchedMessage[[]*model_evaluation_pb.Evaluations, TMetadata]{}, err
		}
	}

	return evaluationsBuilder.FinalizeList()
}

func (rc *RecursiveComputer[TReference, TMetadata]) getKeysParentNodeComputer(ctx context.Context) btree.ParentNodeComputer[*model_evaluation_pb.Keys, TMetadata] {
	return btree.Capturing(ctx, rc.objectManager, func(createdObject model_core.Decodable[model_core.MetadataEntry[TMetadata]], childNodes model_core.Message[[]*model_evaluation_pb.Keys, object.LocalReference]) model_core.PatchedMessage[*model_evaluation_pb.Keys, TMetadata] {
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
}

func (rc *RecursiveComputer[TReference, TMetadata]) getKeysForSortedList(ctx context.Context, sortedKeyStates []*KeyState[TReference, TMetadata]) (model_core.PatchedMessage[[]*model_evaluation_pb.Keys, TMetadata], error) {
	keysBuilder := btree.NewHeightAwareBuilder(
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
			rc.getKeysParentNodeComputer(ctx),
		),
	)
	defer keysBuilder.Discard()

	for _, ks := range sortedKeyStates {
		directDependency, err := model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (*model_evaluation_pb.Keys, error) {
			key, err := ks.keyMessageFetcher.getAny(ctx, rc)
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
			return model_core.PatchedMessage[[]*model_evaluation_pb.Keys, TMetadata]{}, err
		}
		if err := keysBuilder.PushChild(directDependency); err != nil {
			return model_core.PatchedMessage[[]*model_evaluation_pb.Keys, TMetadata]{}, err
		}
	}

	return keysBuilder.FinalizeList()
}

// gatherTopLevelKeyStates recursively gathers all hoisted dependencies
// of a KeyState. This should be invoked when creating a top-level list
// of evaluation results, which can be returned to the client.
func gatherTopLevelKeyStates[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](out map[*KeyState[TReference, TMetadata]]struct{}, ks *KeyState[TReference, TMetadata]) {
	if _, ok := out[ks]; !ok {
		out[ks] = struct{}{}
		for _, ksDep := range ks.valueState.getHoistedDependencies() {
			gatherTopLevelKeyStates(out, ksDep)
		}
	}
}

// GetEvaluations returns a B-tree of evaluations, including graphlets
// for all provided KeyStates, including all of their transitive
// dependencies.
func (rc *RecursiveComputer[TReference, TMetadata]) GetEvaluations(ctx context.Context, keyStates []*KeyState[TReference, TMetadata]) (model_core.PatchedMessage[[]*model_evaluation_pb.Evaluations, TMetadata], error) {
	rc.lock.Lock()
	defer rc.lock.Unlock()

	topLevelKeyStates := map[*KeyState[TReference, TMetadata]]struct{}{}
	for _, ks := range keyStates {
		if ks.valueState.isEvaluated() && (ks.valueState.getError() != nil || ks.valueState.isVariableDependency()) {
			gatherTopLevelKeyStates(topLevelKeyStates, ks)
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

// storeLookupResult writes an object containing a LookupResult message
// to the object store and associates a tag with it. The reference of
// the resulting object is returned, making it possible to read the
// object's contents without resolving the tag.
func (rc *RecursiveComputer[TReference, TMetadata]) storeLookupResult(ctx context.Context, tagKeyHash model_tag.DecodableKeyHash, lookupResult model_core.PatchedMessage[*model_evaluation_cache_pb.LookupResult, TMetadata]) (TReference, error) {
	createdLookupResult, err := model_core.MarshalAndEncodeKeyed(
		model_core.ProtoToBinaryMarshaler(lookupResult),
		rc.referenceFormat,
		rc.cacheKeyedEncoder,
		tagKeyHash.GetDecodingParameters(),
	)
	if err != nil {
		var badReference TReference
		return badReference, err
	}
	lookupResultMetadataEntry, err := createdLookupResult.Capture(ctx, rc.objectManager)
	if err != nil {
		var badReference TReference
		return badReference, err
	}
	lookupResultReference := rc.objectManager.ReferenceObject(lookupResultMetadataEntry)
	return lookupResultReference, rc.tagStore.UpdateTag(ctx, tagKeyHash.Value, lookupResultReference)
}

// readLookupResult resolves a tag and reads the LookupResult message
// that it references. If either the tag or the object does not exist,
// found is set to false.
func (rc *RecursiveComputer[TReference, TMetadata]) readLookupResult(ctx context.Context, tagKeyHash model_tag.DecodableKeyHash) (model_core.Message[*model_evaluation_cache_pb.LookupResult, TReference], model_core.Decodable[TReference], bool, error) {
	var badLookupResult model_core.Message[*model_evaluation_cache_pb.LookupResult, TReference]
	var badReference model_core.Decodable[TReference]
	lookupResultReference, err := model_tag.ResolveDecodableTag(ctx, rc.tagStore, tagKeyHash)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return badLookupResult, badReference, false, nil
		}
		return badLookupResult, badReference, false, err
	}
	lookupResult, err := rc.lookupResultReader.ReadObject(ctx, lookupResultReference)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return badLookupResult, badReference, false, nil
		}
		return badLookupResult, badReference, false, err
	}
	return lookupResult, lookupResultReference, true, nil
}

// forEachCachedKey iterates over a list of keys that is stored in a
// cache entry, yielding the reference and message of each key. As lists
// of keys are always stored in sorted order without any duplicates,
// this function returns false if the list is empty, contains entries
// that are not leaves, or is not sorted.
func (rc *RecursiveComputer[TReference, TMetadata]) forEachCachedKey(
	ctx context.Context,
	keys model_core.Message[[]*model_evaluation_pb.Keys, TReference],
	yield func(keyReference object.LocalReference, key model_core.TopLevelMessage[*anypb.Any, TReference]),
) (bool, error) {
	var previousKeyReference []byte
	var errIter error
	for key := range btree.AllLeaves(
		ctx,
		rc.keysReader,
		keys,
		/* traverser = */ func(keys model_core.Message[*model_evaluation_pb.Keys, TReference]) (*model_core_pb.DecodableReference, error) {
			return keys.Message.GetParent().GetReference(), nil
		},
		&errIter,
	) {
		leaf, ok := key.Message.Level.(*model_evaluation_pb.Keys_Leaf)
		if !ok {
			return false, nil
		}
		keyMessage, err := model_core.FlattenAny(model_core.Nested(key, leaf.Leaf))
		if err != nil {
			return false, nil
		}
		keyReference, err := model_core.ComputeTopLevelMessageReference(keyMessage, rc.referenceFormat)
		if err != nil {
			return false, nil
		}
		rawKeyReference := keyReference.GetRawReference()
		if bytes.Compare(previousKeyReference, rawKeyReference) >= 0 {
			return false, nil
		}
		previousKeyReference = rawKeyReference
		yield(keyReference, keyMessage)
	}
	if errIter != nil {
		return false, errIter
	}
	return previousKeyReference != nil, nil
}

// resolveCachedKeys converts a list of keys that is stored in a cache
// entry to a list of KeyStates, sorted by key reference. KeyStates are
// created for keys that have not been requested before, causing them
// to be evaluated.
func (rc *RecursiveComputer[TReference, TMetadata]) resolveCachedKeys(ctx context.Context, keys model_core.Message[[]*model_evaluation_pb.Keys, TReference]) ([]*KeyState[TReference, TMetadata], bool, error) {
	// Validate the full list before creating any KeyStates, so
	// that malformed lists don't cause keys to be evaluated.
	type cachedKey struct {
		reference object.LocalReference
		message   model_core.TopLevelMessage[*anypb.Any, TReference]
	}
	var cachedKeys []cachedKey
	ok, err := rc.forEachCachedKey(ctx, keys, func(keyReference object.LocalReference, key model_core.TopLevelMessage[*anypb.Any, TReference]) {
		cachedKeys = append(cachedKeys, cachedKey{
			reference: keyReference,
			message:   key,
		})
	})
	if !ok || err != nil {
		return nil, false, err
	}

	keyStates := make([]*KeyState[TReference, TMetadata], 0, len(cachedKeys))
	for _, cachedKey := range cachedKeys {
		keyReference, key := cachedKey.reference, cachedKey.message
		// TODO: We should have one that reloads the message from storage.
		keyMessageFetcher := &staticMessageFetcher[TReference, TMetadata]{
			message: key,
		}
		rc.lock.Lock()
		ks, exists := rc.keys[keyReference]
		if exists && ks.keyMessageFetcher == nil {
			ks.keyMessageFetcher = keyMessageFetcher
		}
		rc.lock.Unlock()
		if !exists {
			// Key has not been requested before. Only
			// consult the base computer in this case.
			typeURL := key.Message.TypeUrl
			initialValueState := rc.newInitialValueState(typeURL)
			evaluationQueue := rc.evaluationQueuePicker.PickQueue(typeURL)
			rc.lock.Lock()
			ks = rc.getOrCreateKeyStateLocked(keyReference, keyMessageFetcher, typeURL, initialValueState, evaluationQueue)
			rc.lock.Unlock()
		}
		keyStates = append(keyStates, ks)
	}
	return keyStates, true, nil
}

// getCachedKeyReferences returns the references of the keys contained
// in a list of keys that is stored in a cache entry. In contrast to
// resolveCachedKeys(), it does not create any KeyStates.
func (rc *RecursiveComputer[TReference, TMetadata]) getCachedKeyReferences(ctx context.Context, keys model_core.Message[[]*model_evaluation_pb.Keys, TReference]) (map[object.LocalReference]struct{}, bool, error) {
	keyReferences := map[object.LocalReference]struct{}{}
	ok, err := rc.forEachCachedKey(ctx, keys, func(keyReference object.LocalReference, key model_core.TopLevelMessage[*anypb.Any, TReference]) {
		keyReferences[keyReference] = struct{}{}
	})
	if !ok || err != nil {
		return nil, false, err
	}
	return keyReferences, true, nil
}

func (rc *RecursiveComputer[TReference, TMetadata]) buildEvaluation(ctx context.Context, value model_core.Message[*model_core_pb.Any, TReference], directVariableDependencies []*KeyState[TReference, TMetadata]) (model_core.PatchedMessage[*model_evaluation_pb.Evaluation, TMetadata], error) {
	inlineCandidates := make(inlinedtree.CandidateList[*model_evaluation_pb.Evaluation, TMetadata], 0, 2)
	defer inlineCandidates.Discard()

	// Attach the computed value, if any.
	if value.IsSet() {
		patchedValue := model_core.Patch(rc.objectManager, value)
		inlineCandidates = append(inlineCandidates, inlinedtree.AlwaysInline(
			patchedValue.Patcher,
			func(evaluation model_core.PatchedMessage[*model_evaluation_pb.Evaluation, TMetadata]) {
				evaluation.Message.Value = patchedValue.Message
			},
		))
	}

	// Attach the keys of the direct variable dependencies.
	keysParentNodeComputer := rc.getKeysParentNodeComputer(ctx)
	directVariableDependenciesList, err := rc.getKeysForSortedList(ctx, directVariableDependencies)
	if err != nil {
		return model_core.PatchedMessage[*model_evaluation_pb.Evaluation, TMetadata]{}, err
	}
	inlineCandidates = append(inlineCandidates, inlinedtree.Candidate[*model_evaluation_pb.Evaluation, TMetadata]{
		ExternalMessage: model_core.ProtoListToBinaryMarshaler(directVariableDependenciesList),
		Encoder:         rc.cacheDeterministicEncoder,
		ParentAppender: func(
			evaluation model_core.PatchedMessage[*model_evaluation_pb.Evaluation, TMetadata],
			externalObject *model_core.Decodable[model_core.CreatedObject[TMetadata]],
		) error {
			directVariableDependencies, err := btree.MaybeMergeNodes(
				directVariableDependenciesList.Message,
				externalObject,
				evaluation.Patcher,
				keysParentNodeComputer,
			)
			if err != nil {
				return err
			}
			evaluation.Message.DirectVariableDependencyKeys = directVariableDependencies
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

func (rc *RecursiveComputer[TReference, TMetadata]) buildGraphletEvaluation(ctx context.Context, value model_core.Message[*model_core_pb.Any, TReference], directVariableDependencies []*KeyState[TReference, TMetadata]) (inlinedtree.Candidate[*model_evaluation_pb.Graphlet, TMetadata], error) {
	evaluation, err := rc.buildEvaluation(ctx, value, directVariableDependencies)
	if err != nil {
		return inlinedtree.Candidate[*model_evaluation_pb.Graphlet, TMetadata]{}, err
	}
	return inlinedtree.Candidate[*model_evaluation_pb.Graphlet, TMetadata]{
		ExternalMessage: model_core.ProtoToBinaryMarshaler(evaluation),
		Encoder:         rc.cacheDeterministicEncoder,
		ParentAppender: inlinedtree.Capturing(ctx, rc.objectManager, func(
			graphlet model_core.PatchedMessage[*model_evaluation_pb.Graphlet, TMetadata],
			externalObject *model_core.Decodable[model_core.MetadataEntry[TMetadata]],
		) {
			if externalObject == nil {
				graphlet.Message.Evaluation = &model_evaluation_pb.Graphlet_EvaluationInline{
					EvaluationInline: evaluation.Message,
				}
			} else {
				graphlet.Message.Evaluation = &model_evaluation_pb.Graphlet_EvaluationExternal{
					EvaluationExternal: graphlet.Patcher.AddDecodableReference(*externalObject),
				}
			}
		}),
	}, nil
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

	ks := rc.getOrCreateKeyStateLocked(keyReference, keyMessageFetcher, key.Message.TypeUrl, initialValueState, evaluationQueue)
	if !ks.valueState.isEvaluated() {
		e.missingDependencies[ks] = struct{}{}
		return nil
	}
	if err := ks.valueState.getError(); err != nil {
		// In case of failures we always assume that the
		// dependency is variable. This is done to ensure that
		// the evaluation results always contain all keys
		// leading up to the failure.
		e.directVariableDependencies[ks] = struct{}{}
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

// variableDependenciesGatherer can be used to compute the hoisted and
// nested variable dependencies of a graphlet. In other words, it
// implements a strategy for determining which dependencies are stored
// above or below a given key.
//
// The nested variable dependencies differ from the hoisted variable
// dependencies in that these are only needed when actually constructing
// the graphlet of the current key. The hoisted variable dependencies
// need to be tracked in the value state permanently, as those are also
// used when constructing graphlets for parents. To permit recomputing
// just the nested variable dependencies, the hoisted variable
// dependencies map can be set to nil.
type variableDependenciesGatherer[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	keyRawReference             []byte
	hoistedVariableDependencies map[*KeyState[TReference, TMetadata]]struct{}
	nestedVariableDependencies  map[*KeyState[TReference, TMetadata]]struct{}
}

func (g *variableDependenciesGatherer[TReference, TMetadata]) gatherDependencies(variableDependencies []*KeyState[TReference, TMetadata]) {
	for _, ksDep := range variableDependencies {
		if cmp := bytes.Compare(g.keyRawReference, ksDep.keyReference.GetRawReference()); cmp < 0 {
			// Hoist dependencies having a higher key reference.
			// That way the size of this graphlet remains bounded.
			if g.hoistedVariableDependencies != nil {
				g.hoistedVariableDependencies[ksDep] = struct{}{}
			}
		} else if cmp > 0 {
			if ksDep.isLookup /* TODO: || ksDep.value.getError() != nil */ {
				// Keys that only have acyclic dependencies
				// and only look up data within their parents
				// are better hoisted, as they mostly just
				// cause churn.
				//
				// Keys that are in an error state are also
				// better hoisted. The idea behind nesting
				// dependencies is that it allows us to
				// perform cache lookups for larger parts of
				// the build graph. As errors are not cached,
				// there is no point in nesting them.
				if g.hoistedVariableDependencies != nil {
					g.hoistedVariableDependencies[ksDep] = struct{}{}
				}
			} else if _, ok := g.nestedVariableDependencies[ksDep]; !ok {
				// Dependencies having a lower key reference
				// can be stored inside this graphlet.
				g.nestedVariableDependencies[ksDep] = struct{}{}
				g.gatherDependencies(ksDep.valueState.getHoistedDependencies())
			}
		} else {
			// Cyclic dependency. Neither hoist nor nest it, as
			// storing it once is enough.
		}
	}
}

func (e *recursivelyComputingEnvironment[TReference, TMetadata]) getVariableDependenciesComputedValueState(ks *KeyState[TReference, TMetadata], dependenciesHashRecordReference object.LocalReference) variableDependenciesComputedValueState[TReference, TMetadata] {
	return e.computer.newVariableDependenciesComputedValueState(ks, sortedKeyStates(e.directVariableDependencies), dependenciesHashRecordReference)
}

// newVariableDependenciesComputedValueState creates the value state of
// a key whose value has been obtained, and has a non-empty set of
// direct variable dependencies. The list of direct variable
// dependencies must be sorted by key reference.
func (*RecursiveComputer[TReference, TMetadata]) newVariableDependenciesComputedValueState(ks *KeyState[TReference, TMetadata], directVariableDependencies []*KeyState[TReference, TMetadata], dependenciesHashRecordReference object.LocalReference) variableDependenciesComputedValueState[TReference, TMetadata] {
	if len(directVariableDependencies) == 0 {
		panic("key does not have any direct variable dependencies")
	}
	gatherer := variableDependenciesGatherer[TReference, TMetadata]{
		keyRawReference:             ks.keyReference.GetRawReference(),
		hoistedVariableDependencies: map[*KeyState[TReference, TMetadata]]struct{}{},
		nestedVariableDependencies:  map[*KeyState[TReference, TMetadata]]struct{}{},
	}
	gatherer.gatherDependencies(directVariableDependencies)
	hoistedVariableDependencies := sortedKeyStates(gatherer.hoistedVariableDependencies)
	if len(hoistedVariableDependencies) == 0 {
		panic("key does not have any hoisted variable dependencies")
	}

	return variableDependenciesComputedValueState[TReference, TMetadata]{
		variableDependencyValueState: variableDependencyValueState[TReference, TMetadata]{
			dependenciesHashRecordReference: dependenciesHashRecordReference,
		},
		directVariableDependencies:  directVariableDependencies,
		hoistedVariableDependencies: hoistedVariableDependencies,
	}
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
	isLookup          bool

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

// computeDependenciesHash computes a hash of the keys and values of a
// list of dependencies that is sorted by key reference. As the
// references of DependenciesHashRecord messages already incorporate
// both the key and value of each dependency, it suffices to hash those.
// All references have the same length, as they use the same reference
// format.
//
// This function must be called with the RecursiveComputer's lock held,
// and may only be called against dependencies that have been evaluated
// successfully and are variable.
func computeDependenciesHash[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](dependencies []*KeyState[TReference, TMetadata]) [sha256.Size]byte {
	hasher := sha256.New()
	for _, ksDep := range dependencies {
		hasher.Write(ksDep.valueState.getDependenciesHashRecordReference().GetRawReference())
	}
	return [sha256.Size]byte(hasher.Sum(nil))
}

// mergeSortedKeyStates merges two lists of KeyStates that are sorted
// by key reference, yielding a single sorted list. If the lists have
// any KeyStates in common, false is returned.
func mergeSortedKeyStates[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](a, b []*KeyState[TReference, TMetadata]) ([]*KeyState[TReference, TMetadata], bool) {
	merged := make([]*KeyState[TReference, TMetadata], 0, len(a)+len(b))
	for len(a) > 0 && len(b) > 0 {
		switch cmp := bytes.Compare(a[0].keyReference.GetRawReference(), b[0].keyReference.GetRawReference()); {
		case cmp < 0:
			merged = append(merged, a[0])
			a = a[1:]
		case cmp > 0:
			merged = append(merged, b[0])
			b = b[1:]
		default:
			return nil, false
		}
	}
	merged = append(merged, a...)
	return append(merged, b...), true
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
		ks *KeyState[TReference, TMetadata],
	) (
		newValueState valueState[TReference, TMetadata],
		graphlet model_core.Message[*model_evaluation_pb.Graphlet, TReference],
		err error,
	)

	// Return the set of dependencies that are referenced by the
	// graphlet returned by getGraphlet(), but for which an
	// evaluation is not contained within. These dependencies need
	// to be placed in a graphlet at a higher level.
	getHoistedDependencies() []*KeyState[TReference, TMetadata]

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

type computingValueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	unevaluatedValueState[TReference, TMetadata]

	previousDirectVariableDependencies []*KeyState[TReference, TMetadata]
}

func (computingValueState[TReference, TMetadata]) disableCacheLookup() valueState[TReference, TMetadata] {
	return nil
}

func (vs *computingValueState[TReference, TMetadata]) gotFailedDependency(err NestedError[TReference, TMetadata]) valueState[TReference, TMetadata] {
	return &evaluationFailedValueState[TReference, TMetadata]{
		err:                        err,
		directVariableDependencies: vs.previousDirectVariableDependencies,
	}
}

func (computingValueState[TReference, TMetadata]) getHoistedDependencies() []*KeyState[TReference, TMetadata] {
	return nil
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

func (vs *earlyFailedValueState[TReference, TMetadata]) getGraphlet(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) (valueState[TReference, TMetadata], model_core.Message[*model_evaluation_pb.Graphlet, TReference], error) {
	return vs, model_core.NewSimpleMessage[TReference](&model_evaluation_pb.Graphlet{}), nil
}

func (earlyFailedValueState[TReference, TMetadata]) getHoistedDependencies() []*KeyState[TReference, TMetadata] {
	return nil
}

type evaluationFailedValueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	evaluatedValueState[TReference, TMetadata]

	err                        error
	directVariableDependencies []*KeyState[TReference, TMetadata]
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

func (vs *evaluationFailedValueState[TReference, TMetadata]) getGraphlet(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) (valueState[TReference, TMetadata], model_core.Message[*model_evaluation_pb.Graphlet, TReference], error) {
	graphletEvaluation, err := rc.buildGraphletEvaluation(ctx, model_core.Message[*model_core_pb.Any, TReference]{}, vs.directVariableDependencies)
	if err != nil {
		return vs, model_core.Message[*model_evaluation_pb.Graphlet, TReference]{}, err
	}
	patchedGraphlet, err := inlinedtree.Build(
		inlinedtree.CandidateList[*model_evaluation_pb.Graphlet, TMetadata]{graphletEvaluation},
		&inlinedtree.Options{
			ReferenceFormat:  rc.referenceFormat,
			MaximumSizeBytes: 1 << 16,
		},
	)
	return vs, model_core.Unpatch(rc.objectManager, patchedGraphlet).Decay(), nil
}

func (vs *evaluationFailedValueState[TReference, TMetadata]) getHoistedDependencies() []*KeyState[TReference, TMetadata] {
	// Given that errors are never stored in the cache, there is no
	// point in giving these nodes nested dependencies. Hoist
	// everything.
	return vs.directVariableDependencies
}

type noDependenciesValueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	evaluatedValueState[TReference, TMetadata]
}

func (noDependenciesValueState[TReference, TMetadata]) getGraphlet(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) (valueState[TReference, TMetadata], model_core.Message[*model_evaluation_pb.Graphlet, TReference], error) {
	panic("key cannot be embedded into another graphlet, as it is not a variable dependency")
}

func (noDependenciesValueState[TReference, TMetadata]) getHoistedDependencies() []*KeyState[TReference, TMetadata] {
	panic("key cannot be embedded into another graphlet, as it is not a variable dependency")
}

func (noDependenciesValueState[TReference, TMetadata]) getDependenciesHashRecordReference() object.LocalReference {
	panic("key cannot be used as a dependency, as its value is not variable")
}

func (noDependenciesValueState[TReference, TMetadata]) isVariableDependency() bool {
	return false
}

type variableDependencyValueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	dependenciesHashRecordReference object.LocalReference
}

func (vs *variableDependencyValueState[TReference, TMetadata]) getDependenciesHashRecordReference() object.LocalReference {
	return vs.dependenciesHashRecordReference
}

func (variableDependencyValueState[TReference, TMetadata]) isVariableDependency() bool {
	return true
}

type variableDependenciesComputedValueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	evaluatedValueState[TReference, TMetadata]
	variableDependencyValueState[TReference, TMetadata]

	directVariableDependencies  []*KeyState[TReference, TMetadata]
	hoistedVariableDependencies []*KeyState[TReference, TMetadata]

	// If set, the value was obtained from a cache entry having the
	// same set of direct variable dependencies. This means that
	// there is no need to upload it to the cache once more.
	valueIsCached bool
}

func (vs *variableDependenciesComputedValueState[TReference, TMetadata]) getHoistedDependencies() []*KeyState[TReference, TMetadata] {
	return vs.hoistedVariableDependencies
}

func (vs *variableDependenciesComputedValueState[TReference, TMetadata]) buildGraphlet(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata], value model_core.Message[*model_core_pb.Any, TReference]) (variableDependenciesMarshaledValueState[TReference, TMetadata], model_core.Message[*model_evaluation_pb.Graphlet, TReference], error) {
	inlineCandidates := make(inlinedtree.CandidateList[*model_evaluation_pb.Graphlet, TMetadata], 0, 2)
	defer inlineCandidates.Discard()

	graphletEvaluation, err := rc.buildGraphletEvaluation(ctx, value, vs.directVariableDependencies)
	if err != nil {
		return variableDependenciesMarshaledValueState[TReference, TMetadata]{}, model_core.Message[*model_evaluation_pb.Graphlet, TReference]{}, err
	}
	inlineCandidates = append(inlineCandidates, graphletEvaluation)

	// Attach evaluations of direct and transitive dependencies that
	// did not get hoisted to the parent.
	gatherer := variableDependenciesGatherer[TReference, TMetadata]{
		keyRawReference:            ks.keyReference.GetRawReference(),
		nestedVariableDependencies: map[*KeyState[TReference, TMetadata]]struct{}{},
	}
	gatherer.gatherDependencies(vs.directVariableDependencies)
	nestedVariableDependencies := sortedKeyStates(gatherer.nestedVariableDependencies)

	evaluationsParentNodeComputer := rc.getEvaluationsParentNodeComputer(ctx)
	dependencyEvaluationsList, err := rc.getEvaluationsForSortedList(ctx, nestedVariableDependencies)
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

	patchedGraphlet, err := inlinedtree.Build(
		inlineCandidates,
		&inlinedtree.Options{
			ReferenceFormat:  rc.referenceFormat,
			MaximumSizeBytes: 1 << 16,
		},
	)
	if err != nil {
		return variableDependenciesMarshaledValueState[TReference, TMetadata]{}, model_core.Message[*model_evaluation_pb.Graphlet, TReference]{}, err
	}

	graphlet := model_core.Unpatch(rc.objectManager, patchedGraphlet).Decay()
	return variableDependenciesMarshaledValueState[TReference, TMetadata]{
		variableDependencyValueState: vs.variableDependencyValueState,
		graphlet:                     graphlet,
		hoistedVariableDependencies:  vs.hoistedVariableDependencies,
		directVariableDependencies:   vs.directVariableDependencies,
		valueIsCached:                vs.valueIsCached,
	}, graphlet, nil
}

type variableDependenciesMarshaledValueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	evaluatedValueState[TReference, TMetadata]
	variableDependencyValueState[TReference, TMetadata]

	graphlet                    model_core.Message[*model_evaluation_pb.Graphlet, TReference]
	hoistedVariableDependencies []*KeyState[TReference, TMetadata]
	directVariableDependencies  []*KeyState[TReference, TMetadata]
	valueIsCached               bool
}

func (vs *variableDependenciesMarshaledValueState[TReference, TMetadata]) getHoistedDependencies() []*KeyState[TReference, TMetadata] {
	return vs.hoistedVariableDependencies
}

type initialMessageValueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	unevaluatedValueState[TReference, TMetadata]
}

func (initialMessageValueState[TReference, TMetadata]) evaluate(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) (valueState[TReference, TMetadata], []*KeyState[TReference, TMetadata]) {
	rootTagKeyHash, err := rc.getCacheLookupTagKeyHash(ks, nil)
	if err != nil {
		return &earlyFailedValueState[TReference, TMetadata]{
			err: err,
		}, nil
	}
	rootLookupResult, rootLookupResultReference, found, err := rc.readLookupResult(ctx, rootTagKeyHash)
	if err != nil {
		return &earlyFailedValueState[TReference, TMetadata]{
			err: err,
		}, nil
	}
	if !found {
		return (&computingMessageValueState[TReference, TMetadata]{}).evaluate(ctx, rc, ks)
	}

	switch r := rootLookupResult.Message.Result.(type) {
	case *model_evaluation_cache_pb.LookupResult_Initial_:
		// Start off by attempting to obtain a cache hit on the
		// graphlet. Retain the expected direct dependencies of
		// the key, so that we can fall back to obtaining a
		// cache hit on the value.
		dependencies, ok, err := rc.resolveCachedKeys(ctx, model_core.Nested(rootLookupResult, r.Initial.GraphletVariableDependencyKeys))
		if err != nil {
			return &earlyFailedValueState[TReference, TMetadata]{
				err: err,
			}, nil
		}
		valueVariableDependencyKeys := model_core.Nested(rootLookupResult, r.Initial.ValueVariableDependencyKeys)
		if !ok {
			// Malformed list of graphlet dependencies.
			// Attempt a lookup of the value instead.
			return (&lookingUpMessageValueState[TReference, TMetadata]{
				scope:                       model_evaluation_cache_pb.LookupTagKeyData_SubsequentLookup_GRAPHLET,
				valueVariableDependencyKeys: valueVariableDependencyKeys,
			}).fallBackToValueLookup(ctx, rc, ks)
		}
		return (&lookingUpMessageValueState[TReference, TMetadata]{
			scope:                       model_evaluation_cache_pb.LookupTagKeyData_SubsequentLookup_GRAPHLET,
			dependencies:                dependencies,
			valueVariableDependencyKeys: valueVariableDependencyKeys,
		}).evaluate(ctx, rc, ks)
	case *model_evaluation_cache_pb.LookupResult_HitValue:
		// It turns out the current key does not have any
		// dependencies. This means that the initial lookup
		// returned a value immediately.
		return &noDependenciesUploadedMessageValueState[TReference, TMetadata]{
			hitValueLookupResultReference: rootLookupResultReference,
		}, nil
	default:
		// Malformed cache entry.
		return (&computingMessageValueState[TReference, TMetadata]{}).evaluate(ctx, rc, ks)
	}
}

func (initialMessageValueState[TReference, TMetadata]) disableCacheLookup() valueState[TReference, TMetadata] {
	return &computingMessageValueState[TReference, TMetadata]{}
}

func (initialMessageValueState[TReference, TMetadata]) gotFailedDependency(err NestedError[TReference, TMetadata]) valueState[TReference, TMetadata] {
	panic("key has never been attempted to be evaluated, meaning that it cannot have any dependencies")
}

func (vs initialMessageValueState[TReference, TMetadata]) getGraphlet(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) (valueState[TReference, TMetadata], model_core.Message[*model_evaluation_pb.Graphlet, TReference], error) {
	return vs, model_core.NewSimpleMessage[TReference](&model_evaluation_pb.Graphlet{}), nil
}

func (initialMessageValueState[TReference, TMetadata]) getHoistedDependencies() []*KeyState[TReference, TMetadata] {
	return nil
}

// lookingUpMessageValueState is the value state of a key for which a
// cache lookup is in progress. The initial lookup result has been
// obtained, and it is now attempting to traverse the chain of
// subsequent lookup results belonging to either the graphlet or the
// value of the key.
//
// The lookup proceeds by waiting for all dependencies to be evaluated,
// computing a hash of their keys and values, and resolving the tag
// corresponding to that hash. This either yields a hit, or returns a
// set of additional dependencies whose values need to be incorporated
// into the hash.
type lookingUpMessageValueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	unevaluatedValueState[TReference, TMetadata]

	// Whether a hit on the graphlet or value is being looked up.
	scope model_evaluation_cache_pb.LookupTagKeyData_SubsequentLookup_Scope

	// Dependencies gathered for the current scope up to this point,
	// sorted by key reference.
	dependencies []*KeyState[TReference, TMetadata]

	// If the graphlet is being looked up, the keys of the direct
	// dependencies of the key that should be used to perform a
	// lookup of the value in case the graphlet cannot be found.
	valueVariableDependencyKeys model_core.Message[[]*model_evaluation_pb.Keys, TReference]
}

func (vs *lookingUpMessageValueState[TReference, TMetadata]) evaluate(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) (valueState[TReference, TMetadata], []*KeyState[TReference, TMetadata]) {
	dependencies := vs.dependencies
	for {
		// Wait for all dependencies gathered up to this point
		// to be evaluated, and compute the hash of their keys
		// and values. If any of the dependencies failed or
		// turns out to be constant, the cache entry is not
		// applicable.
		var unevaluatedDependencies []*KeyState[TReference, TMetadata]
		usable := true
		var dependenciesHash [sha256.Size]byte
		rc.lock.RLock()
		for _, ksDep := range dependencies {
			if vsDep := ksDep.valueState; !vsDep.isEvaluated() {
				unevaluatedDependencies = append(unevaluatedDependencies, ksDep)
			} else if vsDep.getError() != nil || !vsDep.isVariableDependency() {
				usable = false
				break
			}
		}
		if usable && len(unevaluatedDependencies) == 0 {
			dependenciesHash = computeDependenciesHash(dependencies)
		}
		rc.lock.RUnlock()
		if !usable {
			return vs.fallBackToValueLookup(ctx, rc, ks)
		}
		if len(unevaluatedDependencies) > 0 {
			return &lookingUpMessageValueState[TReference, TMetadata]{
				scope:                       vs.scope,
				dependencies:                dependencies,
				valueVariableDependencyKeys: vs.valueVariableDependencyKeys,
			}, unevaluatedDependencies
		}

		tagKeyHash, err := rc.getCacheLookupTagKeyHash(ks, &model_evaluation_cache_pb.LookupTagKeyData_SubsequentLookup{
			Scope:            vs.scope,
			DependenciesHash: dependenciesHash[:],
		})
		if err != nil {
			return &earlyFailedValueState[TReference, TMetadata]{
				err: err,
			}, nil
		}
		lookupResult, lookupResultReference, found, err := rc.readLookupResult(ctx, tagKeyHash)
		if err != nil {
			return &earlyFailedValueState[TReference, TMetadata]{
				err: err,
			}, nil
		}
		if !found {
			return vs.fallBackToValueLookup(ctx, rc, ks)
		}

		switch r := lookupResult.Message.Result.(type) {
		case *model_evaluation_cache_pb.LookupResult_HitGraphlet:
			if vs.scope != model_evaluation_cache_pb.LookupTagKeyData_SubsequentLookup_GRAPHLET {
				return vs.fallBackToValueLookup(ctx, rc, ks)
			}
			evaluation, err := GraphletGetEvaluation(ctx, rc.evaluationReader, model_core.Nested(lookupResult, r.HitGraphlet))
			if err != nil {
				return &earlyFailedValueState[TReference, TMetadata]{
					err: err,
				}, nil
			}
			value, err := model_core.FlattenAny(model_core.Nested(evaluation, evaluation.Message.Value))
			if err != nil {
				return vs.fallBackToValueLookup(ctx, rc, ks)
			}
			return &variableDependenciesUploadedMessageValueState[TReference, TMetadata]{
				variableDependencyValueState: variableDependencyValueState[TReference, TMetadata]{
					dependenciesHashRecordReference: rc.computeDependenciesHashRecordReferenceForMessage(ks.keyReference, value),
				},
				hitGraphletLookupResultReference: lookupResultReference,
				hoistedVariableDependencies:      dependencies,
			}, nil
		case *model_evaluation_cache_pb.LookupResult_HitValue:
			if vs.scope != model_evaluation_cache_pb.LookupTagKeyData_SubsequentLookup_VALUE {
				return vs.fallBackToValueLookup(ctx, rc, ks)
			}
			value, err := model_core.FlattenAny(model_core.Nested(lookupResult, r.HitValue))
			if err != nil {
				return vs.fallBackToValueLookup(ctx, rc, ks)
			}

			// We only obtained the value of the key, not its
			// graphlet. As we know the full set of direct
			// dependencies, the graphlet can be constructed
			// locally. There is no need to upload the value
			// to the cache once more.
			computedValueState := rc.newVariableDependenciesComputedValueState(
				ks,
				dependencies,
				rc.computeDependenciesHashRecordReferenceForMessage(ks.keyReference, value),
			)
			computedValueState.valueIsCached = true
			return &variableDependenciesComputedMessageValueState[TReference, TMetadata]{
				variableDependenciesComputedValueState: computedValueState,
				computedMessageValueState: computedMessageValueState[TReference, TMetadata]{
					value: value,
				},
			}, nil
		case *model_evaluation_cache_pb.LookupResult_MissingDependencies_:
			// The dependencies gathered up to this point
			// are insufficient to obtain a hit. Extend the
			// set of dependencies and perform another lookup.
			additionalDependencies, ok, err := rc.resolveCachedKeys(ctx, model_core.Nested(lookupResult, r.MissingDependencies.AdditionalVariableDependencyKeys))
			if err != nil {
				return &earlyFailedValueState[TReference, TMetadata]{
					err: err,
				}, nil
			}
			if !ok {
				return vs.fallBackToValueLookup(ctx, rc, ks)
			}
			dependencies, ok = mergeSortedKeyStates(dependencies, additionalDependencies)
			if !ok {
				// Additional dependencies overlap with
				// the ones gathered previously.
				return vs.fallBackToValueLookup(ctx, rc, ks)
			}
		default:
			// Malformed cache entry.
			return vs.fallBackToValueLookup(ctx, rc, ks)
		}
	}
}

// fallBackToValueLookup is called when a cache lookup of the current
// scope yields a miss. If the graphlet was being looked up, a lookup of
// the value is attempted. Otherwise, the key is evaluated.
func (vs *lookingUpMessageValueState[TReference, TMetadata]) fallBackToValueLookup(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) (valueState[TReference, TMetadata], []*KeyState[TReference, TMetadata]) {
	if vs.scope == model_evaluation_cache_pb.LookupTagKeyData_SubsequentLookup_GRAPHLET && len(vs.valueVariableDependencyKeys.Message) > 0 {
		dependencies, ok, err := rc.resolveCachedKeys(ctx, vs.valueVariableDependencyKeys)
		if err != nil {
			return &earlyFailedValueState[TReference, TMetadata]{
				err: err,
			}, nil
		}
		if ok {
			return (&lookingUpMessageValueState[TReference, TMetadata]{
				scope:        model_evaluation_cache_pb.LookupTagKeyData_SubsequentLookup_VALUE,
				dependencies: dependencies,
			}).evaluate(ctx, rc, ks)
		}
	}
	return (&computingMessageValueState[TReference, TMetadata]{}).evaluate(ctx, rc, ks)
}

func (lookingUpMessageValueState[TReference, TMetadata]) disableCacheLookup() valueState[TReference, TMetadata] {
	return &computingMessageValueState[TReference, TMetadata]{}
}

func (lookingUpMessageValueState[TReference, TMetadata]) gotFailedDependency(err NestedError[TReference, TMetadata]) valueState[TReference, TMetadata] {
	return &evaluationFailedValueState[TReference, TMetadata]{
		err: err,
	}
}

func (vs *lookingUpMessageValueState[TReference, TMetadata]) getGraphlet(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) (valueState[TReference, TMetadata], model_core.Message[*model_evaluation_pb.Graphlet, TReference], error) {
	return vs, model_core.NewSimpleMessage[TReference](&model_evaluation_pb.Graphlet{}), nil
}

func (lookingUpMessageValueState[TReference, TMetadata]) getHoistedDependencies() []*KeyState[TReference, TMetadata] {
	return nil
}

type computingMessageValueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	computingValueState[TReference, TMetadata]
}

func (vs *computingMessageValueState[TReference, TMetadata]) evaluate(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) (valueState[TReference, TMetadata], []*KeyState[TReference, TMetadata]) {
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
					err:                        err,
					directVariableDependencies: sortedKeyStates(e.directVariableDependencies),
				}, nil
			}
			vs.previousDirectVariableDependencies = sortedKeyStates(e.directVariableDependencies)
			return vs, missingDependencies
		}
		return &evaluationFailedValueState[TReference, TMetadata]{
			err:                        err,
			directVariableDependencies: sortedKeyStates(e.directVariableDependencies),
		}, nil
	}
	anyValue, err := model_core.MarshalTopLevelAny(model_core.Unpatch(rc.objectManager, value))
	if err != nil {
		return &evaluationFailedValueState[TReference, TMetadata]{
			err:                        fmt.Errorf("failed to marshal value yielded by evaluation function: %w", err),
			directVariableDependencies: sortedKeyStates(e.directVariableDependencies),
		}, nil
	}
	if len(e.directVariableDependencies) == 0 {
		return &noDependenciesComputedMessageValueState[TReference, TMetadata]{
			computedMessageValueState: computedMessageValueState[TReference, TMetadata]{
				value: anyValue,
			},
		}, nil
	}
	return &variableDependenciesComputedMessageValueState[TReference, TMetadata]{
		variableDependenciesComputedValueState: e.getVariableDependenciesComputedValueState(
			ks,
			rc.computeDependenciesHashRecordReferenceForMessage(ks.keyReference, anyValue),
		),
		computedMessageValueState: computedMessageValueState[TReference, TMetadata]{
			value: anyValue,
		},
	}, nil
}

func (vs *computingMessageValueState[TReference, TMetadata]) getGraphlet(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) (valueState[TReference, TMetadata], model_core.Message[*model_evaluation_pb.Graphlet, TReference], error) {
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

	rootLookupResultReference, err := rc.storeLookupResult(
		ctx,
		rootTagKeyHash,
		model_core.MustBuildPatchedMessage(
			func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_evaluation_cache_pb.LookupResult {
				return &model_evaluation_cache_pb.LookupResult{
					Result: &model_evaluation_cache_pb.LookupResult_HitValue{
						HitValue: model_core.Patch(
							rc.objectManager,
							model_core.WrapTopLevelAny(vs.value).Decay(),
						).Merge(patcher),
					},
				}
			},
		),
	)
	if err != nil {
		return vs, err
	}

	// Future accesses to this key can reload the value that's part
	// of the LookupResult. This prevents the need for keeping the
	// value in memory.
	return &noDependenciesUploadedMessageValueState[TReference, TMetadata]{
		hitValueLookupResultReference: model_core.CopyDecodable(rootTagKeyHash, rootLookupResultReference),
	}, nil
}

type variableDependenciesComputedMessageValueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	variableDependenciesComputedValueState[TReference, TMetadata]
	computedMessageValueState[TReference, TMetadata]
}

func (vs *variableDependenciesComputedMessageValueState[TReference, TMetadata]) upload(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) (valueState[TReference, TMetadata], error) {
	// Uploading the value requires us to have it in graphlet form
	// anyway. Compute the graphlet and then call
	// variableDependenciesMarshaledMessageValueState.upload().
	marshaledValueState, _, err := vs.getGraphlet(ctx, rc, ks)
	if err != nil {
		return marshaledValueState, err
	}
	return marshaledValueState.upload(ctx, rc, ks)
}

func (vs *variableDependenciesComputedMessageValueState[TReference, TMetadata]) getGraphlet(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) (valueState[TReference, TMetadata], model_core.Message[*model_evaluation_pb.Graphlet, TReference], error) {
	variableDependenciesMarshaledValueState, graphlet, err := vs.buildGraphlet(
		ctx,
		rc,
		ks,
		model_core.WrapTopLevelAny(vs.value).Decay(),
	)
	if err != nil {
		return vs, model_core.Message[*model_evaluation_pb.Graphlet, TReference]{}, err
	}
	return &variableDependenciesMarshaledMessageValueState[TReference, TMetadata]{
		variableDependenciesMarshaledValueState: variableDependenciesMarshaledValueState,
	}, graphlet, nil
}

func (vs *variableDependenciesComputedMessageValueState[TReference, TMetadata]) getDependenciesHashRecordReference() object.LocalReference {
	return vs.dependenciesHashRecordReference
}

type variableDependenciesMarshaledMessageValueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	variableDependenciesMarshaledValueState[TReference, TMetadata]
}

func (vs *variableDependenciesMarshaledMessageValueState[TReference, TMetadata]) upload(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) (valueState[TReference, TMetadata], error) {
	buildHitGraphlet := func() (model_core.PatchedMessage[*model_evaluation_cache_pb.LookupResult, TMetadata], error) {
		return model_core.MustBuildPatchedMessage(
			func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_evaluation_cache_pb.LookupResult {
				return &model_evaluation_cache_pb.LookupResult{
					Result: &model_evaluation_cache_pb.LookupResult_HitGraphlet{
						HitGraphlet: model_core.Patch(
							rc.objectManager,
							vs.graphlet,
						).Merge(patcher),
					},
				}
			},
		), nil
	}

	// Only upload a "hit value" lookup result if the value's
	// dependencies differ from the graphlet's. Given that we
	// always attempt to look up the graphlet first, a "hit value"
	// lookup result entry having the same dependencies would never
	// be requested. There is also no need to upload it if the
	// value was obtained from the cache in the first place.
	var buildHitValue lookupResultBuilder[TMetadata]
	if !vs.valueIsCached && !slices.Equal(vs.hoistedVariableDependencies, vs.directVariableDependencies) {
		buildHitValue = func() (model_core.PatchedMessage[*model_evaluation_cache_pb.LookupResult, TMetadata], error) {
			evaluation, err := GraphletGetEvaluation(ctx, rc.evaluationReader, vs.graphlet)
			if err != nil {
				return model_core.PatchedMessage[*model_evaluation_cache_pb.LookupResult, TMetadata]{}, err
			}
			return model_core.MustBuildPatchedMessage(
				func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_evaluation_cache_pb.LookupResult {
					return &model_evaluation_cache_pb.LookupResult{
						Result: &model_evaluation_cache_pb.LookupResult_HitValue{
							HitValue: model_core.Patch(
								rc.objectManager,
								model_core.Nested(evaluation, evaluation.Message.Value),
							).Merge(patcher),
						},
					}
				},
			), nil
		}
	}

	hitGraphletLookupResultReference, err := rc.uploadVariableDependenciesLookupResults(
		ctx,
		ks,
		vs.hoistedVariableDependencies,
		vs.directVariableDependencies,
		buildHitGraphlet,
		buildHitValue,
	)
	if err != nil {
		return vs, err
	}

	// Subsequent attempts to access the graphlet may use the copy
	// that has been written to storage.
	return &variableDependenciesUploadedMessageValueState[TReference, TMetadata]{
		variableDependencyValueState:     vs.variableDependencyValueState,
		hitGraphletLookupResultReference: hitGraphletLookupResultReference,
		hoistedVariableDependencies:      vs.hoistedVariableDependencies,
	}, nil
}

// lookupResultBuilder is invoked to construct the LookupResult message
// that needs to be stored at the end of a chain of lookup results,
// containing the graphlet or value of a key.
type lookupResultBuilder[TMetadata model_core.ReferenceMetadata] func() (model_core.PatchedMessage[*model_evaluation_cache_pb.LookupResult, TMetadata], error)

// uploadVariableDependenciesLookupResults stores lookup results for a
// key having a non-empty set of variable dependencies, so that
// subsequent builds may obtain cache hits on the key's graphlet and
// value.
//
// As the set of dependencies of a key may vary between evaluations,
// existing lookup results are merged with the ones that are being
// stored. The initial lookup result is reduced to the dependencies
// that are shared by all evaluations, and subsequent lookup results
// returning missing dependencies are used to discriminate between
// evaluations. The reference of the lookup result containing the
// graphlet is returned.
func (rc *RecursiveComputer[TReference, TMetadata]) uploadVariableDependenciesLookupResults(
	ctx context.Context,
	ks *KeyState[TReference, TMetadata],
	graphletVariableDependencies []*KeyState[TReference, TMetadata],
	valueVariableDependencies []*KeyState[TReference, TMetadata],
	buildHitGraphlet lookupResultBuilder[TMetadata],
	buildHitValue lookupResultBuilder[TMetadata],
) (model_core.Decodable[TReference], error) {
	var badReference model_core.Decodable[TReference]
	rootTagKeyHash, err := rc.getCacheLookupTagKeyHash(ks, nil)
	if err != nil {
		return badReference, err
	}
	rootLookupResult, _, found, err := rc.readLookupResult(ctx, rootTagKeyHash)
	if err != nil {
		return badReference, err
	}

	// If an initial lookup result already exists, reduce its sets
	// of dependencies to the ones shared with the current
	// evaluation. If the existing lookup result contains
	// indirection that has been present for too long, discard it
	// and start from scratch. This ensures that the amount of
	// indirection does not continue to grow over time.
	graphletInitialDependencies, valueInitialDependencies := graphletVariableDependencies, valueVariableDependencies
	var containsIndirectionSince *timestamppb.Timestamp
	rootNeedsUpdate := true
	if found {
		if initial, ok := rootLookupResult.Message.Result.(*model_evaluation_cache_pb.LookupResult_Initial_); ok {
			since := initial.Initial.ContainsIndirectionSince
			if since == nil || (since.IsValid() && rc.clock.Now().Sub(since.AsTime()) < rc.cacheLookupResultIndirectionMaximumAge) {
				var graphletChanged, valueChanged bool
				graphletInitialDependencies, graphletChanged, err = rc.intersectCachedKeys(
					ctx,
					model_core.Nested(rootLookupResult, initial.Initial.GraphletVariableDependencyKeys),
					graphletVariableDependencies,
				)
				if err != nil {
					return badReference, err
				}
				valueInitialDependencies, valueChanged, err = rc.intersectCachedKeys(
					ctx,
					model_core.Nested(rootLookupResult, initial.Initial.ValueVariableDependencyKeys),
					valueVariableDependencies,
				)
				if err != nil {
					return badReference, err
				}
				containsIndirectionSince = since
				rootNeedsUpdate = graphletChanged || valueChanged
			}
		}
	}

	// Store the chains of subsequent lookup results for the
	// graphlet and value.
	group, groupCtx := errgroup.WithContext(ctx)
	var hitGraphletLookupResultReference model_core.Decodable[TReference]
	var graphletContainsIndirection, valueContainsIndirection bool
	group.Go(func() error {
		var err error
		hitGraphletLookupResultReference, graphletContainsIndirection, err = rc.uploadLookupResultChain(
			groupCtx,
			ks,
			model_evaluation_cache_pb.LookupTagKeyData_SubsequentLookup_GRAPHLET,
			graphletInitialDependencies,
			graphletVariableDependencies,
			buildHitGraphlet,
		)
		return err
	})
	if buildHitValue != nil {
		group.Go(func() error {
			var err error
			_, valueContainsIndirection, err = rc.uploadLookupResultChain(
				groupCtx,
				ks,
				model_evaluation_cache_pb.LookupTagKeyData_SubsequentLookup_VALUE,
				valueInitialDependencies,
				valueVariableDependencies,
				buildHitValue,
			)
			return err
		})
	}
	if err := group.Wait(); err != nil {
		return badReference, err
	}

	if (graphletContainsIndirection || valueContainsIndirection) && containsIndirectionSince == nil {
		containsIndirectionSince = timestamppb.New(rc.clock.Now())
		rootNeedsUpdate = true
	}
	if rootNeedsUpdate {
		if err := rc.storeInitialLookupResult(
			ctx,
			rootTagKeyHash,
			graphletInitialDependencies,
			valueInitialDependencies,
			containsIndirectionSince,
		); err != nil {
			return badReference, err
		}
	}
	return hitGraphletLookupResultReference, nil
}

// intersectCachedKeys computes the intersection of a list of keys that
// is stored in an existing cache entry with a list of dependencies of
// the current evaluation. If the intersection is empty or the existing
// list is malformed, the list of dependencies of the current
// evaluation is returned. The returned boolean indicates whether the
// resulting list differs from the existing one.
func (rc *RecursiveComputer[TReference, TMetadata]) intersectCachedKeys(ctx context.Context, existingKeys model_core.Message[[]*model_evaluation_pb.Keys, TReference], dependencies []*KeyState[TReference, TMetadata]) ([]*KeyState[TReference, TMetadata], bool, error) {
	existingKeyReferences, ok, err := rc.getCachedKeyReferences(ctx, existingKeys)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return dependencies, true, nil
	}
	intersection := filterKeyStatesByReference(dependencies, existingKeyReferences)
	if len(intersection) == 0 {
		return dependencies, true, nil
	}
	return intersection, len(intersection) != len(existingKeyReferences), nil
}

// filterKeyStatesByReference returns the KeyStates whose key
// references are contained in a set, preserving their order.
func filterKeyStatesByReference[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](keyStates []*KeyState[TReference, TMetadata], keyReferences map[object.LocalReference]struct{}) []*KeyState[TReference, TMetadata] {
	var filtered []*KeyState[TReference, TMetadata]
	for _, ks := range keyStates {
		if _, ok := keyReferences[ks.keyReference]; ok {
			filtered = append(filtered, ks)
		}
	}
	return filtered
}

// uploadLookupResultChain stores the chain of subsequent lookup results
// for a single scope (i.e., graphlet or value). Starting with the
// initial set of dependencies, it traverses existing lookup results
// returning missing dependencies for as long as these are consistent
// with the current evaluation. Lookup results are added or reduced
// where needed. At the end of the chain, a lookup result containing
// the hit is stored.
//
// Both initialDependencies and allDependencies must be sorted by key
// reference, and initialDependencies must be a subset of
// allDependencies. The reference of the lookup result containing the
// hit is returned, and whether the chain contains any indirection.
func (rc *RecursiveComputer[TReference, TMetadata]) uploadLookupResultChain(
	ctx context.Context,
	ks *KeyState[TReference, TMetadata],
	scope model_evaluation_cache_pb.LookupTagKeyData_SubsequentLookup_Scope,
	initialDependencies []*KeyState[TReference, TMetadata],
	allDependencies []*KeyState[TReference, TMetadata],
	buildHit lookupResultBuilder[TMetadata],
) (model_core.Decodable[TReference], bool, error) {
	var badReference model_core.Decodable[TReference]
	gatheredDependencies := make(map[*KeyState[TReference, TMetadata]]struct{}, len(allDependencies))
	for _, ksDep := range initialDependencies {
		gatheredDependencies[ksDep] = struct{}{}
	}

	containsIndirection := false
	for {
		// Hash the dependencies gathered up to this point.
		// Obtain them from allDependencies to ensure they are
		// processed in sorted order.
		sortedGatheredDependencies := make([]*KeyState[TReference, TMetadata], 0, len(gatheredDependencies))
		for _, ksDep := range allDependencies {
			if _, ok := gatheredDependencies[ksDep]; ok {
				sortedGatheredDependencies = append(sortedGatheredDependencies, ksDep)
			}
		}
		rc.lock.RLock()
		dependenciesHash := computeDependenciesHash(sortedGatheredDependencies)
		rc.lock.RUnlock()
		tagKeyHash, err := rc.getCacheLookupTagKeyHash(ks, &model_evaluation_cache_pb.LookupTagKeyData_SubsequentLookup{
			Scope:            scope,
			DependenciesHash: dependenciesHash[:],
		})
		if err != nil {
			return badReference, false, err
		}

		if len(gatheredDependencies) == len(allDependencies) {
			// All dependencies have been gathered. Store
			// the hit, overwriting any existing entry.
			lookupResult, err := buildHit()
			if err != nil {
				return badReference, false, err
			}
			lookupResultReference, err := rc.storeLookupResult(ctx, tagKeyHash, lookupResult)
			if err != nil {
				return badReference, false, err
			}
			return model_core.CopyDecodable(tagKeyHash, lookupResultReference), containsIndirection, nil
		}

		remainingDependencies := make([]*KeyState[TReference, TMetadata], 0, len(allDependencies)-len(gatheredDependencies))
		for _, ksDep := range allDependencies {
			if _, ok := gatheredDependencies[ksDep]; !ok {
				remainingDependencies = append(remainingDependencies, ksDep)
			}
		}

		// If an existing lookup result returns missing
		// dependencies that overlap with the remaining
		// dependencies of the current evaluation, reduce it to
		// the overlapping ones. If there is no overlap or the
		// lookup result is of a different kind, overwrite it.
		additionalDependencies, needsUpdate := remainingDependencies, true
		existingLookupResult, _, found, err := rc.readLookupResult(ctx, tagKeyHash)
		if err != nil {
			return badReference, false, err
		}
		if found {
			if missingDependencies, ok := existingLookupResult.Message.Result.(*model_evaluation_cache_pb.LookupResult_MissingDependencies_); ok {
				existingKeyReferences, ok, err := rc.getCachedKeyReferences(
					ctx,
					model_core.Nested(existingLookupResult, missingDependencies.MissingDependencies.AdditionalVariableDependencyKeys),
				)
				if err != nil {
					return badReference, false, err
				}
				if ok {
					if intersection := filterKeyStatesByReference(remainingDependencies, existingKeyReferences); len(intersection) > 0 {
						additionalDependencies = intersection
						needsUpdate = len(intersection) != len(existingKeyReferences)
					}
				}
			}
		}
		if needsUpdate {
			if err := rc.storeMissingDependenciesLookupResult(ctx, tagKeyHash, additionalDependencies); err != nil {
				return badReference, false, err
			}
		}

		containsIndirection = true
		for _, ksDep := range additionalDependencies {
			gatheredDependencies[ksDep] = struct{}{}
		}
	}
}

// newKeysInlineCandidate creates an inlinedtree.Candidate for a list of
// keys, which is either inlined into the parent message or stored in
// a separate object.
func newKeysInlineCandidate[TParentMessage any, TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	ctx context.Context,
	rc *RecursiveComputer[TReference, TMetadata],
	keyStates []*KeyState[TReference, TMetadata],
	setKeys func(parent TParentMessage, keys []*model_evaluation_pb.Keys),
) (inlinedtree.Candidate[TParentMessage, TMetadata], error) {
	keysParentNodeComputer := rc.getKeysParentNodeComputer(ctx)
	keysList, err := rc.getKeysForSortedList(ctx, keyStates)
	if err != nil {
		return inlinedtree.Candidate[TParentMessage, TMetadata]{}, err
	}
	return inlinedtree.Candidate[TParentMessage, TMetadata]{
		ExternalMessage: model_core.ProtoListToBinaryMarshaler(keysList),
		Encoder:         rc.cacheDeterministicEncoder,
		ParentAppender: func(
			parent model_core.PatchedMessage[TParentMessage, TMetadata],
			externalObject *model_core.Decodable[model_core.CreatedObject[TMetadata]],
		) error {
			keys, err := btree.MaybeMergeNodes(
				keysList.Message,
				externalObject,
				parent.Patcher,
				keysParentNodeComputer,
			)
			if err != nil {
				return err
			}
			setKeys(parent.Message, keys)
			return nil
		},
	}, nil
}

// storeInitialLookupResult stores a LookupResult message containing
// the expected sets of dependencies of a key's graphlet and value.
func (rc *RecursiveComputer[TReference, TMetadata]) storeInitialLookupResult(
	ctx context.Context,
	tagKeyHash model_tag.DecodableKeyHash,
	graphletVariableDependencies []*KeyState[TReference, TMetadata],
	valueVariableDependencies []*KeyState[TReference, TMetadata],
	containsIndirectionSince *timestamppb.Timestamp,
) error {
	inlineCandidates := make(inlinedtree.CandidateList[*model_evaluation_cache_pb.LookupResult_Initial, TMetadata], 0, 2)
	defer inlineCandidates.Discard()

	graphletCandidate, err := newKeysInlineCandidate(
		ctx,
		rc,
		graphletVariableDependencies,
		func(initial *model_evaluation_cache_pb.LookupResult_Initial, keys []*model_evaluation_pb.Keys) {
			initial.GraphletVariableDependencyKeys = keys
		},
	)
	if err != nil {
		return err
	}
	inlineCandidates = append(inlineCandidates, graphletCandidate)

	valueCandidate, err := newKeysInlineCandidate(
		ctx,
		rc,
		valueVariableDependencies,
		func(initial *model_evaluation_cache_pb.LookupResult_Initial, keys []*model_evaluation_pb.Keys) {
			initial.ValueVariableDependencyKeys = keys
		},
	)
	if err != nil {
		return err
	}
	inlineCandidates = append(inlineCandidates, valueCandidate)

	lookupResultInitial, err := inlinedtree.Build(
		inlineCandidates,
		&inlinedtree.Options{
			ReferenceFormat:  rc.referenceFormat,
			MaximumSizeBytes: 1 << 16,
		},
	)
	if err != nil {
		return err
	}
	lookupResultInitial.Message.ContainsIndirectionSince = containsIndirectionSince

	_, err = rc.storeLookupResult(
		ctx,
		tagKeyHash,
		model_core.MustBuildPatchedMessage(
			func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_evaluation_cache_pb.LookupResult {
				return &model_evaluation_cache_pb.LookupResult{
					Result: &model_evaluation_cache_pb.LookupResult_Initial_{
						Initial: lookupResultInitial.Merge(patcher),
					},
				}
			},
		),
	)
	return err
}

// storeMissingDependenciesLookupResult stores a LookupResult message
// indicating that a subsequent lookup needs to be performed, taking
// the values of additional dependencies into account.
func (rc *RecursiveComputer[TReference, TMetadata]) storeMissingDependenciesLookupResult(
	ctx context.Context,
	tagKeyHash model_tag.DecodableKeyHash,
	additionalVariableDependencies []*KeyState[TReference, TMetadata],
) error {
	keysCandidate, err := newKeysInlineCandidate(
		ctx,
		rc,
		additionalVariableDependencies,
		func(missingDependencies *model_evaluation_cache_pb.LookupResult_MissingDependencies, keys []*model_evaluation_pb.Keys) {
			missingDependencies.AdditionalVariableDependencyKeys = keys
		},
	)
	if err != nil {
		return err
	}
	lookupResultMissingDependencies, err := inlinedtree.Build(
		inlinedtree.CandidateList[*model_evaluation_cache_pb.LookupResult_MissingDependencies, TMetadata]{keysCandidate},
		&inlinedtree.Options{
			ReferenceFormat:  rc.referenceFormat,
			MaximumSizeBytes: 1 << 16,
		},
	)
	if err != nil {
		return err
	}

	_, err = rc.storeLookupResult(
		ctx,
		tagKeyHash,
		model_core.MustBuildPatchedMessage(
			func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_evaluation_cache_pb.LookupResult {
				return &model_evaluation_cache_pb.LookupResult{
					Result: &model_evaluation_cache_pb.LookupResult_MissingDependencies_{
						MissingDependencies: lookupResultMissingDependencies.Merge(patcher),
					},
				}
			},
		),
	)
	return err
}

func (vs *variableDependenciesMarshaledMessageValueState[TReference, TMetadata]) getGraphlet(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) (valueState[TReference, TMetadata], model_core.Message[*model_evaluation_pb.Graphlet, TReference], error) {
	return vs, vs.graphlet, nil
}

func (vs *variableDependenciesMarshaledMessageValueState[TReference, TMetadata]) getMessageValue(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata]) (model_core.TopLevelMessage[*anypb.Any, TReference], error) {
	evaluation, err := GraphletGetEvaluation(ctx, rc.evaluationReader, vs.graphlet)
	if err != nil {
		return model_core.TopLevelMessage[*anypb.Any, TReference]{}, err
	}
	return model_core.FlattenAny(model_core.Nested(evaluation, evaluation.Message.Value))
}

func (variableDependenciesMarshaledMessageValueState[TReference, TMetadata]) getNativeValue() (any, error) {
	return nil, errors.New("key does not yield a native value")
}

func (variableDependenciesMarshaledMessageValueState[TReference, TMetadata]) getError() error {
	return nil
}

type noDependenciesUploadedMessageValueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	noDependenciesValueState[TReference, TMetadata]

	hitValueLookupResultReference model_core.Decodable[TReference]
}

func (vs *noDependenciesUploadedMessageValueState[TReference, TMetadata]) upload(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) (valueState[TReference, TMetadata], error) {
	return vs, nil
}

func (vs *noDependenciesUploadedMessageValueState[TReference, TMetadata]) getMessageValue(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata]) (model_core.TopLevelMessage[*anypb.Any, TReference], error) {
	rootLookupResult, err := rc.lookupResultReader.ReadObject(ctx, vs.hitValueLookupResultReference)
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

type variableDependenciesUploadedMessageValueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	evaluatedValueState[TReference, TMetadata]
	variableDependencyValueState[TReference, TMetadata]

	hitGraphletLookupResultReference model_core.Decodable[TReference]
	hoistedVariableDependencies      []*KeyState[TReference, TMetadata]
}

func (vs *variableDependenciesUploadedMessageValueState[TReference, TMetadata]) upload(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) (valueState[TReference, TMetadata], error) {
	return vs, nil
}

func (vs *variableDependenciesUploadedMessageValueState[TReference, TMetadata]) getMessageValue(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata]) (model_core.TopLevelMessage[*anypb.Any, TReference], error) {
	hitGraphletLookupResult, err := rc.lookupResultReader.ReadObject(ctx, vs.hitGraphletLookupResultReference)
	if err != nil {
		return model_core.TopLevelMessage[*anypb.Any, TReference]{}, err
	}
	evaluation, err := GraphletGetEvaluation(
		ctx,
		rc.evaluationReader,
		model_core.Nested(
			hitGraphletLookupResult,
			hitGraphletLookupResult.Message.Result.(*model_evaluation_cache_pb.LookupResult_HitGraphlet).HitGraphlet,
		),
	)
	if err != nil {
		return model_core.TopLevelMessage[*anypb.Any, TReference]{}, err
	}
	return model_core.FlattenAny(model_core.Nested(evaluation, evaluation.Message.Value))
}

func (variableDependenciesUploadedMessageValueState[TReference, TMetadata]) getNativeValue() (any, error) {
	return nil, errors.New("key does not yield a native value")
}

func (variableDependenciesUploadedMessageValueState[TReference, TMetadata]) getError() error {
	return nil
}

func (vs *variableDependenciesUploadedMessageValueState[TReference, TMetadata]) getGraphlet(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) (valueState[TReference, TMetadata], model_core.Message[*model_evaluation_pb.Graphlet, TReference], error) {
	hitGraphletLookupResult, err := rc.lookupResultReader.ReadObject(ctx, vs.hitGraphletLookupResultReference)
	if err != nil {
		return vs, model_core.Message[*model_evaluation_pb.Graphlet, TReference]{}, err
	}
	return vs, model_core.Nested(
		hitGraphletLookupResult,
		hitGraphletLookupResult.Message.Result.(*model_evaluation_cache_pb.LookupResult_HitGraphlet).HitGraphlet,
	), nil
}

func (vs *variableDependenciesUploadedMessageValueState[TReference, TMetadata]) getHoistedDependencies() []*KeyState[TReference, TMetadata] {
	return vs.hoistedVariableDependencies
}

type overriddenMessageValueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	evaluatedValueState[TReference, TMetadata]
	variableDependencyValueState[TReference, TMetadata]

	value model_core.TopLevelMessage[*anypb.Any, TReference]
}

func (overriddenMessageValueState[TReference, TMetadata]) upload(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) (valueState[TReference, TMetadata], error) {
	panic("key already uploaded")
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

func (vs *overriddenMessageValueState[TReference, TMetadata]) getGraphlet(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) (valueState[TReference, TMetadata], model_core.Message[*model_evaluation_pb.Graphlet, TReference], error) {
	// TODO: Should this also use inlinedtree?
	wrappedValue := model_core.WrapTopLevelAny(vs.value).Decay()
	return vs, model_core.Nested(
		wrappedValue,
		&model_evaluation_pb.Graphlet{
			Evaluation: &model_evaluation_pb.Graphlet_EvaluationInline{
				EvaluationInline: &model_evaluation_pb.Evaluation{
					Value: wrappedValue.Message,
				},
			},
		},
	), nil
}

func (overriddenMessageValueState[TReference, TMetadata]) getHoistedDependencies() []*KeyState[TReference, TMetadata] {
	return nil
}

type computingNativeValueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	computingValueState[TReference, TMetadata]
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
					err:                        err,
					directVariableDependencies: sortedKeyStates(e.directVariableDependencies),
				}, nil
			}
			vs.previousDirectVariableDependencies = sortedKeyStates(e.directVariableDependencies)
			return vs, missingDependencies
		}
		return &evaluationFailedValueState[TReference, TMetadata]{
			err:                        err,
			directVariableDependencies: sortedKeyStates(e.directVariableDependencies),
		}, nil
	}

	if len(e.directVariableDependencies) == 0 {
		return &noDependenciesComputedNativeValueState[TReference, TMetadata]{
			computedNativeValueState: computedNativeValueState[TReference, TMetadata]{
				value: value,
			},
		}, nil
	}

	rc.lock.RLock()
	dependenciesHash := computeDependenciesHash(sortedKeyStates(e.directVariableDependencies))
	rc.lock.RUnlock()
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
		variableDependenciesComputedValueState: e.getVariableDependenciesComputedValueState(
			ks,
			dependenciesHashRecordReference,
		),
		computedNativeValueState: computedNativeValueState[TReference, TMetadata]{
			value: value,
		},
	}, nil
}

func (vs *computingNativeValueState[TReference, TMetadata]) getGraphlet(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) (valueState[TReference, TMetadata], model_core.Message[*model_evaluation_pb.Graphlet, TReference], error) {
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
	variableDependenciesComputedValueState[TReference, TMetadata]
	computedNativeValueState[TReference, TMetadata]
}

func (vs *variableDependenciesComputedNativeValueState[TReference, TMetadata]) upload(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) (valueState[TReference, TMetadata], error) {
	// There is no way native values can be cached.
	return vs, nil
}

func (vs *variableDependenciesComputedNativeValueState[TReference, TMetadata]) getGraphlet(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) (valueState[TReference, TMetadata], model_core.Message[*model_evaluation_pb.Graphlet, TReference], error) {
	variableDependenciesMarshaledValueState, graphlet, err := vs.buildGraphlet(
		ctx,
		rc,
		ks,
		model_core.Message[*model_core_pb.Any, TReference]{},
	)
	if err != nil {
		return vs, model_core.Message[*model_evaluation_pb.Graphlet, TReference]{}, err
	}
	return &variableDependenciesMarshaledNativeValueState[TReference, TMetadata]{
		variableDependenciesMarshaledValueState: variableDependenciesMarshaledValueState,
		computedNativeValueState:                vs.computedNativeValueState,
	}, graphlet, nil
}

type variableDependenciesMarshaledNativeValueState[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	variableDependenciesMarshaledValueState[TReference, TMetadata]
	computedNativeValueState[TReference, TMetadata]
}

func (vs *variableDependenciesMarshaledNativeValueState[TReference, TMetadata]) upload(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) (valueState[TReference, TMetadata], error) {
	// There is no way native values can be cached.
	return vs, nil
}

func (vs *variableDependenciesMarshaledNativeValueState[TReference, TMetadata]) getGraphlet(ctx context.Context, rc *RecursiveComputer[TReference, TMetadata], ks *KeyState[TReference, TMetadata]) (valueState[TReference, TMetadata], model_core.Message[*model_evaluation_pb.Graphlet, TReference], error) {
	return vs, vs.graphlet, nil
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
	// ProtoEvaluationReaderForTesting is used to generate mocks that
	// are used by RecursiveComputer's unit tests.
	ProtoEvaluationReaderForTesting = model_parser.MessageObjectReader[object.LocalReference, *model_evaluation_pb.Evaluation]
)
