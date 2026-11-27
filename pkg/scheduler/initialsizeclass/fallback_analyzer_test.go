package initialsizeclass_test

import (
	"context"
	"testing"
	"time"

	encryptedaction_pb "bonanza.build/pkg/proto/encryptedaction"
	"bonanza.build/pkg/scheduler/initialsizeclass"

	"github.com/stretchr/testify/require"

	"google.golang.org/protobuf/types/known/durationpb"
)

func TestFallbackAnalyzer(t *testing.T) {
	ctx := context.Background()
	analyzer := initialsizeclass.NewFallbackAnalyzer(
		initialsizeclass.NewActionTimeoutExtractor(time.Hour),
	)

	exampleAction := encryptedaction_pb.Action_builder{
		AdditionalData: encryptedaction_pb.Action_AdditionalData_builder{
			ExecutionTimeout: &durationpb.Duration{Seconds: 300},
		}.Build(),
	}.Build()

	t.Run("SelectorAbandoned", func(t *testing.T) {
		// Action that is analyzed, but that is immediately
		// abandoned. This should have no effect.
		selector, err := analyzer.Analyze(ctx, exampleAction)
		require.NoError(t, err)

		selector.Abandoned()
	})

	t.Run("SingleSizeClassFailure", func(t *testing.T) {
		// When we have a single size class, we shouldn't do any
		// retried upon failure.
		selector, err := analyzer.Analyze(ctx, exampleAction)
		require.NoError(t, err)

		sizeClassIndex, expectedDuration1, timeout1, learner1 := selector.Select([]uint32{4})
		require.Equal(t, 0, sizeClassIndex)
		require.Equal(t, 300*time.Second, expectedDuration1)
		require.Equal(t, 300*time.Second, timeout1)

		_, _, learner2 := learner1.Failed(true)
		require.Nil(t, learner2)
	})

	t.Run("MultipleSizeClassFailure", func(t *testing.T) {
		// When we have multiple size classes, we should first
		// try executing on the smallest size class, followed by
		// executing on the largest one.
		selector, err := analyzer.Analyze(ctx, exampleAction)
		require.NoError(t, err)

		sizeClassIndex, expectedDuration1, timeout1, learner1 := selector.Select([]uint32{1, 2, 4, 8})
		require.Equal(t, 0, sizeClassIndex)
		require.Equal(t, 300*time.Second, expectedDuration1)
		require.Equal(t, 300*time.Second, timeout1)

		expectedDuration2, timeout2, learner2 := learner1.Failed(true)
		require.NotNil(t, learner2)
		require.Equal(t, 300*time.Second, expectedDuration2)
		require.Equal(t, 300*time.Second, timeout2)

		_, _, learner3 := learner2.Failed(false)
		require.Nil(t, learner3)
	})

	t.Run("MultipleSizeClassFirstSuccess", func(t *testing.T) {
		// When we have multiple size classes and successfully
		// complete an action on the smallest size class, there
		// is nothing else to do. There won't be any retried on
		// a larger size class.
		selector, err := analyzer.Analyze(ctx, exampleAction)
		require.NoError(t, err)

		sizeClassIndex1, expectedDuration1, timeout1, learner1 := selector.Select([]uint32{1, 2, 4, 8})
		require.NotNil(t, learner1)
		require.Equal(t, 0, sizeClassIndex1)
		require.Equal(t, 300*time.Second, expectedDuration1)
		require.Equal(t, 300*time.Second, timeout1)

		_, _, _, learner2 := learner1.Succeeded(100*time.Second, []uint32{1, 2, 4, 8})
		require.Nil(t, learner2)
	})
}
