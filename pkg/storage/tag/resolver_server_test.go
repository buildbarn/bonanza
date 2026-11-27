package tag_test

import (
	"context"
	"testing"

	object_pb "bonanza.build/pkg/proto/storage/object"
	tag_pb "bonanza.build/pkg/proto/storage/tag"
	"bonanza.build/pkg/storage/tag"

	"github.com/buildbarn/bb-storage/pkg/testutil"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestResolverServerWithoutClusterWatermark(t *testing.T) {
	// Storage frontends don't provide a cluster watermark. If they
	// receive a request containing a lease validation context, they
	// are likely misconfigured to forward requests to another
	// storage frontend, instead of storage shards. Such requests
	// should be rejected.
	server := tag.NewResolverServer(nil, nil)
	_, err := server.ResolveTag(context.Background(), tag_pb.ResolveTagRequest_builder{
		LeaseValidationContext: &object_pb.LeaseValidationContext{},
	}.Build())
	testutil.RequireEqualStatus(t, status.Error(codes.InvalidArgument, "This server is not a storage shard, and does not accept lease validation contexts"), err)
}
