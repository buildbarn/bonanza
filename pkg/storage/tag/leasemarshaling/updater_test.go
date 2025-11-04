package leasemarshaling_test

import (
	"context"
	"crypto/ed25519"
	"testing"
	"time"

	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/object"
	"bonanza.build/pkg/storage/tag"
	"bonanza.build/pkg/storage/tag/leasemarshaling"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go.uber.org/mock/gomock"
)

func TestUpdater(t *testing.T) {
	ctrl, ctx := gomock.WithContext(context.Background(), t)

	baseUpdater := NewMockUpdaterForTesting(ctrl)
	marshaler := NewMockLeaseMarshalerForTesting(ctrl)
	updater := leasemarshaling.NewUpdater(baseUpdater, marshaler)

	namespace := util.Must(object.NewNamespace(&object_pb.Namespace{
		InstanceName:    "hello/world",
		ReferenceFormat: object_pb.ReferenceFormat_SHA256_V1,
	}))
	key := tag.Key{
		SignaturePublicKey: ed25519.PublicKey("unused"),
		Hash: []byte{
			0xf1, 0x52, 0x97, 0x3f, 0x6b, 0x65, 0x35, 0x5e,
			0xf2, 0xc7, 0xe0, 0xb1, 0xd6, 0x37, 0x7e, 0xae,
			0xf5, 0x2c, 0xd8, 0x30, 0x1f, 0x88, 0xdc, 0xdb,
			0x4c, 0x87, 0xd6, 0x24, 0x13, 0xf9, 0xef, 0x18,
		},
	}
	signedValue := tag.SignedValue{
		Value: tag.Value{
			Reference: object.MustNewSHA256V1LocalReference("d257cd70c5b9987aed8f48cafcda62e8b8f3883a258f86cd6c85b731f675f9a2", 595814, 58, 12, 7883322),
			Timestamp: time.Unix(1762267622, 0),
		},
		Signature: []byte("unused"),
	}

	t.Run("EmptyLease", func(t *testing.T) {
		baseUpdater.EXPECT().UpdateTag(ctx, namespace, key, signedValue, nil)

		require.NoError(t, updater.UpdateTag(ctx, namespace, key, signedValue, nil))
	})

	t.Run("InvalidLease", func(t *testing.T) {
		marshaler.EXPECT().UnmarshalLease([]byte{1, 2, 3}).Return(nil, status.Error(codes.InvalidArgument, "Incorrect length"))

		testutil.RequireEqualStatus(
			t,
			status.Error(codes.InvalidArgument, "Invalid lease: Incorrect length"),
			updater.UpdateTag(ctx, namespace, key, signedValue, []byte{1, 2, 3}),
		)
	})

	t.Run("ValidLease", func(t *testing.T) {
		marshaler.EXPECT().UnmarshalLease([]byte{4, 5, 6}).Return(42, nil)
		baseUpdater.EXPECT().UpdateTag(ctx, namespace, key, signedValue, 42)

		require.NoError(t, updater.UpdateTag(ctx, namespace, key, signedValue, []byte{4, 5, 6}))
	})
}
