package sharded

import (
	"context"

	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"
)

type uploader[TReference object.BasicReference, TLease any] struct {
	shards     []object.Uploader[TReference, TLease]
	shardNames []string
	picker     Picker
}

// NewUploader creates a decorator for one or more object.Uploaders that
// spreads out incoming requests based on the provided reference.
func NewUploader[TReference object.BasicReference, TLease any](shards []object.Uploader[TReference, TLease], shardNames []string, picker Picker) object.Uploader[TReference, TLease] {
	return &uploader[TReference, TLease]{
		shards:     shards,
		shardNames: shardNames,
		picker:     picker,
	}
}

func (u *uploader[TReference, TLease]) UploadObject(ctx context.Context, reference TReference, contents *object.Contents, childrenLeases []TLease, wantContentsIfIncomplete bool) (object.UploadObjectResult[TLease], error) {
	shardIndex := u.picker.PickShard(reference.GetRawReference())
	result, err := u.shards[shardIndex].UploadObject(ctx, reference, contents, childrenLeases, wantContentsIfIncomplete)
	if err != nil {
		return nil, util.StatusWrapf(err, "Shard %#v", u.shardNames[shardIndex])
	}
	return result, nil
}
