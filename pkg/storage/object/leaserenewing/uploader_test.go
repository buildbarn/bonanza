package leaserenewing_test

import (
	"context"
	"testing"

	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/object"
	"bonanza.build/pkg/storage/object/leaserenewing"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"

	"golang.org/x/sync/semaphore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go.uber.org/mock/gomock"
)

func TestUploader(t *testing.T) {
	ctrl, ctx := gomock.WithContext(context.Background(), t)

	baseUploader := NewMockUploaderForTesting(ctrl)
	uploader := leaserenewing.NewUploader(
		baseUploader,
		semaphore.NewWeighted(1),
		object.NewLimit(&object_pb.Limit{
			Count:     100,
			SizeBytes: 10000000,
		}),
	)

	// Execute these tests repeatedly, so that we can validate that
	// all resources are released properly.
	for i := 0; i <= 10; i++ {
		t.Run("InterruptProcessSingleObject", func(t *testing.T) {
			// It should be possible to interrupt
			// ProcessSingleObject() by providing a context
			// object.
			canceledCtx, cancel := context.WithCancel(ctx)
			cancel()
			uploader.ProcessSingleObject(canceledCtx)
		})

		t.Run("SkipLeafObject", func(t *testing.T) {
			// As leaf objects don't have any outgoing
			// references, this backend should pass through
			// requests in literal form.
			baseUploader.EXPECT().UploadObject(
				ctx,
				object.MustNewSHA256V1GlobalReference("hello/world", "5c2bc09048abb40e95eec67d68c3bbf53a9fcf01e581a1c77fa9068e998ef851", 488549, 0, 0, 0),
				/* contents = */ nil,
				/* childrenLeases = */ nil,
				/* wantContentsIfIncomplete = */ false,
			).Return(object.UploadObjectComplete[any]{
				Lease: "Lease",
			}, nil)

			result, err := uploader.UploadObject(
				ctx,
				object.MustNewSHA256V1GlobalReference("hello/world", "5c2bc09048abb40e95eec67d68c3bbf53a9fcf01e581a1c77fa9068e998ef851", 488549, 0, 0, 0),
				/* contents = */ nil,
				/* childrenLeases = */ nil,
				/* wantContentsIfIncomplete = */ false,
			)
			require.NoError(t, err)
			require.Equal(t, object.UploadObjectComplete[any]{
				Lease: "Lease",
			}, result)
		})

		t.Run("SkipWithChildrenLeases", func(t *testing.T) {
			// This backend should only intercept simple calls that
			// attempt to obtain a lease on an existing object. If
			// contents or leases or children are provided, the
			// request should be passed through in literal form.
			baseUploader.EXPECT().UploadObject(
				ctx,
				object.MustNewSHA256V1GlobalReference("hello/world", "132a1389ca59f99461dba1f307cd7b6bb2e39adbf1dfb3ae02eea2fdf87a346d", 95938, 13, 1, 958050),
				/* contents = */ nil,
				[]any{"Lease 1"},
				/* wantContentsIfIncomplete = */ false,
			).Return(object.UploadObjectComplete[any]{
				Lease: "Lease 2",
			}, nil)

			result, err := uploader.UploadObject(
				ctx,
				object.MustNewSHA256V1GlobalReference("hello/world", "132a1389ca59f99461dba1f307cd7b6bb2e39adbf1dfb3ae02eea2fdf87a346d", 95938, 13, 1, 958050),
				/* contents = */ nil,
				[]any{"Lease 1"},
				/* wantContentsIfIncomplete = */ false,
			)
			require.NoError(t, err)
			require.Equal(t, object.UploadObjectComplete[any]{
				Lease: "Lease 2",
			}, result)
		})

		t.Run("SimpleComplete", func(t *testing.T) {
			// Simple case where the object exists within
			// storage and has up-to-date leases on its
			// outgoing references. The results of the
			// operation should be returned directly.
			baseUploader.EXPECT().UploadObject(
				gomock.Any(),
				object.MustNewSHA256V1GlobalReference("hello/world", "132a1389ca59f99461dba1f307cd7b6bb2e39adbf1dfb3ae02eea2fdf87a346d", 95938, 13, 1, 958050),
				/* contents = */ nil,
				/* childrenLeases = */ nil,
				/* wantContentsIfIncomplete = */ true,
			).Return(object.UploadObjectComplete[any]{
				Lease: "Lease",
			}, nil)

			go uploader.ProcessSingleObject(ctx)
			result, err := uploader.UploadObject(
				ctx,
				object.MustNewSHA256V1GlobalReference("hello/world", "132a1389ca59f99461dba1f307cd7b6bb2e39adbf1dfb3ae02eea2fdf87a346d", 95938, 13, 1, 958050),
				/* contents = */ nil,
				/* childrenLeases = */ nil,
				/* wantContentsIfIncomplete = */ false,
			)
			require.NoError(t, err)
			require.Equal(t, object.UploadObjectComplete[any]{
				Lease: "Lease",
			}, result)
		})

		t.Run("SimpleMissing", func(t *testing.T) {
			// Simple case where the object does not exist. There
			// is little that can be done to renew its leases. The
			// results should be returned directly.
			baseUploader.EXPECT().UploadObject(
				gomock.Any(),
				object.MustNewSHA256V1GlobalReference("hello/world", "3b1bbcb2f4b0cfcb8843cc96b9e6405e630faed02552e5962e1b57947e193b1d", 95938, 13, 1, 958050),
				/* contents = */ nil,
				/* childrenLeases = */ nil,
				/* wantContentsIfIncomplete = */ true,
			).Return(object.UploadObjectMissing[any]{}, nil)

			go uploader.ProcessSingleObject(ctx)
			result, err := uploader.UploadObject(
				ctx,
				object.MustNewSHA256V1GlobalReference("hello/world", "3b1bbcb2f4b0cfcb8843cc96b9e6405e630faed02552e5962e1b57947e193b1d", 95938, 13, 1, 958050),
				/* contents = */ nil,
				/* childrenLeases = */ nil,
				/* wantContentsIfIncomplete = */ false,
			)
			require.NoError(t, err)
			require.Equal(t, object.UploadObjectMissing[any]{}, result)
		})

		t.Run("IncompleteToComplete", func(t *testing.T) {
			// If an object is reported as being incomplete,
			// we should issue further UploadObject() calls
			// to obtain the leases of the children. A final
			// call to UploadObject() should be issued to
			// make the root object complete again.
			contents := object.MustNewContents(
				object_pb.ReferenceFormat_SHA256_V1,
				object.OutgoingReferencesList[object.LocalReference]{
					object.MustNewSHA256V1LocalReference("348a1c1a72e1a3dece7fabff7ac7c78faa06c8994b1573c929fad20da209890d", 95938, 13, 1, 958050),
					object.MustNewSHA256V1LocalReference("4179d172c91a7487c2260c88b28b9892fc1305a904bac4e27af3b43464a08812", 774, 8, 2, 69493),
					object.MustNewSHA256V1LocalReference("48b0846063ab6897a9cd156f11c7de1c07dabfeb02e0b60f1dca1f40e6d3ca2a", 5784, 10, 10, 85439),
					object.MustNewSHA256V1LocalReference("9ac1fefc063b1334861bfe138a89259785df02cef445c36f7e33c8f7c9b56a2c", 48583, 0, 0, 0),
				},
				[]byte("Hello"),
			)
			gomock.InOrder(
				baseUploader.EXPECT().UploadObject(
					gomock.Any(),
					util.Must(object.NewInstanceName("hello/world")).WithLocalReference(contents.GetLocalReference()),
					/* contents = */ nil,
					/* childrenLeases = */ nil,
					/* wantContentsIfIncomplete = */ true,
				).Return(object.UploadObjectIncomplete[any]{
					Contents:                     contents,
					WantOutgoingReferencesLeases: []int{1, 2, 3},
				}, nil),
				baseUploader.EXPECT().UploadObject(
					gomock.Any(),
					object.MustNewSHA256V1GlobalReference("hello/world", "9ac1fefc063b1334861bfe138a89259785df02cef445c36f7e33c8f7c9b56a2c", 48583, 0, 0, 0),
					/* contents = */ nil,
					/* childrenLeases = */ nil,
					/* wantContentsIfIncomplete = */ false,
				).Return(object.UploadObjectComplete[any]{
					Lease: "Lease 1",
				}, nil),
				baseUploader.EXPECT().UploadObject(
					gomock.Any(),
					object.MustNewSHA256V1GlobalReference("hello/world", "4179d172c91a7487c2260c88b28b9892fc1305a904bac4e27af3b43464a08812", 774, 8, 2, 69493),
					/* contents = */ nil,
					/* childrenLeases = */ nil,
					/* wantContentsIfIncomplete = */ true,
				).Return(object.UploadObjectMissing[any]{}, nil),
				baseUploader.EXPECT().UploadObject(
					gomock.Any(),
					object.MustNewSHA256V1GlobalReference("hello/world", "48b0846063ab6897a9cd156f11c7de1c07dabfeb02e0b60f1dca1f40e6d3ca2a", 5784, 10, 10, 85439),
					/* contents = */ nil,
					/* childrenLeases = */ nil,
					/* wantContentsIfIncomplete = */ true,
				).Return(object.UploadObjectComplete[any]{
					Lease: "Lease 2",
				}, nil),
				baseUploader.EXPECT().UploadObject(
					gomock.Any(),
					util.Must(object.NewInstanceName("hello/world")).WithLocalReference(contents.GetLocalReference()),
					/* contents = */ nil,
					[]any{
						nil,
						nil,
						"Lease 2",
						"Lease 1",
					},
					/* wantContentsIfIncomplete = */ false,
				).Return(object.UploadObjectComplete[any]{
					Lease: "Lease 3",
				}, nil),
			)

			go func() {
				for i := 0; i < 4; i++ {
					require.True(t, uploader.ProcessSingleObject(ctx))
				}
			}()
			result, err := uploader.UploadObject(
				ctx,
				util.Must(object.NewInstanceName("hello/world")).WithLocalReference(contents.GetLocalReference()),
				/* contents = */ nil,
				/* childrenLeases = */ nil,
				/* wantContentsIfIncomplete = */ false,
			)
			require.NoError(t, err)
			require.Equal(t, object.UploadObjectComplete[any]{
				Lease: "Lease 3",
			}, result)
		})

		t.Run("InitialUploadObjectFailure", func(t *testing.T) {
			// As a single call to UploadObject() can
			// traverse a full hierarchy of objects, any
			// error messages that are propagated should
			// include the name of the object.
			baseUploader.EXPECT().UploadObject(
				gomock.Any(),
				object.MustNewSHA256V1GlobalReference("hello/world", "52ed02acd303e5b50e5b0d207113b7381c097a5bc4772ee2ac870b58d5339247", 95938, 13, 1, 958050),
				/* contents = */ nil,
				/* childrenLeases = */ nil,
				/* wantContentsIfIncomplete = */ true,
			).Return(nil, status.Error(codes.Unavailable, "Server offline"))

			go uploader.ProcessSingleObject(ctx)
			_, err := uploader.UploadObject(
				ctx,
				object.MustNewSHA256V1GlobalReference("hello/world", "52ed02acd303e5b50e5b0d207113b7381c097a5bc4772ee2ac870b58d5339247", 95938, 13, 1, 958050),
				/* contents = */ nil,
				/* childrenLeases = */ nil,
				/* wantContentsIfIncomplete = */ false,
			)
			testutil.RequireEqualStatus(t, status.Error(codes.Unavailable, "Failed to obtain lease for object with reference SHA256=52ed02acd303e5b50e5b0d207113b7381c097a5bc4772ee2ac870b58d5339247:S=95938:H=13:D=1:M=958208: Server offline"), err)
		})

		t.Run("FinalUploadObjectFailure", func(t *testing.T) {
			contents := object.MustNewContents(
				object_pb.ReferenceFormat_SHA256_V1,
				object.OutgoingReferencesList[object.LocalReference]{
					object.MustNewSHA256V1LocalReference("7ce38508b7b64576dc1719bed9cd450e5f521cf16913ba5ef57c353bc115d946", 59495, 13, 1, 305968),
				},
				[]byte("Hello"),
			)
			gomock.InOrder(
				baseUploader.EXPECT().UploadObject(
					gomock.Any(),
					util.Must(object.NewInstanceName("hello/world")).WithLocalReference(contents.GetLocalReference()),
					/* contents = */ nil,
					/* childrenLeases = */ nil,
					/* wantContentsIfIncomplete = */ true,
				).Return(object.UploadObjectIncomplete[any]{
					Contents:                     contents,
					WantOutgoingReferencesLeases: []int{0},
				}, nil),
				baseUploader.EXPECT().UploadObject(
					gomock.Any(),
					object.MustNewSHA256V1GlobalReference("hello/world", "7ce38508b7b64576dc1719bed9cd450e5f521cf16913ba5ef57c353bc115d946", 59495, 13, 1, 305968),
					/* contents = */ nil,
					/* childrenLeases = */ nil,
					/* wantContentsIfIncomplete = */ true,
				).Return(object.UploadObjectComplete[any]{
					Lease: "Lease",
				}, nil),
				baseUploader.EXPECT().UploadObject(
					gomock.Any(),
					util.Must(object.NewInstanceName("hello/world")).WithLocalReference(contents.GetLocalReference()),
					/* contents = */ nil,
					[]any{"Lease"},
					/* wantContentsIfIncomplete = */ false,
				).Return(nil, status.Error(codes.Unavailable, "Server offline")),
			)

			go func() {
				for i := 0; i < 2; i++ {
					require.True(t, uploader.ProcessSingleObject(ctx))
				}
			}()
			_, err := uploader.UploadObject(
				ctx,
				util.Must(object.NewInstanceName("hello/world")).WithLocalReference(contents.GetLocalReference()),
				/* contents = */ nil,
				/* childrenLeases = */ nil,
				/* wantContentsIfIncomplete = */ false,
			)
			testutil.RequireEqualStatus(t, status.Error(codes.Unavailable, "Failed to update leases for object with reference SHA256=953b996fde970873f7c839d13accd3a7cee159a0c4a41b3fcd32070de1ff954f:S=45:H=14:D=1:M=365568: Server offline"), err)
		})

		t.Run("UnexpectedIncomplete", func(t *testing.T) {
			// If a second call to UploadObject() against a
			// parent object returns UploadObjectIncomplete
			// even though leases are provided, it indicates
			// that the system is unable to make forward
			// progress.
			//
			// We should not report the object as being
			// missing, as that would erroneously cause us
			// to request that the client uploads the object
			// again, which shouldn't be necessary.
			contents := object.MustNewContents(
				object_pb.ReferenceFormat_SHA256_V1,
				object.OutgoingReferencesList[object.LocalReference]{
					object.MustNewSHA256V1LocalReference("076992b65db7a4731f3df48d83b9b4aa49023a4ce4671814da77478317a75824", 458938, 1, 1, 0),
				},
				[]byte("Hello"),
			)
			gomock.InOrder(
				baseUploader.EXPECT().UploadObject(
					gomock.Any(),
					util.Must(object.NewInstanceName("hello/world")).WithLocalReference(contents.GetLocalReference()),
					/* contents = */ nil,
					/* childrenLeases = */ nil,
					/* wantContentsIfIncomplete = */ true,
				).Return(object.UploadObjectIncomplete[any]{
					Contents:                     contents,
					WantOutgoingReferencesLeases: []int{0},
				}, nil),
				baseUploader.EXPECT().UploadObject(
					gomock.Any(),
					object.MustNewSHA256V1GlobalReference("hello/world", "076992b65db7a4731f3df48d83b9b4aa49023a4ce4671814da77478317a75824", 458938, 1, 1, 0),
					/* contents = */ nil,
					/* childrenLeases = */ nil,
					/* wantContentsIfIncomplete = */ true,
				).Return(object.UploadObjectComplete[any]{
					Lease: "Lease",
				}, nil),
				baseUploader.EXPECT().UploadObject(
					gomock.Any(),
					util.Must(object.NewInstanceName("hello/world")).WithLocalReference(contents.GetLocalReference()),
					/* contents = */ nil,
					[]any{"Lease"},
					/* wantContentsIfIncomplete = */ false,
				).Return(object.UploadObjectIncomplete[any]{
					Contents:                     contents,
					WantOutgoingReferencesLeases: []int{0},
				}, nil),
			)

			go func() {
				for i := 0; i < 2; i++ {
					require.True(t, uploader.ProcessSingleObject(ctx))
				}
			}()
			_, err := uploader.UploadObject(
				ctx,
				util.Must(object.NewInstanceName("hello/world")).WithLocalReference(contents.GetLocalReference()),
				/* contents = */ nil,
				/* childrenLeases = */ nil,
				/* wantContentsIfIncomplete = */ false,
			)
			testutil.RequireEqualStatus(t, status.Error(codes.Internal, "Lease of object with reference SHA256=076992b65db7a4731f3df48d83b9b4aa49023a4ce4671814da77478317a75824:S=458938:H=1:D=1:M=0 expired before renewing leases of object with reference SHA256=0aec331c4c9622e592d6cbfc584f9f67f828e1c78d517247174c1f9b431021f2:S=45:H=2:D=1:M=459008 completed"), err)
		})

		t.Run("UnexpectedIncomplete", func(t *testing.T) {
			// The initial UploadObject() call against the
			// parent object should extend the lifetime of
			// the object. This means that if the object is
			// subsequently reported as being missing, the
			// system is misbehaving.
			contents := object.MustNewContents(
				object_pb.ReferenceFormat_SHA256_V1,
				object.OutgoingReferencesList[object.LocalReference]{
					object.MustNewSHA256V1LocalReference("f015273795ac49601603e931ec281af32a65b3e49f0b11df83dc8f8ecb0f1934", 458938, 1, 1, 0),
				},
				[]byte("Hello"),
			)
			gomock.InOrder(
				baseUploader.EXPECT().UploadObject(
					gomock.Any(),
					util.Must(object.NewInstanceName("hello/world")).WithLocalReference(contents.GetLocalReference()),
					/* contents = */ nil,
					/* childrenLeases = */ nil,
					/* wantContentsIfIncomplete = */ true,
				).Return(object.UploadObjectIncomplete[any]{
					Contents:                     contents,
					WantOutgoingReferencesLeases: []int{0},
				}, nil),
				baseUploader.EXPECT().UploadObject(
					gomock.Any(),
					object.MustNewSHA256V1GlobalReference("hello/world", "f015273795ac49601603e931ec281af32a65b3e49f0b11df83dc8f8ecb0f1934", 458938, 1, 1, 0),
					/* contents = */ nil,
					/* childrenLeases = */ nil,
					/* wantContentsIfIncomplete = */ true,
				).Return(object.UploadObjectComplete[any]{
					Lease: "Lease",
				}, nil),
				baseUploader.EXPECT().UploadObject(
					gomock.Any(),
					util.Must(object.NewInstanceName("hello/world")).WithLocalReference(contents.GetLocalReference()),
					/* contents = */ nil,
					[]any{"Lease"},
					/* wantContentsIfIncomplete = */ false,
				).Return(object.UploadObjectMissing[any]{}, nil),
			)

			go func() {
				for i := 0; i < 2; i++ {
					require.True(t, uploader.ProcessSingleObject(ctx))
				}
			}()
			_, err := uploader.UploadObject(
				ctx,
				util.Must(object.NewInstanceName("hello/world")).WithLocalReference(contents.GetLocalReference()),
				/* contents = */ nil,
				/* childrenLeases = */ nil,
				/* wantContentsIfIncomplete = */ false,
			)
			testutil.RequireEqualStatus(t, status.Error(codes.Internal, "Object with reference SHA256=aab3bff658ddaa11e30e3bb1ce737995466f91175e3f54c3fd1efe98eef62410:S=45:H=2:D=1:M=459008 went missing before renewing of leases completed"), err)
		})

		t.Run("IncompleteWithMissingChild", func(t *testing.T) {
			// If an object with two children has one
			// missing child, we should still issue a second
			// UploadObject() call against the parent to
			// store the lease of the child that is present,
			// as that saves work later on.
			//
			// The client should receive
			// WantOutgoingReferencesLeases from the second
			// call to UploadObject(), with the contents
			// that were obtained during the first call.
			contents := object.MustNewContents(
				object_pb.ReferenceFormat_SHA256_V1,
				object.OutgoingReferencesList[object.LocalReference]{
					object.MustNewSHA256V1LocalReference("7c3b027d32a1201a965e26f613856db29eddf9458e4f0f30cf855d34eb634d71", 458938, 0, 0, 0),
					object.MustNewSHA256V1LocalReference("a9947f1669dab14d802d990f40578782b234821b23bc3da1f9aaf6c1db0b5ebf", 458938, 0, 0, 0),
				},
				[]byte("Hello"),
			)
			gomock.InOrder(
				baseUploader.EXPECT().UploadObject(
					gomock.Any(),
					util.Must(object.NewInstanceName("hello/world")).WithLocalReference(contents.GetLocalReference()),
					/* contents = */ nil,
					/* childrenLeases = */ nil,
					/* wantContentsIfIncomplete = */ true,
				).Return(object.UploadObjectIncomplete[any]{
					Contents:                     contents,
					WantOutgoingReferencesLeases: []int{0, 1},
				}, nil),
				baseUploader.EXPECT().UploadObject(
					gomock.Any(),
					object.MustNewSHA256V1GlobalReference("hello/world", "7c3b027d32a1201a965e26f613856db29eddf9458e4f0f30cf855d34eb634d71", 458938, 0, 0, 0),
					/* contents = */ nil,
					/* childrenLeases = */ nil,
					/* wantContentsIfIncomplete = */ false,
				).Return(object.UploadObjectComplete[any]{
					Lease: "Lease",
				}, nil),
				baseUploader.EXPECT().UploadObject(
					gomock.Any(),
					object.MustNewSHA256V1GlobalReference("hello/world", "a9947f1669dab14d802d990f40578782b234821b23bc3da1f9aaf6c1db0b5ebf", 458938, 0, 0, 0),
					/* contents = */ nil,
					/* childrenLeases = */ nil,
					/* wantContentsIfIncomplete = */ false,
				).Return(object.UploadObjectMissing[any]{}, nil),
				baseUploader.EXPECT().UploadObject(
					gomock.Any(),
					util.Must(object.NewInstanceName("hello/world")).WithLocalReference(contents.GetLocalReference()),
					/* contents = */ nil,
					[]any{
						"Lease",
						nil,
					},
					/* wantContentsIfIncomplete = */ false,
				).Return(object.UploadObjectIncomplete[any]{
					WantOutgoingReferencesLeases: []int{1},
				}, nil),
			)

			go func() {
				for i := 0; i < 3; i++ {
					require.True(t, uploader.ProcessSingleObject(ctx))
				}
			}()
			result, err := uploader.UploadObject(
				ctx,
				util.Must(object.NewInstanceName("hello/world")).WithLocalReference(contents.GetLocalReference()),
				/* contents = */ nil,
				/* childrenLeases = */ nil,
				/* wantContentsIfIncomplete = */ true,
			)
			require.NoError(t, err)
			require.Equal(t, object.UploadObjectIncomplete[any]{
				Contents:                     contents,
				WantOutgoingReferencesLeases: []int{1},
			}, result)
		})

		t.Run("IncompleteWithMultipleMissingChildren", func(t *testing.T) {
			// This test is identical to IncompleteWithMissingChild,
			// except that multiple children are missing. In this
			// case the upload result should contain the indices of
			// both children that are missing.
			contents := object.MustNewContents(
				object_pb.ReferenceFormat_SHA256_V1,
				object.OutgoingReferencesList[object.LocalReference]{
					object.MustNewSHA256V1LocalReference("5a5fd94c0c2a6d5783148772a37c087a835744f2b63eb7c9009dea8a7018c80d", 95852, 0, 0, 0),
					object.MustNewSHA256V1LocalReference("631fa5ce15d7ea9c9f49813a5e67ac13fe4dd1d590f302c0feb8c23adb390130", 95852, 0, 0, 0),
					object.MustNewSHA256V1LocalReference("e4a58e4210d9c962faa8dcfab6cae16278cc14621f85a809d0c628e49dd7c2f0", 95852, 0, 0, 0),
				},
				[]byte("Hello"),
			)
			gomock.InOrder(
				baseUploader.EXPECT().UploadObject(
					gomock.Any(),
					util.Must(object.NewInstanceName("hello/world")).WithLocalReference(contents.GetLocalReference()),
					/* contents = */ nil,
					/* childrenLeases = */ nil,
					/* wantContentsIfIncomplete = */ true,
				).Return(object.UploadObjectIncomplete[any]{
					Contents:                     contents,
					WantOutgoingReferencesLeases: []int{0, 1, 2},
				}, nil),
				baseUploader.EXPECT().UploadObject(
					gomock.Any(),
					object.MustNewSHA256V1GlobalReference("hello/world", "5a5fd94c0c2a6d5783148772a37c087a835744f2b63eb7c9009dea8a7018c80d", 95852, 0, 0, 0),
					/* contents = */ nil,
					/* childrenLeases = */ nil,
					/* wantContentsIfIncomplete = */ false,
				).Return(object.UploadObjectComplete[any]{
					Lease: "Lease",
				}, nil),
				baseUploader.EXPECT().UploadObject(
					gomock.Any(),
					object.MustNewSHA256V1GlobalReference("hello/world", "631fa5ce15d7ea9c9f49813a5e67ac13fe4dd1d590f302c0feb8c23adb390130", 95852, 0, 0, 0),
					/* contents = */ nil,
					/* childrenLeases = */ nil,
					/* wantContentsIfIncomplete = */ false,
				).Return(object.UploadObjectMissing[any]{}, nil),
				baseUploader.EXPECT().UploadObject(
					gomock.Any(),
					object.MustNewSHA256V1GlobalReference("hello/world", "e4a58e4210d9c962faa8dcfab6cae16278cc14621f85a809d0c628e49dd7c2f0", 95852, 0, 0, 0),
					/* contents = */ nil,
					/* childrenLeases = */ nil,
					/* wantContentsIfIncomplete = */ false,
				).Return(object.UploadObjectMissing[any]{}, nil),
				baseUploader.EXPECT().UploadObject(
					gomock.Any(),
					util.Must(object.NewInstanceName("hello/world")).WithLocalReference(contents.GetLocalReference()),
					/* contents = */ nil,
					[]any{
						"Lease",
						nil,
						nil,
					},
					/* wantContentsIfIncomplete = */ false,
				).Return(object.UploadObjectIncomplete[any]{
					WantOutgoingReferencesLeases: []int{1, 2},
				}, nil),
			)

			go func() {
				for i := 0; i < 4; i++ {
					require.True(t, uploader.ProcessSingleObject(ctx))
				}
			}()
			result, err := uploader.UploadObject(
				ctx,
				util.Must(object.NewInstanceName("hello/world")).WithLocalReference(contents.GetLocalReference()),
				/* contents = */ nil,
				/* childrenLeases = */ nil,
				/* wantContentsIfIncomplete = */ true,
			)
			require.NoError(t, err)
			require.Equal(t, object.UploadObjectIncomplete[any]{
				Contents:                     contents,
				WantOutgoingReferencesLeases: []int{1, 2},
			}, result)
		})
	}
}
