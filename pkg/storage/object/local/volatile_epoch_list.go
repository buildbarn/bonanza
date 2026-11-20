package local

import (
	"time"

	"github.com/buildbarn/bb-storage/pkg/random"
)

type volatileEpochList struct {
	maximumLocationSpan   uint64
	randomNumberGenerator random.SingleThreadedGenerator
	leaseTracking         *LeaseTrackingConfiguration

	epochID    uint32
	epochState EpochState

	leaseTracker *leaseTracker
}

func (el *volatileEpochList) reinitialize() {
	el.epochID = el.randomNumberGenerator.Uint32()
	el.epochState = EpochState{
		HashSeed: el.randomNumberGenerator.Uint64(),
	}

	// All data is lost. Start with an empty timeline, and raise
	// the watermark to the current time.
	if el.leaseTracker == nil {
		el.leaseTracker = newLeaseTracker(el.leaseTracking, nil, 0)
	} else {
		el.leaseTracker.reset()
		el.leaseTracker.record(0)
	}
	el.updateWatermark()
}

// NewVolatileEpochList creates a simple EpochList that does not support
// any persistency. This is sufficient for setups where losing data upon
// restart is acceptable (e.g., worker level caches).
//
// If leaseTracking is set, the points in time at which data is written
// are tracked, so that leases of objects and the watermark of the store
// can be computed.
func NewVolatileEpochList(maximumLocationSpan uint64, randomNumberGenerator random.SingleThreadedGenerator, leaseTracking *LeaseTrackingConfiguration) EpochList {
	el := &volatileEpochList{
		maximumLocationSpan:   maximumLocationSpan,
		randomNumberGenerator: randomNumberGenerator,
		leaseTracking:         leaseTracking,
	}
	el.reinitialize()
	return el
}

func (el *volatileEpochList) updateWatermark() {
	el.leaseTracker.update(el.epochState.MinimumLocation, el.epochState.MaximumLocation)
}

func (el *volatileEpochList) FinalizeWriteUpToLocation(location uint64) error {
	if int64(location-el.epochState.MaximumLocation) > 0 {
		// Under the rare circumstance that we've written more
		// than 2^64 bytes of data, LocationBlobMap starts
		// allocating at zero again. This causes full data loss.
		// If that happens, reinitialize the EpochList, so that
		// all reference-location map entries are invalidated.
		if location < el.epochState.MaximumLocation {
			el.reinitialize()
		}
		el.leaseTracker.record(el.epochState.MaximumLocation)
		el.epochState.MaximumLocation = location

		// Writing the data will cause old objects to get
		// overwritten. Progress the minimum location.
		if el.epochState.MaximumLocation-el.epochState.MinimumLocation > el.maximumLocationSpan {
			el.epochState.MinimumLocation = el.epochState.MaximumLocation - el.maximumLocationSpan
		}
		el.updateWatermark()
	}
	return nil
}

func (el *volatileEpochList) DiscardUpToLocation(location uint64) {
	if el.epochState.MinimumLocation < location {
		el.epochState.MinimumLocation = location
		if el.epochState.MaximumLocation < el.epochState.MinimumLocation {
			el.epochState.MaximumLocation = el.epochState.MinimumLocation
		}
		el.updateWatermark()
	}
}

func (el *volatileEpochList) DiscardLocation(location uint64) {
	el.leaseTracker.discardLocation(location)
	el.updateWatermark()
}

func (el *volatileEpochList) GetEpochStateForEpochID(epochID uint32) (EpochState, bool) {
	if epochID != el.epochID {
		return EpochState{}, false
	}
	return el.epochState, true
}

func (el *volatileEpochList) GetCurrentEpochState() (EpochState, uint32) {
	return el.epochState, el.epochID
}

func (el *volatileEpochList) GetSafeSince(location uint64) time.Time {
	return el.leaseTracker.getSafeSince(location)
}

func (el *volatileEpochList) GetWatermark() time.Time {
	return el.leaseTracker.getWatermark()
}

func (el *volatileEpochList) InvalidateLeases() {
	el.leaseTracker.invalidate()
	el.updateWatermark()
}
