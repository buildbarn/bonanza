package filesystem_test

import (
	"bytes"
	"context"
	"io"
	"math/rand/v2"
	"testing"

	model_core "bonanza.build/pkg/model/core"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"

	"go.uber.org/mock/gomock"
)

func TestCreateFileMerkleTree(t *testing.T) {
	ctrl, ctx := gomock.WithContext(context.Background(), t)

	fileCreationParameters, err := model_filesystem.NewFileCreationParametersFromProto(
		&model_filesystem_pb.FileCreationParameters{
			Access:                           &model_filesystem_pb.FileAccessParameters{},
			ChunkMinimumSizeBytes:            1 << 16,
			ChunkMaximumSizeBytes:            1 << 18,
			FileContentsListMinimumSizeBytes: 1 << 12,
			FileContentsListMaximumSizeBytes: 1 << 14,
		},
		util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1)),
	)
	require.NoError(t, err)

	t.Run("EmptyFile", func(t *testing.T) {
		// Empty files should be represented by leaving the
		// resulting FileContents set to nil. There shouldn't
		// be any objects that need to be written to storage.
		capturer := NewMockFileMerkleTreeCapturerForTesting(ctrl)

		rootFileContents, err := model_filesystem.CreateFileMerkleTree(
			ctx,
			fileCreationParameters,
			bytes.NewBuffer(nil),
			capturer,
		)
		require.NoError(t, err)
		require.True(t, rootFileContents.IsSet())
		require.Nil(t, rootFileContents.Message)
	})

	t.Run("Hello", func(t *testing.T) {
		// Small files should be represented as single objects.
		// There should be no FileContents list, as those are
		// only used to join multiple objects together.
		capturer := NewMockFileMerkleTreeCapturerForTesting(ctrl)
		metadata1 := NewMockReferenceMetadata(ctrl)
		capturer.EXPECT().CaptureChunk(gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, contents *object.Contents) (model_core.ReferenceMetadata, error) {
				require.Equal(t, object.MustNewSHA256V1LocalReference("185f8db32271fe25f561a6fc938b2e264306ec304eda518007d1764826381969", 5, 0, 0, 0), contents.GetLocalReference())
				return metadata1, nil
			})

		rootFileContents, err := model_filesystem.CreateFileMerkleTree(
			ctx,
			fileCreationParameters,
			bytes.NewBufferString("Hello"),
			capturer,
		)
		require.NoError(t, err)

		references, metadata := rootFileContents.Patcher.SortAndSetReferences()
		testutil.RequireEqualProto(t, &model_filesystem_pb.FileContents{
			Level: &model_filesystem_pb.FileContents_ChunkReference{
				ChunkReference: &model_core_pb.DecodableReference{
					Reference: &model_core_pb.Reference{
						Index: 1,
					},
				},
			},
			TotalSizeBytes: 5,
		}, rootFileContents.Message)
		require.Equal(t, object.OutgoingReferencesList[object.LocalReference]{
			object.MustNewSHA256V1LocalReference("185f8db32271fe25f561a6fc938b2e264306ec304eda518007d1764826381969", 5, 0, 0, 0),
		}, references)
		require.Equal(t, []model_core.ReferenceMetadata{metadata1}, metadata)
	})

	t.Run("ChaCha8_1GB", func(t *testing.T) {
		// Create a Merkle tree for a 1 GB file consisting of
		// the first 1 GB of data returned by a ChaCha8 random
		// number generator with the seed set to zero. The
		// resulting tree should have a height of two.
		rng := rand.NewChaCha8([32]byte{})
		rootFileContents, err := model_filesystem.CreateFileMerkleTree(
			ctx,
			fileCreationParameters,
			io.LimitReader(rng, 1<<30),
			model_filesystem.NewSimpleFileMerkleTreeCapturer(model_core.DiscardingCreatedObjectCapturer),
		)
		require.NoError(t, err)

		references, _ := rootFileContents.Patcher.SortAndSetReferences()
		testutil.RequireEqualProto(t, &model_filesystem_pb.FileContents{
			Level: &model_filesystem_pb.FileContents_List_{
				List: &model_filesystem_pb.FileContents_List{
					Reference: &model_core_pb.DecodableReference{
						Reference: &model_core_pb.Reference{
							Index: 1,
						},
					},
					Sparse: false,
				},
			},
			TotalSizeBytes: 1 << 30,
		}, rootFileContents.Message)
		require.Equal(t, object.OutgoingReferencesList[object.LocalReference]{
			object.MustNewSHA256V1LocalReference("de074da0ad31c5690ed2c086bee725dc858f4f85a591273d49136d1fc0614101", 1938, 2, 34, 14744),
		}, references)
	})
}
