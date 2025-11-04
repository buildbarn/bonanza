package sharded

import (
	"context"
	"time"

	"bonanza.build/pkg/storage/object/sharded"
	"bonanza.build/pkg/storage/tag"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

type resolver[TNamespace any] struct {
	shards     []tag.Resolver[TNamespace]
	shardNames []string
	picker     sharded.Picker
}

// NewResolver creates a decorator for one or more tag.Resolvers that
// spreads out incoming requests based on the provided key.
func NewResolver[TNamespace any](shards []tag.Resolver[TNamespace], shardNames []string, picker sharded.Picker) tag.Resolver[TNamespace] {
	return &resolver[TNamespace]{
		shards:     shards,
		shardNames: shardNames,
		picker:     picker,
	}
}

func getShardIndex(key tag.Key, picker sharded.Picker) (int, error) {
	keyBytes, err := key.ToProto()
	if err != nil {
		return 0, util.StatusWrap(err, "Failed to convert key to message")
	}
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(keyBytes)
	if err != nil {
		return 0, util.StatusWrapWithCode(err, codes.InvalidArgument, "Failed to marshal tag")
	}
	return picker.PickShard(data), nil
}

func (r *resolver[TNamespace]) ResolveTag(ctx context.Context, namespace TNamespace, key tag.Key, minimumTimestamp *time.Time) (tag.SignedValue, bool, error) {
	shardIndex, err := getShardIndex(key, r.picker)
	if err != nil {
		var badSignedValue tag.SignedValue
		return badSignedValue, false, err
	}

	signedValue, complete, err := r.shards[shardIndex].ResolveTag(ctx, namespace, key, minimumTimestamp)
	if err != nil {
		var badSignedValue tag.SignedValue
		return badSignedValue, false, util.StatusWrapf(err, "Shard %#v", r.shardNames[shardIndex])
	}
	return signedValue, complete, nil
}
