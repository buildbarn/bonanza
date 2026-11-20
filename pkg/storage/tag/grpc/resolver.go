package grpc

import (
	"context"
	"time"

	tag_pb "bonanza.build/pkg/proto/storage/tag"
	"bonanza.build/pkg/storage/object"
	"bonanza.build/pkg/storage/tag"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type resolver struct {
	client          tag_pb.ResolverClient
	watermarkSource *object.Watermark
}

// NewResolver creates a tag resolver that forwards all requests to
// resolve tags to a remote server using gRPC.
//
// If watermarkSource is provided, its value is attached to all
// requests. This should only be done by storage frontends, as it allows
// storage shards to check leases of objects against the highest
// watermark of the cluster they are part of.
func NewResolver(client tag_pb.ResolverClient, watermarkSource *object.Watermark) tag.Resolver[object.Namespace] {
	return &resolver{
		client:          client,
		watermarkSource: watermarkSource,
	}
}

func (d *resolver) ResolveTag(ctx context.Context, namespace object.Namespace, key tag.Key, minimumTimestamp *time.Time) (tag.SignedValue, bool, error) {
	keyMessage, err := key.ToProto()
	if err != nil {
		var badSignedValue tag.SignedValue
		return badSignedValue, false, util.StatusWrap(err, "Failed to marshal key")
	}
	var minimumTimestampMessage *timestamppb.Timestamp
	if minimumTimestamp != nil {
		minimumTimestampMessage = timestamppb.New(*minimumTimestamp)
	}

	request := &tag_pb.ResolveTagRequest{
		Namespace:        namespace.ToProto(),
		Key:              keyMessage,
		MinimumTimestamp: minimumTimestampMessage,
	}
	if d.watermarkSource != nil {
		request.Watermark = d.watermarkSource.ToProto()
	}
	response, err := d.client.ResolveTag(ctx, request)
	if err != nil {
		var badSignedValue tag.SignedValue
		return badSignedValue, false, err
	}

	signedValue, err := tag.NewSignedValueFromProto(response.SignedValue, namespace.ReferenceFormat, key)
	if err != nil {
		var badSignedValue tag.SignedValue
		return badSignedValue, false, util.StatusWrapWithCode(err, codes.Internal, "Received invalid signed value from server")
	}
	return signedValue, response.Complete, nil
}
