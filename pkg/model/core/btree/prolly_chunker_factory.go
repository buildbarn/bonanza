package btree

import (
	"hash/fnv"
	"sort"

	"bonanza.build/pkg/encoding/varint"
	"bonanza.build/pkg/model/chunking"
	model_core "bonanza.build/pkg/model/core"

	"google.golang.org/protobuf/proto"
)

var marshalOptions = proto.MarshalOptions{UseCachedSize: true}

type prollyChunkerFactory[TNode proto.Message, TMetadata model_core.ReferenceMetadata] struct {
	minimumSizeBytes int
	horizonSizeBytes int
	isParent         func(TNode) bool
}

// NewProllyChunkerFactory returns a ChunkerFactory that is capable of
// creating Chunkers that perform probabilistic chunking, leading to the
// creation of a Prolly Tree:
//
// https://docs.dolthub.com/architecture/storage-engine/prolly-tree
//
// For each node that is inserted, an FNV-1a hash is computed based on
// the node's message contents and outgoing references. Cuts are placed
// after nodes using an algorithm that is similar to RepLexCDC, which is
// a variant of RepMaxCDC as described in "Content-Defined Chunking with
// tight chunk size bounds":
//
// https://github.com/buildbarn/go-cdc/blob/main/papers/cdc.pdf
//
// Each position at which a cut may be placed is assigned the sequence
// of hashes of the nodes starting at the node preceding it, limited to
// the nodes that start within the minimum size. Starting at the first
// position at which a cut may be placed, the position whose sequence of
// hashes is lexicographically maximal within the horizon size is
// selected. As hashes are 64 bits in size, this is almost always
// decided by the hash of the node preceding the cut. Only if nodes are
// repeated, the nodes succeeding it are considered. This prevents the
// selection of cuts from depending on where the previous cut was placed
// in case the input has low entropy.
//
// If cutting at the selected position would yield an object that could
// have been split up into two objects that both respect the minimum
// size, the search is repeated, only considering positions that still
// permit the previously selected position to be used as a cut
// afterwards.
//
// Objects are thus at least the minimum size. If nodes were
// infinitesimally small, objects would be at most the maximum size
// (twice the minimum size, minus one byte). In practice objects may be
// somewhat larger, as nodes cannot be split. Increasing the horizon
// size improves the stability of the cuts, at the cost of buffering
// more nodes before objects can be emitted.
//
// The size computation that is used by this type assumes that the
// resulting nodes are stored in an object where each node is prefixed
// with a variable length integer indicating the node's size.
func NewProllyChunkerFactory[TMetadata model_core.ReferenceMetadata, TNode proto.Message](
	parameters chunking.Parameters,
	isParent func(TNode) bool,
) ChunkerFactory[TNode, TMetadata] {
	return &prollyChunkerFactory[TNode, TMetadata]{
		minimumSizeBytes: parameters.GetMinimumSizeBytes(),
		horizonSizeBytes: parameters.GetHorizonSizeBytes(),
		isParent:         isParent,
	}
}

func (cf *prollyChunkerFactory[TNode, TMetadata]) NewChunker() Chunker[TNode, TMetadata] {
	return &prollyChunker[TNode, TMetadata]{
		factory:              cf,
		cumulativeSizesBytes: []int{0},
	}
}

type prollyChunker[TNode proto.Message, TMetadata model_core.ReferenceMetadata] struct {
	factory *prollyChunkerFactory[TNode, TMetadata]

	// Nodes that have been pushed, but not popped yet.
	nodes model_core.PatchedMessageList[TNode, TMetadata]

	// The total size of nodes[:i] is stored at index i. This list
	// is thus one element longer than the list of nodes.
	cumulativeSizesBytes []int

	// The hash of the node at index i, which is used to determine
	// the suitability of placing a cut after it, and the ones
	// before it. Hashes are only computed if the nodes are going to
	// be stored in at least two objects.
	hashes []uint64

	// The number of leading nodes for which there is no need to
	// compute a hash anymore, either because the hash has already
	// been computed, or because no cut can be placed after them.
	hashesComputed int
}

