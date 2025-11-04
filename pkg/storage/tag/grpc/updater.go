package grpc

import (
	"context"

	tag_pb "bonanza.build/pkg/proto/storage/tag"
	"bonanza.build/pkg/storage/object"
	"bonanza.build/pkg/storage/tag"

	"github.com/buildbarn/bb-storage/pkg/util"
)

type updater struct {
	client tag_pb.UpdaterClient
}

// NewUpdater creates a tag updater that forwards all requests to update
// tags to a remote server using gRPC.
func NewUpdater(client tag_pb.UpdaterClient) tag.Updater[object.Namespace, []byte] {
	return &updater{
		client: client,
	}
}

func (d *updater) UpdateTag(ctx context.Context, namespace object.Namespace, key tag.Key, value tag.SignedValue, lease []byte) error {
	keyMessage, err := key.ToProto()
	if err != nil {
		return util.StatusWrap(err, "Failed to marshal key")
	}

	_, err = d.client.UpdateTag(ctx, &tag_pb.UpdateTagRequest{
		Namespace:   namespace.ToProto(),
		Key:         keyMessage,
		SignedValue: value.ToProto(),
		Lease:       lease,
	})
	return err
}
