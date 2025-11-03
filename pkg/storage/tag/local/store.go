package local

import (
	"context"

	"bonanza.build/pkg/storage/object"
	object_flatbacked "bonanza.build/pkg/storage/object/flatbacked"
	"bonanza.build/pkg/storage/tag"

	"google.golang.org/protobuf/types/known/anypb"
)

type store struct{}

// NewStore creates a tag store that is backed by local disks.
func NewStore() tag.Store[object.Namespace, object.GlobalReference, object_flatbacked.Lease] {
	return &store{}
}

func (store) ResolveTag(ctx context.Context, namespace object.Namespace, tag *anypb.Any) (reference object.LocalReference, complete bool, err error) {
	panic("TODO")
}

func (store) UpdateTag(ctx context.Context, tag *anypb.Any, reference object.GlobalReference, lease object_flatbacked.Lease, overwrite bool) error {
	panic("TODO")
}
