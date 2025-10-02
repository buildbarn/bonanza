package filesystem_test

import (
	"testing"

	model_core "bonanza.build/pkg/model/core"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go.uber.org/mock/gomock"
)

func TestFileReader(t *testing.T) {
	ctrl, ctx := gomock.WithContext(t.Context(), t)

	fileContentsListReader := NewMockFileContentsListReaderForTesting(ctrl)
	chunkReader := NewMockFileChunkReaderForTesting(ctrl)
	fileReader := model_filesystem.NewFileReader(fileContentsListReader, chunkReader)

	t.Run("ChunkReadFailure", func(t *testing.T) {
		chunkReference := util.Must(
			model_core.NewDecodable(
				object.MustNewSHA256V1LocalReference("629db2c2c2a8ae9445ffed22cbf2f3b5a89d7062a7eb4cbdf369217b10e99838", 1337, 0, 0, 0),
				[]byte{0x15, 0x0d, 0x94, 0xec, 0x02, 0xee, 0x19, 0xbd},
			),
		)
		chunkReader.EXPECT().ReadParsedObject(ctx, chunkReference).Return(nil, status.Error(codes.Internal, "Server offline"))

		_, err := fileReader.FileReadAll(
			ctx,
			model_filesystem.FileContentsEntry[object.LocalReference]{
				EndBytes:  1337,
				Reference: chunkReference,
			},
			/* maximumSizeBytes = */ 1<<20,
		)
		testutil.RequireEqualStatus(t, status.Error(codes.Internal, "Failed to read chunk with reference Yp2ywsKorpRF_-0iy_LztaidcGKn60y982khexDpmDg5BQAAAAAAAA.FQ2U7ALuGb0: Server offline"), err)
	})

	t.Run("ChunkSizeMismatch", func(t *testing.T) {
		// File contents messages contain the size of the
		// decoded chunk they reference. This information is
		// needed to perform random access. We should fail if
		// the size in the file contents message does not match
		// reality.
		chunkReference := util.Must(
			model_core.NewDecodable(
				object.MustNewSHA256V1LocalReference("2b6082b00a6f0bfc5f766b1a94dcb7f1597d0e101ac45ad4500f1e7afa8c2b2c", 100, 0, 0, 0),
				[]byte{0x8e, 0x4f, 0xe8, 0x3a, 0x40, 0xc3, 0xd6, 0x14},
			),
		)
		chunkReader.EXPECT().ReadParsedObject(ctx, chunkReference).Return([]byte("This text is not 100 bytes long"), nil)

		_, err := fileReader.FileReadAll(
			ctx,
			model_filesystem.FileContentsEntry[object.LocalReference]{
				EndBytes:  100,
				Reference: chunkReference,
			},
			/* maximumSizeBytes = */ 1<<20,
		)
		testutil.RequireEqualStatus(t, status.Error(codes.InvalidArgument, "Chunk with reference K2CCsApvC_xfdmsalNy38Vl9DhAaxFrUUA8eevqMKyxkAAAAAAAAAA.jk_oOkDD1hQ is 31 bytes in size, while 100 bytes were expected"), err)
	})

	t.Run("ChunkSuccess", func(t *testing.T) {
		chunkReference := util.Must(
			model_core.NewDecodable(
				object.MustNewSHA256V1LocalReference("820a668d28c9d9180aee73b05cdc29241aa7693da205826186fd9c6f01de9c4c", 20, 0, 0, 0),
				[]byte{0xd3, 0x87, 0xbe, 0x10, 0x4d, 0x70, 0x1d, 0x76},
			),
		)
		chunkReader.EXPECT().ReadParsedObject(ctx, chunkReference).Return([]byte("Hello world"), nil)

		data, err := fileReader.FileReadAll(
			ctx,
			model_filesystem.FileContentsEntry[object.LocalReference]{
				EndBytes:  11,
				Reference: chunkReference,
			},
			/* maximumSizeBytes = */ 1<<20,
		)
		require.NoError(t, err)
		require.Equal(t, []byte("Hello world"), data)
	})

	t.Run("FileContentsListReadFailure", func(t *testing.T) {
		fileContentsListReference := util.Must(
			model_core.NewDecodable(
				object.MustNewSHA256V1LocalReference("c7b9eaba808f3583711c385e0cf959b11e78a6e076bbe3b292e4e118aca4c2e0", 2000, 1, 2, 0),
				[]byte{0x8c, 0xf5, 0x2b, 0x6e, 0x9b, 0xaf, 0xa7, 0x86},
			),
		)
		fileContentsListReader.EXPECT().ReadParsedObject(ctx, fileContentsListReference).Return(nil, status.Error(codes.Internal, "Server offline"))

		_, err := fileReader.FileReadAll(
			ctx,
			model_filesystem.FileContentsEntry[object.LocalReference]{
				EndBytes:  2000,
				Reference: fileContentsListReference,
			},
			/* maximumSizeBytes = */ 1<<20,
		)
		testutil.RequireEqualStatus(t, status.Error(codes.Internal, "Failed to read file contents list with reference x7nquoCPNYNxHDheDPlZsR54puB2u-OykuThGKykwuDQBwABAgAAAA.jPUrbpuvp4Y: Server offline"), err)
	})

	// TODO: Add more testing coverage.
}
