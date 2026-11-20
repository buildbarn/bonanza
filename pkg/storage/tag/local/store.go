package local

import (
	"context"
	"sync"
	"time"

	"bonanza.build/pkg/storage/object"
	object_flatbacked "bonanza.build/pkg/storage/object/flatbacked"
	"bonanza.build/pkg/storage/tag"

	"github.com/buildbarn/bb-storage/pkg/clock"
	"github.com/buildbarn/bb-storage/pkg/lossymap"
)

type store struct {
	clock                clock.Clock
	getWatermark         func() time.Time
	leaseRenewalFraction float64

	lock    sync.RWMutex
	tagsMap lossymap.Map[tag.Key, ValueWithLease, struct{}]
}

// ValueWithLease contains all of the properties of a tag that are
// tracked by the local storage backend.
type ValueWithLease struct {
	SignedValue tag.SignedValue
	Lease       object_flatbacked.Lease
}

// NewStore creates a tag store that is backed by local disks.
//
// Tags are reported as being complete if the lease of the object they
// reference is sufficiently far above the watermark of the cluster,
// which is provided by getWatermark. Tags with leases that are less
// than leaseRenewalFraction of the cluster's retention above the
// watermark are reported as being incomplete, so that the caller
// renews them before they expire.
func NewStore(tagsMap lossymap.Map[tag.Key, ValueWithLease, struct{}], clock clock.Clock, getWatermark func() time.Time, leaseRenewalFraction float64) tag.Store[object.Namespace, object_flatbacked.Lease] {
	return &store{
		clock:                clock,
		getWatermark:         getWatermark,
		leaseRenewalFraction: leaseRenewalFraction,
		tagsMap:              tagsMap,
	}
}

func (s *store) ResolveTag(ctx context.Context, namespace object.Namespace, key tag.Key, minimumTimestamp *time.Time) (tag.SignedValue, bool, error) {
	s.lock.RLock()
	valueWithLease, err := s.tagsMap.Get(key, struct{}{})
	s.lock.RUnlock()
	if err != nil {
		var badSignedValue tag.SignedValue
		return badSignedValue, false, err
	}
	leaseIncompleteCutoff := object_flatbacked.NewLeaseFromTime(
		object_flatbacked.GetLeaseRenewalCutoff(s.getWatermark(), s.clock.Now(), s.leaseRenewalFraction),
	)
	return valueWithLease.SignedValue, valueWithLease.Lease != 0 && valueWithLease.Lease >= leaseIncompleteCutoff, nil
}

func (s *store) UpdateTag(ctx context.Context, namespace object.Namespace, key tag.Key, value tag.SignedValue, lease object_flatbacked.Lease) error {
	// Leases are computed by the storage shard holding the object
	// the tag references, based on its own clock. Prevent clock
	// skew from causing leases to remain valid for longer than
	// intended by capping them to the current time. Watermarks
	// received from other shards are capped in the same way.
	if leaseNow := object_flatbacked.NewLeaseFromTime(s.clock.Now()); lease > leaseNow {
		lease = leaseNow
	}

	s.lock.Lock()
	defer s.lock.Unlock()

	return s.tagsMap.Put(
		key,
		ValueWithLease{
			SignedValue: value,
			Lease:       lease,
		},
		struct{}{},
	)
}
