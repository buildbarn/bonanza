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
	client                       object_pb.UploaderClient
	leaseValidationContextSource object.LeaseValidationContextSource
}

// NewUploader creates an object uploader that forwards requests to
// store objects to a storage shard using gRPC. This is used by storage
// frontends.
//
// Lease validation contexts obtained from leaseValidationContextSource
// are attached to all requests. These allow storage shards to validate
// leases of objects against the watermark of the cluster they are part
// of.
func NewUploader(client object_pb.UploaderClient, leaseValidationContextSource object.LeaseValidationContextSource) object.Uploader[object.GlobalReference, []byte] {
	return &uploader{
		client:                       client,
		leaseValidationContextSource: leaseValidationContextSource,
	}
}

func (u *uploader) UploadObject(ctx context.Context, reference object.GlobalReference, contents *object.Contents, childrenLeases [][]byte, wantContentsIfIncomplete bool) (object.UploadObjectResult[[]byte], error) {
	request := object_pb.UploadObjectRequest_builder{
		Namespace: reference.GetNamespace().ToProto(),
		Reference: reference.GetRawReference(),
	}.Build()
	// TODO: Issue multiple gRPC calls if the combined size
	// of all leases exceeds gRPC's maximum message size.
	request.SetOutgoingReferencesLeases(childrenLeases)
	request.SetWantContentsIfIncomplete(wantContentsIfIncomplete)
	if contents != nil {
		if x := contents.GetFullData(); x != nil {
			request.SetContents(x)
		} else {
			request.SetContents(nil)
		}
	}

	response, err := object.CallWithLeaseValidationContext(
		ctx,
		u.leaseValidationContextSource,
		func(leaseValidationContext *object_pb.LeaseValidationContext) (*object_pb.UploadObjectResponse, error) {
			request.SetLeaseValidationContext(leaseValidationContext)
			return u.client.UploadObject(ctx, request)
		},
	)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			if contents != nil {
				return nil, util.StatusWrapWithCode(err, codes.Internal, "Server reported the object as missing, even though contents were provided")
			}
			return object.UploadObjectMissing[[]byte]{}, nil
		}
		return nil, err
	}

	switch response.WhichType() {
	case object_pb.UploadObjectResponse_Complete_case:
		lease := response.GetComplete().GetLease()
		if len(lease) == 0 {
			return nil, status.Error(codes.Internal, "Server returned an empty lease for a complete object")
		}
		return object.UploadObjectComplete[[]byte]{
			Lease: lease,
		}, nil
	case object_pb.UploadObjectResponse_Incomplete_case:
		var result object.UploadObjectIncomplete[[]byte]
		if contents := response.GetIncomplete().GetContents(); wantContentsIfIncomplete {
			result.Contents, err = object.NewContentsFromFullData(reference.LocalReference, contents)
			if err != nil {
				return nil, util.StatusWrapWithCode(err, codes.Internal, "Server returned invalid object contents")
			}
		} else {
			if contents != nil {
				return nil, status.Error(codes.Internal, "Server returned object contents, even though the client did not requested them")
			}
		}

		wantLeases := response.GetIncomplete().GetWantOutgoingReferencesLeases()
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
