package mirrored

import (
	"context"

	"bonanza.build/pkg/storage/object/mirrored"
	"bonanza.build/pkg/storage/tag"

	"github.com/buildbarn/bb-storage/pkg/util"

	"golang.org/x/sync/errgroup"
)

type updater[TNamespace any, TLeaseA, TLeaseB any] struct {
	replicaA tag.Updater[TNamespace, TLeaseA]
	replicaB tag.Updater[TNamespace, TLeaseB]
}

// NewUpdater creates a decorator for tag.Updater that forwards requests
// to update tags to a pair of backends that are configured to mirror
// each other's contents.
func NewUpdater[TNamespace, TLeaseA, TLeaseB any](replicaA tag.Updater[TNamespace, TLeaseA], replicaB tag.Updater[TNamespace, TLeaseB]) tag.Updater[TNamespace, mirrored.Lease[TLeaseA, TLeaseB]] {
	return &updater[TNamespace, TLeaseA, TLeaseB]{
		replicaA: replicaA,
		replicaB: replicaB,
	}
}

func (u *updater[TNamespace, TLeaseA, TLeaseB]) UpdateTag(ctx context.Context, namespace TNamespace, key tag.Key, value tag.SignedValue, lease mirrored.Lease[TLeaseA, TLeaseB]) error {
	// Forward the request to both replicas in parallel.
	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error {
		if err := u.replicaA.UpdateTag(groupCtx, namespace, key, value, lease.LeaseA); err != nil {
			return util.StatusWrap(err, "Replica A")
		}
		return nil
	})
	group.Go(func() error {
		if err := u.replicaB.UpdateTag(groupCtx, namespace, key, value, lease.LeaseB); err != nil {
			return util.StatusWrap(err, "Replica B")
		}
		return nil
	})
	return group.Wait()
}
