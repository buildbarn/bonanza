package local

import (
	"time"
)

// EpochList is used by the local object store to manage the creation
// and lifetime of epochs. Epochs are assigned to reference-location map
// entries and act as a way of ensuring consistency between the
// reference-location map and location-blob map, even if unclean
// shutdowns are performed.
type EpochList interface {
	EpochIDResolver

	// Indicate that a write of an object to the location-blob map
	// ending at a given location has completed, and that a
	// reference-location map entry is about to be written.
	FinalizeWriteUpToLocation(location uint64) error

	// Indicate that an object ending at a given location was read
	// from the location-blob map, but that its contents were
	// invalid. This means that either data corruption has occurred,
	// or that the object got overwritten while being read.
	DiscardUpToLocation(location uint64)

	// GetSafeSince returns the lease of an object stored at a given
	// location. Leases are expressed as a point in time. An object
	// is guaranteed to be present as long as the watermark of the
	// store does not exceed its lease.
	//
	// The zero time is returned if no lease can be computed for the
	// object, meaning it needs to be rewritten.
	GetSafeSince(location uint64) time.Time

	// GetWatermark returns the point in time at or after which all
	// data written to the store is still present. The watermark
	// never decreases.
	GetWatermark() time.Time

	// GetIncarnation returns the incarnation of the store, which
	// changes whenever the store starts without access to the state
	// needed to compute leases of existing objects. It also returns
	// the value of LeaseTrackingConfiguration.GetLastIncarnationsHash
	// that was persisted alongside the incarnation, if any. Both
	// values are nil if lease tracking is disabled.
	GetIncarnation() (incarnation, lastIncarnationsHash []byte)

	// InvalidateLeases raises the watermark to the current time,
	// thereby invalidating the leases of all objects stored. This
	// method is invoked when it cannot be determined which objects
	// have become inaccessible (e.g., when writing to the
	// reference-location map failed after displacing an existing
	// entry).
	InvalidateLeases()
}
