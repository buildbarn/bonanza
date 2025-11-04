package leasemarshaling

import (
	"context"

	"bonanza.build/pkg/storage/object/leasemarshaling"
	"bonanza.build/pkg/storage/tag"

	"github.com/buildbarn/bb-storage/pkg/util"
)

type updater[TNamespace any, TLease any] struct {
	base      tag.Updater[TNamespace, TLease]
	marshaler leasemarshaling.LeaseMarshaler[TLease]
}

// NewUpdater creates a decorator for tag.Updater that converts leases
// in the format of byte slices to the native representation of the
// storage backend. This is typically needed if a storage backend is
// exposed via the network.
func NewUpdater[TNamespace, TLease any](base tag.Updater[TNamespace, TLease], marshaler leasemarshaling.LeaseMarshaler[TLease]) tag.Updater[TNamespace, []byte] {
	return &updater[TNamespace, TLease]{
		base:      base,
		marshaler: marshaler,
	}
}

func (u *updater[TNamespace, TLease]) UpdateTag(ctx context.Context, namespace TNamespace, key tag.Key, value tag.SignedValue, lease []byte) error {
	var unmarshaledLease TLease
	if len(lease) > 0 {
		var err error
		unmarshaledLease, err = u.marshaler.UnmarshalLease(lease)
		if err != nil {
			return util.StatusWrap(err, "Invalid lease")
		}
	}
	return u.base.UpdateTag(ctx, namespace, key, value, unmarshaledLease)
}
