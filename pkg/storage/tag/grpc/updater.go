package grpc

import (
	"context"

	object_pb "bonanza.build/pkg/proto/storage/object"
	tag_pb "bonanza.build/pkg/proto/storage/tag"
	"bonanza.build/pkg/storage/object"
	"bonanza.build/pkg/storage/tag"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/protobuf/types/known/emptypb"
)

type updater struct {
	client                       tag_pb.UpdaterClient
	leaseValidationContextSource object.LeaseValidationContextSource
}

// NewUpdater creates a tag updater that forwards all requests to update
// tags to a storage shard using gRPC. This is used by storage
// frontends.
//
// Lease validation contexts obtained from leaseValidationContextSource
// are attached to all requests. These allow storage shards to detect
// that leases of tags are invalid.
func NewUpdater(client tag_pb.UpdaterClient, leaseValidationContextSource object.LeaseValidationContextSource) tag.Updater[object.Namespace, []byte] {
	return &updater{
		client:                       client,
		leaseValidationContextSource: leaseValidationContextSource,
	}
}

func (d *updater) UpdateTag(ctx context.Context, namespace object.Namespace, key tag.Key, value tag.SignedValue, lease []byte) error {
	keyMessage, err := key.ToProto()
	if err != nil {
		return util.StatusWrap(err, "Failed to marshal key")
	}

	request := tag_pb.UpdateTagRequest_builder{
		Namespace:   namespace.ToProto(),
		Key:         keyMessage,
		SignedValue: value.ToProto(),
		Lease:       lease,
	}.Build()
	_, err = object.CallWithLeaseValidationContext(
		ctx,
		d.leaseValidationContextSource,
		func(leaseValidationContext *object_pb.LeaseValidationContext) (*emptypb.Empty, error) {
			request.SetLeaseValidationContext(leaseValidationContext)
			return d.client.UpdateTag(ctx, request)
		},
	)
	return err
}
