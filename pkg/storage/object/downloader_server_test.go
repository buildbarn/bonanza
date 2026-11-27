package object_test

import (
	"context"
	"testing"

	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go.uber.org/mock/gomock"
)

func TestDownloaderServer(t *testing.T) {
	ctrl, ctx := gomock.WithContext(context.Background(), t)

	downloader := NewMockDownloaderForTesting(ctrl)
	server := object.NewDownloaderServer(downloader)

	t.Run("InvalidNamespace", func(t *testing.T) {
		_, err := server.DownloadObject(ctx, object_pb.DownloadObjectRequest_builder{
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
		}.Build())
		testutil.RequireEqualStatus(t, status.Error(codes.InvalidArgument, "Invalid namespace: No message provided"), err)
	})

	t.Run("InvalidReference", func(t *testing.T) {
		_, err := server.DownloadObject(ctx, object_pb.DownloadObjectRequest_builder{
			Namespace: object_pb.Namespace_builder{
				InstanceName:    "hello/world",
				ReferenceFormat: object_pb.ReferenceFormat_SHA256_V1,
			}.Build(),
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
		}.Build())
		testutil.RequireEqualStatus(t, status.Error(codes.InvalidArgument, "Invalid reference: Degree is 0 and height is 1, while both either have to be zero or non-zero"), err)
	})

	t.Run("BackendError", func(t *testing.T) {
		downloader.EXPECT().DownloadObject(ctx, object.MustNewSHA256V1GlobalReference("hello/world", "8996ae1fd65a65cf2c4ffa96b1639deffd15ebd1624037aaeca4758529727e7e", 21777, 0, 0, 0)).
			Return(nil, status.Error(codes.Unavailable, "Server offline"))

		_, err := server.DownloadObject(ctx, object_pb.DownloadObjectRequest_builder{
			Namespace: object_pb.Namespace_builder{
				InstanceName:    "hello/world",
				ReferenceFormat: object_pb.ReferenceFormat_SHA256_V1,
			}.Build(),
			Reference: []byte{
				// SHA-256 hash.
				0x89, 0x96, 0xae, 0x1f, 0xd6, 0x5a, 0x65, 0xcf,
				0x2c, 0x4f, 0xfa, 0x96, 0xb1, 0x63, 0x9d, 0xef,
				0xfd, 0x15, 0xeb, 0xd1, 0x62, 0x40, 0x37, 0xaa,
				0xec, 0xa4, 0x75, 0x85, 0x29, 0x72, 0x7e, 0x7e,
				// Size in bytes.
				0x11, 0x55, 0x00,
				// Height.
				0x00,
				// Degree.
				0x00, 0x00,
				// Maximum parents total size in bytes.
				0x00, 0x00,
			},
		}.Build())
		testutil.RequireEqualStatus(t, status.Error(codes.Unavailable, "Server offline"), err)
	})

	t.Run("Success", func(t *testing.T) {
		downloader.EXPECT().DownloadObject(ctx, object.MustNewSHA256V1GlobalReference("hello/world", "185f8db32271fe25f561a6fc938b2e264306ec304eda518007d1764826381969", 5, 0, 0, 0)).
			Return(object.MustNewContents(object_pb.ReferenceFormat_SHA256_V1, nil, []byte("Hello")), nil)

		response, err := server.DownloadObject(ctx, object_pb.DownloadObjectRequest_builder{
			Namespace: object_pb.Namespace_builder{
				InstanceName:    "hello/world",
				ReferenceFormat: object_pb.ReferenceFormat_SHA256_V1,
			}.Build(),
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
		}.Build())
		require.NoError(t, err)
		testutil.RequireEqualProto(t, object_pb.DownloadObjectResponse_builder{
			Contents: []byte("Hello"),
		}.Build(), response)
	})
}
