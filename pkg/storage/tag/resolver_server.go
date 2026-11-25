package tag

import (
	"context"
	"time"

	tag_pb "bonanza.build/pkg/proto/storage/tag"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type resolverServer struct {
	resolver         Resolver[object.Namespace]
	clusterWatermark *object.Watermark
}

// NewResolverServer creates a gRPC server that is capable of resolving
// tags to an object.
//
// Storage shards provide a clusterWatermark, in which case requests are
// required to contain a lease validation context, which is processed by
// it. Storage frontends leave clusterWatermark unset, in which case
// requests containing a lease validation context are rejected. Such
// requests indicate that the storage frontend is misconfigured to
// forward requests to another storage frontend, instead of storage
// shards.
func NewResolverServer(resolver Resolver[object.Namespace], clusterWatermark *object.Watermark) tag_pb.ResolverServer {
	return &resolverServer{
		resolver:         resolver,
		clusterWatermark: clusterWatermark,
	}
}

func (s *resolverServer) ResolveTag(ctx context.Context, request *tag_pb.ResolveTagRequest) (*tag_pb.ResolveTagResponse, error) {
	if s.clusterWatermark != nil {
		if err := s.clusterWatermark.ProcessLeaseValidationContext(request.LeaseValidationContext); err != nil {
			return nil, util.StatusWrap(err, "Invalid lease validation context")
		}
	} else if request.LeaseValidationContext != nil {
		return nil, status.Error(codes.InvalidArgument, "This server is not a storage shard, and does not accept lease validation contexts")
	}

	namespace, err := object.NewNamespace(request.Namespace)
	if err != nil {
		return nil, util.StatusWrap(err, "Invalid namespace")
	}

	key, err := NewKeyFromProto(request.Key)
	if err != nil {
		return nil, err
	}

	var minimumTimestamp *time.Time
	if ts := request.MinimumTimestamp; ts != nil {
		if err := ts.CheckValid(); err != nil {
			return nil, util.StatusWrapWithCode(err, codes.InvalidArgument, "Invalid minimum timestamp")
		}
		v := ts.AsTime()
		minimumTimestamp = &v
	}

	signedValue, complete, err := s.resolver.ResolveTag(ctx, namespace, key, minimumTimestamp)
	if err != nil {
		return nil, err
	}
	return &tag_pb.ResolveTagResponse{
		SignedValue: signedValue.ToProto(),
		/*
				Value: &tag_pb.Value{
					Reference: signedValue.Reference.GetRawReference(),
					Timestamp: timestamppb.New(signedValue.Timestamp),
				},
				Signature: signedValue.Signature,
			},
		*/
		Complete: complete,
	}, nil
}