// isLargeEnough returns true if the nodes in range [start, end) may be
// stored in an object. This is the case if the nodes have the minimum
// size, and the object does not consist of a single parent node, as
// that would only add unnecessary indirection.
func (c *prollyChunker[TNode, TMetadata]) isLargeEnough(start, end int) bool {
	cf := c.factory
	count := end - start
	return c.cumulativeSizesBytes[end]-c.cumulativeSizesBytes[start] >= cf.minimumSizeBytes &&
		(count >= 2 || (count == 1 && !cf.isParent(c.nodes[start].Message)))
}

// getFirstCutPoint returns the first position at which a cut may be
// placed, such that the nodes preceding it are large enough to be
// stored in an object.
func (c *prollyChunker[TNode, TMetadata]) getFirstCutPoint() (int, bool) {
	cf := c.factory
	firstCutPoint := max(
		sort.SearchInts(c.cumulativeSizesBytes, cf.minimumSizeBytes),
		1,
	)
	if firstCutPoint == 1 && len(c.nodes) > 0 && cf.isParent(c.nodes[0].Message) {
		firstCutPoint = 2
	}
	return firstCutPoint, firstCutPoint <= len(c.nodes)
}

// getLastCutPointInHorizon returns the last position at which a cut
// may be placed, given the first position at which a cut may be placed.
func (c *prollyChunker[TNode, TMetadata]) getLastCutPointInHorizon(firstCutPoint int) int {
	return sort.SearchInts(
		c.cumulativeSizesBytes,
		c.cumulativeSizesBytes[firstCutPoint]+c.factory.horizonSizeBytes+1,
	) - 1
}

func (c *prollyChunker[TNode, TMetadata]) PushSingle(node model_core.PatchedMessage[TNode, TMetadata]) error {
	// Schedule the nodes for insertion into objects. Don't insert
	// them immediately, as we need to keep some at hand to ensure
	// cuts are placed at the most optimal position, and the final
	// object on this level respects the minimum number of objects
	// and size.
	messageSizeBytes := marshalOptions.Size(node.Message)
	sizeBytes := node.Patcher.GetReferencesSizeBytes() + varint.SizeBytes(messageSizeBytes) + messageSizeBytes
	c.nodes = append(c.nodes, node)
	c.cumulativeSizesBytes = append(c.cumulativeSizesBytes, c.cumulativeSizesBytes[len(c.cumulativeSizesBytes)-1]+sizeBytes)
	c.hashes = append(c.hashes, 0)

	// Compute hashes of nodes that may need to be compared to
	// determine where cuts are placed. Only do this if the nodes
	// can be stored in at least two objects. This prevents us from
	// computing hashes for levels of the B-tree that only consist
	// of a single object.
	firstCutPoint, ok := c.getFirstCutPoint()
	if !ok || !c.isLargeEnough(firstCutPoint, len(c.nodes)) {
		return nil
	}
	for ; c.hashesComputed < len(c.nodes); c.hashesComputed++ {
		if c.hashesComputed >= firstCutPoint-1 {
			hasher := fnv.New64a()
			node := &c.nodes[c.hashesComputed]
			references, _ := node.Patcher.SortAndSetReferences()
			for _, reference := range references {
				hasher.Write(reference.GetRawReference())
			}
			m, err := marshalOptions.Marshal(node.Message)
			if err != nil {
				return err
			}
			hasher.Write(m)
			c.hashes[c.hashesComputed] = hasher.Sum64()
		}
	}
	return nil
}

