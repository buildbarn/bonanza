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
		chunkerFactory := btree.NewProllyChunkerFactory[*model_filesystem_pb.FileContents, model_core.ReferenceMetadata](
			/* minimumCount = */ 2,
			/* minimumSizeBytes = */ 1024,
			/* maximumSizeBytes = */ 4*1024,
		)
		chunker := chunkerFactory.NewChunker()

		require.False(t, chunker.PopMultiple(false).IsSet())
		require.False(t, chunker.PopMultiple(true).IsSet())
	})

	t.Run("TinyNodes", func(t *testing.T) {
		// Test that minimumCount is respected when creating
		// nodes. We insert 10 nodes. These should be spread out
		// across 3 objects containing [3, 3, 4] nodes.
		chunkerFactory := btree.NewProllyChunkerFactory[*model_filesystem_pb.FileContents, model_core.ReferenceMetadata](
			/* minimumCount = */ 3,
			/* minimumSizeBytes = */ 1,
			/* maximumSizeBytes = */ 2,
		)
		chunker := chunkerFactory.NewChunker()

		metadatas := make([]*MockReferenceMetadata, 0, 10)
		for i := 1000; i < 1010; i++ {
			metadata := NewMockReferenceMetadata(ctrl)
			require.NoError(t, chunker.PushSingle(
				model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.ReferenceMetadata]) *model_filesystem_pb.FileContents {
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
			metadatas = append(metadatas, metadata)
		}

		// The first call to PopMultiple() should construct a
		// list of the first three nodes.
		nodes := chunker.PopMultiple(false)
		require.True(t, nodes.IsSet())

		references, metadata := nodes.Patcher.SortAndSetReferences()
		require.Equal(t, object.OutgoingReferencesList[object.LocalReference]{
			object.MustNewSHA256V1LocalReference("5b2484693d5051be0fae63f4f862ce606cdc30ffbcd8a8a44b5b1b226b459262", uint32(1000), 0, 0, 0),
			object.MustNewSHA256V1LocalReference("5b2484693d5051be0fae63f4f862ce606cdc30ffbcd8a8a44b5b1b226b459262", uint32(1001), 0, 0, 0),
			object.MustNewSHA256V1LocalReference("5b2484693d5051be0fae63f4f862ce606cdc30ffbcd8a8a44b5b1b226b459262", uint32(1002), 0, 0, 0),
		}, references)
		require.Equal(t, []model_core.ReferenceMetadata{
			metadatas[0],
			metadatas[1],
			metadatas[2],
		}, metadata)

		// The second call to PopMultiple() should construct a
		// list of the next three nodes.
		nodes = chunker.PopMultiple(false)
		require.True(t, nodes.IsSet())

		references, metadata = nodes.Patcher.SortAndSetReferences()
		require.Equal(t, object.OutgoingReferencesList[object.LocalReference]{
			object.MustNewSHA256V1LocalReference("5b2484693d5051be0fae63f4f862ce606cdc30ffbcd8a8a44b5b1b226b459262", uint32(1003), 0, 0, 0),
			object.MustNewSHA256V1LocalReference("5b2484693d5051be0fae63f4f862ce606cdc30ffbcd8a8a44b5b1b226b459262", uint32(1004), 0, 0, 0),
			object.MustNewSHA256V1LocalReference("5b2484693d5051be0fae63f4f862ce606cdc30ffbcd8a8a44b5b1b226b459262", uint32(1005), 0, 0, 0),
		}, references)
		require.Equal(t, []model_core.ReferenceMetadata{
			metadatas[3],
			metadatas[4],
			metadatas[5],
		}, metadata)

		// The third call to PopMultiple() should not return
		// anything, as we have 4 nodes remaining. As
		// minimumCount is set to 3, we either have to return
		// all 4 nodes, or at least 2 nodes to be pushed.
		require.False(t, chunker.PopMultiple(false).IsSet())

		// If finalization is requested, the final 4 nodes
		// should be returned as part of a single list, as there
		// is no way to split them.
		nodes = chunker.PopMultiple(true)
		require.True(t, nodes.IsSet())

		references, metadata = nodes.Patcher.SortAndSetReferences()
		require.Equal(t, object.OutgoingReferencesList[object.LocalReference]{
			object.MustNewSHA256V1LocalReference("5b2484693d5051be0fae63f4f862ce606cdc30ffbcd8a8a44b5b1b226b459262", uint32(1006), 0, 0, 0),
			object.MustNewSHA256V1LocalReference("5b2484693d5051be0fae63f4f862ce606cdc30ffbcd8a8a44b5b1b226b459262", uint32(1007), 0, 0, 0),
			object.MustNewSHA256V1LocalReference("5b2484693d5051be0fae63f4f862ce606cdc30ffbcd8a8a44b5b1b226b459262", uint32(1008), 0, 0, 0),
			object.MustNewSHA256V1LocalReference("5b2484693d5051be0fae63f4f862ce606cdc30ffbcd8a8a44b5b1b226b459262", uint32(1009), 0, 0, 0),
		}, references)
		require.Equal(t, []model_core.ReferenceMetadata{
			metadatas[6],
			metadatas[7],
			metadatas[8],
			metadatas[9],
		}, metadata)

		// Once all nodes have been processed, PopMultiple()
		// should no longer do anything, even if finalization is
		// requested.
		require.False(t, chunker.PopMultiple(true).IsSet())
	})
}
