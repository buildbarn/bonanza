package initialsizeclass

import (
	"context"
	"time"

	model_core "bonanza.build/pkg/model/core"
	model_tag "bonanza.build/pkg/model/tag"
	encryptedaction_pb "bonanza.build/pkg/proto/encryptedaction"
	model_initialsizeclass_pb "bonanza.build/pkg/proto/model/initialsizeclass"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/clock"
	"github.com/buildbarn/bb-storage/pkg/random"
	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// PreviousExecutionStatsStore is used by FeedbackDrivenAnalyzer
// to gain access to previous execution stats in storage.
type PreviousExecutionStatsStore model_tag.MutableProtoStore[*model_initialsizeclass_pb.PreviousExecutionStats]

// PreviousExecutionStatsHandle refers to a single previous execution
// stats message read from storage.
type PreviousExecutionStatsHandle model_tag.MutableProtoHandle[*model_initialsizeclass_pb.PreviousExecutionStats]

type feedbackDrivenAnalyzer struct {
	fallback                    Analyzer
	store                       PreviousExecutionStatsStore
	commonKeyDataReference      object.LocalReference
	decodingParametersSizeBytes int
	randomNumberGenerator       random.SingleThreadedGenerator
	clock                       clock.Clock
	actionTimeoutExtractor      *ActionTimeoutExtractor
	failureCacheDuration        time.Duration
	strategyCalculator          StrategyCalculator
	historySize                 int
}

// NewFeedbackDrivenAnalyzer creates an Analyzer that selects the
// initial size class on which actions are run by reading previous
// execution stats from storage and analyzing these results. Upon
// completion, stats in storage are updated.
func NewFeedbackDrivenAnalyzer(fallback Analyzer, store PreviousExecutionStatsStore, commonKeyDataReference object.LocalReference, decodingParametersSizeBytes int, randomNumberGenerator random.SingleThreadedGenerator, clock clock.Clock, actionTimeoutExtractor *ActionTimeoutExtractor, failureCacheDuration time.Duration, strategyCalculator StrategyCalculator, historySize int) Analyzer {
	return &feedbackDrivenAnalyzer{
		fallback:                    fallback,
		store:                       store,
		commonKeyDataReference:      commonKeyDataReference,
		decodingParametersSizeBytes: decodingParametersSizeBytes,
		randomNumberGenerator:       randomNumberGenerator,
		clock:                       clock,
		actionTimeoutExtractor:      actionTimeoutExtractor,
		failureCacheDuration:        failureCacheDuration,
		strategyCalculator:          strategyCalculator,
		historySize:                 historySize,
	}
}

func (a *feedbackDrivenAnalyzer) Analyze(ctx context.Context, action *encryptedaction_pb.Action) (Selector, error) {
	// Actions that don't have a stable fingerprint cannot be
	// processed by this analyzer. Call into the fallback analyzer
	// when such actions are observed.
	stableFingerprint := action.AdditionalData.GetStableFingerprint()
	if len(stableFingerprint) == 0 {
		return a.fallback.Analyze(ctx, action)
	}

	timeout, err := a.actionTimeoutExtractor.ExtractTimeout(action)
	if err != nil {
		return nil, err
	}

	tagKeyData, _ := model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.NoopReferenceMetadata]) *model_initialsizeclass_pb.TagKeyData {
		return &model_initialsizeclass_pb.TagKeyData{
			CommonKeyDataReference: patcher.AddReference(model_core.MetadataEntry[model_core.NoopReferenceMetadata]{
				LocalReference: a.commonKeyDataReference,
			}),
			PlatformPkixPublicKey: action.PlatformPkixPublicKey,
			StableFingerprint:     stableFingerprint,
		}
	}).SortAndSetReferences()
	tagKeyHash, err := model_tag.NewDecodableKeyHashFromMessage(tagKeyData, a.decodingParametersSizeBytes)
	if err != nil {
		return nil, util.StatusWrapWithCode(err, codes.Internal, "Failed to compute tag key hash")
	}

	handle, err := a.store.Get(ctx, tagKeyHash)
	if err != nil {
		return nil, util.StatusWrapf(err, "Failed to read previous execution stats for tag with key hash %s", model_tag.DecodableKeyHashToString(tagKeyHash))
	}
	return &feedbackDrivenSelector{
		analyzer:        a,
		handle:          handle,
		originalTimeout: timeout,
	}, nil
}

type feedbackDrivenSelector struct {
	analyzer        *feedbackDrivenAnalyzer
	handle          PreviousExecutionStatsHandle
	originalTimeout time.Duration
}

