package filesystem_test

import (
	"testing"

	"bonanza.build/pkg/encoding/varint"
	model_core "bonanza.build/pkg/model/core"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func marshalFileContentsList(entries []*model_filesystem_pb.FileContents) []byte {
	var out []byte
	for _, entry := range entries {
		entryData, err := proto.Marshal(entry)
		if err != nil {
			panic(err)
		}
		out = varint.AppendForward(out, len(entryData))
		out = append(out, entryData...)
	}
	return out
}

func TestFileContentsListObjectParser(t *testing.T) {
	objectParser := model_filesystem.NewFileContentsListObjectParser[object.LocalReference]()

	t.Run("InvalidMessage", func(t *testing.T) {
		_, _, err := objectParser.ParseObject(
			model_core.NewSimpleMessage[object.LocalReference](
				[]byte("Not a valid Protobuf message"),
			),
			/* decodingParameters = */ nil,
		)
		testutil.RequireEqualStatus(t, status.Error(codes.InvalidArgument, "Length of element at offset 0 is 3695 bytes, which exceeds maximum permitted size of 26 bytes"), err)
	})

	t.Run("TooFewParts", func(t *testing.T) {
		// A file contents list should contain at least two
		// parts. If only a single part were present, the tree
		// could have been collapsed.
		data := marshalFileContentsList([]*model_filesystem_pb.FileContents{{
			TotalSizeBytes: 42,
			Level: &model_filesystem_pb.FileContents_ChunkReference{
				ChunkReference: &model_core_pb.DecodableReference{
					Reference: &model_core_pb.Reference{
						Index: 1,
					},
					DecodingParameters: []byte{1, 2, 3, 4},
				},
			},
		}})
		_, _, err := objectParser.ParseObject(
			model_core.NewMessage(
				data,
				object.OutgoingReferencesList[object.LocalReference]{
					object.MustNewSHA256V1LocalReference("f5eeff3dfd9cdee18e36c40bd7853d427f560b2ce1d71ddcb4015af94e21626e", 42, 0, 0, 0),
				},
			),
			/* decodingParameters = */ nil,
		)
		testutil.RequireEqualStatus(t, status.Error(codes.InvalidArgument, "File contents list contains fewer than two parts"), err)
	})

	t.Run("EmptyPart", func(t *testing.T) {
		// Any part of the file should have a non-zero size.
		// Otherwise it could have safely been omitted from the
		// file contents list.
		data := marshalFileContentsList([]*model_filesystem_pb.FileContents{
			{
				TotalSizeBytes: 12,
				Level: &model_filesystem_pb.FileContents_ChunkReference{
					ChunkReference: &model_core_pb.DecodableReference{
						Reference: &model_core_pb.Reference{
							Index: 1,
						},
						DecodingParameters: []byte{1, 2, 3, 4},
					},
				},
			},
			{
				TotalSizeBytes: 0,
				Level: &model_filesystem_pb.FileContents_ChunkReference{
					ChunkReference: &model_core_pb.DecodableReference{
						Reference: &model_core_pb.Reference{
							Index: 2,
						},
						DecodingParameters: []byte{1, 2, 3, 4},
					},
				},
			},
			{
				TotalSizeBytes: 70,
				Level: &model_filesystem_pb.FileContents_ChunkReference{
					ChunkReference: &model_core_pb.DecodableReference{
						Reference: &model_core_pb.Reference{
							Index: 3,
						},
						DecodingParameters: []byte{1, 2, 3, 4},
					},
				},
			},
		})
		_, _, err := objectParser.ParseObject(
			model_core.NewMessage(
				data,
				object.OutgoingReferencesList[object.LocalReference]{
					object.MustNewSHA256V1LocalReference("13b91bea68c2ada69d09dcf883c71d889ed9f88895425567f6ad2e8dfec9f604", 12, 0, 0, 0),
					object.MustNewSHA256V1LocalReference("89cd4d45b9f48a92939ed530c38a77c43cb2f9030eef38775edfc993d70eb247", 1, 0, 0, 0),
					object.MustNewSHA256V1LocalReference("a17c02397e375773f33ca1d55e275ece80c13f950ca5bd813dc70cb32a111fd2", 70, 0, 0, 0),
				},
			),
			/* decodingParameters = */ nil,
		)
		testutil.RequireEqualStatus(t, status.Error(codes.InvalidArgument, "Part at index 1: File contents does not have any data"), err)
	})

	t.Run("Overflow", func(t *testing.T) {
		// The combined size of all parts can't exceed the
		// maximum value of uint64, as it wouldn't be possible
		// to refer to these parts through a single FileContents
		// message in the parent.
		data := marshalFileContentsList([]*model_filesystem_pb.FileContents{
			{
				TotalSizeBytes: 0x8000000000000000,
				Level: &model_filesystem_pb.FileContents_List_{
					List: &model_filesystem_pb.FileContents_List{
						Reference: &model_core_pb.DecodableReference{
							Reference: &model_core_pb.Reference{
								Index: 1,
							},
							DecodingParameters: []byte{1, 2, 3, 4},
						},
					},
				},
			},
			{
				TotalSizeBytes: 0x8000000000000000,
				Level: &model_filesystem_pb.FileContents_List_{
					List: &model_filesystem_pb.FileContents_List{
						Reference: &model_core_pb.DecodableReference{
							Reference: &model_core_pb.Reference{
								Index: 2,
							},
							DecodingParameters: []byte{1, 2, 3, 4},
						},
					},
				},
			},
		})
		_, _, err := objectParser.ParseObject(
			model_core.NewMessage(
				data,
				object.OutgoingReferencesList[object.LocalReference]{
					object.MustNewSHA256V1LocalReference("123f76702502e562a2112c690e1a7ac10952e662f7f0a07b550d575f544fea22", 50000, 4, 1000, 900000),
					object.MustNewSHA256V1LocalReference("a5f8c2ce71af9856ac04423d776688766c05f3b4a0b8850f68bfcbd1bc94b45c", 50000, 4, 1000, 900000),
				},
			),
			/* decodingParameters = */ nil,
		)
		testutil.RequireEqualStatus(t, status.Error(codes.InvalidArgument, "Combined size of all parts exceeds maximum file size of 18446744073709551615 bytes"), err)
	})

	t.Run("InvalidReferenceIndex", func(t *testing.T) {
		data := marshalFileContentsList([]*model_filesystem_pb.FileContents{
			{
				TotalSizeBytes: 200,
				Level: &model_filesystem_pb.FileContents_ChunkReference{
					ChunkReference: &model_core_pb.DecodableReference{
						Reference: &model_core_pb.Reference{
							Index: 7,
						},
						DecodingParameters: []byte{1, 2, 3, 4},
					},
				},
			},
			{
				TotalSizeBytes: 300,
				Level: &model_filesystem_pb.FileContents_ChunkReference{
					ChunkReference: &model_core_pb.DecodableReference{
						Reference: &model_core_pb.Reference{
							Index: 2,
						},
						DecodingParameters: []byte{1, 2, 3, 4},
					},
				},
			},
		})
		_, _, err := objectParser.ParseObject(
			model_core.NewMessage(
				data,
				object.OutgoingReferencesList[object.LocalReference]{
					object.MustNewSHA256V1LocalReference("38dc1b3b70088a0bde56511eeb571e0b5aa873407ad198148befb347ef31282a", 200, 0, 0, 0),
					object.MustNewSHA256V1LocalReference("635fef9b02b336f9254473d6b09c41f5027c38046c46bb514afc788292c1508e", 300, 0, 0, 0),
				},
			),
			/* decodingParameters = */ nil,
		)
		testutil.RequireEqualStatus(t, status.Error(codes.InvalidArgument, "Part at index 0: Reference message contains index 7, which is outside expected range [1, 2]"), err)
	})

	t.Run("Success", func(t *testing.T) {
		data := marshalFileContentsList([]*model_filesystem_pb.FileContents{
			{
				TotalSizeBytes: 200,
				Level: &model_filesystem_pb.FileContents_ChunkReference{
					ChunkReference: &model_core_pb.DecodableReference{
						Reference: &model_core_pb.Reference{
							Index: 1,
						},
						DecodingParameters: []byte{1, 2, 3, 4},
					},
				},
			},
			{
				TotalSizeBytes: 300,
				Level: &model_filesystem_pb.FileContents_ChunkReference{
					ChunkReference: &model_core_pb.DecodableReference{
						Reference: &model_core_pb.Reference{
							Index: 2,
						},
						DecodingParameters: []byte{1, 2, 3, 4},
					},
				},
			},
		})
		fileContentsList, sizeBytes, err := objectParser.ParseObject(
			model_core.NewMessage(
				data,
				object.OutgoingReferencesList[object.LocalReference]{
					object.MustNewSHA256V1LocalReference("38dc1b3b70088a0bde56511eeb571e0b5aa873407ad198148befb347ef31282a", 200, 0, 0, 0),
					object.MustNewSHA256V1LocalReference("635fef9b02b336f9254473d6b09c41f5027c38046c46bb514afc788292c1508e", 300, 0, 0, 0),
				},
			),
			/* decodingParameters = */ nil,
		)
		require.NoError(t, err)
		require.Len(t, fileContentsList, 2)
		require.Equal(t, uint64(200), fileContentsList[0].GetEndBytes())
		reference0 := fileContentsList[0].GetReference()
		require.Equal(t, util.Must(model_core.NewDecodable(object.MustNewSHA256V1LocalReference("38dc1b3b70088a0bde56511eeb571e0b5aa873407ad198148befb347ef31282a", 200, 0, 0, 0), []byte{1, 2, 3, 4})), *reference0)
		require.Equal(t, uint64(500), fileContentsList[1].GetEndBytes())
		reference1 := fileContentsList[1].GetReference()
		require.Equal(t, util.Must(model_core.NewDecodable(object.MustNewSHA256V1LocalReference("635fef9b02b336f9254473d6b09c41f5027c38046c46bb514afc788292c1508e", 300, 0, 0, 0), []byte{1, 2, 3, 4})), *reference1)
		require.Equal(t, 38, sizeBytes)
	})
}
