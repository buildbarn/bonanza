package local_test

import (
	"context"
	"sync"
	"testing"
	"time"

	pb "bonanza.build/pkg/proto/storage/object/local"
	"bonanza.build/pkg/storage/object/local"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go.uber.org/mock/gomock"
)

func TestPeriodicSyncerProcessLocationsChanged(t *testing.T) {
	ctrl, ctx := gomock.WithContext(context.Background(), t)

	source := NewMockPersistentStateSource(ctrl)
	var sourceLock sync.RWMutex
	store := NewMockPersistentStateStore(ctrl)
	clock := NewMockClock(ctrl)
	clock.EXPECT().Now().Return(time.Unix(1000, 0))
	errorLogger := NewMockErrorLogger(ctrl)
	dataSyncer := NewMockDataSyncer(ctrl)
	periodicSyncer := local.NewPeriodicSyncer(
		source,
		&sourceLock,
		store,
		clock,
		errorLogger,
		30*time.Second,
		time.Minute,
		0xdf280dd45b2c39e,
		dataSyncer.Call,
	)

	exampleEpochStates := []*pb.EpochState{{
		HashSeed:         0xc6e9c01b35dfeb40,
		LocationIncrease: 100,
	}}

	t.Run("KeepGoing", func(t *testing.T) {
		// Simulate the entire flow of writing the persistent
		// state after PersistentBlockList allocates a location
		// for storing objects contents.
		locationsChangedWakeup := make(chan struct{}, 1)
		close(locationsChangedWakeup)

		timer1 := NewMockTimer(ctrl)
		timerChan1 := make(chan time.Time, 1)
		timerChan1 <- time.Unix(1060, 0)

		timer2 := NewMockTimer(ctrl)
		timerChan2 := make(chan time.Time, 1)
		timerChan2 <- time.Unix(1095, 0)

		gomock.InOrder(
			source.EXPECT().GetLocationsChangedWakeup().Return(locationsChangedWakeup),

			// Synchronization should be started, though a delay
			// should be added before it. This is to ensure we don't
			// synchronize against storage too aggressively and
			// create too many epochs.
			clock.EXPECT().Now().Return(time.Unix(1001, 0)),
			clock.EXPECT().NewTimer(59*time.Second).Return(timer1, timerChan1),
			source.EXPECT().NotifySyncStarting(false),

			// Failure to synchronize the data should cause a delay,
			// but not another call to NotifySyncStarting(). That
			// would increase the number of epochs, which we'd
			// better not do until we know for sure that storage is
			// back online.
			dataSyncer.EXPECT().Call().Return(status.Error(codes.Internal, "Disk on fire")),
			errorLogger.EXPECT().Log(testutil.EqStatus(t, status.Error(codes.Internal, "Failed to synchronize data: Disk on fire"))),
			clock.EXPECT().NewTimer(30*time.Second).Return(timer2, timerChan2),

			// Successfully complete synchronizing data. This should
			// cause the PersistentBlockList to be notified, so that
			// new epochs can be exposed as part of
			// GetPersistentState().
			dataSyncer.EXPECT().Call(),
			source.EXPECT().NotifySyncCompleted(),

			// The persistent state should be updated immediately,
			// so that the data that has been synchronized remains
			// available after restarts.
			source.EXPECT().GetPersistentState().Return(uint32(7), uint64(13), exampleEpochStates),
			store.EXPECT().WritePersistentState(&pb.PersistentState{
				MinimumEpochId:                         7,
				MinimumLocation:                        13,
				Epochs:                                 exampleEpochStates,
				ReferenceLocationMapHashInitialization: 0xdf280dd45b2c39e,
			}),
		)

		require.True(t, periodicSyncer.ProcessLocationsChanged(ctx))
	})

	t.Run("Cancelled", func(t *testing.T) {
		// Simulate the flow that needs to be executed upon shutdown.
		locationsChangedWakeup := make(chan struct{}, 1)
		gomock.InOrder(
			source.EXPECT().GetLocationsChangedWakeup().Return(locationsChangedWakeup),

			// First sync with isFinalSync set to false.
			source.EXPECT().NotifySyncStarting(false),
			dataSyncer.EXPECT().Call(),
			source.EXPECT().NotifySyncCompleted(),

			// Second sync with isFinalSync set to true.
			// After this point the storage backend permits
			// no new writes.
			source.EXPECT().NotifySyncStarting(true),
			dataSyncer.EXPECT().Call(),
			source.EXPECT().NotifySyncCompleted(),

			source.EXPECT().GetPersistentState().Return(uint32(13), uint64(20), exampleEpochStates),
			store.EXPECT().WritePersistentState(&pb.PersistentState{
				MinimumEpochId:                         13,
				MinimumLocation:                        20,
				Epochs:                                 exampleEpochStates,
				ReferenceLocationMapHashInitialization: 0xdf280dd45b2c39e,
			}),
		)

		cancelledCtx, cancel := context.WithCancel(ctx)
		cancel()
		require.False(t, periodicSyncer.ProcessLocationsChanged(cancelledCtx))
	})
}
