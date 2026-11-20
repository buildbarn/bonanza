package readcaching_test

import (
	"context"
	"testing"
	"time"

	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/object"
	"bonanza.build/pkg/storage/object/readcaching"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go.uber.org/mock/gomock"
)

func TestDownloader(t *testing.T) {
	ctrl, ctx := gomock.WithContext(context.Background(), t)

	slowDownloader := NewMockDownloaderForTesting(ctrl)
	fastStore := NewMockFlatStoreForTesting(ctrl)
	downloader := readcaching.NewDownloader(slowDownloader, fastStore)

	t.Run("FastBackendGetFailure", func(t *testing.T) {
		// Internal errors returned by the fast storage backend
		// should be propagated immediately.
		fastStore.EXPECT().
			DownloadObject(ctx, object.MustNewSHA256V1FlatReference("185f8db32271fe25f561a6fc938b2e264306ec304eda518007d1764826381969", 5038)).
			Return(nil, status.Error(codes.Internal, "Node unreachable"))

		_, err := downloader.DownloadObject(ctx, object.MustNewSHA256V1GlobalReference("example", "185f8db32271fe25f561a6fc938b2e264306ec304eda518007d1764826381969", 5038, 1, 6, 0))
		testutil.RequireEqualStatus(t, status.Error(codes.Internal, "Get object from fast backend: Node unreachable"), err)
	})

	t.Run("FastBackendUnflattenFailure", func(t *testing.T) {
		// The fast backend stores objects with the degree set
		// to zero. This means that if the fast backend returns
		// an object, we must manually validate the outgoing
		// references contained in the object.
		fastStore.EXPECT().
			DownloadObject(ctx, object.MustNewSHA256V1FlatReference("527deddd1ace8a41a54d3768bfc35b41a5ab2a59be011b84b48fdee0753bdf5c", 45)).
			Return(
				object.MustNewContents(
					object_pb.ReferenceFormat_SHA256_V1,
					[]object.LocalReference{},
					[]byte{
						// SHA-256 hash.
						0x0e, 0xde, 0x36, 0xff, 0x67, 0x56, 0x6d, 0x77,
						0x68, 0x34, 0xc7, 0x09, 0x0b, 0x17, 0xb6, 0x68,
						0x74, 0x54, 0x26, 0x78, 0xaf, 0xba, 0x9b, 0xda,
						0xe7, 0x8c, 0x42, 0x23, 0xc0, 0x2d, 0xfe, 0xd3,
						// Size in bytes.
						0x8b, 0x80, 0x1f,
						// Height.
						0xff,
						// Degree.
						0x40, 0x34,
						// Maximum parents total size in bytes.
						0x42, 0x28,

						// Payload.
						0x48, 0x65, 0x6c, 0x6c, 0x6f,
					},
				),
				nil,
			)

		_, err := downloader.DownloadObject(ctx, object.MustNewSHA256V1GlobalReference("example", "527deddd1ace8a41a54d3768bfc35b41a5ab2a59be011b84b48fdee0753bdf5c", 45, 1, 1, 0))
		testutil.RequireEqualStatus(t, status.Error(codes.InvalidArgument, "Unflatten object contents: Outgoing reference at index 0 has height 255, which is too high"), err)
	})

	t.Run("FastBackendSuccess", func(t *testing.T) {
		// Successfully obtain an object from the fast backend
		// that can be unflattened into an object that matches
		// the originally provided reference.
		fastStore.EXPECT().
			DownloadObject(ctx, object.MustNewSHA256V1FlatReference("796527480ab3eacff7e5fbe0d828527c9a0f06b448a2b200de9774e0efa1bb68", 85)).
			Return(
				object.MustNewContents(
					object_pb.ReferenceFormat_SHA256_V1,
					[]object.LocalReference{},
					[]byte{
						// SHA-256 hash.
						0x94, 0x19, 0x7c, 0xdd, 0xbb, 0x0d, 0xab, 0x09,
						0x5c, 0xff, 0xd1, 0x0d, 0xc5, 0x28, 0xcb, 0x22,
						0x6c, 0x20, 0xab, 0x8c, 0x6e, 0x33, 0x3f, 0x50,
						0xfa, 0x76, 0x7e, 0x9d, 0xe3, 0xac, 0xec, 0x6e,
						// Size in bytes.
						0x61, 0x47, 0x0b,
						// Height.
						0x9b,
						// Degree.
						0x98, 0x27,
						// Maximum parents total size in bytes.
						0x7b, 0x1b,

						// SHA-256 hash.
						0xa5, 0x12, 0xb8, 0x92, 0xa4, 0x93, 0x27, 0x93,
						0x95, 0x5e, 0x7b, 0xe6, 0x5c, 0x2c, 0xb9, 0x2b,
						0x27, 0x55, 0x8d, 0xb7, 0x1f, 0x87, 0xa0, 0xfa,
						0x99, 0xc0, 0x63, 0x25, 0x13, 0x6d, 0x48, 0x49,
						// Size in bytes.
						0x2c, 0x8b, 0x13,
						// Height.
						0xda,
						// Degree.
						0xfe, 0x2e,
						// Maximum parents total size in bytes.
						0x78, 0x24,

						// Payload.
						0x48, 0x65, 0x6c, 0x6c, 0x6f,
					},
				),
				nil,
			)

		objectContents, err := downloader.DownloadObject(ctx, object.MustNewSHA256V1GlobalReference("example", "796527480ab3eacff7e5fbe0d828527c9a0f06b448a2b200de9774e0efa1bb68", 85, 219, 2, 1306624))
		require.NoError(t, err)
		require.Equal(t, object.MustNewSHA256V1LocalReference("796527480ab3eacff7e5fbe0d828527c9a0f06b448a2b200de9774e0efa1bb68", 85, 219, 2, 1306624), objectContents.GetLocalReference())
		require.Equal(t, object.MustNewSHA256V1LocalReference("94197cddbb0dab095cffd10dc528cb226c20ab8c6e333f50fa767e9de3acec6e", 739169, 155, 10136, 11756), objectContents.GetOutgoingReference(0))
		require.Equal(t, object.MustNewSHA256V1LocalReference("a512b892a4932793955e7be65c2cb92b27558db71f87a0fa99c06325136d4849", 1280812, 218, 12030, 25536), objectContents.GetOutgoingReference(1))
		require.Equal(t, []byte("Hello"), objectContents.GetPayload())
	})

	t.Run("SlowBackendGetFailure", func(t *testing.T) {
		// If the fast backend returns NOT_FOUND, the request
		// should be forwarded to the slow backend. Any errors
		// returned by the slow backend should be propagated.
		fastStore.EXPECT().
			DownloadObject(ctx, object.MustNewSHA256V1FlatReference("b3a5b373cc386887a03d0c10e5d78c198480015c4c902b03267aed6a41f99fa5", 241)).
			Return(nil, status.Error(codes.NotFound, "Object not found"))
		slowDownloader.EXPECT().
			DownloadObject(ctx, object.MustNewSHA256V1GlobalReference("example", "b3a5b373cc386887a03d0c10e5d78c198480015c4c902b03267aed6a41f99fa5", 241, 12, 3, 51023)).
			Return(nil, status.Error(codes.Internal, "Node unreachable"))

		_, err := downloader.DownloadObject(ctx, object.MustNewSHA256V1GlobalReference("example", "b3a5b373cc386887a03d0c10e5d78c198480015c4c902b03267aed6a41f99fa5", 241, 12, 3, 51023))
		testutil.RequireEqualStatus(t, status.Error(codes.Internal, "Get object from slow backend: Node unreachable"), err)
	})

	t.Run("FastBackendPutFailure", func(t *testing.T) {
		// If the object is mirrored from the slow backend to
		// the fast backend, it should be flattened. This means
		// that outgoing references should now be stored at the
		// start of the payload. Make the write fail.
		fastStore.EXPECT().
			DownloadObject(ctx, object.MustNewSHA256V1FlatReference("16cdd68fd8f9ccc88a1346a7faf8f36cc3935babc8a99d609a9725bf071c6a6f", 85)).
			Return(nil, status.Error(codes.NotFound, "Object not found"))
		slowDownloader.EXPECT().
			DownloadObject(ctx, object.MustNewSHA256V1GlobalReference("example", "16cdd68fd8f9ccc88a1346a7faf8f36cc3935babc8a99d609a9725bf071c6a6f", 85, 15, 2, 585697)).
			Return(
				object.MustNewContents(
					object_pb.ReferenceFormat_SHA256_V1,
					[]object.LocalReference{
						object.MustNewSHA256V1LocalReference("40b4d32bc0df8e514702782a011398eeb61d6aa41df89974af79e895d9e0f193", 9581, 12, 17, 95918),
						object.MustNewSHA256V1LocalReference("aadb11407d21a1ec9bc984d7577a294f825073e37825e6b3df5acebf83bf0f58", 858, 14, 5, 584839),
					},
					[]byte{0x48, 0x65, 0x6c, 0x6c, 0x6f},
				),
				nil,
			)
		fastStore.EXPECT().
			UploadObject(ctx, object.MustNewSHA256V1FlatReference("16cdd68fd8f9ccc88a1346a7faf8f36cc3935babc8a99d609a9725bf071c6a6f", 85), gomock.Any(), gomock.Len(0), false).
			DoAndReturn(func(ctx context.Context, reference object.FlatReference, objectContents *object.Contents, leases []time.Time, wantContentsIfIncomplete bool) (object.UploadObjectResult[time.Time], error) {
				require.Equal(t, object.MustNewSHA256V1LocalReference("16cdd68fd8f9ccc88a1346a7faf8f36cc3935babc8a99d609a9725bf071c6a6f", 85, 0, 0, 0), objectContents.GetLocalReference())
				require.Equal(t, []byte{
					// SHA-256 hash.
					0x40, 0xb4, 0xd3, 0x2b, 0xc0, 0xdf, 0x8e, 0x51,
					0x47, 0x02, 0x78, 0x2a, 0x01, 0x13, 0x98, 0xee,
					0xb6, 0x1d, 0x6a, 0xa4, 0x1d, 0xf8, 0x99, 0x74,
					0xaf, 0x79, 0xe8, 0x95, 0xd9, 0xe0, 0xf1, 0x93,
					// Size in bytes.
					0x6d, 0x25, 0x00,
					// Height.
					0x0c,
					// Degree.
					0x11, 0x00,
					// Maximum parents total size in bytes.
					0xb6, 0x33,

					// SHA-256 hash.
					0xaa, 0xdb, 0x11, 0x40, 0x7d, 0x21, 0xa1, 0xec,
					0x9b, 0xc9, 0x84, 0xd7, 0x57, 0x7a, 0x29, 0x4f,
					0x82, 0x50, 0x73, 0xe3, 0x78, 0x25, 0xe6, 0xb3,
					0xdf, 0x5a, 0xce, 0xbf, 0x83, 0xbf, 0x0f, 0x58,
					// Size in bytes.
					0x5a, 0x03, 0x00,
					// Height.
					0x0e,
					// Degree.
					0x05, 0x00,
					// Maximum parents total size in bytes.
					0xed, 0x48,

					// Payload.
					0x48, 0x65, 0x6c, 0x6c, 0x6f,
				}, objectContents.GetPayload())

				return nil, status.Error(codes.Internal, "Node unreachable")
			})

		_, err := downloader.DownloadObject(ctx, object.MustNewSHA256V1GlobalReference("example", "16cdd68fd8f9ccc88a1346a7faf8f36cc3935babc8a99d609a9725bf071c6a6f", 85, 15, 2, 585697))
		testutil.RequireEqualStatus(t, status.Error(codes.Internal, "Put object in fast backend: Node unreachable"), err)
	})

	t.Run("MirrorSuccess", func(t *testing.T) {
		// Request to object that did not exist in the fast backend,
		// and got mirrored from the slow backend successfully.
		fastStore.EXPECT().
			DownloadObject(ctx, object.MustNewSHA256V1FlatReference("e0643f027c868b649cba8f131459b5f1217c9897080a11391f1f9f6e53e7aa68", 45)).
			Return(nil, status.Error(codes.NotFound, "Object not found"))
		slowDownloader.EXPECT().
			DownloadObject(ctx, object.MustNewSHA256V1GlobalReference("example", "e0643f027c868b649cba8f131459b5f1217c9897080a11391f1f9f6e53e7aa68", 45, 2, 1, 50)).
			Return(
				object.MustNewContents(
					object_pb.ReferenceFormat_SHA256_V1,
					[]object.LocalReference{
						object.MustNewSHA256V1LocalReference("803b91e1102b8e3bfbed642ec7b0d6c8669948add8c84584977e2840a813d2b6", 50, 1, 1, 0),
					},
					[]byte{0x48, 0x65, 0x6c, 0x6c, 0x6f},
				),
				nil,
			)
		fastStore.EXPECT().
			UploadObject(ctx, object.MustNewSHA256V1FlatReference("e0643f027c868b649cba8f131459b5f1217c9897080a11391f1f9f6e53e7aa68", 45), gomock.Any(), gomock.Len(0), false).
			DoAndReturn(func(ctx context.Context, reference object.FlatReference, objectContents *object.Contents, leases []time.Time, wantContentsIfIncomplete bool) (object.UploadObjectResult[time.Time], error) {
				require.Equal(t, object.MustNewSHA256V1LocalReference("e0643f027c868b649cba8f131459b5f1217c9897080a11391f1f9f6e53e7aa68", 45, 0, 0, 0), objectContents.GetLocalReference())
				require.Equal(t, []byte{
					// SHA-256 hash.
					0x80, 0x3b, 0x91, 0xe1, 0x10, 0x2b, 0x8e, 0x3b,
					0xfb, 0xed, 0x64, 0x2e, 0xc7, 0xb0, 0xd6, 0xc8,
					0x66, 0x99, 0x48, 0xad, 0xd8, 0xc8, 0x45, 0x84,
					0x97, 0x7e, 0x28, 0x40, 0xa8, 0x13, 0xd2, 0xb6,
					// Size in bytes.
					0x32, 0x00, 0x00,
					// Height.
					0x01,
					// Degree.
					0x01, 0x00,
					// Maximum parents total size in bytes.
					0x00, 0x00,

					// Payload.
					0x48, 0x65, 0x6c, 0x6c, 0x6f,
				}, objectContents.GetPayload())

				return object.UploadObjectComplete[time.Time]{}, nil
			})

		objectContents, err := downloader.DownloadObject(ctx, object.MustNewSHA256V1GlobalReference("example", "e0643f027c868b649cba8f131459b5f1217c9897080a11391f1f9f6e53e7aa68", 45, 2, 1, 50))
		require.NoError(t, err)
		require.Equal(t, object.MustNewSHA256V1LocalReference("e0643f027c868b649cba8f131459b5f1217c9897080a11391f1f9f6e53e7aa68", 45, 2, 1, 50), objectContents.GetLocalReference())
		require.Equal(t, object.MustNewSHA256V1LocalReference("803b91e1102b8e3bfbed642ec7b0d6c8669948add8c84584977e2840a813d2b6", 50, 1, 1, 0), objectContents.GetOutgoingReference(0))
		require.Equal(t, []byte("Hello"), objectContents.GetPayload())
	})
}
