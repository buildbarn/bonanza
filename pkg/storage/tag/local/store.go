package local

import (
	"context"
	"sync"
	"time"

	"bonanza.build/pkg/ds/lossymap"
	"bonanza.build/pkg/storage/object"
	object_flatbacked "bonanza.build/pkg/storage/object/flatbacked"
	"bonanza.build/pkg/storage/tag"

	"github.com/buildbarn/bb-storage/pkg/clock"
)

type store struct {
	clock                     clock.Clock
	leaseCompletenessDuration object_flatbacked.Lease

	lock    sync.RWMutex
	tagsMap lossymap.Map[tag.Key, ValueWithLease, struct{}]
}

type ValueWithLease struct {
	SignedValue tag.SignedValue
	Lease       object_flatbacked.Lease
}

// NewStore creates a tag store that is backed by local disks.
func NewStore(tagsMap lossymap.Map[tag.Key, ValueWithLease, struct{}], clock clock.Clock, leaseCompletenessDuration time.Duration) tag.Store[object.Namespace, object_flatbacked.Lease] {
	return &store{
		clock:                     clock,
		leaseCompletenessDuration: object_flatbacked.Lease(leaseCompletenessDuration.Nanoseconds()),
		tagsMap:                   tagsMap,
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
	leaseNow := object_flatbacked.Lease(s.clock.Now().UnixNano())
	leaseIncompleteCutoff := leaseNow - s.leaseCompletenessDuration
	return valueWithLease.SignedValue, valueWithLease.Lease >= leaseIncompleteCutoff, nil
}

func (s *store) UpdateTag(ctx context.Context, namespace object.Namespace, key tag.Key, value tag.SignedValue, lease object_flatbacked.Lease) error {
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
