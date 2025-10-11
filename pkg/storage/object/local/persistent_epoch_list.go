package local

import (
	"math/bits"

	pb "bonanza.build/pkg/proto/storage/object/local"

	"github.com/buildbarn/bb-storage/pkg/random"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// notificationChannel is a helper type to manage the channels returned
// by GetLocationsChangedWakeup. For each of the channels that these
// functions hand out, we must make sure that we call close() exactly
// once.
//
// Forgetting to call close() may cause PeriodicSyncer's goroutines to
// get stuck indefinitely. Calling close() more than once causes us to
// crash.
type notificationChannel struct {
	channel    chan struct{}
	isBlocking bool
}

func newNotificationChannel() notificationChannel {
	return notificationChannel{
		channel:    make(chan struct{}, 1),
		isBlocking: true,
	}
}

func (nc *notificationChannel) block() {
	if !nc.isBlocking {
		*nc = newNotificationChannel()
	}
}

func (nc *notificationChannel) unblock() {
	if nc.isBlocking {
		close(nc.channel)
		nc.isBlocking = false
	}
}

type persistentEpochInfo struct {
	hashSeed        uint64
	maximumLocation uint64
}

// PersistentEpochList is an implementation of EpochList whose internal
// state can be extracted and persisted. This allows data contained in
// the local object store to be accessed after restarts.
type PersistentEpochList struct {
	maximumLocationSpan   uint64
	randomNumberGenerator random.SingleThreadedGenerator

	// When closedForWriting is set, the EpochList will return a
	// failure for all future calls to FinalizeWriteUpToLocation.
	// closedForWriting can only transition from false to true. This
	// flag is set while the storage backend is shutting down, and
	// no success responses should be returned to any callers trying
	// to write objects.
	closedForWriting bool

	minimumEpochID         uint32
	minimumLocation        uint64
	epochs                 []persistentEpochInfo
	synchronizingEpochs    int
	synchronizedEpochs     int
	locationsChangedWakeup notificationChannel
}

var (
	_ EpochList             = (*PersistentEpochList)(nil)
	_ PersistentStateSource = (*PersistentEpochList)(nil)
)

// NewPersistentEpochList creates an instance of PersistentEpochList
// having state on epochs that have been reloaded from a persistent
// state file.
func NewPersistentEpochList(
	maximumLocationSpan uint64,
	randomNumberGenerator random.SingleThreadedGenerator,
	minimumEpochID uint32,
	minimumLocation uint64,
	epochs []*pb.EpochState,
) *PersistentEpochList {
	el := &PersistentEpochList{
		maximumLocationSpan:   maximumLocationSpan,
		randomNumberGenerator: randomNumberGenerator,

		minimumEpochID:         minimumEpochID,
		minimumLocation:        minimumLocation,
		locationsChangedWakeup: newNotificationChannel(),
	}

	// Reload all epochs, so that epochs written by a previous
	// incarnation of this process can be read again.
	maximumLocation := minimumLocation
	for _, epoch := range epochs {
		var carryOut uint64
		maximumLocation, carryOut = bits.Add64(maximumLocation, epoch.LocationIncrease, 0)
		if carryOut != 0 {
			el.reinitialize()
		}
		el.epochs = append(el.epochs, persistentEpochInfo{
			hashSeed:        epoch.HashSeed,
			maximumLocation: maximumLocation,
		})
		el.synchronizingEpochs++
		el.synchronizedEpochs++
	}
	el.pruneStaleEpochs()
	return el
}

func (el *PersistentEpochList) reinitialize() {
	el.minimumEpochID = 0
	el.minimumLocation = 0
	el.epochs = el.epochs[:0]
	el.synchronizingEpochs = 0
	el.synchronizedEpochs = 0
}

// pruneStaleEpochs removes epochs that exclusively reference locations
// that have either been overwritten or discarded.
func (el *PersistentEpochList) pruneStaleEpochs() {
	epochsToPrune := 0
	for len(el.epochs) > 0 && el.epochs[epochsToPrune].maximumLocation <= el.minimumLocation {
		epochsToPrune++
	}

	el.minimumEpochID += uint32(epochsToPrune)
	el.epochs = el.epochs[epochsToPrune:]
	if el.synchronizingEpochs < epochsToPrune {
		el.synchronizingEpochs = 0
	} else {
		el.synchronizingEpochs -= epochsToPrune
	}
	if el.synchronizedEpochs < epochsToPrune {
		el.synchronizedEpochs = 0
	} else {
		el.synchronizedEpochs -= epochsToPrune
	}
}

// FinalizeWriteUpToLocation is called after writing an object to
// storage has finished. This either causes a new epoch to be started,
// or the maximum location covered by the latest epoch to be raised. It
// may also cause old epochs to be discarded, if it is known that all
// objects covered by those epochs have in the meantime been
// overwritten.
func (el *PersistentEpochList) FinalizeWriteUpToLocation(location uint64) error {
	if el.closedForWriting {
		return status.Error(codes.Unavailable, "Cannot write object to storage, as storage is shutting down")
	}

	maximumLocation := el.minimumLocation
	if len(el.epochs) > 0 {
		maximumLocation = el.epochs[len(el.epochs)-1].maximumLocation
	}
	if int64(location-maximumLocation) > 0 {
		// Under the rare circumstance that we've written more
		// than 2^64 bytes of data, LocationBlobMap starts
		// allocating at zero again. This causes full data loss.
		// If that happens, reinitialize the EpochList, so that
		// all reference-location map entries are invalidated.
		if location < maximumLocation {
			el.reinitialize()
		}

		// If this is the first write since the start of the
		// last synchronization, we should create a new epoch.
		if len(el.epochs) == el.synchronizingEpochs {
			el.epochs = append(el.epochs, persistentEpochInfo{
				hashSeed:        el.randomNumberGenerator.Uint64(),
				maximumLocation: location,
			})
		} else {
			el.epochs[len(el.epochs)-1].maximumLocation = location
		}

		// Writing the data will cause old objects to get
		// overwritten. Progress the minimum location.
		if location-el.minimumLocation > el.maximumLocationSpan {
			el.minimumLocation = location - el.maximumLocationSpan
		}
		el.pruneStaleEpochs()
		el.locationsChangedWakeup.unblock()
	}
	return nil
}

// DiscardUpToLocation discards epochs up to a given location. This
// method is invoked when data corruption is detected.
func (el *PersistentEpochList) DiscardUpToLocation(location uint64) {
	if el.minimumLocation < location {
		el.minimumLocation = location
		el.pruneStaleEpochs()
		el.locationsChangedWakeup.unblock()
	}
}

// GetEpochStateForEpochID returns a hash seed that was used by a given
// epoch ID, and the minimum and maximum locations covered by this
// epoch. This can be used to suppress reference-location map entries
// that refer to overwritten or corrupted data.
func (el *PersistentEpochList) GetEpochStateForEpochID(epochID uint32) (EpochState, bool) {
	epochIndex := epochID - el.minimumEpochID
	if epochIndex >= uint32(len(el.epochs)) {
		return EpochState{}, false
	}
	epoch := &el.epochs[epochIndex]
	return EpochState{
		HashSeed:        epoch.hashSeed,
		MinimumLocation: el.minimumLocation,
		MaximumLocation: epoch.maximumLocation,
	}, true
}

// GetCurrentEpochState returns the hash seed to use when writing new
// entries into the reference-location map.
func (el *PersistentEpochList) GetCurrentEpochState() (EpochState, uint32) {
	if len(el.epochs) != el.synchronizingEpochs+1 {
		panic("GetCurrentEpochState() should always be preceded by FinalizeWriteUpToLocation(), which should have created a new epoch")
	}
	epoch := &el.epochs[el.synchronizingEpochs]
	return EpochState{
		HashSeed:        epoch.hashSeed,
		MinimumLocation: el.minimumLocation,
		MaximumLocation: epoch.maximumLocation,
	}, el.minimumEpochID + uint32(el.synchronizingEpochs)
}

// GetLocationsChangedWakeup returns a channel that triggers when there
// was data stored in the location-blob map since the last persistent
// state was written to disk.
func (el *PersistentEpochList) GetLocationsChangedWakeup() <-chan struct{} {
	return el.locationsChangedWakeup.channel
}

// NotifySyncStarting needs to be called right before the data on the
// storage medium underneath the LocationBlobMap is synchronized. This
// causes the epoch ID to be increased when the next blob is stored.
func (el *PersistentEpochList) NotifySyncStarting(isFinalSync bool) {
	if isFinalSync {
		el.closedForWriting = true
	}
	el.synchronizingEpochs = len(el.epochs)
}

// NotifySyncCompleted needs to be called right after the data on the
// storage medium underneath the LocationBlobMap is synchronized. This
// causes the next call to GetPersistentState() to return information on
// the newly synchronized data.
func (el *PersistentEpochList) NotifySyncCompleted() {
	el.synchronizedEpochs = el.synchronizingEpochs
	if el.synchronizedEpochs == len(el.epochs) {
		el.locationsChangedWakeup.block()
	}
}

// GetPersistentState returns information that needs to be persisted to
// disk to be able to restore the layout of the EpochList after a
// restart.
func (el *PersistentEpochList) GetPersistentState() (uint32, uint64, []*pb.EpochState) {
	epochStates := make([]*pb.EpochState, 0, el.synchronizedEpochs)
	previousMaximumLocation := el.minimumLocation
	for _, epoch := range el.epochs[:el.synchronizedEpochs] {
		epochStates = append(epochStates, &pb.EpochState{
			HashSeed:         epoch.hashSeed,
			LocationIncrease: epoch.maximumLocation - previousMaximumLocation,
		})
		previousMaximumLocation = epoch.maximumLocation
	}
	return el.minimumEpochID, el.minimumLocation, epochStates
}
