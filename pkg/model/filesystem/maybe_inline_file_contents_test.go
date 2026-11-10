package filesystem_test

import (
	"context"
	"testing"

	model_core "bonanza.build/pkg/model/core"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"

	"golang.org/x/sync/semaphore"

	"go.uber.org/mock/gomock"
)

func TestMaybeInlineFileContents(t *testing.T) {
	ctrl, ctx := gomock.WithContext(context.Background(), t)

	fileCreationParameters, err := model_filesystem.NewFileCreationParametersFromProto(
		&model_filesystem_pb.FileCreationParameters{
			Access:                           &model_filesystem_pb.FileAccessParameters{},
			ChunkMinimumSizeBytes:            1 << 16,
			ChunkHorizonSizeBytes:            1 << 19,
			FileContentsListMinimumSizeBytes: 1 << 12,
			FileContentsListHorizonSizeBytes: 8 << 12,
		},
		util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1)),
		model_encoding.NewBinaryEncoderFactory(true),
	)
	require.NoError(t, err)

	fileContentsListReader := NewMockFileContentsListReaderForTesting(ctrl)
	chunkReader := NewMockFileChunkReaderForTesting(ctrl)
	fileReader := model_filesystem.NewFileReader(
		fileContentsListReader,
		chunkReader,
		semaphore.NewWeighted(1),
	)

	t.Run("Empty", func(t *testing.T) {
		// Empty data should be represented by leaving the
		// oneof unset.
		capturer := NewMockFileMerkleTreeCapturerForTesting(ctrl)
		contents, err := model_filesystem.CreateMaybeInlineFileContents(ctx, fileCreationParameters, nil, 10, capturer)
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_filesystem_pb.MaybeInlineFileContents{}, contents.Message)
		require.Equal(t, uint64(0), model_filesystem.GetMaybeInlineFileContentsSizeBytes(contents.Message))

		references, _ := contents.Patcher.SortAndSetReferences()
		data, err := fileReader.MaybeInlineFileContentsReadAll(ctx, model_core.NewMessage(contents.Message, references), 100)
		require.NoError(t, err)
		require.Empty(t, data)
	})

	t.Run("Inline", func(t *testing.T) {
		// Data that does not exceed the limit should be stored
		// inline, without creating any objects.
		capturer := NewMockFileMerkleTreeCapturerForTesting(ctrl)
		contents, err := model_filesystem.CreateMaybeInlineFileContents(ctx, fileCreationParameters, []byte("Hello"), 5, capturer)
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_filesystem_pb.MaybeInlineFileContents{
			Level: &model_filesystem_pb.MaybeInlineFileContents_ContentsInline{
				ContentsInline: []byte("Hello"),
			},
		}, contents.Message)
		require.Equal(t, uint64(5), model_filesystem.GetMaybeInlineFileContentsSizeBytes(contents.Message))

		references, _ := contents.Patcher.SortAndSetReferences()
		m := model_core.NewMessage(contents.Message, references)
		data, err := fileReader.MaybeInlineFileContentsReadAll(ctx, m, 100)
		require.NoError(t, err)
		require.Equal(t, []byte("Hello"), data)

		_, err = fileReader.MaybeInlineFileContentsReadAll(ctx, m, 4)
		require.ErrorContains(t, err, "exceeds the permitted maximum")

		p := make([]byte, 3)
		n, err := fileReader.MaybeInlineFileContentsReadAt(ctx, m, p, 1)
		require.NoError(t, err)
		require.Equal(t, 3, n)
		require.Equal(t, []byte("ell"), p)

		_, err = fileReader.MaybeInlineFileContentsReadAt(ctx, m, p, 3)
		require.ErrorContains(t, err, "Read exceeds size of data")
	})

	t.Run("External", func(t *testing.T) {
		// Data that exceeds the limit should be stored in the
		// same way as file contents.
		capturer := NewMockFileMerkleTreeCapturerForTesting(ctrl)
		metadata1 := NewMockReferenceMetadata(ctrl)
		chunkReference := object.MustNewSHA256V1LocalReference("185f8db32271fe25f561a6fc938b2e264306ec304eda518007d1764826381969", 5, 0, 0, 0)
		capturer.EXPECT().CaptureChunk(gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, contents *object.Contents) (model_core.ReferenceMetadata, error) {
				require.Equal(t, chunkReference, contents.GetLocalReference())
				return metadata1, nil
			})

		contents, err := model_filesystem.CreateMaybeInlineFileContents(ctx, fileCreationParameters, []byte("Hello"), 4, capturer)
		require.NoError(t, err)
		references, metadata := contents.Patcher.SortAndSetReferences()
		testutil.RequireEqualProto(t, &model_filesystem_pb.MaybeInlineFileContents{
			Level: &model_filesystem_pb.MaybeInlineFileContents_ContentsExternal{
				ContentsExternal: &model_filesystem_pb.FileContents{
					Level: &model_filesystem_pb.FileContents_ChunkReference{
						ChunkReference: &model_core_pb.DecodableReference{
							Reference: &model_core_pb.Reference{
								Index: 1,
							},
						},
					},
					TotalSizeBytes: 5,
				},
			},
		}, contents.Message)
		require.Equal(t, object.OutgoingReferencesList[object.LocalReference]{chunkReference}, references)
		require.Equal(t, []model_core.ReferenceMetadata{metadata1}, metadata)
		require.Equal(t, uint64(5), model_filesystem.GetMaybeInlineFileContentsSizeBytes(contents.Message))

		m := model_core.NewMessage(contents.Message, references)
		decodableChunkReference := util.Must(model_core.NewDecodable(chunkReference, []byte(nil)))
		chunkReader.EXPECT().ReadObject(gomock.Any(), decodableChunkReference).Return([]byte("Hello"), nil).Times(2)

		data, err := fileReader.MaybeInlineFileContentsReadAll(ctx, m, 100)
		require.NoError(t, err)
		require.Equal(t, []byte("Hello"), data)

		p := make([]byte, 3)
		n, err := fileReader.MaybeInlineFileContentsReadAt(ctx, m, p, 2)
		require.NoError(t, err)
		require.Equal(t, 3, n)
		require.Equal(t, []byte("llo"), p)

		_, err = fileReader.MaybeInlineFileContentsReadAt(ctx, m, p, 3)
		require.ErrorContains(t, err, "Read exceeds size of data")
	})
}
