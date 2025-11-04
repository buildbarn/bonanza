package sharded

import (
	"context"

	"bonanza.build/pkg/storage/object/sharded"
	"bonanza.build/pkg/storage/tag"

	"github.com/buildbarn/bb-storage/pkg/util"
)

type updater[TNamespace any, TLease any] struct {
	shards     []tag.Updater[TNamespace, TLease]
	shardNames []string
	picker     sharded.Picker
}

// NewUpdater creates a decorator for one or more tag.Updaters that
// spreads out incoming requests based on the provided reference.
func NewUpdater[TNamespace, TLease any](shards []tag.Updater[TNamespace, TLease], shardNames []string, picker sharded.Picker) tag.Updater[TNamespace, TLease] {
	return &updater[TNamespace, TLease]{
		shards:     shards,
		shardNames: shardNames,
		picker:     picker,
	}
}

func (u *updater[TNamespace, TLease]) UpdateTag(ctx context.Context, namespace TNamespace, key tag.Key, value tag.SignedValue, lease TLease) error {
	shardIndex, err := getShardIndex(key, u.picker)
	if err != nil {
		return err
	}

	if err := u.shards[shardIndex].UpdateTag(ctx, namespace, key, value, lease); err != nil {
		return util.StatusWrapf(err, "Shard %#v", u.shardNames[shardIndex])
	}
	return nil
}
