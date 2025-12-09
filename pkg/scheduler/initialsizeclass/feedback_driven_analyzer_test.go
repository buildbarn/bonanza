package initialsizeclass_test

import (
	"context"
	"testing"
	"time"

	model_core "bonanza.build/pkg/model/core"
	encryptedaction_pb "bonanza.build/pkg/proto/encryptedaction"
	model_initialsizeclass_pb "bonanza.build/pkg/proto/model/initialsizeclass"
	"bonanza.build/pkg/scheduler/initialsizeclass"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"go.uber.org/mock/gomock"
)

func TestFeedbackDrivenAnalyzer(t *testing.T) {
	ctrl, ctx := gomock.WithContext(context.Background(), t)

	baseAnalyzer := NewMockAnalyzer(ctrl)
	store := NewMockPreviousExecutionStatsStore(ctrl)
	randomNumberGenerator := NewMockSingleThreadedGenerator(ctrl)
	clock := NewMockClock(ctrl)
	actionTimeoutExtractor := initialsizeclass.NewActionTimeoutExtractor(60 * time.Minute)
	strategyCalculator := NewMockStrategyCalculator(ctrl)
	analyzer := initialsizeclass.NewFeedbackDrivenAnalyzer(
		baseAnalyzer,
		store,
		/* commonKeyDataReference = */ object.MustNewSHA256V1LocalReference("16444aacdf49a3800efa7518f12968656fad66b8d68c558e54e942786bdc74ab", 2938, 0, 0, 0),
		/* decodingParametersSizeBytes = */ 12,
		randomNumberGenerator,
		clock,
		actionTimeoutExtractor,
		/* failureCacheDuration = */ 24*time.Hour,
		strategyCalculator,
		/* historySize = */ 5,
	)

	exampleAction := &encryptedaction_pb.Action{
		PlatformPkixPublicKey: []byte{
			0x30, 0x2a, 0x30, 0x05, 0x06, 0x03, 0x2b, 0x65,
			0x6e, 0x03, 0x21, 0x00, 0x34, 0x83, 0xce, 0xa0,
			0x9f, 0x40, 0xe2, 0xe0, 0x28, 0x4a, 0x9b, 0x63,
			0xcc, 0xc8, 0x57, 0x69, 0xf9, 0xc1, 0x6d, 0x6f,
			0x6a, 0x7e, 0x1c, 0x75, 0xc0, 0x59, 0x86, 0x6f,
			0x29, 0xa8, 0x10, 0x0c,
		},
		AdditionalData: &encryptedaction_pb.Action_AdditionalData{
			StableFingerprint: []byte{
				0x46, 0xb8, 0x81, 0xe9, 0x98, 0x4a, 0x99, 0xc8,
				0xc1, 0xca, 0x3e, 0xcc, 0xac, 0x4b, 0xef, 0xfe,
				0x02, 0xf4, 0xa9, 0x1c, 0x02, 0x79, 0x46, 0x40,
				0x8f, 0x6a, 0x3c, 0x49, 0x38, 0x51, 0xe5, 0x98,
			},
			ExecutionTimeout: &durationpb.Duration{Seconds: 30 * 60},
		},
	}
	exampleTagKeyHash, err := model_core.NewDecodable(
		/* tagKeyHash = */ [...]byte{
			0x17, 0x1a, 0x21, 0xb7, 0xfc, 0xa9, 0x93, 0x83,
			0x31, 0xe0, 0xab, 0x2c, 0x79, 0x65, 0xc3, 0xc1,
			0x0d, 0x95, 0x4d, 0x52, 0x59, 0xab, 0xcb, 0x39,
			0x49, 0x6c, 0xe9, 0x79, 0x4b, 0x8d, 0x56, 0xc4,
		},
		/* decodingParameters = */ []byte{
			0xde, 0x73, 0x4a, 0xfa, 0x95, 0x95, 0x58, 0x47,
			0xde, 0x6f, 0xb8, 0x80,
		},
	)
	require.NoError(t, err)

	t.Run("StorageFailure", func(t *testing.T) {
		// Failures reading existing entries from storage should
		// be propagated.
		store.EXPECT().Get(ctx, exampleTagKeyHash).
			Return(nil, status.Error(codes.Internal, "Network error"))

		_, err := analyzer.Analyze(ctx, exampleAction)
		testutil.RequireEqualStatus(t, status.Error(codes.Internal, "Failed to read previous execution stats for tag with key hash Fxoht_ypk4Mx4KsseWXDwQ2VTVJZq8s5SWzpeUuNVsQ.3nNK-pWVWEfeb7iA: Network error"), err)
	})

	t.Run("InitialAbandoned", func(t *testing.T) {
		handle := NewMockPreviousExecutionStatsHandle(ctrl)
		store.EXPECT().Get(ctx, exampleTagKeyHash).Return(handle, nil)

		selector, err := analyzer.Analyze(ctx, exampleAction)
		require.NoError(t, err)

		// Return an empty stats message. The strategy
		// calculator will most likely just return a uniform
		// distribution. Let's pick the smallest size class.
		var stats model_initialsizeclass_pb.PreviousExecutionStats
		handle.EXPECT().GetMutableProto().Return(&stats).AnyTimes()
		strategyCalculator.EXPECT().GetStrategies(gomock.Not(gomock.Nil()), []uint32{1, 2, 4, 8}, 30*time.Minute).
			Return([]initialsizeclass.Strategy{
				{
					Probability:                0.25,
					ForegroundExecutionTimeout: 15 * time.Second,
				},
				{
					Probability:                0.25,
					ForegroundExecutionTimeout: 15 * time.Second,
				},
				{
					Probability:                0.25,
					ForegroundExecutionTimeout: 15 * time.Second,
				},
			})
		randomNumberGenerator.EXPECT().Float64().Return(0.1)

		sizeClassIndex, expectedDuration, timeout, learner := selector.Select([]uint32{1, 2, 4, 8})
		require.Equal(t, 0, sizeClassIndex)
		require.Equal(t, 15*time.Second, expectedDuration)
		require.Equal(t, 15*time.Second, timeout)

		// Action didn't get run after all.
		handle.EXPECT().Release(false)

		learner.Abandoned()
		testutil.RequireEqualProto(t, &model_initialsizeclass_pb.PreviousExecutionStats{
			SizeClasses: map[uint32]*model_initialsizeclass_pb.PerSizeClassStats{},
		}, &stats)
	})

	t.Run("InitialSuccess", func(t *testing.T) {
		handle := NewMockPreviousExecutionStatsHandle(ctrl)
		store.EXPECT().Get(ctx, exampleTagKeyHash).Return(handle, nil)

		selector, err := analyzer.Analyze(ctx, exampleAction)
		require.NoError(t, err)

		// Same as before: empty stats message. Now pick the
		// second smallest size class.
		var stats model_initialsizeclass_pb.PreviousExecutionStats
		handle.EXPECT().GetMutableProto().Return(&stats).AnyTimes()
		strategyCalculator.EXPECT().GetStrategies(gomock.Not(gomock.Nil()), []uint32{1, 2, 4, 8}, 30*time.Minute).
			Return([]initialsizeclass.Strategy{
				{
					Probability:                0.25,
					ForegroundExecutionTimeout: 15 * time.Second,
				},
				{
					Probability:                0.25,
					ForegroundExecutionTimeout: 15 * time.Second,
				},
				{
					Probability:                0.25,
					ForegroundExecutionTimeout: 15 * time.Second,
				},
			})
		randomNumberGenerator.EXPECT().Float64().Return(0.4)

		sizeClassIndex, expectedDuration, timeout, learner1 := selector.Select([]uint32{1, 2, 4, 8})
		require.Equal(t, 1, sizeClassIndex)
		require.Equal(t, 15*time.Second, expectedDuration)
		require.Equal(t, 15*time.Second, timeout)

		// Report that execution succeeded. This should cause
		// the execution time to be recorded.
		handle.EXPECT().Release(true)

		_, _, _, learner2 := learner1.Succeeded(time.Minute, []uint32{1, 2, 4, 8})
		require.Nil(t, learner2)
		testutil.RequireEqualProto(t, &model_initialsizeclass_pb.PreviousExecutionStats{
			SizeClasses: map[uint32]*model_initialsizeclass_pb.PerSizeClassStats{
				2: {
					PreviousExecutions: []*model_initialsizeclass_pb.PreviousExecution{
						{Outcome: &model_initialsizeclass_pb.PreviousExecution_Succeeded{Succeeded: &durationpb.Duration{Seconds: 60}}},
					},
				},
			},
		}, &stats)
	})

	t.Run("SuccessAfterFailure", func(t *testing.T) {
		handle := NewMockPreviousExecutionStatsHandle(ctrl)
		store.EXPECT().Get(ctx, exampleTagKeyHash).Return(handle, nil)

		selector, err := analyzer.Analyze(ctx, exampleAction)
		require.NoError(t, err)

		// Let the action run on size class 1.
		stats := model_initialsizeclass_pb.PreviousExecutionStats{
			SizeClasses: map[uint32]*model_initialsizeclass_pb.PerSizeClassStats{
				8: {
					PreviousExecutions: []*model_initialsizeclass_pb.PreviousExecution{
						{Outcome: &model_initialsizeclass_pb.PreviousExecution_Succeeded{Succeeded: &durationpb.Duration{Seconds: 10}}},
					},
				},
			},
		}
		handle.EXPECT().GetMutableProto().Return(&stats).AnyTimes()
		strategyCalculator.EXPECT().GetStrategies(gomock.Not(gomock.Nil()), []uint32{1, 2, 4, 8}, 30*time.Minute).
			Return([]initialsizeclass.Strategy{
				{
					Probability:                0.6,
					ForegroundExecutionTimeout: 40 * time.Second,
				},
				{
					Probability:                0.2,
					ForegroundExecutionTimeout: 30 * time.Second,
				},
				{
					Probability:                0.1,
					ForegroundExecutionTimeout: 20 * time.Second,
				},
			})
		randomNumberGenerator.EXPECT().Float64().Return(0.55)

		sizeClassIndex, expectedDuration1, timeout1, learner1 := selector.Select([]uint32{1, 2, 4, 8})
		require.Equal(t, 0, sizeClassIndex)
		require.Equal(t, 40*time.Second, expectedDuration1)
		require.Equal(t, 40*time.Second, timeout1)

		// Let execution fail on size class 1. Because this is
		// not the largest size class, a new learner for size
		// class 8 is returned.
		expectedDuration2, timeout2, learner2 := learner1.Failed(false)
		require.NotNil(t, learner2)
		require.Equal(t, 10*time.Second, expectedDuration2)
		require.Equal(t, 30*time.Minute, timeout2)

		// Report success on size class 8. This should cause the
		// result of both executions to be stored.
		handle.EXPECT().Release(true)

		_, _, _, learner3 := learner2.Succeeded(12*time.Second, []uint32{1, 2, 4, 8})
		require.Nil(t, learner3)
		testutil.RequireEqualProto(t, &model_initialsizeclass_pb.PreviousExecutionStats{
			SizeClasses: map[uint32]*model_initialsizeclass_pb.PerSizeClassStats{
				1: {
					PreviousExecutions: []*model_initialsizeclass_pb.PreviousExecution{
						{Outcome: &model_initialsizeclass_pb.PreviousExecution_Failed{Failed: &emptypb.Empty{}}},
					},
				},
				8: {
					PreviousExecutions: []*model_initialsizeclass_pb.PreviousExecution{
						{Outcome: &model_initialsizeclass_pb.PreviousExecution_Succeeded{Succeeded: &durationpb.Duration{Seconds: 10}}},
						{Outcome: &model_initialsizeclass_pb.PreviousExecution_Succeeded{Succeeded: &durationpb.Duration{Seconds: 12}}},
					},
				},
			},
		}, &stats)
	})

	t.Run("SkipSmallerAfterFailure", func(t *testing.T) {
		handle := NewMockPreviousExecutionStatsHandle(ctrl)
		store.EXPECT().Get(ctx, exampleTagKeyHash).Return(handle, nil)

		selector, err := analyzer.Analyze(ctx, exampleAction)
		require.NoError(t, err)

		// Provide statistics for an action that failed
		// recently. We should always schedule these on the
		// largest size class, so that we don't introduce
		// unnecessary delays.
		stats := model_initialsizeclass_pb.PreviousExecutionStats{
			SizeClasses: map[uint32]*model_initialsizeclass_pb.PerSizeClassStats{
				8: {
					PreviousExecutions: []*model_initialsizeclass_pb.PreviousExecution{
						{Outcome: &model_initialsizeclass_pb.PreviousExecution_Succeeded{Succeeded: &durationpb.Duration{Seconds: 10}}},
					},
				},
			},
			LastSeenFailure: &timestamppb.Timestamp{Seconds: 1620218381},
		}
		handle.EXPECT().GetMutableProto().Return(&stats).AnyTimes()
		clock.EXPECT().Now().Return(time.Unix(1620242374, 0))

		sizeClassIndex, expectedDuration, timeout, learner := selector.Select([]uint32{1, 2, 4, 8})
		require.Equal(t, 3, sizeClassIndex)
		require.Equal(t, 10*time.Second, expectedDuration)
		require.Equal(t, 30*time.Minute, timeout)

		// Abandoning it should not cause any changes to it.
		handle.EXPECT().Release(false)

		learner.Abandoned()
		testutil.RequireEqualProto(t, &model_initialsizeclass_pb.PreviousExecutionStats{
			SizeClasses: map[uint32]*model_initialsizeclass_pb.PerSizeClassStats{
				8: {
					PreviousExecutions: []*model_initialsizeclass_pb.PreviousExecution{
						{Outcome: &model_initialsizeclass_pb.PreviousExecution_Succeeded{Succeeded: &durationpb.Duration{Seconds: 10}}},
					},
				},
			},
			LastSeenFailure: &timestamppb.Timestamp{Seconds: 1620218381},
		}, &stats)
	})

	t.Run("BackgroundRun", func(t *testing.T) {
		handle := NewMockPreviousExecutionStatsHandle(ctrl)
		store.EXPECT().Get(ctx, exampleTagKeyHash).Return(handle, nil)

		selector, err := analyzer.Analyze(ctx, exampleAction)
		require.NoError(t, err)

		// Provide statistics for an action that has never been
		// run before. It should be run on the largest size
		// class, but we do want to perform a background run on
		// the smallest size class. If both succeed, we have
		// more freedom when scheduling this action the next
		// time.
		var stats model_initialsizeclass_pb.PreviousExecutionStats
		handle.EXPECT().GetMutableProto().Return(&stats).AnyTimes()
		strategyCalculator.EXPECT().GetStrategies(gomock.Not(gomock.Nil()), []uint32{1, 2, 4, 8}, 30*time.Minute).
			Return([]initialsizeclass.Strategy{
				{
					Probability:     1.0,
					RunInBackground: true,
				},
			})
		randomNumberGenerator.EXPECT().Float64().Return(0.32)

		sizeClassIndex1, expectedDuration1, timeout1, learner1 := selector.Select([]uint32{1, 2, 4, 8})
		require.Equal(t, 3, sizeClassIndex1)
		require.Equal(t, 30*time.Minute, expectedDuration1)
		require.Equal(t, 30*time.Minute, timeout1)

		// Once execution on the largest size class has
		// succeeded, we should obtain a new learner for running
		// it on the smallest size class.
		//
		// Because the execution timeout to be used on the
		// smallest size class depends on that of the largest
		// size class, we should see a request to recompute the
		// execution timeout.
		strategyCalculator.EXPECT().GetBackgroundExecutionTimeout(gomock.Not(gomock.Nil()), []uint32{1, 2, 4, 8}, 0, 30*time.Minute).DoAndReturn(
			func(perSizeClassStatsMap map[uint32]*model_initialsizeclass_pb.PerSizeClassStats, sizeClasses []uint32, sizeClassIndex int, originalTimeout time.Duration) time.Duration {
				testutil.RequireEqualProto(t, &model_initialsizeclass_pb.PreviousExecutionStats{
					SizeClasses: map[uint32]*model_initialsizeclass_pb.PerSizeClassStats{
						8: {
							PreviousExecutions: []*model_initialsizeclass_pb.PreviousExecution{
								{Outcome: &model_initialsizeclass_pb.PreviousExecution_Succeeded{Succeeded: &durationpb.Duration{Seconds: 42}}},
							},
						},
					},
				}, &stats)
				return 80 * time.Second
			})

		sizeClassIndex2, expectedDuration2, timeout2, learner2 := learner1.Succeeded(42*time.Second, []uint32{1, 2, 4, 8})
		require.NotNil(t, learner2)
		require.Equal(t, 0, sizeClassIndex2)
		require.Equal(t, 80*time.Second, expectedDuration2)
		require.Equal(t, 80*time.Second, timeout2)

		// Once execution on the smallest size class completes,
		// both outcomes are stored.
		handle.EXPECT().Release(true)

		_, _, _, learner3 := learner2.Succeeded(72*time.Second, []uint32{1, 2, 4, 8})
		require.Nil(t, learner3)
		testutil.RequireEqualProto(t, &model_initialsizeclass_pb.PreviousExecutionStats{
			SizeClasses: map[uint32]*model_initialsizeclass_pb.PerSizeClassStats{
				1: {
					PreviousExecutions: []*model_initialsizeclass_pb.PreviousExecution{
						{Outcome: &model_initialsizeclass_pb.PreviousExecution_Succeeded{Succeeded: &durationpb.Duration{Seconds: 72}}},
					},
				},
				8: {
					PreviousExecutions: []*model_initialsizeclass_pb.PreviousExecution{
						{Outcome: &model_initialsizeclass_pb.PreviousExecution_Succeeded{Succeeded: &durationpb.Duration{Seconds: 42}}},
					},
				},
			},
		}, &stats)
	})

	// TODO: Are there more test cases we want to cover?
}
