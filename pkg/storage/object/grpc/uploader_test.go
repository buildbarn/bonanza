package grpc_test

import (
	"context"
	"testing"

	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/object"
	"bonanza.build/pkg/storage/object/grpc"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go.uber.org/mock/gomock"
)

func TestUploader(t *testing.T) {
	ctrl, ctx := gomock.WithContext(context.Background(), t)

	client := NewMockUploaderClient(ctrl)
	uploader := grpc.NewUploader(client)

	t.Run("ServerFailure", func(t *testing.T) {
		// Errors returned by the server should be propagated to
		// the caller.
		client.EXPECT().UploadObject(ctx, testutil.EqProto(t, &object_pb.UploadObjectRequest{
			Namespace: &object_pb.Namespace{
				InstanceName:    "hello/world",
				ReferenceFormat: object_pb.ReferenceFormat_SHA256_V1,
			},
			Reference: []byte{
				// SHA-256 hash.
				0xcc, 0x18, 0xf9, 0xe7, 0x1e, 0xc1, 0x30, 0x1a,
				0xba, 0xe9, 0x93, 0x8f, 0xfa, 0xd6, 0x84, 0xad,
				0x09, 0xab, 0xb1, 0x48, 0xc9, 0x31, 0xcc, 0x03,
				0xe4, 0x35, 0xf0, 0x10, 0xc8, 0xfb, 0xf9, 0xcf,
				// Size in bytes.
				0x2d, 0x00, 0x00,
				// Height.
				0x0d,
				// Degree.
				0x01, 0x00,
				// Maximum parents total size in bytes.
				0x19, 0x50,
			},
			Contents: []byte{
				// SHA-256 hash.
				0x28, 0x65, 0x92, 0xc3, 0xb9, 0x95, 0x8a, 0x49,
				0x4b, 0xb2, 0x92, 0xf5, 0xd9, 0xdc, 0x29, 0x24,
				0x67, 0x1a, 0x64, 0xce, 0x8f, 0x40, 0x9e, 0x41,
				0x4f, 0xdd, 0x45, 0xee, 0xa1, 0x67, 0xc9, 0x8e,
				// Size in bytes.
				0x78, 0x05, 0x00,
				// Height.
				0x0c,
				// Degree.
				0x14, 0x00,
				// Maximum parents total size in bytes.
				0x16, 0x50,

				// Payload.
				0x48, 0x65, 0x6c, 0x6c, 0x6f,
			},
			OutgoingReferencesLeases: [][]byte{
				{1, 2, 3},
			},
		})).Return(nil, status.Error(codes.Unavailable, "Server offline"))

		contents := object.MustNewContents(
			object_pb.ReferenceFormat_SHA256_V1,
			object.OutgoingReferencesList[object.LocalReference]{
				object.MustNewSHA256V1LocalReference("286592c3b9958a494bb292f5d9dc2924671a64ce8f409e414fdd45eea167c98e", 1400, 12, 20, 1059483),
			},
			[]byte("Hello"),
		)
		_, err := uploader.UploadObject(
			ctx,
			object.NewInstanceName("hello/world").WithLocalReference(contents.GetLocalReference()),
			contents,
			[][]byte{
				{1, 2, 3},
			},
			/* wantContentsIfIncomplete = */ false,
		)
		testutil.RequireEqualStatus(t, status.Error(codes.Unavailable, "Server offline"), err)
	})

	t.Run("NotFound", func(t *testing.T) {
		// NOT_FOUND errors returned by the server should be
		// translated to an UploadObjectResult of type
		// UploadObjectMissing.
		client.EXPECT().UploadObject(ctx, testutil.EqProto(t, &object_pb.UploadObjectRequest{
			Namespace: &object_pb.Namespace{
				InstanceName:    "hello/world",
				ReferenceFormat: object_pb.ReferenceFormat_SHA256_V1,
			},
			Reference: []byte{
				// SHA-256 hash.
				0xcb, 0x04, 0x79, 0xff, 0x6f, 0xba, 0x85, 0xfc,
				0x7d, 0xf3, 0x1a, 0x82, 0x32, 0xd8, 0x49, 0xe0,
				0x00, 0x4e, 0x40, 0xcc, 0x7d, 0x6a, 0x3b, 0x94,
				0xc1, 0xc4, 0xdd, 0xc4, 0x64, 0x77, 0x1d, 0xf8,
				// Size in bytes.
				0xe5, 0x07, 0x09,
				// Height.
				0x05,
				// Degree.
				0xf4, 0x01,
				// Maximum parents total size in bytes.
				0x69, 0x47,
			},
			WantContentsIfIncomplete: true,
		})).Return(nil, status.Error(codes.NotFound, "Object does not exist"))

		result, err := uploader.UploadObject(
			ctx,
			object.MustNewSHA256V1GlobalReference("hello/world", "cb0479ff6fba85fc7df31a8232d849e0004e40cc7d6a3b94c1c4ddc464771df8", 591845, 5, 500, 504858),
			/* contents = */ nil,
			/* outgoingReferencesLeases = */ nil,
			/* wantContentsIfIncomplete = */ true,
		)
		require.NoError(t, err)
		require.Equal(t, object.UploadObjectMissing[[]byte]{}, result)
	})

	t.Run("UnexpectedNotFound", func(t *testing.T) {
		// If the client explicitly provided the contents of an
		// object, the call to UploadObject() is not permitted
		// to return UploadObjectMissing.
		client.EXPECT().UploadObject(ctx, testutil.EqProto(t, &object_pb.UploadObjectRequest{
			Namespace: &object_pb.Namespace{
				InstanceName:    "hello/world",
				ReferenceFormat: object_pb.ReferenceFormat_SHA256_V1,
			},
			Reference: []byte{
				// SHA-256 hash.
				0x18, 0x5f, 0x8d, 0xb3, 0x22, 0x71, 0xfe, 0x25,
				0xf5, 0x61, 0xa6, 0xfc, 0x93, 0x8b, 0x2e, 0x26,
				0x43, 0x06, 0xec, 0x30, 0x4e, 0xda, 0x51, 0x80,
				0x07, 0xd1, 0x76, 0x48, 0x26, 0x38, 0x19, 0x69,
				// Size in bytes.
				0x05, 0x00, 0x00,
				// Height.
				0x00,
				// Degree.
				0x00, 0x00,
				// Maximum parents total size in bytes.
				0x00, 0x00,
			},
			Contents: []byte("Hello"),
		})).Return(nil, status.Error(codes.NotFound, "Object does not exist"))

		contents := object.MustNewContents(
			object_pb.ReferenceFormat_SHA256_V1,
			/* outgoingReferences = */ nil,
			[]byte("Hello"),
		)
		_, err := uploader.UploadObject(
			ctx,
			object.NewInstanceName("hello/world").WithLocalReference(contents.GetLocalReference()),
			contents,
			/* outgoingReferencesLeases = */ nil,
			/* wantContentsIfIncomplete = */ false,
		)
		testutil.RequireEqualStatus(t, status.Error(codes.Internal, "Server reported the object as missing, even though contents were provided: Object does not exist"), err)
	})

	t.Run("MissingLease", func(t *testing.T) {
		// Default initialized instances of TLease are used to
		// indicate an absence of a lease. This means that
		// UploadObject() MUST NOT return an empty byte array.
		client.EXPECT().UploadObject(ctx, testutil.EqProto(t, &object_pb.UploadObjectRequest{
			Namespace: &object_pb.Namespace{
				InstanceName:    "hello/world",
				ReferenceFormat: object_pb.ReferenceFormat_SHA256_V1,
			},
			Reference: []byte{
				// SHA-256 hash.
				0xe7, 0xbc, 0x8c, 0x11, 0x9f, 0x96, 0xf2, 0x44,
				0x8c, 0xee, 0xf8, 0xb1, 0xc2, 0x9f, 0x62, 0x16,
				0x02, 0x0a, 0xdb, 0x5a, 0x05, 0x1d, 0xfd, 0x0a,
				0x9f, 0xf1, 0x8e, 0xc8, 0x52, 0x85, 0x01, 0x8b,
				// Size in bytes.
				0x87, 0x54, 0x00,
				// Height.
				0x00,
				// Degree.
				0x00, 0x00,
				// Maximum parents total size in bytes.
				0x00, 0x00,
			},
		})).Return(&object_pb.UploadObjectResponse{
			Type: &object_pb.UploadObjectResponse_Complete_{
				Complete: &object_pb.UploadObjectResponse_Complete{},
			},
		}, nil)

		_, err := uploader.UploadObject(
			ctx,
			object.MustNewSHA256V1GlobalReference("hello/world", "e7bc8c119f96f2448ceef8b1c29f6216020adb5a051dfd0a9ff18ec85285018b", 21639, 0, 0, 0),
			/* contents = */ nil,
			/* outgoingReferencesLeases = */ nil,
			/* wantContentsIfIncomplete = */ false,
		)
		testutil.RequireEqualStatus(t, status.Error(codes.Internal, "Server returned an empty lease for a complete object"), err)
	})

	t.Run("SuccessComplete", func(t *testing.T) {
		client.EXPECT().UploadObject(ctx, testutil.EqProto(t, &object_pb.UploadObjectRequest{
			Namespace: &object_pb.Namespace{
				InstanceName:    "hello/world",
				ReferenceFormat: object_pb.ReferenceFormat_SHA256_V1,
			},
			Reference: []byte{
				// SHA-256 hash.
				0xf0, 0x96, 0x13, 0x84, 0x9b, 0x0b, 0xff, 0xb5,
				0x68, 0x83, 0xbc, 0x5b, 0x65, 0xe1, 0xaa, 0x49,
				0xd3, 0xaa, 0x58, 0x6e, 0x90, 0x31, 0x65, 0x8e,
				0x63, 0xbd, 0x81, 0x8e, 0xf5, 0x30, 0x8a, 0x87,
				// Size in bytes.
				0x87, 0x54, 0x00,
				// Height.
				0x00,
				// Degree.
				0x00, 0x00,
				// Maximum parents total size in bytes.
				0x00, 0x00,
			},
		})).Return(&object_pb.UploadObjectResponse{
			Type: &object_pb.UploadObjectResponse_Complete_{
				Complete: &object_pb.UploadObjectResponse_Complete{
					Lease: []byte{1, 2, 3},
				},
			},
		}, nil)

		result, err := uploader.UploadObject(
			ctx,
			object.MustNewSHA256V1GlobalReference("hello/world", "f09613849b0bffb56883bc5b65e1aa49d3aa586e9031658e63bd818ef5308a87", 21639, 0, 0, 0),
			/* contents = */ nil,
			/* outgoingReferencesLeases = */ nil,
			/* wantContentsIfIncomplete = */ false,
		)
		require.NoError(t, err)
		require.Equal(t, object.UploadObjectComplete[[]byte]{
			Lease: []byte{1, 2, 3},
		}, result)
	})

	t.Run("MissingObjectContents", func(t *testing.T) {
		// The server MUST respect the want_contents_if_incomplete
		// option. If set to true, the server MUST return the
		// object's contents as part of a response of type
		// 'incomplete'.
		client.EXPECT().UploadObject(ctx, testutil.EqProto(t, &object_pb.UploadObjectRequest{
			Namespace: &object_pb.Namespace{
				InstanceName:    "hello/world",
				ReferenceFormat: object_pb.ReferenceFormat_SHA256_V1,
			},
			Reference: []byte{
				// SHA-256 hash.
				0x66, 0x47, 0x06, 0xbb, 0xec, 0xb6, 0x3f, 0x5e,
				0x9d, 0x61, 0x36, 0x6b, 0x73, 0x84, 0x0e, 0x2d,
				0xe2, 0x6a, 0xd5, 0xb3, 0xe2, 0x0e, 0x61, 0x50,
				0x6a, 0x42, 0xb7, 0x14, 0x1d, 0x1a, 0xfb, 0xe2,
				// Size in bytes.
				0x8e, 0xe7, 0x00,
				// Height.
				0x03,
				// Degree.
				0x0c, 0x00,
				// Maximum parents total size in bytes.
				0x7a, 0x4e,
			},
			WantContentsIfIncomplete: true,
		})).Return(&object_pb.UploadObjectResponse{
			Type: &object_pb.UploadObjectResponse_Incomplete_{
				Incomplete: &object_pb.UploadObjectResponse_Incomplete{
					WantOutgoingReferencesLeases: []uint32{7},
				},
			},
		}, nil)

		_, err := uploader.UploadObject(
			ctx,
			object.MustNewSHA256V1GlobalReference("hello/world", "664706bbecb63f5e9d61366b73840e2de26ad5b3e20e61506a42b7141d1afbe2", 59278, 3, 12, 948573),
			/* contents = */ nil,
			/* outgoingReferencesLeases = */ nil,
			/* wantContentsIfIncomplete = */ true,
		)
		testutil.RequireEqualStatus(t, status.Error(codes.Internal, "Server returned invalid object contents: Data is 0 bytes in size, while 59278 bytes were expected"), err)
	})

	t.Run("UnexpectedObjectContents", func(t *testing.T) {
		// Conversely, if want_contents_if_incomplete is set to false,
		// the server MUST omit the object's contents. Returning them
		// would only waste bandwidth.
		client.EXPECT().UploadObject(ctx, testutil.EqProto(t, &object_pb.UploadObjectRequest{
			Namespace: &object_pb.Namespace{
				InstanceName:    "hello/world",
				ReferenceFormat: object_pb.ReferenceFormat_SHA256_V1,
			},
			Reference: []byte{
				// SHA-256 hash.
				0x41, 0x73, 0xc1, 0x91, 0x5b, 0x0f, 0x39, 0x11,
				0x26, 0x0c, 0x6f, 0x55, 0x57, 0x7e, 0x5d, 0x2f,
				0x61, 0x56, 0xa3, 0x6b, 0x69, 0x71, 0xc2, 0xfb,
				0x1d, 0xea, 0x51, 0x69, 0x3b, 0xdb, 0xf9, 0xd0,
				// Size in bytes.
				0x28, 0x00, 0x00,
				// Height.
				0x01,
				// Degree.
				0x01, 0x00,
				// Maximum parents total size in bytes.
				0x00, 0x00,
			},
		})).Return(&object_pb.UploadObjectResponse{
			Type: &object_pb.UploadObjectResponse_Incomplete_{
				Incomplete: &object_pb.UploadObjectResponse_Incomplete{
					Contents: []byte{
						// SHA-256 hash.
						0x18, 0x5f, 0x8d, 0xb3, 0x22, 0x71, 0xfe, 0x25,
						0xf5, 0x61, 0xa6, 0xfc, 0x93, 0x8b, 0x2e, 0x26,
						0x43, 0x06, 0xec, 0x30, 0x4e, 0xda, 0x51, 0x80,
						0x07, 0xd1, 0x76, 0x48, 0x26, 0x38, 0x19, 0x69,
						// Size in bytes.
						0x05, 0x00, 0x00,
						// Height.
						0x00,
						// Degree.
						0x00, 0x00,
						// Maximum parents total size in bytes.
						0x00, 0x00,
					},
					WantOutgoingReferencesLeases: []uint32{0},
				},
			},
		}, nil)

		_, err := uploader.UploadObject(
			ctx,
			object.MustNewSHA256V1GlobalReference("hello/world", "4173c1915b0f3911260c6f55577e5d2f6156a36b6971c2fb1dea51693bdbf9d0", 40, 1, 1, 0),
			/* contents = */ nil,
			/* outgoingReferencesLeases = */ nil,
			/* wantContentsIfIncomplete = */ false,
		)
		testutil.RequireEqualStatus(t, status.Error(codes.Internal, "Server returned object contents, even though the client did not requested them"), err)
	})

	t.Run("InvalidObjectContents", func(t *testing.T) {
		// If the response contains the contents of an object,
		// it must match with the reference that was provided as
		// part of the request.
		client.EXPECT().UploadObject(ctx, testutil.EqProto(t, &object_pb.UploadObjectRequest{
			Namespace: &object_pb.Namespace{
				InstanceName:    "hello/world",
				ReferenceFormat: object_pb.ReferenceFormat_SHA256_V1,
			},
			Reference: []byte{
				// SHA-256 hash.
				0x07, 0x66, 0x9b, 0xac, 0x63, 0xe1, 0xa4, 0x3b,
				0xf4, 0x78, 0xb8, 0x5b, 0x5d, 0xbf, 0x67, 0x3a,
				0x7e, 0x71, 0x78, 0xd9, 0xcc, 0x91, 0xf4, 0x91,
				0xe5, 0x23, 0xab, 0xc4, 0xed, 0xe3, 0x27, 0x3e,
				// Size in bytes.
				0x28, 0x00, 0x00,
				// Height.
				0x01,
				// Degree.
				0x01, 0x00,
				// Maximum parents total size in bytes.
				0x00, 0x00,
			},
			WantContentsIfIncomplete: true,
		})).Return(&object_pb.UploadObjectResponse{
			Type: &object_pb.UploadObjectResponse_Incomplete_{
				Incomplete: &object_pb.UploadObjectResponse_Incomplete{
					Contents: []byte{
						// SHA-256 hash.
						0x22, 0xa4, 0x21, 0xb5, 0x54, 0xfd, 0xbf, 0x72,
						0x3e, 0xce, 0x49, 0xbc, 0x01, 0x51, 0x36, 0xf1,
						0xa0, 0x2a, 0x61, 0x59, 0x69, 0xc6, 0x7f, 0xfe,
						0xcc, 0x25, 0xc1, 0x93, 0xbf, 0x08, 0xff, 0x23,
						// Size in bytes.
						0xab, 0x1c, 0x00,
						// Height.
						0x00,
						// Degree.
						0x00, 0x00,
						// Maximum parents total size in bytes.
						0x00, 0x00,
					},
					WantOutgoingReferencesLeases: []uint32{0},
				},
			},
		}, nil)

		_, err := uploader.UploadObject(
			ctx,
			object.MustNewSHA256V1GlobalReference("hello/world", "07669bac63e1a43bf478b85b5dbf673a7e7178d9cc91f491e523abc4ede3273e", 40, 1, 1, 0),
			/* contents = */ nil,
			/* outgoingReferencesLeases = */ nil,
			/* wantContentsIfIncomplete = */ true,
		)
		testutil.RequireEqualStatus(t, status.Error(codes.Internal, "Server returned invalid object contents: Data has SHA-256 hash 3cc743969e89a9a736707cc5b8fdd9e198c5fc9a7dfe002bb56d2a3ad4d651dd, while 07669bac63e1a43bf478b85b5dbf673a7e7178d9cc91f491e523abc4ede3273e was expected"), err)
	})

	t.Run("MissingWantOutgoingReferencesLeases", func(t *testing.T) {
		// If the server has no leases of outgoing references to
		// report that are invalid/expired, it should have
		// returned a non-empty list of indices.
		client.EXPECT().UploadObject(ctx, testutil.EqProto(t, &object_pb.UploadObjectRequest{
			Namespace: &object_pb.Namespace{
				InstanceName:    "hello/world",
				ReferenceFormat: object_pb.ReferenceFormat_SHA256_V1,
			},
			Reference: []byte{
				// SHA-256 hash.
				0x06, 0x0d, 0x8e, 0x86, 0xcf, 0x07, 0x1e, 0x4e,
				0x81, 0x7a, 0x59, 0x06, 0x89, 0x63, 0x55, 0xdf,
				0x03, 0x57, 0xb9, 0x84, 0x0b, 0xe2, 0xa3, 0x34,
				0x2c, 0x52, 0x4d, 0x1f, 0x00, 0xfe, 0xd5, 0x8a,
				// Size in bytes.
				0x40, 0x01, 0x00,
				// Height.
				0x01,
				// Degree.
				0x08, 0x00,
				// Maximum parents total size in bytes.
				0x00, 0x00,
			},
		})).Return(&object_pb.UploadObjectResponse{
			Type: &object_pb.UploadObjectResponse_Incomplete_{
				Incomplete: &object_pb.UploadObjectResponse_Incomplete{},
			},
		}, nil)

		_, err := uploader.UploadObject(
			ctx,
			object.MustNewSHA256V1GlobalReference("hello/world", "060d8e86cf071e4e817a5906896355df0357b9840be2a3342c524d1f00fed58a", 320, 1, 8, 0),
			/* contents = */ nil,
			/* outgoingReferencesLeases = */ nil,
			/* wantContentsIfIncomplete = */ false,
		)
		testutil.RequireEqualStatus(t, status.Error(codes.Internal, "Server returned an empty list of outgoing references for which leases are expired"), err)
	})

	t.Run("UnsortedWantOutgoingReferencesLeases", func(t *testing.T) {
		// The server MUST return the indices in sorted order.
		// This allows the caller to more efficiently merge the
		// results of multiple servers (e.g., when mirroring is
		// used).
		client.EXPECT().UploadObject(ctx, testutil.EqProto(t, &object_pb.UploadObjectRequest{
			Namespace: &object_pb.Namespace{
				InstanceName:    "hello/world",
				ReferenceFormat: object_pb.ReferenceFormat_SHA256_V1,
			},
			Reference: []byte{
				// SHA-256 hash.
				0xac, 0x70, 0x49, 0x04, 0x83, 0xed, 0x7e, 0x25,
				0xbd, 0xee, 0x02, 0xcf, 0xeb, 0xd3, 0x6f, 0x3b,
				0xbd, 0xa1, 0xc9, 0xde, 0x48, 0x34, 0xe0, 0x86,
				0x8c, 0xf6, 0xbf, 0x41, 0x82, 0x6e, 0x01, 0x75,
				// Size in bytes.
				0x40, 0x01, 0x00,
				// Height.
				0x01,
				// Degree.
				0x08, 0x00,
				// Maximum parents total size in bytes.
				0x00, 0x00,
			},
		})).Return(&object_pb.UploadObjectResponse{
			Type: &object_pb.UploadObjectResponse_Incomplete_{
				Incomplete: &object_pb.UploadObjectResponse_Incomplete{
					WantOutgoingReferencesLeases: []uint32{5, 3},
				},
			},
		}, nil)

		_, err := uploader.UploadObject(
			ctx,
			object.MustNewSHA256V1GlobalReference("hello/world", "ac70490483ed7e25bdee02cfebd36f3bbda1c9de4834e0868cf6bf41826e0175", 320, 1, 8, 0),
			/* contents = */ nil,
			/* outgoingReferencesLeases = */ nil,
			/* wantContentsIfIncomplete = */ false,
		)
		testutil.RequireEqualStatus(t, status.Error(codes.Internal, "Server returned list of outgoing references for which leases are expired that is not properly sorted"), err)
	})

	t.Run("OutOfBoundsWantOutgoingReferencesLeases", func(t *testing.T) {
		// The indices in the want_outgoing_references_leaves
		// list must respect the degree of the object.
		client.EXPECT().UploadObject(ctx, testutil.EqProto(t, &object_pb.UploadObjectRequest{
			Namespace: &object_pb.Namespace{
				InstanceName:    "hello/world",
				ReferenceFormat: object_pb.ReferenceFormat_SHA256_V1,
			},
			Reference: []byte{
				// SHA-256 hash.
				0x06, 0x53, 0x77, 0xb9, 0xd8, 0xef, 0x98, 0x3d,
				0x10, 0xf4, 0x5c, 0xd1, 0xf8, 0x96, 0x0b, 0x9a,
				0x93, 0x07, 0x2d, 0x14, 0xfd, 0x5c, 0xbd, 0xb1,
				0xd9, 0x2b, 0x92, 0x2e, 0xc8, 0xae, 0x33, 0xe8,
				// Size in bytes.
				0x40, 0x01, 0x00,
				// Height.
				0x01,
				// Degree.
				0x08, 0x00,
				// Maximum parents total size in bytes.
				0x00, 0x00,
			},
		})).Return(&object_pb.UploadObjectResponse{
			Type: &object_pb.UploadObjectResponse_Incomplete_{
				Incomplete: &object_pb.UploadObjectResponse_Incomplete{
					WantOutgoingReferencesLeases: []uint32{8},
				},
			},
		}, nil)

		_, err := uploader.UploadObject(
			ctx,
			object.MustNewSHA256V1GlobalReference("hello/world", "065377b9d8ef983d10f45cd1f8960b9a93072d14fd5cbdb1d92b922ec8ae33e8", 320, 1, 8, 0),
			/* contents = */ nil,
			/* outgoingReferencesLeases = */ nil,
			/* wantContentsIfIncomplete = */ false,
		)
		testutil.RequireEqualStatus(t,
			status.Error(codes.Internal, "List of outgoing references for which leases are expired contains indices that exceed the object's degree"), err)
	})

	t.Run("SuccessIncomplete", func(t *testing.T) {
		// The indices in the want_outgoing_references_leaves
		// list must respect the degree of the object.
		client.EXPECT().UploadObject(ctx, testutil.EqProto(t, &object_pb.UploadObjectRequest{
			Namespace: &object_pb.Namespace{
				InstanceName:    "hello/world",
				ReferenceFormat: object_pb.ReferenceFormat_SHA256_V1,
			},
			Reference: []byte{
				// SHA-256 hash.
				0x8b, 0xb8, 0xd0, 0x81, 0x3a, 0xfb, 0xa8, 0x44,
				0x2e, 0x3a, 0x30, 0x5b, 0x5e, 0xc1, 0x4f, 0x8e,
				0x77, 0x7f, 0x2f, 0x02, 0x4a, 0x46, 0xca, 0x22,
				0x5e, 0xe1, 0x01, 0x6c, 0x6d, 0x16, 0x6e, 0x4b,
				// Size in bytes.
				0x40, 0x01, 0x00,
				// Height.
				0x01,
				// Degree.
				0x08, 0x00,
				// Maximum parents total size in bytes.
				0x00, 0x00,
			},
		})).Return(&object_pb.UploadObjectResponse{
			Type: &object_pb.UploadObjectResponse_Incomplete_{
				Incomplete: &object_pb.UploadObjectResponse_Incomplete{
					WantOutgoingReferencesLeases: []uint32{3, 7},
				},
			},
		}, nil)

		result, err := uploader.UploadObject(
			ctx,
			object.MustNewSHA256V1GlobalReference("hello/world", "8bb8d0813afba8442e3a305b5ec14f8e777f2f024a46ca225ee1016c6d166e4b", 320, 1, 8, 0),
			/* contents = */ nil,
			/* outgoingReferencesLeases = */ nil,
			/* wantContentsIfIncomplete = */ false,
		)
		require.NoError(t, err)
		require.Equal(t, object.UploadObjectIncomplete[[]byte]{
			WantOutgoingReferencesLeases: []int{3, 7},
		}, result)
	})
}
