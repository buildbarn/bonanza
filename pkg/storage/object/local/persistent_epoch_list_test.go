package local_test

import (
	"testing"
	"time"

	pb "bonanza.build/pkg/proto/storage/object/local"
	"bonanza.build/pkg/storage/object/local"

	"github.com/buildbarn/bb-storage/pkg/random"
	"github.com/stretchr/testify/require"

	"go.uber.org/mock/gomock"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestPersistentEpochListLeaseTracking(t *testing.T) {
	t0 := time.Unix(1700000000, 0).UTC()
	t1 := t0.Add(100 * time.Second)

	// Persistent state of a store containing 1000 bytes of data,
	// written by a previous incarnation that had lease tracking
	// enabled. Lease tracking state is only present if the previous
	// incarnation shut down cleanly.
	newPersistentState := func(cleanShutdown bool) *pb.PersistentState {
		persistentState := &pb.PersistentState{
			MinimumEpochId:  1,
			MinimumLocation: 0,
			Epochs: []*pb.EpochState{
				{HashSeed: 123, LocationIncrease: 1000},
			},
		}
		if cleanShutdown {
			persistentState.LeaseTracking = &pb.LeaseTrackingState{
				LocationTimeline: []*pb.LocationTimelineEntry{
					{Location: 0, Time: timestamppb.New(t0)},
					{Location: 500, Time: timestamppb.New(t0.Add(10 * time.Second))},
				},
				Watermark: timestamppb.New(t0.Add(5 * time.Second)),
			}
		}
		return persistentState
	}
	newLeaseTracking := func(ctrl *gomock.Controller, now time.Time) *local.LeaseTrackingConfiguration {
		clock := NewMockClock(ctrl)
		clock.EXPECT().Now().Return(now).AnyTimes()
		return &local.LeaseTrackingConfiguration{Clock: clock}
	}

	t.Run("Disabled", func(t *testing.T) {
		// If lease tracking is disabled (e.g., because the
		// store is used as a cache), no leases are handed out,
		// and no state for computing them is persisted. State
		// left behind by a previous incarnation is discarded.
		el := local.NewPersistentEpochList(10000, random.NewFastSingleThreadedGenerator(), nil, newPersistentState(true))
		require.NoError(t, el.FinalizeWriteUpToLocation(1100))
		el.DiscardLocation(600)

		require.Equal(t, time.Time{}, el.GetSafeSince(600))
		require.Equal(t, time.Time{}, el.GetWatermark())
		require.Nil(t, el.GetPersistentState().LeaseTracking)
	})

	t.Run("CleanShutdown", func(t *testing.T) {
		// After a clean shutdown, leases handed out by the
		// previous incarnation remain valid.
		ctrl := gomock.NewController(t)
		el := local.NewPersistentEpochList(10000, random.NewFastSingleThreadedGenerator(), newLeaseTracking(ctrl, t1), newPersistentState(true))

		require.Equal(t, t0.Add(10*time.Second), el.GetSafeSince(600))
		require.Equal(t, t0.Add(5*time.Second), el.GetWatermark())
	})

	t.Run("UncleanShutdown", func(t *testing.T) {
		// After an unclean shutdown, or if lease tracking was
		// previously disabled, data written after the
		// persistent state was written may have been lost, even
		// though leases for it were handed out. The watermark
		// must be raised to the current time, and existing
		// objects should be reported as having no lease, so
		// that they get rewritten.
		ctrl := gomock.NewController(t)
		el := local.NewPersistentEpochList(10000, random.NewFastSingleThreadedGenerator(), newLeaseTracking(ctrl, t1), newPersistentState(false))

		require.Equal(t, time.Time{}, el.GetSafeSince(600))
		require.Equal(t, t1, el.GetWatermark())

		// Data written from now on gets a lease.
		require.NoError(t, el.FinalizeWriteUpToLocation(1100))
		require.Equal(t, t1, el.GetSafeSince(1000))
	})

	t.Run("InvalidateLeases", func(t *testing.T) {
		// Invalidating leases should raise the watermark to the
		// current time. This should be retained as part of the
		// persistent state written upon clean shutdown.
		ctrl := gomock.NewController(t)
		el := local.NewPersistentEpochList(10000, random.NewFastSingleThreadedGenerator(), newLeaseTracking(ctrl, t1), newPersistentState(true))
		require.Equal(t, t0.Add(5*time.Second), el.GetWatermark())

		el.InvalidateLeases()
		require.Equal(t, t1, el.GetWatermark())

		el.NotifySyncStarting(true)
		el.NotifySyncCompleted()
		require.Equal(t, timestamppb.New(t1), el.GetPersistentState().LeaseTracking.Watermark)
	})

	t.Run("PersistLeaseTrackingState", func(t *testing.T) {
		// Lease tracking state should only be persisted as
		// part of a clean shutdown, as it becomes invalid as
		// soon as data is written after it.
		ctrl := gomock.NewController(t)
		el := local.NewPersistentEpochList(10000, random.NewFastSingleThreadedGenerator(), newLeaseTracking(ctrl, t1), newPersistentState(true))
		el.NotifySyncStarting(false)
		el.NotifySyncCompleted()
		require.Nil(t, el.GetPersistentState().LeaseTracking)

		el.NotifySyncStarting(true)
		el.NotifySyncCompleted()
		leaseTracking := el.GetPersistentState().LeaseTracking
		require.NotNil(t, leaseTracking)
		require.Equal(t, timestamppb.New(t0.Add(5*time.Second)), leaseTracking.Watermark)
	})
}
