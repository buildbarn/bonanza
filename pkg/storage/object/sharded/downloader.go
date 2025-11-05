package sharded

import (
	"context"

	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"
)

type dowloader[TReference object.BasicReference] struct {
	shards     []object.Downloader[TReference]
	shardNames []string
	picker     Picker
}

// NewDownloader creates a decorator for one or more object.Downloaders
// that spreads out incoming requests based on the provided reference.
func NewDownloader[TReference object.BasicReference](shards []object.Downloader[TReference], shardNames []string, picker Picker) object.Downloader[TReference] {
	return &dowloader[TReference]{
		shards:     shards,
		shardNames: shardNames,
		picker:     picker,
	}
}

func (d *dowloader[TReference]) DownloadObject(ctx context.Context, reference TReference) (*object.Contents, error) {
	shardIndex := d.picker.PickShard(reference.GetRawReference())
	contents, err := d.shards[shardIndex].DownloadObject(ctx, reference)
	if err != nil {
		return nil, util.StatusWrapf(err, "Shard %#v", d.shardNames[shardIndex])
	}
	return contents, nil
}