func getExpectedExecutionDuration(perSizeClassStatsMap map[uint32]*model_initialsizeclass_pb.PerSizeClassStats, sizeClass uint32, timeout time.Duration) time.Duration {
	if perSizeClassStats, ok := perSizeClassStatsMap[sizeClass]; ok {
		if medianExecutionTime := getOutcomesFromPreviousExecutions(perSizeClassStats.PreviousExecutions).GetMedianExecutionTime(); medianExecutionTime != nil && *medianExecutionTime < timeout {
			return *medianExecutionTime
		}
	}
	return timeout
}

func (s *feedbackDrivenSelector) Select(sizeClasses []uint32) (int, time.Duration, time.Duration, Learner) {
	a := s.analyzer
	stats := s.handle.GetMutableProto()
	if stats.SizeClasses == nil {
		stats.SizeClasses = map[uint32]*model_initialsizeclass_pb.PerSizeClassStats{}
	}
	perSizeClassStatsMap := stats.SizeClasses
	largestSizeClass := sizeClasses[len(sizeClasses)-1]
	if lastSeenFailure := stats.LastSeenFailure; lastSeenFailure.CheckValid() != nil || lastSeenFailure.AsTime().Before(a.clock.Now().Add(-a.failureCacheDuration)) {
		strategies := a.strategyCalculator.GetStrategies(perSizeClassStatsMap, sizeClasses, s.originalTimeout)

		// Randomly pick a size class according to the probabilities
		// that we computed above.
		r := a.randomNumberGenerator.Float64()
		for i, strategy := range strategies {
			if r < strategy.Probability {
				smallerSizeClass := sizeClasses[i]
				if strategy.RunInBackground {
					// The action is prone to failures. Run
					// it on the largest size class first.
					// Upon success, still run it on the
					// smaller size class for training
					// purposes.
					return len(sizeClasses) - 1,
						getExpectedExecutionDuration(perSizeClassStatsMap, largestSizeClass, s.originalTimeout),
						s.originalTimeout,
						&largestBackgroundLearner{
							cleanLearner: cleanLearner{
								baseLearner: baseLearner{
									analyzer: s.analyzer,
									handle:   s.handle,
								},
							},
							largestSizeClass: largestSizeClass,
							largestTimeout:   s.originalTimeout,
							smallerSizeClass: smallerSizeClass,
						}
				}
				// The action doesn't seem prone to
				// failures. Just run it on the smaller
				// size class, only falling back to the
				// largest size class upon failure.
				smallerTimeout := strategy.ForegroundExecutionTimeout
				return i,
					getExpectedExecutionDuration(perSizeClassStatsMap, smallerSizeClass, smallerTimeout),
					smallerTimeout,
					&smallerForegroundLearner{
						cleanLearner: cleanLearner{
							baseLearner: baseLearner{
								analyzer: s.analyzer,
								handle:   s.handle,
							},
						},
						smallerSizeClass: smallerSizeClass,
						smallerTimeout:   smallerTimeout,
						largestSizeClass: largestSizeClass,
						largestTimeout:   s.originalTimeout,
					}
			}
			r -= strategy.Probability
		}
	}

	// Random selection ended up choosing the largest size class. We
	// can use the original timeout value. There is never any need
	// to retry.
	return len(sizeClasses) - 1,
		getExpectedExecutionDuration(perSizeClassStatsMap, largestSizeClass, s.originalTimeout),
		s.originalTimeout,
		&largestLearner{
			cleanLearner: cleanLearner{
				baseLearner: baseLearner{
					analyzer: s.analyzer,
					handle:   s.handle,
				},
			},
			largestSizeClass: largestSizeClass,
		}
}

func (s *feedbackDrivenSelector) Abandoned() {
	s.handle.Release(false)
	s.handle = nil
}

// baseLearner is the base type for all Learner objects returned by
// FeedbackDrivenAnalyzer.
type baseLearner struct {
	analyzer *feedbackDrivenAnalyzer
	handle   PreviousExecutionStatsHandle
}

func (l *baseLearner) addPreviousExecution(sizeClass uint32, previousExecution *model_initialsizeclass_pb.PreviousExecution) {
	perSizeClassStatsMap := l.handle.GetMutableProto().SizeClasses
	perSizeClassStats, ok := perSizeClassStatsMap[sizeClass]
	if !ok {
		// Size class does not exist yet. Create it.
		perSizeClassStats = &model_initialsizeclass_pb.PerSizeClassStats{}
		perSizeClassStatsMap[sizeClass] = perSizeClassStats
	}

	// Append new outcome, potentially removing the oldest one present.
	perSizeClassStats.PreviousExecutions = append(perSizeClassStats.PreviousExecutions, previousExecution)
	if l, historySize := len(perSizeClassStats.PreviousExecutions), l.analyzer.historySize; l > historySize {
		perSizeClassStats.PreviousExecutions = perSizeClassStats.PreviousExecutions[l-historySize:]
	}
}

