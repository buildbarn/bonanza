package core_test

import (
	"math"
	"testing"

	model_core "bonanza.build/pkg/model/core"
	model_format "bonanza.build/pkg/model/format"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

	"go.uber.org/mock/gomock"
)

func TestNewPatchedMessageFromExisting(t *testing.T) {
	ctrl := gomock.NewController(t)

	t.Run("Nil", func(t *testing.T) {
		metadataCreator := NewMockReferenceMetadataCreatorForTesting(ctrl)
		m1 := model_core.NewPatchedMessageFromExisting(
			model_core.NewMessage(
				(*model_filesystem_pb.DirectoryEntry_Node)(nil),
				object.OutgoingReferencesList[object.LocalReference]{
					object.MustNewSHA256V1LocalReference("31233528b0ccc08d56724b2f132154967a89c4fb79de65fc65e3eeb42d9f89e4", 4828, 0, 0, 0),
					object.MustNewSHA256V1LocalReference("46d71098267fa33992257c061ba8fc48017e2bcac8f9ac3be8853c8337ec896e", 58511, 0, 0, 0),
					object.MustNewSHA256V1LocalReference("e1d1549332e44eddf28662dda4ca1aae36c3dcd597cd63b3c69737f88afd75d5", 213, 0, 0, 0),
				},
			),
			metadataCreator.Call,
		)

		references, metadata := m1.Patcher.SortAndSetReferences()
		require.Empty(t, references)
		require.Empty(t, metadata)

		require.Nil(t, m1.Message)
	})

	t.Run("ValidReference", func(t *testing.T) {
		metadataCreator := NewMockReferenceMetadataCreatorForTesting(ctrl)
		metadata1 := NewMockReferenceMetadata(ctrl)
		metadataCreator.EXPECT().Call(1).Return(metadata1)
		m1 := model_core.NewPatchedMessageFromExisting(
			model_core.NewMessage(
				&model_filesystem_pb.DirectoryEntry_Node{
					Name: "a",
					Kind: &model_filesystem_pb.DirectoryEntry_Node_File{File: &model_filesystem_pb.FileProperties{
						Contents: &model_filesystem_pb.FileContents{
							Level: &model_filesystem_pb.FileContents_ChunkReference{ChunkReference: model_core_pb.DecodableReference_builder{
								Reference: model_core_pb.Reference_builder{
									Index: 2,
								}.Build(),
							}.Build()},
							TotalSizeBytes: 23,
						},
					}},
				},
				object.OutgoingReferencesList[object.LocalReference]{
					object.MustNewSHA256V1LocalReference("31233528b0ccc08d56724b2f132154967a89c4fb79de65fc65e3eeb42d9f89e4", 4828, 0, 0, 0),
					object.MustNewSHA256V1LocalReference("46d71098267fa33992257c061ba8fc48017e2bcac8f9ac3be8853c8337ec896e", 58511, 0, 0, 0),
					object.MustNewSHA256V1LocalReference("e1d1549332e44eddf28662dda4ca1aae36c3dcd597cd63b3c69737f88afd75d5", 213, 0, 0, 0),
				},
			),
			metadataCreator.Call,
		)

		references, metadata := m1.Patcher.SortAndSetReferences()
		require.Equal(t, object.OutgoingReferencesList[object.LocalReference]{
			object.MustNewSHA256V1LocalReference("46d71098267fa33992257c061ba8fc48017e2bcac8f9ac3be8853c8337ec896e", 58511, 0, 0, 0),
		}, references)
		require.Equal(t, []model_core.ReferenceMetadata{metadata1}, metadata)

		testutil.RequireEqualProto(t, &model_filesystem_pb.DirectoryEntry_Node{
			Name: "a",
			Kind: &model_filesystem_pb.DirectoryEntry_Node_File{File: &model_filesystem_pb.FileProperties{
				Contents: &model_filesystem_pb.FileContents{
					Level: &model_filesystem_pb.FileContents_ChunkReference{ChunkReference: model_core_pb.DecodableReference_builder{
						Reference: model_core_pb.Reference_builder{
							Index: 1,
						}.Build(),
					}.Build()},
					TotalSizeBytes: 23,
				},
			}},
		}, m1.Message)
	})

	t.Run("InvalidReference", func(t *testing.T) {
		// If a message contains invalid outgoing references, we
		// still permit the message to be copied. However, we do
		// want to set the indices to MaxUint32 to ensure that
		// any attempt to access them fails.
		metadataCreator := NewMockReferenceMetadataCreatorForTesting(ctrl)
		m1 := model_core.NewPatchedMessageFromExisting(
			model_core.NewMessage(
				&model_filesystem_pb.DirectoryEntry_Node{
					Name: "hello",
					Kind: &model_filesystem_pb.DirectoryEntry_Node_File{File: &model_filesystem_pb.FileProperties{
						Contents: &model_filesystem_pb.FileContents{
							Level: &model_filesystem_pb.FileContents_ChunkReference{ChunkReference: model_core_pb.DecodableReference_builder{
								Reference: model_core_pb.Reference_builder{
									Index: 42,
								}.Build(),
							}.Build()},
							TotalSizeBytes: 583,
						},
					}},
				},
				object.OutgoingReferencesList[object.LocalReference]{
					object.MustNewSHA256V1LocalReference("31233528b0ccc08d56724b2f132154967a89c4fb79de65fc65e3eeb42d9f89e4", 4828, 0, 0, 0),
				},
			),
			metadataCreator.Call,
		)

		references, metadata := m1.Patcher.SortAndSetReferences()
		require.Empty(t, references)
		require.Empty(t, metadata)

		testutil.RequireEqualProto(t, &model_filesystem_pb.DirectoryEntry_Node{
			Name: "hello",
			Kind: &model_filesystem_pb.DirectoryEntry_Node_File{File: &model_filesystem_pb.FileProperties{
				Contents: &model_filesystem_pb.FileContents{
					Level: &model_filesystem_pb.FileContents_ChunkReference{ChunkReference: model_core_pb.DecodableReference_builder{
						Reference: model_core_pb.Reference_builder{
							Index: math.MaxUint32,
						}.Build(),
					}.Build()},
					TotalSizeBytes: 583,
				},
			}},
		}, m1.Message)
	})
}

