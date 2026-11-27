package local_test

import (
	"testing"
	"time"

	pb "bonanza.build/pkg/proto/storage/object/local"
	"bonanza.build/pkg/storage/object/local"

	"github.com/buildbarn/bb-storage/pkg/random"
	"github.com/stretchr/testify/require"

	"google.golang.org/protobuf/types/known/timestamppb"

	"go.uber.org/mock/gomock"
)

func TestPersistentEpochListLeaseTracking(t *testing.T) {
	t0 := time.Unix(1700000000, 0).UTC()
	t1 := t0.Add(100 * time.Second)

	// Persistent state of a store containing 1000 bytes of data,
	// written by a previous incarnation that had lease tracking
	// enabled. Lease tracking state is only present if the previous
	// incarnation shut down cleanly.
	newPersistentState := func(cleanShutdown bool) *pb.PersistentState {
		persistentState := pb.PersistentState_builder{
			MinimumEpochId:  1,
			MinimumLocation: 0,
			Epochs: []*pb.EpochState{
				pb.EpochState_builder{HashSeed: 123, LocationIncrease: 1000}.Build(),
			},
		}.Build()
		if cleanShutdown {
			persistentState.SetLeaseTracking(pb.LeaseTrackingState_builder{
				LocationTimeline: []*pb.LocationTimelineEntry{
					pb.LocationTimelineEntry_builder{Location: 0, Time: timestamppb.New(t0)}.Build(),
					pb.LocationTimelineEntry_builder{Location: 500, Time: timestamppb.New(t0.Add(10 * time.Second))}.Build(),
				},
				Watermark:            timestamppb.New(t0.Add(5 * time.Second)),
				Incarnation:          []byte("incarnation"),
				LastIncarnationsHash: []byte("hash"),
			}.Build())
		}
		return persistentState
	}
	newLeaseTracking := func(ctrl *gomock.Controller, now time.Time) *local.LeaseTrackingConfiguration {
		clock := NewMockClock(ctrl)
		clock.EXPECT().Now().Return(now).AnyTimes()
		return &local.LeaseTrackingConfiguration{
			Clock: clock,
			GetLastIncarnationsHash: func() []byte {
				return []byte("newHash")
			},
		}
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
		require.Nil(t, el.GetPersistentState().GetLeaseTracking())
		incarnation, lastIncarnationsHash := el.GetIncarnation()
		require.Nil(t, incarnation)
		require.Nil(t, lastIncarnationsHash)
	})

	t.Run("CleanShutdown", func(t *testing.T) {
		// After a clean shutdown, leases handed out by the
		// previous incarnation remain valid. The incarnation
		// and the last incarnations hash should be preserved.
		ctrl := gomock.NewController(t)
		el := local.NewPersistentEpochList(10000, random.NewFastSingleThreadedGenerator(), newLeaseTracking(ctrl, t1), newPersistentState(true))

		require.Equal(t, t0.Add(10*time.Second), el.GetSafeSince(600))
		require.Equal(t, t0.Add(5*time.Second), el.GetWatermark())
		incarnation, lastIncarnationsHash := el.GetIncarnation()
		require.Equal(t, []byte("incarnation"), incarnation)
		require.Equal(t, []byte("hash"), lastIncarnationsHash)
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

		// A new incarnation should be generated, so that other
		// storage servers can detect that this storage server
		// may have lost data.
		incarnation, lastIncarnationsHash := el.GetIncarnation()
		require.Len(t, incarnation, 16)
		require.Nil(t, lastIncarnationsHash)

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
		require.Equal(t, timestamppb.New(t1), el.GetPersistentState().GetLeaseTracking().GetWatermark())
	})

	t.Run("PersistLeaseTrackingState", func(t *testing.T) {
		// Lease tracking state should only be persisted as
		// part of a clean shutdown, as it becomes invalid as
		// soon as data is written after it.
		ctrl := gomock.NewController(t)
		el := local.NewPersistentEpochList(10000, random.NewFastSingleThreadedGenerator(), newLeaseTracking(ctrl, t1), newPersistentState(true))
		el.NotifySyncStarting(false)
		el.NotifySyncCompleted()
		require.Nil(t, el.GetPersistentState().GetLeaseTracking())

		el.NotifySyncStarting(true)
		el.NotifySyncCompleted()
		leaseTracking := el.GetPersistentState().GetLeaseTracking()
		require.NotNil(t, leaseTracking)
		require.Equal(t, timestamppb.New(t0.Add(5*time.Second)), leaseTracking.GetWatermark())
		require.Equal(t, []byte("incarnation"), leaseTracking.GetIncarnation())
		require.Equal(t, []byte("newHash"), leaseTracking.GetLastIncarnationsHash())
	})
}

func TestPersistentEpochListOutOfOrderWrites(t *testing.T) {
	// Writes to the location-blob map may complete out of order.
	// Object A is allocated at [0, 100) and object B at [100, 200),
	// but B completes first. A synchronization starts and completes
	// before A completes.
	el := local.NewPersistentEpochList(10000, random.NewFastSingleThreadedGenerator(), nil, &pb.PersistentState{})
	require.NoError(t, el.FinalizeWriteUpToLocation(200))
	_, epochIDB := el.GetCurrentEpochState()
	el.NotifySyncStarting(false)
	el.NotifySyncCompleted()

	// A's data is not guaranteed to be covered by the
	// synchronization. It should therefore be placed in a new
	// epoch, which is only persisted after the next
	// synchronization. This used to cause GetCurrentEpochState()
	// to panic.
	require.NoError(t, el.FinalizeWriteUpToLocation(100))
	epochStateA, epochIDA := el.GetCurrentEpochState()
	require.Equal(t, epochIDB+1, epochIDA)
	require.True(t, epochStateA.IsValidLocation(0, 100))
	require.Len(t, el.GetPersistentState().GetEpochs(), 1)

	el.NotifySyncStarting(false)
	el.NotifySyncCompleted()
	epochs := el.GetPersistentState().GetEpochs()
	require.Len(t, epochs, 2)
	require.Equal(t, uint64(200), epochs[0].GetLocationIncrease())
	require.Equal(t, uint64(0), epochs[1].GetLocationIncrease())

	// Reloading the persistent state should allow entries
	// belonging to either epoch to be interpreted.
	el = local.NewPersistentEpochList(10000, random.NewFastSingleThreadedGenerator(), nil, el.GetPersistentState())
	_, ok := el.GetEpochStateForEpochID(epochIDB)
	require.True(t, ok)
	epochStateA, ok = el.GetEpochStateForEpochID(epochIDA)
	require.True(t, ok)
	require.True(t, epochStateA.IsValidLocation(0, 100))
}
