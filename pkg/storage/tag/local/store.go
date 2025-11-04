package local

import (
	"context"
	"time"

	"bonanza.build/pkg/storage/object"
	object_flatbacked "bonanza.build/pkg/storage/object/flatbacked"
	"bonanza.build/pkg/storage/tag"
)

type store struct{}

// NewStore creates a tag store that is backed by local disks.
func NewStore() tag.Store[object.Namespace, object_flatbacked.Lease] {
	return &store{}
}

func (store) ResolveTag(ctx context.Context, namespace object.Namespace, key tag.Key, minimumTimestamp *time.Time) (value tag.SignedValue, complete bool, err error) {
	panic("TODO")
}

func (store) UpdateTag(ctx context.Context, namespace object.Namespace, key tag.Key, value tag.SignedValue, lease object_flatbacked.Lease) error {
	panic("TODO")
}