func TestMarshalOpaque(t *testing.T) {
	ctrl := gomock.NewController(t)

	t.Run("RoundTrip", func(t *testing.T) {
		// Create a message that only references the last of
		// three outgoing references.
		metadataCreator := NewMockReferenceMetadataCreatorForTesting(ctrl)
		metadata1 := NewMockReferenceMetadata(ctrl)
		metadataCreator.EXPECT().Call(2).Return(metadata1)
		m1 := model_core.NewPatchedMessageFromExisting(
			model_core.NewMessage(
				&model_filesystem_pb.FileContents{
					Level: &model_filesystem_pb.FileContents_ChunkReference{ChunkReference: model_core_pb.DecodableReference_builder{
						Reference: model_core_pb.Reference_builder{
							Index: 3,
						}.Build(),
					}.Build()},
					TotalSizeBytes: 23,
				},
				object.OutgoingReferencesList[object.LocalReference]{
					object.MustNewSHA256V1LocalReference("31233528b0ccc08d56724b2f132154967a89c4fb79de65fc65e3eeb42d9f89e4", 4828, 0, 0, 0),
					object.MustNewSHA256V1LocalReference("46d71098267fa33992257c061ba8fc48017e2bcac8f9ac3be8853c8337ec896e", 58511, 0, 0, 0),
					object.MustNewSHA256V1LocalReference("e1d1549332e44eddf28662dda4ca1aae36c3dcd597cd63b3c69737f88afd75d5", 213, 0, 0, 0),
				},
			),
			metadataCreator.Call,
		)

		m2, err := model_core.MarshalOpaque(m1.ToMarshaler(model_format.NewProto[model_filesystem_pb.FileContents]()))
		require.NoError(t, err)

		// The resulting Opaque should only reference the
		// single object that is referenced by the message.
		references, metadata := m2.Patcher.SortAndSetReferences()
		require.Equal(t, object.OutgoingReferencesList[object.LocalReference]{
			object.MustNewSHA256V1LocalReference("e1d1549332e44eddf28662dda4ca1aae36c3dcd597cd63b3c69737f88afd75d5", 213, 0, 0, 0),
		}, references)
		require.Equal(t, []model_core.ReferenceMetadata{metadata1}, metadata)
		testutil.RequireEqualProto(t, model_core_pb.ReferenceSet_builder{
			Indices: []uint32{1},
		}.Build(), m2.Message.GetReferences())

		// Unmarshaling it should yield the original message,
		// with the reference renumbered to be top-level.
		m3, err := model_core.UnmarshalOpaque(
			model_core.NewMessage(m2.Message, object.OutgoingReferences[object.LocalReference](references)),
			model_format.NewProto[model_filesystem_pb.FileContents](),
		)
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_filesystem_pb.FileContents{
			Level: &model_filesystem_pb.FileContents_ChunkReference{ChunkReference: model_core_pb.DecodableReference_builder{
				Reference: model_core_pb.Reference_builder{
					Index: 1,
				}.Build(),
			}.Build()},
			TotalSizeBytes: 23,
		}, m3.Message)
		require.Equal(t, 1, m3.OutgoingReferences.GetDegree())
		require.Equal(
			t,
			object.MustNewSHA256V1LocalReference("e1d1549332e44eddf28662dda4ca1aae36c3dcd597cd63b3c69737f88afd75d5", 213, 0, 0, 0),
			m3.OutgoingReferences.GetOutgoingReference(0),
		)

		// The same should hold when providing the type at runtime.
		m4, err := model_core.UnmarshalOpaque(
			model_core.NewMessage(m2.Message, object.OutgoingReferences[object.LocalReference](references)),
			model_format.NewDynamicProto((&model_filesystem_pb.FileContents{}).ProtoReflect().Type()),
		)
		require.NoError(t, err)
		testutil.RequireEqualProto(t, m3.Message, m4.Message)
	})
}
