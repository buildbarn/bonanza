package tag

import (
	"context"

	"bonanza.build/pkg/proto/storage/tag"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/protobuf/types/known/emptypb"
)

type updaterServer struct {
	updater          Updater[object.Namespace, []byte]
	clusterWatermark *object.Watermark
}

// NewUpdaterServer creates a gRPC server that is capable of creating or
// updating existing tags contained in the tag store. This is used by
// storage shards to accept updates from storage frontends.
//
// Requests are required to contain a lease validation context, which
// is processed by clusterWatermark.
func NewUpdaterServer(updater Updater[object.Namespace, []byte], clusterWatermark *object.Watermark) tag.UpdaterServer {
	return &updaterServer{
		updater:          updater,
		clusterWatermark: clusterWatermark,
	}
}

func (s *updaterServer) UpdateTag(ctx context.Context, request *tag.UpdateTagRequest) (*emptypb.Empty, error) {
	if err := s.clusterWatermark.ProcessLeaseValidationContext(request.LeaseValidationContext); err != nil {
		return nil, util.StatusWrap(err, "Invalid lease validation context")
	}

	namespace, err := object.NewNamespace(request.Namespace)
	if err != nil {
		return nil, util.StatusWrap(err, "Invalid namespace")
	}

	key, err := NewKeyFromProto(request.Key)
	if err != nil {
		return nil, util.StatusWrap(err, "Invalid key")
	}

	signedValue, err := NewSignedValueFromProto(request.SignedValue, namespace.ReferenceFormat, key)
	if err != nil {
		return nil, util.StatusWrap(err, "Invalid signed value")
	}

	if err := s.updater.UpdateTag(ctx, namespace, key, signedValue, request.Lease); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}
