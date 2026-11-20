package flatbacked_test

import (
	"testing"
	"time"

	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/object"
	object_flatbacked "bonanza.build/pkg/storage/object/flatbacked"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go.uber.org/mock/gomock"
)

func TestStore(t *testing.T) {
	ctrl, ctx := gomock.WithContext(t.Context(), t)

	flatStore := NewMockFlatStoreForTesting(ctrl)
	clock := NewMockClock(ctrl)
	var watermark time.Time
	leasesMap := NewMockLeasesMap(ctrl)
	store := object_flatbacked.NewStore(
		flatStore,
		clock,
		func() time.Time { return watermark },
		/* leaseRenewalFraction = */ 0.5,
		leasesMap,
	)

	t.Run("DownloadObject", func(t *testing.T) {
		t.Run("DownloadFailure", func(t *testing.T) {
			flatStore.EXPECT().DownloadObject(ctx, object.MustNewSHA256V1FlatReference("c4b950448ae9553f8d3dc7b08d0c3ba817287a065ff42aa66b1fc58b8a6ba997", 4845)).
				Return(nil, status.Error(codes.NotFound, "Object not found"))

			_, err := store.DownloadObject(ctx, object.MustNewSHA256V1LocalReference("c4b950448ae9553f8d3dc7b08d0c3ba817287a065ff42aa66b1fc58b8a6ba997", 4845, 2, 2, 593045))
			testutil.RequireEqualStatus(t, status.Error(codes.NotFound, "Object not found"), err)
		})

		t.Run("UnflattenFailure", func(t *testing.T) {
			// Objects are downloaded from the storage
			// backend using flat references. This means
			// that this backend performs the unflattening,
			// which may fail.
			flatStore.EXPECT().DownloadObject(ctx, object.MustNewSHA256V1FlatReference("527deddd1ace8a41a54d3768bfc35b41a5ab2a59be011b84b48fdee0753bdf5c", 45)).
				Return(object.MustNewContents(object_pb.ReferenceFormat_SHA256_V1, nil, []byte{
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
				}), nil)

			_, err := store.DownloadObject(ctx, object.MustNewSHA256V1LocalReference("527deddd1ace8a41a54d3768bfc35b41a5ab2a59be011b84b48fdee0753bdf5c", 45, 1, 1, 0))
			testutil.RequireEqualStatus(t, status.Error(codes.InvalidArgument, "Unflatten object contents: Outgoing reference at index 0 has height 255, which is too high"), err)
		})

		t.Run("Success", func(t *testing.T) {
			flatStore.EXPECT().DownloadObject(ctx, object.MustNewSHA256V1FlatReference("e0643f027c868b649cba8f131459b5f1217c9897080a11391f1f9f6e53e7aa68", 45)).
				Return(object.MustNewContents(object_pb.ReferenceFormat_SHA256_V1, nil, []byte{
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
				}), nil)

			contents, err := store.DownloadObject(ctx, object.MustNewSHA256V1LocalReference("e0643f027c868b649cba8f131459b5f1217c9897080a11391f1f9f6e53e7aa68", 45, 2, 1, 50))
			require.NoError(t, err)
			require.Equal(t, object.MustNewSHA256V1LocalReference("e0643f027c868b649cba8f131459b5f1217c9897080a11391f1f9f6e53e7aa68", 45, 2, 1, 50), contents.GetLocalReference())
			require.Equal(t, object.MustNewSHA256V1LocalReference("803b91e1102b8e3bfbed642ec7b0d6c8669948add8c84584977e2840a813d2b6", 50, 1, 1, 0), contents.GetOutgoingReference(0))
			require.Equal(t, []byte("Hello"), contents.GetPayload())
		})
	})

	t.Run("UploadObject", func(t *testing.T) {
		t.Run("UploadFailure", func(t *testing.T) {
			clock.EXPECT().Now().Return(time.Unix(1750779383, 0))
			flatStore.EXPECT().UploadObject(
				ctx,
				object.MustNewSHA256V1FlatReference("e0643f027c868b649cba8f131459b5f1217c9897080a11391f1f9f6e53e7aa68", 45),
				object.MustNewContents(object_pb.ReferenceFormat_SHA256_V1, nil, []byte{
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
				}),
				/* childrenLeases = */ nil,
				/* wantContentsIfIncomplete = */ false,
			).Return(nil, status.Error(codes.Unavailable, "Server offline"))

			_, err := store.UploadObject(
				ctx,
				object.MustNewSHA256V1LocalReference("e0643f027c868b649cba8f131459b5f1217c9897080a11391f1f9f6e53e7aa68", 45, 2, 1, 50),
				object.MustNewContents(
					object_pb.ReferenceFormat_SHA256_V1,
					[]object.LocalReference{
						object.MustNewSHA256V1LocalReference("803b91e1102b8e3bfbed642ec7b0d6c8669948add8c84584977e2840a813d2b6", 50, 1, 1, 0),
					},
					[]byte("Hello"),
				),
				/* childrenLeases = */ nil,
				/* wantContentsIfIncomplete = */ false,
			)
			testutil.RequireEqualStatus(t, status.Error(codes.Unavailable, "Server offline"), err)
		})

		t.Run("ObjectMissing", func(t *testing.T) {
			clock.EXPECT().Now().Return(time.Unix(1750779496, 0))
			flatStore.EXPECT().UploadObject(
				ctx,
				object.MustNewSHA256V1FlatReference("1aceda1d6fb0e4b8b2a05391f31a8db60cb380a064bd0dc9ac5bd9fbbe4d21dd", 200),
				/* contents = */ nil,
				/* childrenLeases = */ nil,
				/* wantContentsIfIncomplete = */ false,
			).Return(object.UploadObjectMissing[time.Time]{}, nil)

			result, err := store.UploadObject(
				ctx,
				object.MustNewSHA256V1LocalReference("1aceda1d6fb0e4b8b2a05391f31a8db60cb380a064bd0dc9ac5bd9fbbe4d21dd", 200, 3, 2, 58483),
				/* contents = */ nil,
				/* childrenLeases = */ nil,
				/* wantContentsIfIncomplete = */ true,
			)
			require.NoError(t, err)
			require.Equal(t, object.UploadObjectMissing[object_flatbacked.Lease]{}, result)
		})

		t.Run("LeafComplete", func(t *testing.T) {
			// If the storage backend is in possession of a
			// leaf object, we don't need to do any work in
			// addition to generating a lease for it.
			clock.EXPECT().Now().Return(time.Unix(1750779732, 0))
			flatStore.EXPECT().UploadObject(
				ctx,
				object.MustNewSHA256V1FlatReference("dc06bda3e352fb5b96ad1daf126b7e7faadb5f9ce8d29c512a64a9d351612d13", 600),
				/* contents = */ nil,
				/* childrenLeases = */ nil,
				/* wantContentsIfIncomplete = */ false,
			).Return(object.UploadObjectComplete[time.Time]{
				Lease: time.Unix(1750779700, 0),
			}, nil)

			result, err := store.UploadObject(
				ctx,
				object.MustNewSHA256V1LocalReference("dc06bda3e352fb5b96ad1daf126b7e7faadb5f9ce8d29c512a64a9d351612d13", 600, 0, 0, 0),
				/* contents = */ nil,
				/* childrenLeases = */ nil,
				/* wantContentsIfIncomplete = */ false,
			)
			require.NoError(t, err)
			require.Equal(t, object.UploadObjectComplete[object_flatbacked.Lease]{
				Lease: 1750779700000000000,
			}, result)
		})

		t.Run("ParentDownloadNotFound", func(t *testing.T) {
			// For parent objects we need to download
			// them, so that we know what the object's
			// children are. However, if this returns
			// NOT_FOUND it is inconsistent with the results
			// of the UploadObject() call.
			clock.EXPECT().Now().Return(time.Unix(1750779881, 0))
			flatStore.EXPECT().UploadObject(
				ctx,
				object.MustNewSHA256V1FlatReference("48f4ede314091089d0ad0767f593621a5e0296d1c46840a4d29cae3b9b35788e", 600),
				/* contents = */ nil,
				/* childrenLeases = */ nil,
				/* wantContentsIfIncomplete = */ false,
			).Return(object.UploadObjectComplete[time.Time]{Lease: time.Unix(1750784000, 0)}, nil)
			flatStore.EXPECT().DownloadObject(ctx, object.MustNewSHA256V1FlatReference("48f4ede314091089d0ad0767f593621a5e0296d1c46840a4d29cae3b9b35788e", 600)).
				Return(nil, status.Error(codes.NotFound, "Object not found"))

			_, err := store.UploadObject(
				ctx,
				object.MustNewSHA256V1LocalReference("48f4ede314091089d0ad0767f593621a5e0296d1c46840a4d29cae3b9b35788e", 600, 2, 1, 405),
				/* contents = */ nil,
				/* childrenLeases = */ nil,
				/* wantContentsIfIncomplete = */ true,
			)
			testutil.RequireEqualStatus(t, status.Error(codes.Internal, "Backend reported that the object does not exist, even though uploading it reported it as being complete"), err)
		})

		t.Run("ParentDownloadNotFound", func(t *testing.T) {
			// For parent objects we need to download
			// them, so that we know what the object's
			// children are. However, if this returns
			// NOT_FOUND it is inconsistent with the results
			// of the UploadObject() call.
			clock.EXPECT().Now().Return(time.Unix(1750779881, 0))
			flatStore.EXPECT().UploadObject(
				ctx,
				object.MustNewSHA256V1FlatReference("48f4ede314091089d0ad0767f593621a5e0296d1c46840a4d29cae3b9b35788e", 600),
				/* contents = */ nil,
				/* childrenLeases = */ nil,
				/* wantContentsIfIncomplete = */ false,
			).Return(object.UploadObjectComplete[time.Time]{Lease: time.Unix(1750784000, 0)}, nil)
			flatStore.EXPECT().DownloadObject(ctx, object.MustNewSHA256V1FlatReference("48f4ede314091089d0ad0767f593621a5e0296d1c46840a4d29cae3b9b35788e", 600)).
				Return(nil, status.Error(codes.NotFound, "Object not found"))

			_, err := store.UploadObject(
				ctx,
				object.MustNewSHA256V1LocalReference("48f4ede314091089d0ad0767f593621a5e0296d1c46840a4d29cae3b9b35788e", 600, 2, 1, 405),
				/* contents = */ nil,
				/* childrenLeases = */ nil,
				/* wantContentsIfIncomplete = */ true,
			)
			testutil.RequireEqualStatus(t, status.Error(codes.Internal, "Backend reported that the object does not exist, even though uploading it reported it as being complete"), err)
		})

		t.Run("Complete", func(t *testing.T) {
			// Leases below the watermark are expired. As
			// leases of the children are provided, leases
			// that are only close to the watermark are
			// accepted.
			watermark = time.Unix(1750784450, 0)
			clock.EXPECT().Now().Return(time.Unix(1750784570, 0)).Times(2)
			flatStore.EXPECT().UploadObject(
				ctx,
				object.MustNewSHA256V1FlatReference("b4684e6f6b2b7c2685d4732965be683c3cc6c4917b866b9fc6ef641c2bcc0bdc", 85),
				object.MustNewContents(object_pb.ReferenceFormat_SHA256_V1, nil, []byte{
					// SHA-256 hash.
					0x39, 0x5a, 0x01, 0x57, 0xd8, 0xd3, 0x11, 0x71,
					0xcb, 0x43, 0xa4, 0xab, 0xc1, 0xe3, 0x7b, 0xf6,
					0xd3, 0x8a, 0x46, 0xe0, 0x53, 0x03, 0x7c, 0x2c,
					0x38, 0x01, 0x6d, 0x0d, 0xd6, 0xf4, 0xec, 0x67,
					// Size in bytes.
					0x52, 0x4f, 0x01,
					// Height.
					0x00,
					// Degree.
					0x00, 0x00,
					// Maximum parents total size in bytes.
					0x00, 0x00,

					// SHA-256 hash.
					0xbf, 0x67, 0x61, 0x41, 0xb3, 0x4a, 0xd9, 0xb7,
					0xf8, 0x19, 0xd6, 0x2e, 0x7b, 0x7e, 0x32, 0xb9,
					0xc8, 0xa4, 0x0c, 0xb3, 0x98, 0x16, 0x87, 0x7c,
					0xc7, 0x66, 0x5f, 0x2f, 0xd7, 0x0d, 0xd8, 0x8f,
					// Size in bytes.
					0xa4, 0x72, 0x00,
					// Height.
					0x00,
					// Degree.
					0x00, 0x00,
					// Maximum parents total size in bytes.
					0x00, 0x00,

					// Payload.
					0x48, 0x65, 0x6c, 0x6c, 0x6f,
				}),
				/* childrenLeases = */ nil,
				/* wantContentsIfIncomplete = */ false,
			).Return(object.UploadObjectComplete[time.Time]{Lease: time.Unix(1750784520, 0)}, nil)
			leasesMap.EXPECT().Put(
				object.MustNewSHA256V1LocalReference("395a0157d8d31171cb43a4abc1e37bf6d38a46e053037c2c38016d0dd6f4ec67", 85842, 0, 0, 0),
				/* value = */ object_flatbacked.Lease(1750784560000000000),
				/* leaseExpirationCutoff = */ object_flatbacked.Lease(1750784450000000000),
			)
			leasesMap.EXPECT().Get(
				object.MustNewSHA256V1LocalReference("bf676141b34ad9b7f819d62e7b7e32b9c8a40cb39816877cc7665f2fd70dd88f", 29348, 0, 0, 0),
				/* leaseExpirationCutoff = */ object_flatbacked.Lease(1750784450000000000),
			).Return(object_flatbacked.Lease(1750784500000000000), nil)

			result, err := store.UploadObject(
				ctx,
				object.MustNewSHA256V1LocalReference("b4684e6f6b2b7c2685d4732965be683c3cc6c4917b866b9fc6ef641c2bcc0bdc", 85, 1, 2, 0),
				object.MustNewContents(
					object_pb.ReferenceFormat_SHA256_V1,
					[]object.LocalReference{
						object.MustNewSHA256V1LocalReference("395a0157d8d31171cb43a4abc1e37bf6d38a46e053037c2c38016d0dd6f4ec67", 85842, 0, 0, 0),
						object.MustNewSHA256V1LocalReference("bf676141b34ad9b7f819d62e7b7e32b9c8a40cb39816877cc7665f2fd70dd88f", 29348, 0, 0, 0),
					},
					[]byte("Hello"),
				),
				/* childrenLeases = */ []object_flatbacked.Lease{
					1750784560000000000,
					0,
				},
				/* wantContentsIfIncomplete = */ true,
			)
			require.NoError(t, err)

			// The lease of the parent is the minimum of
			// its own lease and those of its children.
			require.Equal(t, object.UploadObjectComplete[object_flatbacked.Lease]{
				Lease: 1750784500000000000,
			}, result)
		})

		// TODO: Make testing coverage more complete.
	})
}