func (l *baseLearner) updateLastSeenFailure() {
	stats := l.handle.GetMutableProto()
	stats.LastSeenFailure = timestamppb.New(l.analyzer.clock.Now())
}

// cleanLearner is a common type for all Learner objects returned by
// FeedbackDrivenAnalyzer that haven't made any modifications to the
// underlying PreviousExecutionStatsHandle yet. Abandoning learners of
// this type will not cause any writes to storage.
type cleanLearner struct {
	baseLearner
}

func (l *cleanLearner) Abandoned() {
	l.handle.Release(false)
	l.handle = nil
}

// smallerForegroundLearner is the initial Learner that is returned by
// FeedbackDrivenAnalyzer when executing an action on a smaller size
// class under the assumption execution is going to succeed.
type smallerForegroundLearner struct {
	cleanLearner
	smallerSizeClass uint32
	smallerTimeout   time.Duration
	largestSizeClass uint32
	largestTimeout   time.Duration
}

func (l *smallerForegroundLearner) Succeeded(duration time.Duration, sizeClasses []uint32) (int, time.Duration, time.Duration, Learner) {
	l.addPreviousExecution(l.smallerSizeClass, &model_initialsizeclass_pb.PreviousExecution{
		Outcome: &model_initialsizeclass_pb.PreviousExecution_Succeeded{
			Succeeded: durationpb.New(duration),
		},
	})
	l.handle.Release(true)
	l.handle = nil
	return 0, 0, 0, nil
}

func (l *smallerForegroundLearner) Failed(timedOut bool) (time.Duration, time.Duration, Learner) {
	// Retry execution on the largest size class. Store the outcome
	// of this invocation, so that we can write it to storage in
	// case the action does succeed on the largest size class.
	newL := &largestForegroundLearner{
		cleanLearner: cleanLearner{
			baseLearner: baseLearner{
				analyzer: l.analyzer,
				handle:   l.handle,
			},
		},
		smallerSizeClass: l.smallerSizeClass,
		largestSizeClass: l.largestSizeClass,
	}
	if timedOut {
		newL.smallerExecution.Outcome = &model_initialsizeclass_pb.PreviousExecution_TimedOut{
			TimedOut: durationpb.New(l.smallerTimeout),
		}
	} else {
		newL.smallerExecution.Outcome = &model_initialsizeclass_pb.PreviousExecution_Failed{
			Failed: &emptypb.Empty{},
		}
	}
	perSizeClassStatsMap := l.handle.GetMutableProto().SizeClasses
	return getExpectedExecutionDuration(perSizeClassStatsMap, l.largestSizeClass, l.largestTimeout), l.largestTimeout, newL
}

// largestForegroundLearner is the final Learner that is returned by
// FeedbackDrivenAnalyzer when initially executing an action on a
// smaller size class under the assumption execution is going to
// succeed (which didn't end up being the case).
type largestForegroundLearner struct {
	cleanLearner
	smallerSizeClass uint32
	smallerExecution model_initialsizeclass_pb.PreviousExecution
	largestSizeClass uint32
}

func (l *largestForegroundLearner) Succeeded(duration time.Duration, sizeClasses []uint32) (int, time.Duration, time.Duration, Learner) {
	l.addPreviousExecution(l.smallerSizeClass, &l.smallerExecution)
	l.addPreviousExecution(l.largestSizeClass, &model_initialsizeclass_pb.PreviousExecution{
		Outcome: &model_initialsizeclass_pb.PreviousExecution_Succeeded{
			Succeeded: durationpb.New(duration),
		},
	})
	l.handle.Release(true)
	l.handle = nil
	return 0, 0, 0, nil
}

func (l *largestForegroundLearner) Failed(timedOut bool) (time.Duration, time.Duration, Learner) {
	l.updateLastSeenFailure()
	l.handle.Release(true)
	l.handle = nil
	return 0, 0, nil
}

// largestBackgroundLearner is the initial Learner that is returned by
// FeedbackDrivenAnalyzer when executing an action on a smaller size
// class under the assumption that doing this is going to fail anyway.
// Before executing the action on the smaller size class, we run it on
// the largest size class. That way the user isn't blocked.
type largestBackgroundLearner struct {
	cleanLearner
	largestSizeClass uint32
	largestTimeout   time.Duration
	smallerSizeClass uint32
}

