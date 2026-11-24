package object

import (
	"context"
	"time"

	"bonanza.build/pkg/proto/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type uploaderServer struct {
	uploader         Uploader[GlobalReference, []byte]
	clusterWatermark *Watermark
	getWatermark     func() (time.Time, time.Time)
}

// NewUploaderServer creates a gRPC server object for exposing an
// Uploader. This can be used to make objects contained in an object
// store accessible via the network.
//
// If clusterWatermark is provided, it is raised to the cluster
// watermark that clients provide as part of their requests. If
// getWatermark is provided, it is used to report the watermark of the
// server to clients, together with the current time.
func NewUploaderServer(uploader Uploader[GlobalReference, []byte], clusterWatermark *Watermark, getWatermark func() (watermark, now time.Time)) object.UploaderServer {
	return &uploaderServer{
		uploader:         uploader,
		clusterWatermark: clusterWatermark,
		getWatermark:     getWatermark,
	}
}

func (s *uploaderServer) UploadObject(ctx context.Context, request *object.UploadObjectRequest) (*object.UploadObjectResponse, error) {
	if s.clusterWatermark != nil {
		if err := s.clusterWatermark.RaiseFromProto(request.Watermark); err != nil {
			return nil, util.StatusWrap(err, "Invalid cluster watermark")
		}
	}

	namespace, err := NewNamespace(request.Namespace)
	if err != nil {
		return nil, util.StatusWrap(err, "Invalid namespace")
	}
	reference, err := namespace.NewGlobalReference(request.Reference)
	if err != nil {
		return nil, util.StatusWrap(err, "Invalid reference")
	}
	var contents *Contents
	if len(request.Contents) > 0 {
		contents, err = NewContentsFromFullData(reference.LocalReference, request.Contents)
		if err != nil {
			return nil, util.StatusWrap(err, "Invalid object contents")
		}
	}

	degree := reference.GetDegree()
	if l := len(request.OutgoingReferencesLeases); l != 0 && l != degree {
		return nil, status.Errorf(codes.InvalidArgument, "Client provided %d leases, even though the object has a degree of %d", l, degree)
	}

	if request.WantContentsIfIncomplete {
		if contents != nil {
			return nil, status.Error(codes.InvalidArgument, "Client requested the object's contents, even though it is already in possession of them")
		}
		if degree == 0 {
			return nil, status.Error(codes.InvalidArgument, "Client requested the object's contents, even though the object does not have any outgoing references")
		}
	}

	result, err := s.uploader.UploadObject(
		ctx,
		reference,
		contents,
		request.OutgoingReferencesLeases,
		request.WantContentsIfIncomplete,
	)
	if err != nil {
		return nil, err
	}

	switch resultData := result.(type) {
	case UploadObjectComplete[[]byte]:
		return &object.UploadObjectResponse{
			Type: &object.UploadObjectResponse_Complete_{
				Complete: &object.UploadObjectResponse_Complete{
					Lease: resultData.Lease,
				},
			},
		}, nil
	case UploadObjectIncomplete[[]byte]:
		var contents []byte
		if resultData.Contents != nil {
			contents = resultData.Contents.GetFullData()
		}
		wantLeases := make([]uint32, 0, len(resultData.WantOutgoingReferencesLeases))
		for _, lease := range resultData.WantOutgoingReferencesLeases {
			wantLeases = append(wantLeases, uint32(lease))
		}
		return &object.UploadObjectResponse{
			Type: &object.UploadObjectResponse_Incomplete_{
				Incomplete: &object.UploadObjectResponse_Incomplete{
					Contents:                     contents,
					WantOutgoingReferencesLeases: wantLeases,
				},
			},
		}, nil
	case UploadObjectMissing[[]byte]:
		return nil, status.Error(codes.NotFound, "Object not found")
	default:
		panic("unexpected upload object result")
	}
}

func (s *uploaderServer) GetWatermark(ctx context.Context, request *emptypb.Empty) (*object.GetWatermarkResponse, error) {
	if s.getWatermark == nil {
		return nil, status.Error(codes.Unimplemented, "This server does not report its watermark")
	}
	watermark, obtainedAt := s.getWatermark()
	response := &object.Watermark{
		ObtainedAt: timestamppb.New(obtainedAt),
	}
	if !watermark.IsZero() {
		response.Watermark = timestamppb.New(watermark)
	}
	return &object.GetWatermarkResponse{Watermark: response}, nil
}
