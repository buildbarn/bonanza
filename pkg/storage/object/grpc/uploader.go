package grpc

import (
	"context"

	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type uploader struct {
	client object_pb.UploaderClient
}

// NewUploader creates an object uploader that forwards requests to
// store objects to a remote server using gRPC.
func NewUploader(client object_pb.UploaderClient) object.Uploader[object.GlobalReference, []byte] {
	return &uploader{
		client: client,
	}
}

func (u *uploader) UploadObject(ctx context.Context, reference object.GlobalReference, contents *object.Contents, childrenLeases [][]byte, wantContentsIfIncomplete bool) (object.UploadObjectResult[[]byte], error) {
	request := &object_pb.UploadObjectRequest{
		Namespace: reference.GetNamespace().ToProto(),
		Reference: reference.GetRawReference(),
		// TODO: Issue multiple gRPC calls if the combined size
		// of all leases exceeds gRPC's maximum message size.
		OutgoingReferencesLeases: childrenLeases,
		WantContentsIfIncomplete: wantContentsIfIncomplete,
	}
	if contents != nil {
		request.Contents = contents.GetFullData()
	}

	response, err := u.client.UploadObject(ctx, request)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			if contents != nil {
				return nil, util.StatusWrapWithCode(err, codes.Internal, "Server reported the object as missing, even though contents were provided")
			}
			return object.UploadObjectMissing[[]byte]{}, nil
		}
		return nil, err
	}

	switch responseType := response.Type.(type) {
	case *object_pb.UploadObjectResponse_Complete_:
		lease := responseType.Complete.Lease
		if len(lease) == 0 {
			return nil, status.Error(codes.Internal, "Server returned an empty lease for a complete object")
		}
		return object.UploadObjectComplete[[]byte]{
			Lease: lease,
		}, nil
	case *object_pb.UploadObjectResponse_Incomplete_:
		var result object.UploadObjectIncomplete[[]byte]
		if contents := responseType.Incomplete.Contents; wantContentsIfIncomplete {
			result.Contents, err = object.NewContentsFromFullData(reference.LocalReference, contents)
			if err != nil {
				return nil, util.StatusWrapWithCode(err, codes.Internal, "Server returned invalid object contents")
			}
		} else {
			if contents != nil {
				return nil, status.Error(codes.Internal, "Server returned object contents, even though the client did not requested them")
			}
		}

		wantLeases := responseType.Incomplete.WantOutgoingReferencesLeases
		if len(wantLeases) == 0 {
			return nil, status.Error(codes.Internal, "Server returned an empty list of outgoing references for which leases are expired")
		}
		for i := 1; i < len(wantLeases); i++ {
			if wantLeases[i-1] >= wantLeases[i] {
				return nil, status.Error(codes.Internal, "Server returned list of outgoing references for which leases are expired that is not properly sorted")
			}
		}
		if degree := uint32(reference.GetDegree()); wantLeases[len(wantLeases)-1] >= degree {
			return nil, status.Error(codes.Internal, "List of outgoing references for which leases are expired contains indices that exceed the object's degree")
		}
		result.WantOutgoingReferencesLeases = make([]int, 0, len(wantLeases))
		for _, lease := range wantLeases {
			result.WantOutgoingReferencesLeases = append(result.WantOutgoingReferencesLeases, int(lease))
		}

		return result, nil
	default:
		return nil, status.Error(codes.Internal, "Server returned a response of an unknown type")
	}
}
