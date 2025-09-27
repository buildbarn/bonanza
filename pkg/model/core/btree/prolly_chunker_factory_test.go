package btree_test

import (
	"testing"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	"bonanza.build/pkg/storage/object"

	"github.com/stretchr/testify/require"

	"go.uber.org/mock/gomock"
)

func TestProllyChunkerFactory(t *testing.T) {
	ctrl := gomock.NewController(t)

	t.Run("Empty", func(t *testing.T) {
		// If we don't push any children into the level builder,
		// PopMultiple() should always return a nil node, even
		// when finalizing.
		chunkerFactory := btree.NewProllyChunkerFactory[model_core.ReferenceMetadata](
			/* minimumSizeBytes = */ 1024,
			/* maximumSizeBytes = */ 4*1024,
			/* isParent = */ func(contents *model_filesystem_pb.FileContents) bool {
				return contents.GetFileContentsListReference() != nil
			},
		)
		chunker := chunkerFactory.NewChunker()

		require.False(t, chunker.PopMultiple(false).IsSet())
		require.False(t, chunker.PopMultiple(true).IsSet())
	})

	t.Run("Tiny", func(t *testing.T) {
		t.Run("LeafNodes", func(t *testing.T) {
			// When the minimum and maximum sizes are set far too
			// low, we should still store at least one leaf node in
			// every object.
			chunkerFactory := btree.NewProllyChunkerFactory[model_core.ReferenceMetadata](
				/* minimumSizeBytes = */ 0,
				/* maximumSizeBytes = */ 0,
				/* isParent = */ func(contents *model_filesystem_pb.FileContents) bool {
					return contents.GetFileContentsListReference() != nil
				},
			)
			chunker := chunkerFactory.NewChunker()

			expectedMetadatas := make([]*MockReferenceMetadata, 0, 10)
			for i := 1000; i < 1010; i++ {
				metadata := NewMockReferenceMetadata(ctrl)
				require.NoError(t, chunker.PushSingle(
					model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.ReferenceMetadata]) *model_filesystem_pb.FileContents {
						return &model_filesystem_pb.FileContents{
							TotalSizeBytes: uint64(i),
							Level: &model_filesystem_pb.FileContents_ChunkReference{
								ChunkReference: &model_core_pb.DecodableReference{
									Reference: patcher.AddReference(model_core.MetadataEntry[model_core.ReferenceMetadata]{
										LocalReference: object.MustNewSHA256V1LocalReference("5b2484693d5051be0fae63f4f862ce606cdc30ffbcd8a8a44b5b1b226b459262", uint32(i), 0, 0, 0),
										Metadata:       metadata,
									}),
								},
							},
						}
					}),
				))
				expectedMetadatas = append(expectedMetadatas, metadata)
			}

			for i, metadata := range expectedMetadatas {
				nodes := chunker.PopMultiple(true)
				require.True(t, nodes.IsSet())

				references, actualMetadatas := nodes.Patcher.SortAndSetReferences()
				require.Equal(t, object.OutgoingReferencesList[object.LocalReference]{
					object.MustNewSHA256V1LocalReference("5b2484693d5051be0fae63f4f862ce606cdc30ffbcd8a8a44b5b1b226b459262", uint32(1000+i), 0, 0, 0),
				}, references)
				require.Equal(t, []model_core.ReferenceMetadata{
					metadata,
				}, actualMetadatas)
			}
			require.False(t, chunker.PopMultiple(true).IsSet())
		})

		t.Run("ParentNodes", func(t *testing.T) {
			// We should never store single parent nodes in
			// their own object, as that would just be
			// unnecessary indirection.
			chunkerFactory := btree.NewProllyChunkerFactory[model_core.ReferenceMetadata](
				/* minimumSizeBytes = */ 0,
				/* maximumSizeBytes = */ 0,
				/* isParent = */ func(contents *model_filesystem_pb.FileContents) bool {
					return contents.GetFileContentsListReference() != nil
				},
			)
			chunker := chunkerFactory.NewChunker()

			expectedMetadatas := make([]*MockReferenceMetadata, 0, 10)
			for i := 1000; i < 1010; i++ {
				metadata := NewMockReferenceMetadata(ctrl)
				require.NoError(t, chunker.PushSingle(
					model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.ReferenceMetadata]) *model_filesystem_pb.FileContents {
						return &model_filesystem_pb.FileContents{
							TotalSizeBytes: uint64(i),
							Level: &model_filesystem_pb.FileContents_FileContentsListReference{
								FileContentsListReference: &model_core_pb.DecodableReference{
									Reference: patcher.AddReference(model_core.MetadataEntry[model_core.ReferenceMetadata]{
										LocalReference: object.MustNewSHA256V1LocalReference("5b2484693d5051be0fae63f4f862ce606cdc30ffbcd8a8a44b5b1b226b459262", uint32(i), 0, 0, 0),
										Metadata:       metadata,
									}),
								},
							},
						}
					}),
				))
				expectedMetadatas = append(expectedMetadatas, metadata)
			}

			for i := 0; i < len(expectedMetadatas); i += 2 {
				nodes := chunker.PopMultiple(true)
				require.True(t, nodes.IsSet())

				references, actualMetadatas := nodes.Patcher.SortAndSetReferences()
				require.Equal(t, object.OutgoingReferencesList[object.LocalReference]{
					object.MustNewSHA256V1LocalReference("5b2484693d5051be0fae63f4f862ce606cdc30ffbcd8a8a44b5b1b226b459262", uint32(1000+i), 0, 0, 0),
					object.MustNewSHA256V1LocalReference("5b2484693d5051be0fae63f4f862ce606cdc30ffbcd8a8a44b5b1b226b459262", uint32(1001+i), 0, 0, 0),
				}, references)
				require.Equal(t, []model_core.ReferenceMetadata{
					expectedMetadatas[i],
					expectedMetadatas[i+1],
				}, actualMetadatas)
			}
			require.False(t, chunker.PopMultiple(true).IsSet())
		})
	})
}
