package btree_test

import (
	"testing"

	"bonanza.build/pkg/model/chunking"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_format "bonanza.build/pkg/model/format"
	model_chunking_pb "bonanza.build/pkg/proto/model/chunking"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	"bonanza.build/pkg/storage/object"

	"github.com/stretchr/testify/require"

	"google.golang.org/protobuf/types/known/emptypb"
)

func TestHeightAwareBuilderRootSize(t *testing.T) {
	const minimumSizeBytes = 1024
	parameters, err := chunking.NewParametersFromProto(
		model_chunking_pb.Parameters_builder{
			MinimumSizeBytes: minimumSizeBytes,
			HorizonSizeBytes: 8 * 1024,
		}.Build(),
		object.SHA256V1ReferenceFormat,
	)
	require.NoError(t, err)

	newBuilder := func() btree.Builder[*model_filesystem_pb.FileContents, model_core.CreatedObjectTree] {
		return btree.NewHeightAwareBuilder(
			btree.NewProllyChunkerFactory[model_core.CreatedObjectTree](
				parameters,
				/* isParent = */ func(node *model_filesystem_pb.FileContents) bool {
					return node.GetList() != nil
				},
			),
			btree.NewObjectCreatingNodeMerger(
				model_encoding.NewChainedDeterministicBinaryEncoder(nil),
				object.SHA256V1ReferenceFormat,
				/* parentNodeComputer = */ func(createdObject model_core.Decodable[model_core.CreatedObject[model_core.CreatedObjectTree]], childNodes []*model_filesystem_pb.FileContents) (model_core.PatchedMessage[*model_filesystem_pb.FileContents, model_core.CreatedObjectTree], error) {
					totalSizeBytes := uint64(0)
					for _, childNode := range childNodes {
						totalSizeBytes += childNode.GetTotalSizeBytes()
					}
					return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_filesystem_pb.FileContents {
						return &model_filesystem_pb.FileContents{
							Level: &model_filesystem_pb.FileContents_List_{List: &model_filesystem_pb.FileContents_List{
								Reference: patcher.AddDecodableReference(
									model_core.CopyDecodable(
										createdObject,
										model_core.MetadataEntry[model_core.CreatedObjectTree]{
											LocalReference: createdObject.Value.GetLocalReference(),
											Metadata:       model_core.CreatedObjectTree(createdObject.Value),
										},
									),
								),
							}},
							TotalSizeBytes: totalSizeBytes,
						}
					}), nil
				},
			),
		)
	}

	// Create B-trees containing a varying number of nodes.
	finalize := func(nodesCount int) []*model_filesystem_pb.FileContents {
		builder := newBuilder()
		defer builder.Discard()
		for i := range nodesCount {
			require.NoError(t, builder.PushChild(model_core.NewSimplePatchedMessage[model_core.CreatedObjectTree](
				&model_filesystem_pb.FileContents{
					Level:          &model_filesystem_pb.FileContents_Hole{Hole: &emptypb.Empty{}},
					TotalSizeBytes: uint64(i*7919%1000 + 1),
				},
			)))
		}
		root, err := builder.FinalizeList()
		require.NoError(t, err)
		return root.Message
	}
	rootSizeBytes := func(root []*model_filesystem_pb.FileContents) int {
		data, err := model_format.NewProtoList[model_filesystem_pb.FileContents]().NewMarshaler(root).MarshalBinary()
		require.NoError(t, err)
		return len(data)
	}

	sawParents := false
	for nodesCount := 0; nodesCount < 2000; nodesCount += 37 {
		root := finalize(nodesCount)

		// The root must either be smaller than the minimum
		// object size, or consist of a single node.
		require.True(
			t,
			len(root) <= 1 || rootSizeBytes(root) < minimumSizeBytes,
			"Root for %d nodes is %d bytes in size", nodesCount, rootSizeBytes(root),
		)

		// The total size stored in the tree must be preserved.
		expectedTotalSizeBytes, totalSizeBytes := uint64(0), uint64(0)
		for i := range nodesCount {
			expectedTotalSizeBytes += uint64(i*7919%1000 + 1)
		}
		for _, node := range root {
			totalSizeBytes += node.GetTotalSizeBytes()
			if node.GetList() != nil {
				sawParents = true
			}
		}
		require.Equal(t, expectedTotalSizeBytes, totalSizeBytes)

		// Finalizing the same B-tree twice must yield the same
		// root.
		require.Equal(t, rootSizeBytes(root), rootSizeBytes(finalize(nodesCount)))
	}

	// Ensure that the test actually covers the case where nodes
	// need to be moved into separate objects.
	require.True(t, sawParents)
}
