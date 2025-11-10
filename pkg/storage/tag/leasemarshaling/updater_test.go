package leasemarshaling_test

import (
	"context"
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
		SignaturePublicKey: [...]byte{
			0xe2, 0x59, 0xc1, 0x20, 0x1c, 0x38, 0x28, 0x47,
			0x40, 0x9e, 0xd7, 0xb2, 0xd2, 0x4b, 0x30, 0xd6,
			0x3a, 0x8f, 0x95, 0x31, 0x8e, 0x91, 0xe6, 0x37,
			0x11, 0x64, 0xa0, 0x92, 0x26, 0xc5, 0xf7, 0x12,
		},
		Hash: [...]byte{
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
		Signature: [...]byte{
			0x29, 0x23, 0x2c, 0x7a, 0x45, 0x32, 0x58, 0x4e,
			0xca, 0xb9, 0x12, 0x63, 0x34, 0x01, 0x00, 0x26,
			0x68, 0xcc, 0x68, 0x83, 0xd5, 0x4c, 0xf2, 0xe1,
			0x02, 0x52, 0xa3, 0x94, 0xe0, 0xc0, 0x6d, 0x38,
			0xc6, 0xcd, 0x6d, 0x4b, 0xcf, 0x43, 0x3a, 0xb4,
			0xde, 0x78, 0xaf, 0x3f, 0xb7, 0xc5, 0xfd, 0x89,
			0xbf, 0xbf, 0x30, 0xdf, 0x7a, 0x22, 0xb1, 0xa5,
			0x6c, 0x2f, 0x03, 0x1c, 0xc6, 0x6e, 0xe0, 0xbd,
		},
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
