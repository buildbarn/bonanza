package leaserenewing_test

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/object"
	"bonanza.build/pkg/storage/tag"
	"bonanza.build/pkg/storage/tag/leaserenewing"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"

	"go.uber.org/mock/gomock"
	"golang.org/x/sync/semaphore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestResolver(t *testing.T) {
	ctrl, ctx := gomock.WithContext(context.Background(), t)

	tagStore := NewMockTagStore(ctrl)
	objectUploader := NewMockObjectUploader(ctrl)
	resolver := leaserenewing.NewResolver[object.Namespace, object.GlobalReference, any](
		tagStore,
		objectUploader,
		semaphore.NewWeighted(10),
		object.NewLimit(&object_pb.Limit{
			Count:     4,
			SizeBytes: 100000,
		}),
	)

	// Run the worker loop in the background.
	workerCtx, cancelWorker := context.WithCancel(ctx)
	var workerDone sync.WaitGroup
	workerDone.Add(1)
	go func() {
		defer workerDone.Done()
		for resolver.ProcessSingleObject(workerCtx) {
		}
	}()
	defer func() {
		cancelWorker()
		workerDone.Wait()
	}()

	namespace := util.Must(object.NewNamespace(&object_pb.Namespace{
		InstanceName:    "hello/world",
		ReferenceFormat: object_pb.ReferenceFormat_SHA256_V1,
	}))
	key1 := tag.Key{Hash: [...]byte{1, 31: 0}}
	key2 := tag.Key{Hash: [...]byte{2, 31: 0}}

	// DAGs of height two, where only the second leaf needs to have
	// its lease renewed. Both roots share the same middle object.
	leaf1Reference := object.MustNewSHA256V1LocalReference("0000000000000000000000000000000000000000000000000000000000000001", 100, 0, 0, 0)
	leaf2Reference := object.MustNewSHA256V1LocalReference("0000000000000000000000000000000000000000000000000000000000000002", 100, 0, 0, 0)
	leaf3Reference := object.MustNewSHA256V1LocalReference("0000000000000000000000000000000000000000000000000000000000000003", 100, 0, 0, 0)
	middleContents := object.MustNewContents(object_pb.ReferenceFormat_SHA256_V1, []object.LocalReference{leaf1Reference, leaf2Reference}, []byte("Middle"))
	root1Contents := object.MustNewContents(object_pb.ReferenceFormat_SHA256_V1, []object.LocalReference{middleContents.GetLocalReference()}, []byte("Root 1"))
	root2Children := []object.LocalReference{middleContents.GetLocalReference(), leaf3Reference}
	middleIndex, leaf3Index := 0, 1
	if bytes.Compare(root2Children[0].GetRawReference(), root2Children[1].GetRawReference()) > 0 {
		root2Children[0], root2Children[1] = root2Children[1], root2Children[0]
		middleIndex, leaf3Index = 1, 0
	}
	root2Contents := object.MustNewContents(object_pb.ReferenceFormat_SHA256_V1, root2Children, []byte("Root 2"))
	newSignedValue := func(rootContents *object.Contents) tag.SignedValue {
		return tag.SignedValue{
			Value: tag.Value{
				Reference: rootContents.GetLocalReference(),
				Timestamp: time.Unix(1000, 0),
			},
		}
	}
	signedValue1 := newSignedValue(root1Contents)
	signedValue2 := newSignedValue(root2Contents)

	expectRootIncomplete := func(rootContents *object.Contents) *gomock.Call {
		return objectUploader.EXPECT().
			UploadObject(gomock.Any(), namespace.WithLocalReference(rootContents.GetLocalReference()), nil, nil, true).
			Return(object.UploadObjectIncomplete[any]{
				Contents:                     rootContents,
				WantOutgoingReferencesLeases: []int{0},
			}, nil)
	}
	expectMiddleIncomplete := func() *gomock.Call {
		return objectUploader.EXPECT().
			UploadObject(gomock.Any(), namespace.WithLocalReference(middleContents.GetLocalReference()), nil, nil, true).
			Return(object.UploadObjectIncomplete[any]{
				Contents:                     middleContents,
				WantOutgoingReferencesLeases: []int{1},
			}, nil)
	}
	expectLeaf2 := func(result object.UploadObjectResult[any]) *gomock.Call {
		return objectUploader.EXPECT().
			UploadObject(gomock.Any(), namespace.WithLocalReference(leaf2Reference), nil, nil, false).
			Return(result, nil)
	}
	expectUpdateLeases := func(contents *object.Contents, childrenLeases []any, lease string) *gomock.Call {
		return objectUploader.EXPECT().
			UploadObject(gomock.Any(), namespace.WithLocalReference(contents.GetLocalReference()), nil, childrenLeases, false).
			Return(object.UploadObjectComplete[any]{Lease: lease}, nil)
	}

	// Execute these tests repeatedly, so that we can validate that
	// all resources are released properly. If capacity of the limit
	// were leaked, later iterations would hang.
	for i := 0; i < 10; i++ {
		t.Run("AlreadyComplete", func(t *testing.T) {
			// If the tag already has a valid lease, no
			// leases need to be renewed.
			tagStore.EXPECT().ResolveTag(ctx, namespace, key1, nil).Return(signedValue1, true, nil)

			actualSignedValue, complete, err := resolver.ResolveTag(ctx, namespace, key1, nil)
			require.NoError(t, err)
			require.True(t, complete)
			require.Equal(t, signedValue1, actualSignedValue)
		})

		t.Run("Renewed", func(t *testing.T) {
			// Leases should only be renewed for the parts
			// of the DAG that storage reports as needing
			// renewal. Leases of other children are left
			// at the zero value.
			tagStore.EXPECT().ResolveTag(ctx, namespace, key1, nil).Return(signedValue1, false, nil)
			gomock.InOrder(
				expectRootIncomplete(root1Contents),
				expectMiddleIncomplete(),
				expectLeaf2(object.UploadObjectComplete[any]{Lease: "Leaf 2 lease"}),
				expectUpdateLeases(middleContents, []any{nil, "Leaf 2 lease"}, "Middle lease"),
				expectUpdateLeases(root1Contents, []any{"Middle lease"}, "Root 1 lease"),
				tagStore.EXPECT().UpdateTag(gomock.Any(), namespace, key1, signedValue1, "Root 1 lease"),
			)

			actualSignedValue, complete, err := resolver.ResolveTag(ctx, namespace, key1, nil)
			require.NoError(t, err)
			require.True(t, complete)
			require.Equal(t, signedValue1, actualSignedValue)
		})

		t.Run("ObjectMissing", func(t *testing.T) {
			// If an object underneath the root is absent,
			// the tag should be reported as being
			// incomplete. Leases of objects above it are
			// still updated, so that retrying after the
			// absent object is uploaded is cheaper.
			tagStore.EXPECT().ResolveTag(ctx, namespace, key1, nil).Return(signedValue1, false, nil)
			gomock.InOrder(
				expectRootIncomplete(root1Contents),
				expectMiddleIncomplete(),
				expectLeaf2(object.UploadObjectMissing[any]{}),
				expectUpdateLeases(middleContents, []any{nil, nil}, "Middle lease"),
				expectUpdateLeases(root1Contents, []any{nil}, "Root 1 lease"),
			)

			actualSignedValue, complete, err := resolver.ResolveTag(ctx, namespace, key1, nil)
			require.NoError(t, err)
			require.False(t, complete)
			require.Equal(t, signedValue1, actualSignedValue)
		})

		t.Run("StorageFailure", func(t *testing.T) {
			// Errors should be propagated to all objects
			// above the one that failed.
			tagStore.EXPECT().ResolveTag(ctx, namespace, key1, nil).Return(signedValue1, false, nil)
			gomock.InOrder(
				expectRootIncomplete(root1Contents),
				expectMiddleIncomplete(),
				objectUploader.EXPECT().
					UploadObject(gomock.Any(), namespace.WithLocalReference(leaf2Reference), nil, nil, false).
					Return(nil, status.Error(codes.Unavailable, "Server offline")),
			)

			_, _, err := resolver.ResolveTag(ctx, namespace, key1, nil)
			testutil.RequirePrefixedStatus(t, status.Error(codes.Unavailable, "Failed to obtain lease for object with reference "), err)
		})

		t.Run("OverlappingSubgraphs", func(t *testing.T) {
			// Two tags are resolved concurrently, whose
			// DAGs share the same middle object. The lease
			// of the middle object should only be renewed
			// once.
			tagStore.EXPECT().ResolveTag(ctx, namespace, key1, nil).Return(signedValue1, false, nil)
			tagStore.EXPECT().ResolveTag(ctx, namespace, key2, nil).Return(signedValue2, false, nil)

			// Only start resolving the second tag once the
			// middle object is being processed. The second
			// root has an additional child, which is only
			// checked after the second root has joined the
			// renewal of the middle object. Don't let
			// renewal of the middle object complete before
			// that happens.
			middleChecked := make(chan struct{})
			leaf3Checked := make(chan struct{})
			checkRoot1 := expectRootIncomplete(root1Contents)
			checkMiddle := expectMiddleIncomplete().
				After(checkRoot1).
				Do(func(context.Context, object.GlobalReference, *object.Contents, []any, bool) { close(middleChecked) })
			checkLeaf2 := expectLeaf2(object.UploadObjectComplete[any]{Lease: "Leaf 2 lease"}).
				After(checkMiddle).
				Do(func(context.Context, object.GlobalReference, *object.Contents, []any, bool) { <-leaf3Checked })
			updateMiddle := expectUpdateLeases(middleContents, []any{nil, "Leaf 2 lease"}, "Middle lease").After(checkLeaf2)
			gomock.InOrder(
				expectUpdateLeases(root1Contents, []any{"Middle lease"}, "Root 1 lease").After(updateMiddle),
				tagStore.EXPECT().UpdateTag(gomock.Any(), namespace, key1, signedValue1, "Root 1 lease"),
			)

			checkRoot2 := objectUploader.EXPECT().
				UploadObject(gomock.Any(), namespace.WithLocalReference(root2Contents.GetLocalReference()), nil, nil, true).
				Return(object.UploadObjectIncomplete[any]{
					Contents:                     root2Contents,
					WantOutgoingReferencesLeases: []int{0, 1},
				}, nil)
			checkLeaf3 := objectUploader.EXPECT().
				UploadObject(gomock.Any(), namespace.WithLocalReference(leaf3Reference), nil, nil, false).
				Return(object.UploadObjectComplete[any]{Lease: "Leaf 3 lease"}, nil).
				After(checkRoot2).
				Do(func(context.Context, object.GlobalReference, *object.Contents, []any, bool) { close(leaf3Checked) })
			root2Leases := []any{nil, nil}
			root2Leases[middleIndex] = "Middle lease"
			root2Leases[leaf3Index] = "Leaf 3 lease"
			gomock.InOrder(
				expectUpdateLeases(root2Contents, root2Leases, "Root 2 lease").After(updateMiddle).After(checkLeaf3),
				tagStore.EXPECT().UpdateTag(gomock.Any(), namespace, key2, signedValue2, "Root 2 lease"),
			)

			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				actualSignedValue, complete, err := resolver.ResolveTag(ctx, namespace, key1, nil)
				require.NoError(t, err)
				require.True(t, complete)
				require.Equal(t, signedValue1, actualSignedValue)
			}()
			<-middleChecked
			go func() {
				defer wg.Done()
				actualSignedValue, complete, err := resolver.ResolveTag(ctx, namespace, key2, nil)
				require.NoError(t, err)
				require.True(t, complete)
				require.Equal(t, signedValue2, actualSignedValue)
			}()
			wg.Wait()
		})

		t.Run("ExceedsLimit", func(t *testing.T) {
			// DAGs whose height exceeds the limit cannot
			// be processed, as they would never be
			// dequeued.
			smallResolver := leaserenewing.NewResolver[object.Namespace, object.GlobalReference, any](
				tagStore,
				objectUploader,
				semaphore.NewWeighted(10),
				object.NewLimit(&object_pb.Limit{
					Count:     1,
					SizeBytes: 100000,
				}),
			)
			tagStore.EXPECT().ResolveTag(ctx, namespace, key1, nil).Return(signedValue1, false, nil)

			_, _, err := smallResolver.ResolveTag(ctx, namespace, key1, nil)
			testutil.RequirePrefixedStatus(t, status.Error(codes.InvalidArgument, "Failed to obtain lease for object with reference "), err)
		})
	}
}