func (l *largestBackgroundLearner) Succeeded(duration time.Duration, sizeClasses []uint32) (int, time.Duration, time.Duration, Learner) {
	l.addPreviousExecution(l.largestSizeClass, &model_initialsizeclass_pb.PreviousExecution{
		Outcome: &model_initialsizeclass_pb.PreviousExecution_Succeeded{
			Succeeded: durationpb.New(duration),
		},
	})
	for i, sizeClass := range sizeClasses {
		if sizeClass == l.smallerSizeClass {
			// The smaller size class on which we originally
			// wanted to run the action still exists.
			// Request that it's run on that size class once
			// again, for training purposes.
			perSizeClassStatsMap := l.handle.GetMutableProto().SizeClasses
			smallerTimeout := l.analyzer.strategyCalculator.GetBackgroundExecutionTimeout(
				perSizeClassStatsMap,
				sizeClasses,
				i,
				l.largestTimeout)
			return i,
				getExpectedExecutionDuration(perSizeClassStatsMap, l.smallerSizeClass, smallerTimeout),
				smallerTimeout,
				&smallerBackgroundLearner{
					baseLearner: baseLearner{
						analyzer: l.analyzer,
						handle:   l.handle,
					},
					smallerSizeClass: l.smallerSizeClass,
					smallerTimeout:   smallerTimeout,
				}
		}
	}
	// Corner case: the smaller size class disappeared before we got
	// a chance to schedule the action on it. Let's not do any
	// background learning.
	l.handle.Release(true)
	l.handle = nil
	return 0, 0, 0, nil
}

func (l *largestBackgroundLearner) Failed(timedOut bool) (time.Duration, time.Duration, Learner) {
	l.updateLastSeenFailure()
	l.handle.Release(true)
	l.handle = nil
	return 0, 0, nil
}

// smallerBackgroundLearner is the final Learner that is returned by
// FeedbackDrivenAnalyzer when executing an action on a smaller size
// class under the assumption that doing this is going to fail anyway.
// The action has already run on the largest size class and succeeded.
// We can now run it on the smaller size class for training purposes.
type smallerBackgroundLearner struct {
	baseLearner
	smallerSizeClass uint32
	smallerTimeout   time.Duration
}

func (l *smallerBackgroundLearner) Abandoned() {
	// Still make sure the results of the execution on the largest
	// size class end up getting written.
	l.handle.Release(true)
	l.handle = nil
}

func (l *smallerBackgroundLearner) Failed(timedOut bool) (time.Duration, time.Duration, Learner) {
	if timedOut {
		l.addPreviousExecution(l.smallerSizeClass, &model_initialsizeclass_pb.PreviousExecution{
			Outcome: &model_initialsizeclass_pb.PreviousExecution_TimedOut{
				TimedOut: durationpb.New(l.smallerTimeout),
			},
		})
	} else {
		l.addPreviousExecution(l.smallerSizeClass, &model_initialsizeclass_pb.PreviousExecution{
			Outcome: &model_initialsizeclass_pb.PreviousExecution_Failed{
				Failed: &emptypb.Empty{},
			},
		})
	}
	l.handle.Release(true)
	l.handle = nil
	return 0, 0, nil
}

func (l *smallerBackgroundLearner) Succeeded(duration time.Duration, sizeClasses []uint32) (int, time.Duration, time.Duration, Learner) {
	l.addPreviousExecution(l.smallerSizeClass, &model_initialsizeclass_pb.PreviousExecution{
		Outcome: &model_initialsizeclass_pb.PreviousExecution_Succeeded{
			Succeeded: durationpb.New(duration),
		},
	})
	l.handle.Release(true)
	l.handle = nil
	return 0, 0, 0, nil
}

// largestLearner is returned by FeedbackDrivenAnalyzer when executing
// an action on the largest size class immediately. there is no need to
// do any fallback to different size classes. It's also not necessary to
// register failures, as those samples don't contribute to the analysis
// in any way.
type largestLearner struct {
	cleanLearner
	largestSizeClass uint32
}

func (l *largestLearner) Succeeded(duration time.Duration, sizeClasses []uint32) (int, time.Duration, time.Duration, Learner) {
	l.addPreviousExecution(l.largestSizeClass, &model_initialsizeclass_pb.PreviousExecution{
		Outcome: &model_initialsizeclass_pb.PreviousExecution_Succeeded{
			Succeeded: durationpb.New(duration),
		},
	})
	l.handle.Release(true)
	l.handle = nil
	return 0, 0, 0, nil
}

func (l *largestLearner) Failed(timedOut bool) (time.Duration, time.Duration, Learner) {
	l.updateLastSeenFailure()
	l.handle.Release(true)
	l.handle = nil
	return 0, 0, nil
}
