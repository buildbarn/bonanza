package local

import (
	"crypto/rand"
	"time"

	pb "bonanza.build/pkg/proto/storage/object/local"

	"github.com/buildbarn/bb-storage/pkg/clock"
)

// LeaseTrackingConfiguration contains the parameters for tracking
// leases of objects and the watermark of a local object store. Lease
// tracking is only needed when the store is used by storage shards.
type LeaseTrackingConfiguration struct {
	Clock clock.Clock

	// The watermark is computed as if all data that will be
	// overwritten within this amount of time is already gone.
	WatermarkLookahead time.Duration

	// If set, objects whose lease is lower than the value returned
	// by this function are rewritten when UploadObject() is called,
	// so that they get a new lease. This is only used by Store.
	MinimumSafeSince func() time.Time

	// If set, the value returned by this function is persisted as
	// part of a clean shutdown, and reported by GetIncarnation()
	// after the next startup. This is used by storage shards to
	// preserve the last LeaseValidationContext.incarnations_hash
	// they received across restarts.
	GetLastIncarnationsHash func() []byte

	// If set, this function is called as part of a clean shutdown,
	// before the lease tracking state is persisted. Leases stored
	// in other data structures (e.g., maps of leases or tags) are
	// only valid after a restart if the lease tracking state is
	// present. This function can be used to ensure that such data
	// structures are written to persistent storage first.
	SyncOnCleanShutdown func() error
}

// incarnationSizeBytes is the size of incarnations that are generated
// when no lease tracking state is present.
const incarnationSizeBytes = 16

// leaseTracker keeps track of the points in time at which data was
// written to the location-blob map, so that leases of objects and the
// watermark of the store can be computed.
//
// A nil leaseTracker is valid, and corresponds to lease tracking being
// disabled. All leases and the watermark are then reported as the zero
// time, and no state is persisted.
type leaseTracker struct {
	clock                   clock.Clock
	watermarkLookahead      time.Duration
	getLastIncarnationsHash func() []byte

	// The incarnation of the store, and the last incarnations hash
	// that was persisted alongside it.
	incarnation          []byte
	lastIncarnationsHash []byte

	locationTimeline locationTimeline
	watermark        time.Time
}

// newLeaseTracker creates a leaseTracker. State is only persisted when
// the previous instance shut down cleanly, in which case the timeline
// and the incarnation are reloaded from it. Otherwise, data written
// after the persistent state was written may have been lost, even
// though leases of such data may have been handed out. The watermark is
// then raised to the current time to invalidate them, and a new
// incarnation is generated.
func newLeaseTracker(configuration *LeaseTrackingConfiguration, state *pb.LeaseTrackingState, maximumLocation uint64) *leaseTracker {
	if configuration == nil {
		return nil
	}
	lt := &leaseTracker{
		clock:                   configuration.Clock,
		watermarkLookahead:      configuration.WatermarkLookahead,
		getLastIncarnationsHash: configuration.GetLastIncarnationsHash,
		incarnation:             state.GetIncarnation(),
		lastIncarnationsHash:    state.GetLastIncarnationsHash(),
		locationTimeline: newLocationTimelineFromPersistentState(
			state.GetLocationTimeline(),
			state.GetWatermark(),
			maximumLocation,
		),
	}
	now := lt.clock.Now()
	if state == nil {
		lt.locationTimeline.raiseFloor(now)
	}
	if len(lt.incarnation) == 0 {
		lt.incarnation = make([]byte, incarnationSizeBytes)
		rand.Read(lt.incarnation)
		lt.lastIncarnationsHash = nil
	}
	lt.locationTimeline.record(maximumLocation, now)
	return lt
}

// reset is called when all data stored in the location-blob map is
// lost.
func (lt *leaseTracker) reset() {
	if lt != nil {
		lt.locationTimeline.reset(lt.clock.Now())
	}
}

// record that writing data at a given location starts now.
func (lt *leaseTracker) record(location uint64) {
	if lt != nil {
		lt.locationTimeline.record(location, lt.clock.Now())
	}
}

// update the watermark after the minimum or maximum location of the
// data stored in the location-blob map has changed.
func (lt *leaseTracker) update(minimumLocation, maximumLocation uint64) {
	if lt != nil {
		now := lt.clock.Now()
		lt.locationTimeline.prune(minimumLocation)
		lt.watermark = lt.locationTimeline.getWatermark(
			minimumLocation+lt.locationTimeline.getLookaheadBytes(maximumLocation, lt.watermarkLookahead, now),
			now,
		)
	}
}

// invalidate raises the watermark to the current time, thereby
// invalidating the leases of all objects.
func (lt *leaseTracker) invalidate() {
	if lt != nil {
		lt.locationTimeline.raiseFloor(lt.clock.Now())
	}
}

// discardLocation is called when a reference-location map entry
// pointing to a given location is discarded.
func (lt *leaseTracker) discardLocation(location uint64) {
	if lt != nil {
		lt.locationTimeline.discardLocation(location)
	}
}

func (lt *leaseTracker) getSafeSince(location uint64) time.Time {
	if lt == nil {
		return time.Time{}
	}
	return lt.locationTimeline.getSafeSince(location)
}

func (lt *leaseTracker) getIncarnation() ([]byte, []byte) {
	if lt == nil {
		return nil, nil
	}
	return lt.incarnation, lt.lastIncarnationsHash
}

func (lt *leaseTracker) getWatermark() time.Time {
	if lt == nil {
		return time.Time{}
	}
	return lt.watermark
}

// getPersistentState returns the state that needs to be persisted to
// be able to compute leases of objects after a restart. As the state is
// only valid if no data is written after it, it is only returned as
// part of a clean shutdown.
func (lt *leaseTracker) getPersistentState(maximumLocation uint64, cleanShutdown bool) *pb.LeaseTrackingState {
	if lt == nil || !cleanShutdown {
		return nil
	}
	entries, watermark := lt.locationTimeline.getPersistentState(maximumLocation)
	state := pb.LeaseTrackingState_builder{
		LocationTimeline: entries,
		Watermark:        watermark,
	}.Build()
	if lt.incarnation != nil {
		state.SetIncarnation(lt.incarnation)
	}
	if lt.getLastIncarnationsHash != nil {
		state.SetLastIncarnationsHash(lt.getLastIncarnationsHash())
	}
	return state
}
