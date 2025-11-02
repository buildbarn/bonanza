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
		chunkerFactory := btree.NewProllyChunkerFactory[*MockReferenceMetadata](
			/* minimumSizeBytes = */ 1024,
			/* maximumSizeBytes = */ 4*1024,
			/* isParent = */ func(contents *model_filesystem_pb.FileContents) bool {
				return contents.GetList() != nil
			},
		)
		chunker := chunkerFactory.NewChunker()

		require.Empty(t, chunker.PopMultiple(btree.PopDefinitive))
		require.Empty(t, chunker.PopMultiple(btree.PopAll))
	})

	t.Run("Tiny", func(t *testing.T) {
		t.Run("LeafNodes", func(t *testing.T) {
			// When the minimum and maximum sizes are set far too
			// low, we should still store at least one leaf node in
			// every object.
			chunkerFactory := btree.NewProllyChunkerFactory[*MockReferenceMetadata](
				/* minimumSizeBytes = */ 0,
				/* maximumSizeBytes = */ 0,
				/* isParent = */ func(contents *model_filesystem_pb.FileContents) bool {
					return contents.GetList() != nil
				},
			)
			chunker := chunkerFactory.NewChunker()

			expectedNodes := make(model_core.PatchedMessageList[*model_filesystem_pb.FileContents, *MockReferenceMetadata], 0, 10)
			for i := 1000; i < 1010; i++ {
				node := model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[*MockReferenceMetadata]) *model_filesystem_pb.FileContents {
					return &model_filesystem_pb.FileContents{
						TotalSizeBytes: uint64(i),
						Level: &model_filesystem_pb.FileContents_ChunkReference{
							ChunkReference: &model_core_pb.DecodableReference{
								Reference: patcher.AddReference(model_core.MetadataEntry[*MockReferenceMetadata]{
									LocalReference: object.MustNewSHA256V1LocalReference("5b2484693d5051be0fae63f4f862ce606cdc30ffbcd8a8a44b5b1b226b459262", uint32(i), 0, 0, 0),
									Metadata:       NewMockReferenceMetadata(ctrl),
								}),
							},
						},
					}
				})
				require.NoError(t, chunker.PushSingle(node))
				expectedNodes = append(expectedNodes, node)
			}

			for i := range expectedNodes {
				require.Equal(t, expectedNodes[i:i+1], chunker.PopMultiple(btree.PopAll))
			}
			require.Empty(t, chunker.PopMultiple(btree.PopAll))
		})

		t.Run("ParentNodes", func(t *testing.T) {
			// We should never store single parent nodes in
			// their own object, as that would just be
			// unnecessary indirection.
			chunkerFactory := btree.NewProllyChunkerFactory[*MockReferenceMetadata](
				/* minimumSizeBytes = */ 0,
				/* maximumSizeBytes = */ 0,
				/* isParent = */ func(contents *model_filesystem_pb.FileContents) bool {
					return contents.GetList() != nil
				},
			)
			chunker := chunkerFactory.NewChunker()

			expectedNodes := make(model_core.PatchedMessageList[*model_filesystem_pb.FileContents, *MockReferenceMetadata], 0, 10)
			for i := 1000; i < 1010; i++ {
				node := model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[*MockReferenceMetadata]) *model_filesystem_pb.FileContents {
					return &model_filesystem_pb.FileContents{
						TotalSizeBytes: uint64(i),
						Level: &model_filesystem_pb.FileContents_List_{
							List: &model_filesystem_pb.FileContents_List{
								Reference: &model_core_pb.DecodableReference{
									Reference: patcher.AddReference(model_core.MetadataEntry[*MockReferenceMetadata]{
										LocalReference: object.MustNewSHA256V1LocalReference("5b2484693d5051be0fae63f4f862ce606cdc30ffbcd8a8a44b5b1b226b459262", uint32(i), 0, 0, 0),
										Metadata:       NewMockReferenceMetadata(ctrl),
									}),
								},
								Sparse: false,
							},
						},
					}
				})
				require.NoError(t, chunker.PushSingle(node))
				expectedNodes = append(expectedNodes, node)
			}

			for i := 0; i < len(expectedNodes); i += 2 {
				require.Equal(t, expectedNodes[i:i+2], chunker.PopMultiple(btree.PopAll))
			}
			require.Empty(t, chunker.PopMultiple(btree.PopAll))
		})
	})
}
