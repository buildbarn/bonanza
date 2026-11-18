package core_test

import (
	"math"
	"testing"

	model_core "bonanza.build/pkg/model/core"
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
							Level: &model_filesystem_pb.FileContents_ChunkReference{
								ChunkReference: &model_core_pb.DecodableReference{
									Reference: &model_core_pb.Reference{
										Index: 2,
									},
								},
							},
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
					Level: &model_filesystem_pb.FileContents_ChunkReference{
						ChunkReference: &model_core_pb.DecodableReference{
							Reference: &model_core_pb.Reference{
								Index: 1,
							},
						},
					},
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
							Level: &model_filesystem_pb.FileContents_ChunkReference{
								ChunkReference: &model_core_pb.DecodableReference{
									Reference: &model_core_pb.Reference{
										Index: 42,
									},
								},
							},
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
					Level: &model_filesystem_pb.FileContents_ChunkReference{
						ChunkReference: &model_core_pb.DecodableReference{
							Reference: &model_core_pb.Reference{
								Index: math.MaxUint32,
							},
						},
					},
					TotalSizeBytes: 583,
				},
			}},
		}, m1.Message)
	})
}
