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
	client tag_pb.ResolverClient
}

// NewResolver creates a tag resolver that forwards all requests to
// resolve tags to a remote server using gRPC.
func NewResolver(client tag_pb.ResolverClient) tag.Resolver[object.Namespace] {
	return &resolver{
		client: client,
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

	response, err := d.client.ResolveTag(ctx, &tag_pb.ResolveTagRequest{
		Namespace:        namespace.ToProto(),
		Key:              keyMessage,
		MinimumTimestamp: minimumTimestampMessage,
	})
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
