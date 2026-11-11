package btree_test

import (
	"fmt"
	"math/rand/v2"
	"testing"

	"bonanza.build/pkg/encoding/varint"
	"bonanza.build/pkg/model/chunking"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	model_chunking_pb "bonanza.build/pkg/proto/model/chunking"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"

	"go.uber.org/mock/gomock"
	"google.golang.org/protobuf/proto"
)

func TestProllyChunkerFactory(t *testing.T) {
	ctrl := gomock.NewController(t)

	t.Run("Empty", func(t *testing.T) {
		// If we don't push any children into the level builder,
		// PopMultiple() should always return a nil node, even
		// when finalizing.
		chunkerFactory := btree.NewProllyChunkerFactory[*MockReferenceMetadata](
			util.Must(chunking.NewParametersFromProto(&model_chunking_pb.Parameters{
				MinimumSizeBytes: 1024,
				HorizonSizeBytes: 8 * 1024,
			}, util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1)))),
			/* isParent = */ func(contents *model_filesystem_pb.FileContents) bool {
				return contents.GetList() != nil
			},
		)
		chunker := chunkerFactory.NewChunker()

		require.Empty(t, chunker.PopMultiple(btree.PopDefinitive))
		require.Empty(t, chunker.PopMultiple(btree.PopAll))
	})

	t.Run("Streaming", func(t *testing.T) {
		// Popping objects while nodes are being pushed should
		// yield the same results as popping objects after all
		// nodes have been pushed. Furthermore, all objects
		// should respect the minimum size, and it should not be
		// possible to split them into two objects that both
		// respect the minimum size.
		const minimumSizeBytes = 1024
		chunkerFactory := btree.NewProllyChunkerFactory[*MockReferenceMetadata](
			util.Must(chunking.NewParametersFromProto(&model_chunking_pb.Parameters{
				MinimumSizeBytes: minimumSizeBytes,
				HorizonSizeBytes: 8 * minimumSizeBytes,
			}, util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1)))),
			/* isParent = */ func(contents *model_filesystem_pb.FileContents) bool {
				return contents.GetList() != nil
			},
		)

		for _, repeatedPercentage := range []int{0, 50, 95, 99} {
			t.Run(fmt.Sprintf("Repeated%d", repeatedPercentage), func(t *testing.T) {
				const nodesCount = 10000
				random := rand.New(rand.NewPCG(1, 2))
				totalSizesBytes := make([]uint64, 0, nodesCount)
				for range nodesCount {
					totalSizesBytes = append(totalSizesBytes, random.Uint64()>>random.IntN(64))
				}

				// Let a percentage of the nodes be identical to each
				// other, so that cuts are also placed based on the
				// nodes succeeding them.
				symbols := make([]int, 0, nodesCount)
				for i := range nodesCount {
					if random.IntN(100) < repeatedPercentage {
						symbols = append(symbols, 0)
					} else {
						symbols = append(symbols, i)
					}
				}
				newNode := func(i int) model_core.PatchedMessage[*model_filesystem_pb.FileContents, *MockReferenceMetadata] {
					i = symbols[i]
					return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[*MockReferenceMetadata]) *model_filesystem_pb.FileContents {
						return &model_filesystem_pb.FileContents{
							TotalSizeBytes: totalSizesBytes[i],
							Level: &model_filesystem_pb.FileContents_ChunkReference{
								ChunkReference: &model_core_pb.DecodableReference{
									Reference: patcher.AddReference(model_core.MetadataEntry[*MockReferenceMetadata]{
										LocalReference: object.MustNewSHA256V1LocalReference("5b2484693d5051be0fae63f4f862ce606cdc30ffbcd8a8a44b5b1b226b459262", uint32(i+1), 0, 0, 0),
										Metadata:       NewMockReferenceMetadata(ctrl),
									}),
								},
							},
						}
					})
				}

				getObjectSizes := func(popInBetween bool) [][]int {
					chunker := chunkerFactory.NewChunker()
					var objectSizes [][]int
					appendObjects := func(threshold btree.PopThreshold) {
						for {
							nodes := chunker.PopMultiple(threshold)
							if len(nodes) == 0 {
								return
							}
							nodeSizes := make([]int, 0, len(nodes))
							for _, node := range nodes {
								messageSizeBytes := proto.Size(node.Message)
								nodeSizes = append(nodeSizes, node.Patcher.GetReferencesSizeBytes()+varint.SizeBytes(messageSizeBytes)+messageSizeBytes)
							}
							objectSizes = append(objectSizes, nodeSizes)
						}
					}
					for i := range nodesCount {
						require.NoError(t, chunker.PushSingle(newNode(i)))
						if popInBetween {
							appendObjects(btree.PopDefinitive)
						}
					}
					appendObjects(btree.PopAll)
					return objectSizes
				}

				objectSizes := getObjectSizes(true)
				require.Equal(t, objectSizes, getObjectSizes(false))

				totalNodesCount := 0
				for _, nodeSizes := range objectSizes {
					totalNodesCount += len(nodeSizes)

					objectSizeBytes := 0
					for _, nodeSizeBytes := range nodeSizes {
						objectSizeBytes += nodeSizeBytes
					}
					require.GreaterOrEqual(t, objectSizeBytes, minimumSizeBytes)

					firstObjectSizeBytes := 0
					for _, nodeSizeBytes := range nodeSizes {
						if firstObjectSizeBytes >= minimumSizeBytes {
							break
						}
						firstObjectSizeBytes += nodeSizeBytes
					}
					require.Less(t, objectSizeBytes-firstObjectSizeBytes, minimumSizeBytes)
				}
				require.Equal(t, nodesCount, totalNodesCount)
				require.Greater(t, len(objectSizes), 100)
			})
		}
	})
}
