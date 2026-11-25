package filesystem_test

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"math/rand/v2"
	"testing"

	"bonanza.build/pkg/encoding/utf8stat"
	model_core "bonanza.build/pkg/model/core"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_chunking_pb "bonanza.build/pkg/proto/model/chunking"
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
			Access: &model_filesystem_pb.FileAccessParameters{},
			ChunkParameters: &model_chunking_pb.Parameters{
				MinimumSizeBytes: 1 << 16,
				HorizonSizeBytes: 1 << 19,
			},
			FileContentsListParameters: &model_chunking_pb.Parameters{
				MinimumSizeBytes: 1 << 12,
				HorizonSizeBytes: 8 << 12,
			},
		},
		util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1)),
		model_encoding.NewBinaryEncoderFactory(true),
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
			fileCreationParameters.NewChunkingReader(bytes.NewBuffer(nil)),
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
			fileCreationParameters.NewChunkingReader(bytes.NewBufferString("Hello")),
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
			Utf8Stat:       1,
		}, rootFileContents.Message)
		require.Equal(t, object.OutgoingReferencesList[object.LocalReference]{
			object.MustNewSHA256V1LocalReference("185f8db32271fe25f561a6fc938b2e264306ec304eda518007d1764826381969", 5, 0, 0, 0),
		}, references)
		require.Equal(t, []model_core.ReferenceMetadata{metadata1}, metadata)
	})

	t.Run("ReusedReader", func(t *testing.T) {
		// Callers may reuse a single buffered reader for
		// multiple files by calling Reset(). This should yield
		// the same results as using a new reader for every
		// file, regardless of the sizes of the files.
		rng := rand.NewChaCha8([32]byte{})
		var files [][]byte
		for _, sizeBytes := range []int{3 << 20, 5, 0, 1 << 20, 100} {
			data := make([]byte, sizeBytes)
			rng.Read(data)
			files = append(files, data)
		}

		createFileMerkleTree := func(f *bufio.Reader) (*model_filesystem_pb.FileContents, object.OutgoingReferencesList[object.LocalReference]) {
			fileContents, err := model_filesystem.CreateFileMerkleTree(
				ctx,
				fileCreationParameters,
				f,
				model_filesystem.NewSimpleFileMerkleTreeCapturer(model_core.DiscardingCreatedObjectCapturer),
			)
			require.NoError(t, err)
			references, _ := fileContents.Patcher.SortAndSetReferences()
			return fileContents.Message, references
		}

		reusedReader := fileCreationParameters.NewChunkingReader(nil)
		for _, data := range files {
			expectedMessage, expectedReferences := createFileMerkleTree(fileCreationParameters.NewChunkingReader(bytes.NewReader(data)))
			reusedReader.Reset(bytes.NewReader(data))
			actualMessage, actualReferences := createFileMerkleTree(reusedReader)
			testutil.RequireEqualProto(t, expectedMessage, actualMessage)
			require.Equal(t, expectedReferences, actualReferences)
		}
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
			fileCreationParameters.NewChunkingReader(io.LimitReader(rng, 1<<30)),
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
			object.MustNewSHA256V1LocalReference("fe7615d5abef587dc4fc97a76155c7c7ddee0f2664104f08246e945567b0e0a5", 6726, 2, 118, 8154),
		}, references)
	})

	t.Run("UTF8Text", func(t *testing.T) {
		// Create a Merkle tree for a large text file containing
		// multi-byte runes, so that chunk boundaries are likely
		// to split runes. The UTF-8 stat of the root should be
		// identical to that of the file as a whole.
		rng := rand.New(rand.NewPCG(1, 2))
		pieces := []string{"a", "b", " ", "\n", "é", "€", "𝄞"}
		var text bytes.Buffer
		for text.Len() < 64<<20 {
			text.WriteString(pieces[rng.IntN(len(pieces))])
		}
		text.WriteString("\n")
		expectedStat := utf8stat.Compute(text.Bytes())
		require.True(t, expectedStat.IsValidFile())
		require.True(t, expectedStat.HasTrailingNewline())

		rootFileContents, err := model_filesystem.CreateFileMerkleTree(
			ctx,
			fileCreationParameters,
			fileCreationParameters.NewChunkingReader(&text),
			model_filesystem.NewSimpleFileMerkleTreeCapturer(model_core.DiscardingCreatedObjectCapturer),
		)
		require.NoError(t, err)

		references, _ := rootFileContents.Patcher.SortAndSetReferences()
		require.Len(t, references, 1)
		require.Greater(t, references[0].GetHeight(), 1)
		require.Equal(t, uint64(expectedStat), rootFileContents.Message.Utf8Stat)
	})
}
