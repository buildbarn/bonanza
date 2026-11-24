package object_test

import (
	"context"
	"testing"
	"time"

	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"go.uber.org/mock/gomock"
)

func TestUploaderServer(t *testing.T) {
	ctrl, ctx := gomock.WithContext(context.Background(), t)

	uploader := NewMockUploaderForTestingBytes(ctrl)
	server := object.NewUploaderServer(uploader, nil, nil)

	t.Run("InvalidNamespace", func(t *testing.T) {
		_, err := server.UploadObject(ctx, &object_pb.UploadObjectRequest{
			Reference: []byte{
				// SHA-256 hash.
				0xf7, 0x42, 0x5b, 0x6c, 0x3d, 0xe5, 0x64, 0x77,
				0xb1, 0x48, 0x52, 0x91, 0xb2, 0x53, 0x78, 0xb9,
				0x3e, 0xc2, 0x46, 0x69, 0xb9, 0x06, 0xea, 0x2c,
				0x46, 0xe5, 0xf2, 0x8c, 0x6e, 0x4b, 0xc0, 0x1e,
				// Size in bytes.
				0x06, 0x54, 0x00,
				// Height.
				0x00,
				// Degree.
				0x00, 0x00,
				// Maximum parents total size in bytes.
				0x00, 0x00,
			},
		})
		testutil.RequireEqualStatus(t, status.Error(codes.InvalidArgument, "Invalid namespace: No message provided"), err)
	})

	t.Run("InvalidReference", func(t *testing.T) {
		_, err := server.UploadObject(ctx, &object_pb.UploadObjectRequest{
			Namespace: &object_pb.Namespace{
				InstanceName:    "hello/world",
				ReferenceFormat: object_pb.ReferenceFormat_SHA256_V1,
			},
			Reference: []byte{
				// SHA-256 hash.
				0x59, 0x6b, 0x9a, 0x92, 0x27, 0xec, 0xbe, 0xd4,
				0x41, 0xa4, 0x3f, 0x76, 0x0d, 0x88, 0x97, 0x47,
				0xfa, 0x98, 0x82, 0x82, 0x6e, 0xad, 0x60, 0x97,
				0x5a, 0x23, 0xcf, 0xb2, 0x4a, 0xa3, 0x21, 0xa7,
				// Size in bytes.
				0xe9, 0xd4, 0x00,
				// Height.
				0x01,
				// Degree.
				0x00, 0x00,
				// Maximum parents total size in bytes.
				0x00, 0x00,
			},
		})
		testutil.RequireEqualStatus(t, status.Error(codes.InvalidArgument, "Invalid reference: Degree is 0 and height is 1, while both either have to be zero or non-zero"), err)
	})

	t.Run("InvalidContents", func(t *testing.T) {
		_, err := server.UploadObject(ctx, &object_pb.UploadObjectRequest{
			Namespace: &object_pb.Namespace{
				InstanceName:    "hello/world",
				ReferenceFormat: object_pb.ReferenceFormat_SHA256_V1,
			},
			Reference: []byte{
				// SHA-256 hash.
				0x0d, 0x85, 0x7e, 0x7a, 0xfd, 0xe4, 0x4e, 0x5c,
				0x40, 0x1d, 0x51, 0x0e, 0xe2, 0x53, 0x02, 0xa0,
				0xf7, 0xd0, 0x9d, 0xc7, 0x25, 0xff, 0x68, 0x42,
				0x31, 0xc9, 0x58, 0x7c, 0x79, 0xeb, 0x1f, 0x5b,
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
		})
		testutil.RequireEqualStatus(t, status.Error(codes.InvalidArgument, "Invalid object contents: Data has SHA-256 hash 185f8db32271fe25f561a6fc938b2e264306ec304eda518007d1764826381969, while 0d857e7afde44e5c401d510ee25302a0f7d09dc725ff684231c9587c79eb1f5b was expected"), err)
	})

	t.Run("InvalidLeasesCount", func(t *testing.T) {
		_, err := server.UploadObject(ctx, &object_pb.UploadObjectRequest{
			Namespace: &object_pb.Namespace{
				InstanceName:    "hello/world",
				ReferenceFormat: object_pb.ReferenceFormat_SHA256_V1,
			},
			Reference: []byte{
				// SHA-256 hash.
				0xba, 0xec, 0x0e, 0xab, 0x58, 0x9d, 0x71, 0xd8,
				0xfb, 0x2b, 0x46, 0x98, 0x74, 0xe2, 0x12, 0x6b,
				0xc7, 0xc9, 0x5e, 0x27, 0x1c, 0x26, 0xe0, 0x91,
				0x62, 0xf5, 0x2b, 0xfc, 0xde, 0x8b, 0x27, 0xd7,
				// Size in bytes.
				0x8a, 0x99, 0x00,
				// Height.
				0x01,
				// Degree.
				0x0c, 0x00,
				// Maximum parents total size in bytes.
				0x00, 0x00,
			},
			OutgoingReferencesLeases: [][]byte{
				nil,
				{1, 2, 3},
			},
		})
		testutil.RequireEqualStatus(t, status.Error(codes.InvalidArgument, "Client provided 2 leases, even though the object has a degree of 12"), err)
	})

	t.Run("UnnecessaryWantContentsIfIncomplete", func(t *testing.T) {
		// It doesn't make sense for a client to provide the
		// object's contents as part of UploadObject(), and at
		// the same time ask for a copy of the contents when the
		// object is incomplete. This only wastes bandwidth.
		_, err := server.UploadObject(ctx, &object_pb.UploadObjectRequest{
			Namespace: &object_pb.Namespace{
				InstanceName:    "hello/world",
				ReferenceFormat: object_pb.ReferenceFormat_SHA256_V1,
			},
			Reference: []byte{
				// SHA-256 hash.
				0x8b, 0x5a, 0xaa, 0xd9, 0x0e, 0x51, 0x1b, 0x29,
				0x05, 0x13, 0x89, 0x30, 0xa9, 0xdd, 0x29, 0x45,
				0xbc, 0x0f, 0xd6, 0xa1, 0x5e, 0x37, 0xc3, 0x20,
				0xc0, 0x98, 0x9d, 0x2e, 0xc6, 0xed, 0x27, 0x6f,
				// Size in bytes.
				0x2d, 0x00, 0x00,
				// Height.
				0x01,
				// Degree.
				0x01, 0x00,
				// Maximum parents total size in bytes.
				0x00, 0x00,
			},
			Contents: []byte{
				// SHA-256 hash.
				0x51, 0xa5, 0x99, 0x83, 0x0e, 0x11, 0x4d, 0x67,
				0x7e, 0x97, 0xc8, 0x42, 0x0a, 0x80, 0xf7, 0x93,
				0xac, 0x78, 0x80, 0xe1, 0xa1, 0xdc, 0xe0, 0x4c,
				0x6d, 0xf2, 0x0a, 0x67, 0x6e, 0xab, 0x80, 0x28,
				// Size in bytes.
				0xc0, 0xfe, 0x00,
				// Height.
				0x00,
				// Degree.
				0x00, 0x00,
				// Maximum parents total size in bytes.
				0x00, 0x00,

				// Payload.
				0x48, 0x65, 0x6c, 0x6c, 0x6f,
			},
			WantContentsIfIncomplete: true,
		})
		testutil.RequireEqualStatus(t, status.Error(codes.InvalidArgument, "Client requested the object's contents, even though it is already in possession of them"), err)
	})

	t.Run("WantContentsIfIncompleteAgainstLeaf", func(t *testing.T) {
		// Objects that don't have any outgoing references can't
		// go into an incomplete state, as they don't depend on
		// any leases. This means that setting
		// want_contents_if_incomplete is meaningless.
		_, err := server.UploadObject(ctx, &object_pb.UploadObjectRequest{
			Namespace: &object_pb.Namespace{
				InstanceName:    "hello/world",
				ReferenceFormat: object_pb.ReferenceFormat_SHA256_V1,
			},
			Reference: []byte{
				// SHA-256 hash.
				0x47, 0x9a, 0x9e, 0x91, 0x76, 0x71, 0x22, 0x06,
				0x6f, 0xf6, 0x53, 0xfa, 0xdc, 0xc9, 0x8f, 0x57,
				0x46, 0xa8, 0x8f, 0x2f, 0x68, 0xfd, 0xfc, 0x43,
				0x0e, 0xb4, 0x3a, 0x1b, 0x9a, 0xca, 0x8f, 0xbd,
				// Size in bytes.
				0x51, 0x3e, 0x00,
				// Height.
				0x00,
				// Degree.
				0x00, 0x00,
				// Maximum parents total size in bytes.
				0x00, 0x00,
			},
			WantContentsIfIncomplete: true,
		})
		testutil.RequireEqualStatus(t, status.Error(codes.InvalidArgument, "Client requested the object's contents, even though the object does not have any outgoing references"), err)
	})

	t.Run("SuccessComplete", func(t *testing.T) {
		uploader.EXPECT().UploadObject(
			ctx,
			object.MustNewSHA256V1GlobalReference("hello/world", "8693e433b985caef948beeb9e124c0bcb09803f6d8d0c16a9b6f450a179aac8e", 28532, 3, 2, 256),
			/* contents = */ nil,
			/* childrenLeases = */ [][]byte{
				{1, 2, 3},
				{4, 5, 6},
			},
			/* wantContentsIfIncomplete = */ false,
		).Return(object.UploadObjectComplete[[]byte]{
			Lease: []byte{7, 8, 9},
		}, nil)

		response, err := server.UploadObject(ctx, &object_pb.UploadObjectRequest{
			Namespace: &object_pb.Namespace{
				InstanceName:    "hello/world",
				ReferenceFormat: object_pb.ReferenceFormat_SHA256_V1,
			},
			Reference: []byte{
				// SHA-256 hash.
				0x86, 0x93, 0xe4, 0x33, 0xb9, 0x85, 0xca, 0xef,
				0x94, 0x8b, 0xee, 0xb9, 0xe1, 0x24, 0xc0, 0xbc,
				0xb0, 0x98, 0x03, 0xf6, 0xd8, 0xd0, 0xc1, 0x6a,
				0x9b, 0x6f, 0x45, 0x0a, 0x17, 0x9a, 0xac, 0x8e,
				// Size in bytes.
				0x74, 0x6f, 0x00,
				// Height.
				0x03,
				// Degree.
				0x02, 0x00,
				// Maximum parents total size in bytes.
				0x00, 0x01,
			},
			OutgoingReferencesLeases: [][]byte{
				{1, 2, 3},
				{4, 5, 6},
			},
		})
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &object_pb.UploadObjectResponse{
			Type: &object_pb.UploadObjectResponse_Complete_{
				Complete: &object_pb.UploadObjectResponse_Complete{
					Lease: []byte{7, 8, 9},
				},
			},
		}, response)
	})

	t.Run("SuccessIncompleteWithoutContents", func(t *testing.T) {
		uploader.EXPECT().UploadObject(
			ctx,
			object.MustNewSHA256V1GlobalReference("hello/world", "2046e13faa09ceb376fe9ff31be9c3a592880c1fe345e222a8442459acb0b014", 28532, 3, 2, 256),
			/* contents = */ nil,
			/* childrenLeases = */ nil,
			/* wantContentsIfIncomplete = */ false,
		).Return(object.UploadObjectIncomplete[[]byte]{
			WantOutgoingReferencesLeases: []int{0, 1},
		}, nil)

		response, err := server.UploadObject(ctx, &object_pb.UploadObjectRequest{
			Namespace: &object_pb.Namespace{
				InstanceName:    "hello/world",
				ReferenceFormat: object_pb.ReferenceFormat_SHA256_V1,
			},
			Reference: []byte{
				// SHA-256 hash.
				0x20, 0x46, 0xe1, 0x3f, 0xaa, 0x09, 0xce, 0xb3,
				0x76, 0xfe, 0x9f, 0xf3, 0x1b, 0xe9, 0xc3, 0xa5,
				0x92, 0x88, 0x0c, 0x1f, 0xe3, 0x45, 0xe2, 0x22,
				0xa8, 0x44, 0x24, 0x59, 0xac, 0xb0, 0xb0, 0x14,
				// Size in bytes.
				0x74, 0x6f, 0x00,
				// Height.
				0x03,
				// Degree.
				0x02, 0x00,
				// Maximum parents total size in bytes.
				0x00, 0x01,
			},
		})
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &object_pb.UploadObjectResponse{
			Type: &object_pb.UploadObjectResponse_Incomplete_{
				Incomplete: &object_pb.UploadObjectResponse_Incomplete{
					WantOutgoingReferencesLeases: []uint32{0, 1},
				},
			},
		}, response)
	})

	t.Run("SuccessIncompleteWithContents", func(t *testing.T) {
		uploader.EXPECT().UploadObject(
			ctx,
			object.MustNewSHA256V1GlobalReference("hello/world", "78464d06cd7fa7086884c31c04e85679c2f03813d0f7cea6e4dc83b0b97abeda", 45, 1, 1, 0),
			/* contents = */ nil,
			/* childrenLeases = */ nil,
			/* wantContentsIfIncomplete = */ true,
		).Return(object.UploadObjectIncomplete[[]byte]{
			WantOutgoingReferencesLeases: []int{0},
			Contents: object.MustNewContents(
				object_pb.ReferenceFormat_SHA256_V1,
				[]object.LocalReference{
					object.MustNewSHA256V1LocalReference("41b16120a7fd00408e2a133fe29e878e510b48aecb0ea1edadd02588d9bf4c04", 9652, 0, 0, 0),
				},
				[]byte("Hello"),
			),
		}, nil)

		response, err := server.UploadObject(ctx, &object_pb.UploadObjectRequest{
			Namespace: &object_pb.Namespace{
				InstanceName:    "hello/world",
				ReferenceFormat: object_pb.ReferenceFormat_SHA256_V1,
			},
			Reference: []byte{
				// SHA-256 hash.
				0x78, 0x46, 0x4d, 0x06, 0xcd, 0x7f, 0xa7, 0x08,
				0x68, 0x84, 0xc3, 0x1c, 0x04, 0xe8, 0x56, 0x79,
				0xc2, 0xf0, 0x38, 0x13, 0xd0, 0xf7, 0xce, 0xa6,
				0xe4, 0xdc, 0x83, 0xb0, 0xb9, 0x7a, 0xbe, 0xda,
				// Size in bytes.
				0x2d, 0x00, 0x00,
				// Height.
				0x01,
				// Degree.
				0x01, 0x00,
				// Maximum parents total size in bytes.
				0x00, 0x00,
			},
			WantContentsIfIncomplete: true,
		})
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &object_pb.UploadObjectResponse{
			Type: &object_pb.UploadObjectResponse_Incomplete_{
				Incomplete: &object_pb.UploadObjectResponse_Incomplete{
					Contents: []byte{
						// SHA-256 hash.
						0x41, 0xb1, 0x61, 0x20, 0xa7, 0xfd, 0x00, 0x40,
						0x8e, 0x2a, 0x13, 0x3f, 0xe2, 0x9e, 0x87, 0x8e,
						0x51, 0x0b, 0x48, 0xae, 0xcb, 0x0e, 0xa1, 0xed,
						0xad, 0xd0, 0x25, 0x88, 0xd9, 0xbf, 0x4c, 0x04,
						// Size in bytes.
						0xb4, 0x25, 0x00,
						// Height.
						0x00,
						// Degree.
						0x00, 0x00,
						// Maximum parents total size in bytes.
						0x00, 0x00,

						// Payload.
						0x48, 0x65, 0x6c, 0x6c, 0x6f,
					},
					WantOutgoingReferencesLeases: []uint32{0},
				},
			},
		}, response)
	})

	t.Run("SuccessMissing", func(t *testing.T) {
		// UploadObjectMissing results should get translated to
		// NOT_FOUND errors at the RPC level. This makes metrics
		// on the monitoring side more useful.
		uploader.EXPECT().UploadObject(
			ctx,
			object.MustNewSHA256V1GlobalReference("hello/world", "a966f39598fc9a54634a928879f543ec0043f4b6acf587bc32cb431fe0a47f86", 29890, 0, 0, 0),
			/* contents = */ nil,
			/* childrenLeases = */ nil,
			/* wantContentsIfIncomplete = */ false,
		).Return(object.UploadObjectMissing[[]byte]{}, nil)

		_, err := server.UploadObject(ctx, &object_pb.UploadObjectRequest{
			Namespace: &object_pb.Namespace{
				InstanceName:    "hello/world",
				ReferenceFormat: object_pb.ReferenceFormat_SHA256_V1,
			},
			Reference: []byte{
				// SHA-256 hash.
				0xa9, 0x66, 0xf3, 0x95, 0x98, 0xfc, 0x9a, 0x54,
				0x63, 0x4a, 0x92, 0x88, 0x79, 0xf5, 0x43, 0xec,
				0x00, 0x43, 0xf4, 0xb6, 0xac, 0xf5, 0x87, 0xbc,
				0x32, 0xcb, 0x43, 0x1f, 0xe0, 0xa4, 0x7f, 0x86,
				// Size in bytes.
				0xc2, 0x74, 0x00,
				// Height.
				0x00,
				// Degree.
				0x00, 0x00,
				// Maximum parents total size in bytes.
				0x00, 0x00,
			},
		})
		testutil.RequireEqualStatus(t, status.Error(codes.NotFound, "Object not found"), err)
	})
}

