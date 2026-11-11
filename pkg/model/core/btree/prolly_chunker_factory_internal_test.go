package btree

import (
	"fmt"
	"math/rand/v2"
	"testing"

	model_core "bonanza.build/pkg/model/core"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	"bonanza.build/pkg/storage/object"

	"github.com/stretchr/testify/require"
)

// getLexicographicMaximumNaive provides the same behavior as
// getLexicographicMaximum(), but compares all sequences fully.
func getLexicographicMaximumNaive(hashes []uint64, cumulativeSizesBytes []int, windowSizeBytes, first, last int) int {
	best := first
	for i := first + 1; i <= last; i++ {
		for k := 0; k == 0 || cumulativeSizesBytes[i+k]-cumulativeSizesBytes[i] < windowSizeBytes; k++ {
			if h, hBest := hashes[i+k], hashes[best+k]; h > hBest {
				best = i
				break
			} else if h < hBest {
				break
			}
		}
	}
	return best
}

func TestGetLexicographicMaximum(t *testing.T) {
	// Compare against the naïve implementation using inputs with
	// a small alphabet, so that lots of repetition occurs.
	random := rand.New(rand.NewPCG(1, 2))
	for iteration := 0; iteration < 200000; iteration++ {
		alphabetSize := 1 + random.IntN(4)
		alphabetSizesBytes := make([]int, alphabetSize)
		for i := range alphabetSizesBytes {
			alphabetSizesBytes[i] = 1 + random.IntN(4)
		}

		var input []int
		if random.IntN(2) == 0 {
			// Random symbols.
			input = make([]int, 1+random.IntN(100))
			for i := range input {
				input[i] = random.IntN(alphabetSize)
			}
		} else {
			// Repetitions of a pattern, followed by
			// random symbols.
			pattern := make([]int, 1+random.IntN(5))
			for i := range pattern {
				pattern[i] = random.IntN(alphabetSize)
			}
			for range 1 + random.IntN(30) {
				input = append(input, pattern...)
			}
			for range random.IntN(10) {
				input = append(input, random.IntN(alphabetSize))
			}
		}

		hashes := make([]uint64, 0, len(input))
		cumulativeSizesBytes := make([]int, 1, len(input)+1)
		for _, symbol := range input {
			hashes = append(hashes, uint64(symbol))
			cumulativeSizesBytes = append(cumulativeSizesBytes, cumulativeSizesBytes[len(cumulativeSizesBytes)-1]+alphabetSizesBytes[symbol])
		}

		// Pick a range of indices for which the sequences are
		// fully contained in the input.
		windowSizeBytes := random.IntN(20)
		last := len(input) - 1
		for last >= 0 && cumulativeSizesBytes[len(input)]-cumulativeSizesBytes[last] < windowSizeBytes+4 {
			last--
		}
		if last < 0 {
			continue
		}
		first := random.IntN(last + 1)
		last = first + random.IntN(last-first+1)

		require.Equal(
			t,
			getLexicographicMaximumNaive(hashes, cumulativeSizesBytes, windowSizeBytes, first, last),
			getLexicographicMaximum(hashes, cumulativeSizesBytes, windowSizeBytes, first, last),
			fmt.Sprintf("input=%v sizes=%v window=%d first=%d last=%d", input, alphabetSizesBytes, windowSizeBytes, first, last),
		)
	}
}

func TestProllyChunkerFactoryTiny(t *testing.T) {
	// These tests construct prollyChunkerFactory directly, as
	// NewProllyChunkerFactory() only accepts validated parameters,
	// which disallow sizes this small.
	t.Run("LeafNodes", func(t *testing.T) {
		// When the minimum and horizon sizes are set far too
		// low, we should still store at least one leaf node in
		// every object.
		chunkerFactory := &prollyChunkerFactory[*model_filesystem_pb.FileContents, model_core.NoopReferenceMetadata]{
			isParent: func(contents *model_filesystem_pb.FileContents) bool {
				return contents.GetList() != nil
			},
		}
		chunker := chunkerFactory.NewChunker()

		expectedNodes := make(model_core.PatchedMessageList[*model_filesystem_pb.FileContents, model_core.NoopReferenceMetadata], 0, 10)
		for i := 1000; i < 1010; i++ {
			node := model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.NoopReferenceMetadata]) *model_filesystem_pb.FileContents {
				return &model_filesystem_pb.FileContents{
					TotalSizeBytes: uint64(i),
					Level: &model_filesystem_pb.FileContents_ChunkReference{
						ChunkReference: &model_core_pb.DecodableReference{
							Reference: patcher.AddReference(model_core.MetadataEntry[model_core.NoopReferenceMetadata]{
								LocalReference: object.MustNewSHA256V1LocalReference("5b2484693d5051be0fae63f4f862ce606cdc30ffbcd8a8a44b5b1b226b459262", uint32(i), 0, 0, 0),
								Metadata:       model_core.NoopReferenceMetadata{},
							}),
						},
					},
				}
			})
			require.NoError(t, chunker.PushSingle(node))
			expectedNodes = append(expectedNodes, node)
		}

		for i := range expectedNodes {
			require.Equal(t, expectedNodes[i:i+1], chunker.PopMultiple(PopAll))
		}
		require.Empty(t, chunker.PopMultiple(PopAll))
	})

	t.Run("ParentNodes", func(t *testing.T) {
		// We should never store single parent nodes in
		// their own object, as that would just be
		// unnecessary indirection.
		chunkerFactory := &prollyChunkerFactory[*model_filesystem_pb.FileContents, model_core.NoopReferenceMetadata]{
			isParent: func(contents *model_filesystem_pb.FileContents) bool {
				return contents.GetList() != nil
			},
		}
		chunker := chunkerFactory.NewChunker()

		expectedNodes := make(model_core.PatchedMessageList[*model_filesystem_pb.FileContents, model_core.NoopReferenceMetadata], 0, 10)
		for i := 1000; i < 1010; i++ {
			node := model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.NoopReferenceMetadata]) *model_filesystem_pb.FileContents {
				return &model_filesystem_pb.FileContents{
					TotalSizeBytes: uint64(i),
					Level: &model_filesystem_pb.FileContents_List_{
						List: &model_filesystem_pb.FileContents_List{
							Reference: &model_core_pb.DecodableReference{
								Reference: patcher.AddReference(model_core.MetadataEntry[model_core.NoopReferenceMetadata]{
									LocalReference: object.MustNewSHA256V1LocalReference("5b2484693d5051be0fae63f4f862ce606cdc30ffbcd8a8a44b5b1b226b459262", uint32(i), 0, 0, 0),
									Metadata:       model_core.NoopReferenceMetadata{},
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
			require.Equal(t, expectedNodes[i:i+2], chunker.PopMultiple(PopAll))
		}
		require.Empty(t, chunker.PopMultiple(PopAll))
	})
}