// getLexicographicMaximum returns the index i in range [first, last]
// for which the sequence of hashes starting at index i is
// lexicographically maximal. Sequences are limited to the elements
// that start within windowSizeBytes, but always contain at least one
// element. If multiple sequences are equal, the first one is returned.
//
// The caller must ensure that the sequences of all indices in range
// [first, last] are fully contained within the hashes that are
// provided.
//
// A naïve implementation of this function would have a running time
// that is quadratic in case of repetitive inputs. This implementation
// uses an algorithm similar to Duval's algorithm for computing maximum
// suffixes, which runs in linear time.
func getLexicographicMaximum(hashes []uint64, cumulativeSizesBytes []int, windowSizeBytes, first, last int) int {
	// best: the index of the best sequence found so far.
	// bestRepeated: the highest index at which the same sequence
	// as the best one is found.
	// i: the index of the sequence that is currently compared
	// against the one at bestRepeated.
	// matched: the number of leading elements of the sequences at
	// bestRepeated and i that are known to be equal.
	best, bestRepeated := first, first
	i, matched := first+1, 0
	for i <= last {
		period := i - bestRepeated
		if matched > 0 && cumulativeSizesBytes[i+matched]-cumulativeSizesBytes[i] >= windowSizeBytes {
			// The sequences at bestRepeated and i are
			// equal. Prefer the first occurrence. If the
			// input is periodic, skip ahead by a full
			// period.
			bestRepeated = i
			if matched >= period {
				i += period
				matched -= period
			} else {
				i++
				matched = 0
			}
		} else if h, hBest := hashes[i+matched], hashes[bestRepeated+matched]; h < hBest {
			// None of the sequences in range [i, i+matched]
			// are better than the best one.
			i += matched + 1
			matched = 0
		} else if h > hBest {
			// The sequence at index i is better. If the
			// input is periodic, the sequences at the next
			// periods are even better.
			best = i + min(matched, last-i)/period*period
			bestRepeated = best
			i = best + 1
			matched = 0
		} else {
			matched++
		}
	}
	return best
}

// findCutPoint determines the position at which the first object on
// this level should end, assuming that no more nodes are pushed.
func (c *prollyChunker[TNode, TMetadata]) findCutPoint() int {
	firstCutPoint, ok := c.getFirstCutPoint()
	if !ok || !c.isLargeEnough(firstCutPoint, len(c.nodes)) {
		// The remaining nodes cannot be split into multiple
		// objects that are large enough.
		return len(c.nodes)
	}

	// Find the position within the horizon whose sequence of
	// hashes is lexicographically maximal. If cutting at this
	// position would yield an object that is too large, repeat the
	// search with a smaller horizon, so that the position can still
	// be considered for the next object.
	lastCutPoint := c.getLastCutPointInHorizon(firstCutPoint)
	nextObjectEnd := len(c.nodes)
	for {
		for !c.isLargeEnough(lastCutPoint, nextObjectEnd) {
			lastCutPoint--
		}
		bestCutPoint := getLexicographicMaximum(
			c.hashes,
			c.cumulativeSizesBytes,
			c.factory.minimumSizeBytes,
			firstCutPoint-1,
			lastCutPoint-1,
		) + 1
		if !c.isLargeEnough(firstCutPoint, bestCutPoint) {
			return bestCutPoint
		}
		nextObjectEnd = bestCutPoint
	}
}

func (c *prollyChunker[TNode, TMetadata]) PopMultiple(threshold PopThreshold) model_core.PatchedMessageList[TNode, TMetadata] {
	// Determine whether we've collected enough nodes to be able to
	// create a new object.
	switch threshold {
	case PopDefinitive:
		// Only pop an object if all positions within the
		// horizon have been observed, and are followed by
		// enough nodes to fill another object. Pushing more
		// nodes can then no longer affect where the cut is
		// placed.
		firstCutPoint, ok := c.getFirstCutPoint()
		if !ok || !c.isLargeEnough(c.getLastCutPointInHorizon(firstCutPoint), len(c.nodes)) {
			return nil
		}
	case PopChild:
		firstCutPoint, ok := c.getFirstCutPoint()
		if !ok || !c.isLargeEnough(firstCutPoint, len(c.nodes)) {
			return nil
		}
	case PopLargeEnough:
		if !c.isLargeEnough(0, len(c.nodes)) {
			return nil
		}
	case PopAll:
		if len(c.nodes) == 0 {
			return nil
		}
	default:
		panic("unknown pop threshold")
	}

	// Remove nodes that are placed in the object.
	cutPoint := c.findCutPoint()
	nodes := c.nodes[:cutPoint]
	c.nodes = c.nodes[cutPoint:]
	c.cumulativeSizesBytes = c.cumulativeSizesBytes[cutPoint:]
	cutSizeBytes := c.cumulativeSizesBytes[0]
	for i := range c.cumulativeSizesBytes {
		c.cumulativeSizesBytes[i] -= cutSizeBytes
	}
	c.hashes = c.hashes[cutPoint:]
	c.hashesComputed = max(c.hashesComputed-cutPoint, 0)
	return nodes
}

func (c *prollyChunker[TNode, TMetadata]) Discard() {
	c.nodes.Discard()
}
