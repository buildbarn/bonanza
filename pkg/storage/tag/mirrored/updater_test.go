package mirrored_test

import (
	"context"
	"crypto/ed25519"
	"testing"
	"time"

	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/object"
	object_mirrored "bonanza.build/pkg/storage/object/mirrored"
	"bonanza.build/pkg/storage/tag"
	tag_mirrored "bonanza.build/pkg/storage/tag/mirrored"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go.uber.org/mock/gomock"
)

func TestUpdater(t *testing.T) {
	ctrl, ctx := gomock.WithContext(context.Background(), t)

	replicaA := NewMockUpdaterForTesting(ctrl)
	replicaB := NewMockUpdaterForTesting(ctrl)
	updater := tag_mirrored.NewUpdater(replicaA, replicaB)

	namespace := util.Must(object.NewNamespace(&object_pb.Namespace{
		InstanceName:    "hello/world",
		ReferenceFormat: object_pb.ReferenceFormat_SHA256_V1,
	}))
	key := tag.Key{
		SignaturePublicKey: ed25519.PublicKey{},
		Hash: []byte{
			0xd9, 0xd1, 0xff, 0xa7, 0xf4, 0xfc, 0x01, 0xbb,
			0x1e, 0x97, 0xef, 0x6e, 0xae, 0xe2, 0xd7, 0xbf,
			0x71, 0x9e, 0xd8, 0x46, 0xbf, 0xd0, 0xf0, 0xd5,
			0x7b, 0xd1, 0xaf, 0x68, 0xf1, 0x73, 0xd6, 0x8e,
		},
	}
	signedValue := tag.SignedValue{
		Value: tag.Value{
			Reference: object.MustNewSHA256V1LocalReference("8ed6814114c216e75bef25dcba0d6b6c9600f2d49f07e3ae970697effae188d1", 595814, 58, 12, 7883322),
			Timestamp: time.Unix(1762263134, 0),
		},
		Signature: []byte{123},
	}

	t.Run("FailureReplicaA", func(t *testing.T) {
		// If updating the tag only succeeds for one of the
		// replicas, the error message should be propagated.
		replicaA.EXPECT().
			UpdateTag(
				gomock.Any(),
				namespace,
				key,
				signedValue,
				"Lease A",
			).
			Return(status.Error(codes.PermissionDenied, "User is not permitted to update tags"))
		replicaB.EXPECT().
			UpdateTag(
				gomock.Any(),
				namespace,
				key,
				signedValue,
				"Lease B",
			)

		testutil.RequireEqualStatus(
			t,
			status.Error(codes.PermissionDenied, "Replica A: User is not permitted to update tags"),
			updater.UpdateTag(
				ctx,
				namespace,
				key,
				signedValue,
				object_mirrored.Lease[any, any]{
					LeaseA: "Lease A",
					LeaseB: "Lease B",
				},
			),
		)
	})

	t.Run("FailureReplicaB", func(t *testing.T) {
		// If both replicas return an error, the error returned
		// by the first replica that failed should be returned.
		replicaA.EXPECT().
			UpdateTag(
				gomock.Any(),
				namespace,
				key,
				signedValue,
				"Lease A",
			).
			Do(func(ctx context.Context, namespace object.Namespace, key tag.Key, signedValue tag.SignedValue, lease any) {
				<-ctx.Done()
			}).
			Return(status.Error(codes.Canceled, "Request canceled"))
		replicaB.EXPECT().
			UpdateTag(
				gomock.Any(),
				namespace,
				key,
				signedValue,
				"Lease B",
			).
			Return(status.Error(codes.Unavailable, "Server offline"))

		testutil.RequireEqualStatus(
			t,
			status.Error(codes.Unavailable, "Replica B: Server offline"),
			updater.UpdateTag(
				ctx,
				namespace,
				key,
				signedValue,
				object_mirrored.Lease[any, any]{
					LeaseA: "Lease A",
					LeaseB: "Lease B",
				},
			),
		)
	})

	t.Run("Success", func(t *testing.T) {
		replicaA.EXPECT().
			UpdateTag(
				gomock.Any(),
				namespace,
				key,
				signedValue,
				"Lease A",
			)
		replicaB.EXPECT().
			UpdateTag(
				gomock.Any(),
				namespace,
				key,
				signedValue,
				"Lease B",
			)

		require.NoError(t, updater.UpdateTag(
			ctx,
			namespace,
			key,
			signedValue,
			object_mirrored.Lease[any, any]{
				LeaseA: "Lease A",
				LeaseB: "Lease B",
			},
		))
	})
}
