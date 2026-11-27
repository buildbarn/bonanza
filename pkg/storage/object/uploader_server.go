package object

import (
	"context"
	"time"

	"bonanza.build/pkg/proto/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type uploaderServer struct {
	uploader         Uploader[GlobalReference, []byte]
	clusterWatermark *Watermark
	getWatermark     func() (time.Time, time.Time)
}

// NewUploaderServer creates a gRPC server object for exposing an
// Uploader. This is used by storage shards to make objects contained
// in their object store accessible to storage frontends.
//
// Requests are required to contain a lease validation context, which
// is processed by clusterWatermark. getWatermark is used to report the
// watermark of the storage shard to storage frontends, together with
// the current time.
func NewUploaderServer(uploader Uploader[GlobalReference, []byte], clusterWatermark *Watermark, getWatermark func() (watermark, now time.Time)) object.UploaderServer {
	return &uploaderServer{
		uploader:         uploader,
		clusterWatermark: clusterWatermark,
		getWatermark:     getWatermark,
	}
}

func (s *uploaderServer) UploadObject(ctx context.Context, request *object.UploadObjectRequest) (*object.UploadObjectResponse, error) {
	if err := s.clusterWatermark.ProcessLeaseValidationContext(request.GetLeaseValidationContext()); err != nil {
		return nil, util.StatusWrap(err, "Invalid lease validation context")
	}

	namespace, err := NewNamespace(request.GetNamespace())
	if err != nil {
		return nil, util.StatusWrap(err, "Invalid namespace")
	}
	reference, err := namespace.NewGlobalReference(request.GetReference())
	if err != nil {
		return nil, util.StatusWrap(err, "Invalid reference")
	}
	var contents *Contents
	if data := request.GetContents(); len(data) > 0 {
		contents, err = NewContentsFromFullData(reference.LocalReference, data)
		if err != nil {
			return nil, util.StatusWrap(err, "Invalid object contents")
		}
	}

	degree := reference.GetDegree()
	if l := len(request.GetOutgoingReferencesLeases()); l != 0 && l != degree {
		return nil, status.Errorf(codes.InvalidArgument, "Client provided %d leases, even though the object has a degree of %d", l, degree)
	}

	if request.GetWantContentsIfIncomplete() {
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
		request.GetOutgoingReferencesLeases(),
		request.GetWantContentsIfIncomplete(),
	)
	if err != nil {
		return nil, err
	}

	switch resultData := result.(type) {
	case UploadObjectComplete[[]byte]:
		uc := &object.UploadObjectResponse_Complete{}
		uc.SetLease(resultData.Lease)
		uor := &object.UploadObjectResponse{}
		uor.SetComplete(proto.ValueOrDefault(uc))
		return uor, nil
	case UploadObjectIncomplete[[]byte]:
		var contents []byte
		if resultData.Contents != nil {
			contents = resultData.Contents.GetFullData()
		}
		wantLeases := make([]uint32, 0, len(resultData.WantOutgoingReferencesLeases))
		for _, lease := range resultData.WantOutgoingReferencesLeases {
			wantLeases = append(wantLeases, uint32(lease))
		}
		ui := &object.UploadObjectResponse_Incomplete{}
		ui.SetContents(contents)
		ui.SetWantOutgoingReferencesLeases(wantLeases)
		uor := &object.UploadObjectResponse{}
		uor.SetIncomplete(proto.ValueOrDefault(ui))
		return uor, nil
	case UploadObjectMissing[[]byte]:
		return nil, status.Error(codes.NotFound, "Object not found")
	default:
		panic("unexpected upload object result")
	}
}

func (s *uploaderServer) GetWatermark(ctx context.Context, request *emptypb.Empty) (*object.GetWatermarkResponse, error) {
	watermark, obtainedAt := s.getWatermark()
	response := object.Watermark_builder{
		ObtainedAt: timestamppb.New(obtainedAt),
	}.Build()
	if !watermark.IsZero() {
		response.SetWatermark(timestamppb.New(watermark))
	}
	return object.GetWatermarkResponse_builder{
		Watermark:   response,
		Incarnation: s.clusterWatermark.GetIncarnation(),
	}.Build(), nil
}
