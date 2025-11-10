package mirrored_test

import (
	"context"
	"testing"
	"time"

	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/object"
	"bonanza.build/pkg/storage/tag"
	"bonanza.build/pkg/storage/tag/mirrored"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go.uber.org/mock/gomock"
)

func TestResolver(t *testing.T) {
	ctrl, ctx := gomock.WithContext(context.Background(), t)

	replicaA := NewMockResolverForTesting(ctrl)
	replicaB := NewMockResolverForTesting(ctrl)
	resolver := mirrored.NewResolver(replicaA, replicaB)

	namespace := util.Must(object.NewNamespace(&object_pb.Namespace{
		InstanceName:    "hello/world",
		ReferenceFormat: object_pb.ReferenceFormat_SHA256_V1,
	}))
	key := tag.Key{
		SignaturePublicKey: [...]byte{
			0x07, 0x78, 0x45, 0x66, 0x2a, 0xb1, 0x90, 0xbd,
			0x90, 0x54, 0x51, 0xf1, 0x47, 0x04, 0x62, 0x9a,
			0xf8, 0x4a, 0x4d, 0xc0, 0xce, 0x4e, 0xa3, 0xed,
			0x07, 0x7c, 0x95, 0xbb, 0x6a, 0xce, 0x54, 0xdd,
		},
		Hash: [...]byte{
			0xd9, 0xd1, 0xff, 0xa7, 0xf4, 0xfc, 0x01, 0xbb,
			0x1e, 0x97, 0xef, 0x6e, 0xae, 0xe2, 0xd7, 0xbf,
			0x71, 0x9e, 0xd8, 0x46, 0xbf, 0xd0, 0xf0, 0xd5,
			0x7b, 0xd1, 0xaf, 0x68, 0xf1, 0x73, 0xd6, 0x8e,
		},
	}
	minimumTimestamp := time.Unix(1762262471, 0)
	oldSignedValue := tag.SignedValue{
		Value: tag.Value{
			Reference: object.MustNewSHA256V1LocalReference("2572ad3fb952a78dffe4988445912245fcb4acd998750f956a3a069911aa2da6", 595814, 58, 12, 7883322),
			Timestamp: time.Unix(1762264230, 0),
		},
		Signature: [...]byte{
			0xe0, 0x13, 0x2e, 0x05, 0x15, 0xa0, 0x50, 0xe3,
			0x1c, 0xf9, 0x89, 0x02, 0x49, 0x77, 0x01, 0xd1,
			0x31, 0xa5, 0x10, 0xca, 0xcd, 0xeb, 0xec, 0xcd,
			0x3f, 0x0b, 0x58, 0x85, 0x0c, 0x65, 0x3c, 0x46,
			0x69, 0x2f, 0x1d, 0x83, 0xef, 0x01, 0xe5, 0x61,
			0x0b, 0x80, 0x09, 0x58, 0x63, 0xe5, 0x90, 0xb3,
			0xed, 0x37, 0xd1, 0x96, 0xfd, 0xec, 0xd5, 0x28,
			0x93, 0xb8, 0x9f, 0x8d, 0xd1, 0xd3, 0xad, 0xc3,
		},
	}
	newSignedValue := tag.SignedValue{
		Value: tag.Value{
			Reference: object.MustNewSHA256V1LocalReference("5b6723cc264c3d4605f359fa4e1a041f7e0fcd98763eb2b0c49bb2181a8e67f7", 595814, 58, 12, 7883322),
			Timestamp: time.Unix(1762264233, 0),
		},
		Signature: [...]byte{
			0xe0, 0x13, 0x2e, 0x05, 0x15, 0xa0, 0x50, 0xe3,
			0x1c, 0xf9, 0x89, 0x02, 0x49, 0x77, 0x01, 0xd1,
			0x31, 0xa5, 0x10, 0xca, 0xcd, 0xeb, 0xec, 0xcd,
			0x3f, 0x0b, 0x58, 0x85, 0x0c, 0x65, 0x3c, 0x46,
			0x69, 0x2f, 0x1d, 0x83, 0xef, 0x01, 0xe5, 0x61,
			0x0b, 0x80, 0x09, 0x58, 0x63, 0xe5, 0x90, 0xb3,
			0xed, 0x37, 0xd1, 0x96, 0xfd, 0xec, 0xd5, 0x28,
			0x93, 0xb8, 0x9f, 0x8d, 0xd1, 0xd3, 0xad, 0xc3,
		},
	}

	t.Run("FailureReplicaA", func(t *testing.T) {
		// If a failure occurs resolving the tag through replica
		// A, the error should be propagated.
		var badSignedValue tag.SignedValue
		replicaA.EXPECT().
			ResolveTag(gomock.Any(), namespace, key, &minimumTimestamp).
			Return(badSignedValue, false, status.Error(codes.PermissionDenied, "User is not permitted to resolve tags"))
		replicaB.EXPECT().
			ResolveTag(gomock.Any(), namespace, key, &minimumTimestamp).
			Return(newSignedValue, true, nil)

		_, _, err := resolver.ResolveTag(ctx, namespace, key, &minimumTimestamp)
		testutil.RequireEqualStatus(t, status.Error(codes.PermissionDenied, "Replica A: User is not permitted to resolve tags"), err)
	})

	t.Run("FailureReplicaB", func(t *testing.T) {
		// Similarly, the same should happen if a failure occurs
		// through replica B.
		var badSignedValue tag.SignedValue
		replicaA.EXPECT().
			ResolveTag(gomock.Any(), namespace, key, &minimumTimestamp).
			Do(func(ctx context.Context, namespace object.Namespace, key tag.Key, minimumTimestamp *time.Time) {
				<-ctx.Done()
			}).
			Return(badSignedValue, false, status.Error(codes.Canceled, "Request canceled"))
		replicaB.EXPECT().
			ResolveTag(gomock.Any(), namespace, key, &minimumTimestamp).
			Return(badSignedValue, false, status.Error(codes.PermissionDenied, "User is not permitted to resolve tags"))

		_, _, err := resolver.ResolveTag(ctx, namespace, key, &minimumTimestamp)
		testutil.RequireEqualStatus(t, status.Error(codes.PermissionDenied, "Replica B: User is not permitted to resolve tags"), err)
	})

	t.Run("NotFoundBoth", func(t *testing.T) {
		// If both replicas return NOT_FOUND, then there is
		// nothing meaningful we can return.
		var badSignedValue tag.SignedValue
		replicaA.EXPECT().
			ResolveTag(gomock.Any(), namespace, key, &minimumTimestamp).
			Return(badSignedValue, false, status.Error(codes.NotFound, "Tag does not exist in replica A"))
		replicaB.EXPECT().
			ResolveTag(gomock.Any(), namespace, key, &minimumTimestamp).
			Return(badSignedValue, false, status.Error(codes.NotFound, "Tag does not exist in replica B"))

		_, _, err := resolver.ResolveTag(ctx, namespace, key, &minimumTimestamp)
		testutil.RequireEqualStatus(t, status.Error(codes.NotFound, "Tag not found"), err)
	})

	t.Run("NotFoundA", func(t *testing.T) {
		// If only replica A gives us a reference, we may return
		// it, but we can't announce it as being complete. Lease
		// renewing should ensure the tag gets replicated to
		// replica B.
		var badSignedValue tag.SignedValue
		replicaA.EXPECT().
			ResolveTag(gomock.Any(), namespace, key, &minimumTimestamp).
			Return(badSignedValue, false, status.Error(codes.NotFound, "Tag does not exist in replica A"))
		replicaB.EXPECT().
			ResolveTag(gomock.Any(), namespace, key, &minimumTimestamp).
			Return(newSignedValue, true, nil)

		actualSignedValue, complete, err := resolver.ResolveTag(ctx, namespace, key, &minimumTimestamp)
		require.NoError(t, err)
		require.Equal(t, newSignedValue, actualSignedValue)
		require.False(t, complete)
	})

	t.Run("NotFoundB", func(t *testing.T) {
		// The same holds if only replica B is able to give us a
		// reference.
		replicaA.EXPECT().
			ResolveTag(gomock.Any(), namespace, key, &minimumTimestamp).
			Return(newSignedValue, false, nil)
		var badSignedValue tag.SignedValue
		replicaB.EXPECT().
			ResolveTag(gomock.Any(), namespace, key, &minimumTimestamp).
			Return(badSignedValue, false, status.Error(codes.NotFound, "Tag does not exist in replica A"))

		actualSignedValue, complete, err := resolver.ResolveTag(ctx, namespace, key, &minimumTimestamp)
		require.NoError(t, err)
		require.Equal(t, newSignedValue, actualSignedValue)
		require.False(t, complete)
	})

	t.Run("MismatchingReferencesA", func(t *testing.T) {
		// If both replicas return a different reference, we
		// return the newest, while reporting it as incomplete.
		// This should cause the caller to renew the lease and
		// write the tag with the updated lease to both
		// replicas.
		replicaA.EXPECT().
			ResolveTag(gomock.Any(), namespace, key, &minimumTimestamp).
			Return(newSignedValue, true, nil)
		replicaB.EXPECT().
			ResolveTag(gomock.Any(), namespace, key, &minimumTimestamp).
			Return(oldSignedValue, true, nil)

		actualSignedValue, complete, err := resolver.ResolveTag(ctx, namespace, key, &minimumTimestamp)
		require.NoError(t, err)
		require.Equal(t, newSignedValue, actualSignedValue)
		require.False(t, complete)
	})

	t.Run("MismatchingReferencesB", func(t *testing.T) {
		// Swapping the responses should not cause any difference.
		replicaA.EXPECT().
			ResolveTag(gomock.Any(), namespace, key, &minimumTimestamp).
			Return(oldSignedValue, true, nil)
		replicaB.EXPECT().
			ResolveTag(gomock.Any(), namespace, key, &minimumTimestamp).
			Return(newSignedValue, true, nil)

		actualSignedValue, complete, err := resolver.ResolveTag(ctx, namespace, key, &minimumTimestamp)
		require.NoError(t, err)
		require.Equal(t, newSignedValue, actualSignedValue)
		require.False(t, complete)
	})

	t.Run("SuccessIncomplete", func(t *testing.T) {
		// If both replicas return the same reference, but one
		// of them reports it as being incomplete, we should
		// also report it as such.
		replicaA.EXPECT().
			ResolveTag(gomock.Any(), namespace, key, &minimumTimestamp).
			Return(newSignedValue, true, nil)
		replicaB.EXPECT().
			ResolveTag(gomock.Any(), namespace, key, &minimumTimestamp).
			Return(newSignedValue, false, nil)

		actualSignedValue, complete, err := resolver.ResolveTag(ctx, namespace, key, &minimumTimestamp)
		require.NoError(t, err)
		require.Equal(t, newSignedValue, actualSignedValue)
		require.False(t, complete)
	})

	t.Run("SuccessComplete", func(t *testing.T) {
		// If both replicas return the same reference and report
		// it as being complete, we may do the same.
		replicaA.EXPECT().
			ResolveTag(gomock.Any(), namespace, key, &minimumTimestamp).
			Return(newSignedValue, true, nil)
		replicaB.EXPECT().
			ResolveTag(gomock.Any(), namespace, key, &minimumTimestamp).
			Return(newSignedValue, true, nil)

		actualSignedValue, complete, err := resolver.ResolveTag(ctx, namespace, key, &minimumTimestamp)
		require.NoError(t, err)
		require.Equal(t, newSignedValue, actualSignedValue)
		require.True(t, complete)
	})
}
