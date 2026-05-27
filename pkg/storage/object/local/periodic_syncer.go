package local

import (
	"context"
	"sync"
	"time"

	pb "bonanza.build/pkg/proto/storage/object/local"
	"github.com/buildbarn/bb-storage/pkg/clock"
	"github.com/buildbarn/bb-storage/pkg/util"
)

// PeriodicSyncer can be used to monitor PersistentEpochList for
// allocations for storing objects. When such events occur, the state of
// the PersistentEpochList is extracted and written to disk. This allows
// its contents to be recovered after a restart.
type PeriodicSyncer struct {
	store                                  PersistentStateStore
	clock                                  clock.Clock
	errorLogger                            util.ErrorLogger
	errorRetryInterval                     time.Duration
	minimumEpochInterval                   time.Duration
	referenceLocationMapHashInitialization uint64
	dataSyncer                             DataSyncer

	sourceLock *sync.RWMutex
	source     PersistentStateSource

	lastSynchronizationTime time.Time
}

// DataSyncer is a callback that
// PeriodicSyncer.ProcessLocationsChanged() invokes into to request that
// the contents of objects are synchronized to disk.
//
// Synchronizing these is a requirements to ensure that the
// ReferenceLocationMap does not reference objects that are only
// partially written.
type DataSyncer func() error

// NewPeriodicSyncer creates a new PeriodicSyncer according to the
// arguments provided.
func NewPeriodicSyncer(
	source PersistentStateSource,
	sourceLock *sync.RWMutex,
	store PersistentStateStore,
	clock clock.Clock,
	errorLogger util.ErrorLogger,
	errorRetryInterval,
	minimumEpochInterval time.Duration,
	referenceLocationMapHashInitialization uint64,
	dataSyncer DataSyncer,
) *PeriodicSyncer {
	return &PeriodicSyncer{
		store:                                  store,
		clock:                                  clock,
		errorLogger:                            errorLogger,
		errorRetryInterval:                     errorRetryInterval,
		minimumEpochInterval:                   minimumEpochInterval,
		referenceLocationMapHashInitialization: referenceLocationMapHashInitialization,
		dataSyncer:                             dataSyncer,

		source:                  source,
		sourceLock:              sourceLock,
		lastSynchronizationTime: clock.Now(),
	}
}

func (ps *PeriodicSyncer) logErrorAndSleep(err error) {
	// TODO: Should we add Prometheus metrics here, or introduce a
	// MetricsErrorLogger?
	ps.errorLogger.Log(err)
	_, t := ps.clock.NewTimer(ps.errorRetryInterval)
	<-t
}

func (ps *PeriodicSyncer) writePersistentState() error {
	ps.sourceLock.RLock()
	minimumEpochID, minimumLocation, epochs := ps.source.GetPersistentState()
	ps.sourceLock.RUnlock()

	return ps.store.WritePersistentState(&pb.PersistentState{
		MinimumEpochId:                         minimumEpochID,
		MinimumLocation:                        minimumLocation,
		Epochs:                                 epochs,
		ReferenceLocationMapHashInitialization: ps.referenceLocationMapHashInitialization,
	})
}

func (ps *PeriodicSyncer) writePersistentStateRetrying() {
	for {
		err := ps.writePersistentState()
		if err == nil {
			break
		}
		ps.logErrorAndSleep(util.StatusWrap(err, "Failed to write persistent state"))
	}
}

func (ps *PeriodicSyncer) notifyAndSyncDataLocked(isFinalSync bool) {
	ps.source.NotifySyncStarting(isFinalSync)
	ps.sourceLock.Unlock()
	for {
		// TODO: Add metrics for the duration of DataSyncer
		// calls? That could give us insight in the actual load
		// of the underlying storage medium.
		err := ps.dataSyncer()
		if err == nil {
			break
		}
		ps.logErrorAndSleep(util.StatusWrap(err, "Failed to synchronize data"))
	}
	ps.sourceLock.Lock()
	ps.source.NotifySyncCompleted()
}

// ProcessLocationsChanged waits for allocations of locations for
// storing object contents to be made against a PersistentEpochList. It
// causes data on the underlying block device to be synchronized after a
// certain amount of time, followed by updating the persistent state
// stored on disk.
//
// This function must generally be called in a loop in a separate
// goroutine, so that the persistent state is updated continuously.
// The return value of this method denotes whether the caller must
// continue to call this method. When false, it indicates the provided
// context was cancelled, due to a shutdown being requested.
func (ps *PeriodicSyncer) ProcessLocationsChanged(ctx context.Context) bool {
	ps.sourceLock.RLock()
	ch := ps.source.GetLocationsChangedWakeup()
	ps.sourceLock.RUnlock()

	// Insert a delay prior to synchronizing and updating persisting
	// state. We don't want to synchronize too often, as this both
	// adds load to the system and causes to add many epochs to the
	// PersistentEpochList.
	keepGoing := true
	var t <-chan time.Time
	select {
	case <-ch:
		// The system was already busy at the start of
		// ProcessLocationsChanged(). At least make sure that we
		// respect the minimum epoch interval.
		_, t = ps.clock.NewTimer(
			ps.lastSynchronizationTime.
				Add(ps.minimumEpochInterval).
				Sub(ps.clock.Now()),
		)
	default:
		// The system was idle for some time. Wait a bit, so
		// that the current epoch gets a meaningful amount of
		// data.
		select {
		case <-ctx.Done():
			keepGoing = false
		case <-ch:
			_, t = ps.clock.NewTimer(ps.minimumEpochInterval)
		}
	}
	if keepGoing {
		select {
		case <-ctx.Done():
			keepGoing = false
		case ps.lastSynchronizationTime = <-t:
		}
	}

	ps.sourceLock.Lock()
	ps.notifyAndSyncDataLocked(false)
	if !keepGoing {
		// Perform a second sync when shutting down. This one
		// causes future writes to get rejected. By not doing
		// this as part of the first sync, the amount of time
		// being read-only prior shutdown remains minimal.
		ps.notifyAndSyncDataLocked(true)
	}
	ps.sourceLock.Unlock()

	ps.writePersistentStateRetrying()
	return keepGoing
}