func TestUploaderServerWatermark(t *testing.T) {
	ctrl, ctx := gomock.WithContext(context.Background(), t)

	t.Run("NotReported", func(t *testing.T) {
		// Servers that don't track a watermark should report
		// so explicitly.
		server := object.NewUploaderServer(NewMockUploaderForTestingBytes(ctrl), nil, nil)
		_, err := server.GetWatermark(ctx, &emptypb.Empty{})
		testutil.RequireEqualStatus(t, status.Error(codes.Unimplemented, "This server does not report its watermark"), err)
	})

	t.Run("Reported", func(t *testing.T) {
		clock := NewMockClock(ctrl)
		clock.EXPECT().Now().Return(time.Unix(1700001000, 0)).AnyTimes()
		clusterWatermark := object.NewWatermark(clock, time.Minute)
		server := object.NewUploaderServer(
			NewMockUploaderForTestingBytes(ctrl),
			clusterWatermark,
			func() (time.Time, time.Time) { return time.Unix(1700000000, 0), time.Unix(1700001000, 0) },
		)

		response, err := server.GetWatermark(ctx, &emptypb.Empty{})
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &object_pb.GetWatermarkResponse{
			Watermark: &object_pb.Watermark{
				Watermark:  &timestamppb.Timestamp{Seconds: 1700000000},
				ObtainedAt: &timestamppb.Timestamp{Seconds: 1700001000},
			},
		}, response)

		// The cluster watermark provided by clients should be
		// tracked, even if the request is invalid otherwise.
		_, err = server.UploadObject(ctx, &object_pb.UploadObjectRequest{
			Watermark: &object_pb.Watermark{
				Watermark:  &timestamppb.Timestamp{Seconds: 1700000500},
				ObtainedAt: &timestamppb.Timestamp{Seconds: 1700000990},
			},
		})
		require.Error(t, err)
		require.Equal(t, time.Unix(1700000500, 0), clusterWatermark.Get())

		// Cluster watermarks that are too old should cause
		// requests to be rejected.
		_, err = server.UploadObject(ctx, &object_pb.UploadObjectRequest{
			Watermark: &object_pb.Watermark{
				Watermark:  &timestamppb.Timestamp{Seconds: 1700000900},
				ObtainedAt: &timestamppb.Timestamp{Seconds: 1700000939},
			},
		})
		testutil.RequireEqualStatus(t, status.Error(codes.Unavailable, "Invalid cluster watermark: Watermark of the cluster was obtained 1m1s ago, which exceeds the maximum permitted age of 1m0s, meaning data may have been lost in the meantime"), err)
		require.Equal(t, time.Unix(1700000500, 0), clusterWatermark.Get())
	})
}
