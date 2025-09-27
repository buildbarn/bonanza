package btree

import (
	"hash/fnv"

	"bonanza.build/pkg/encoding/varint"
	model_core "bonanza.build/pkg/model/core"

	"google.golang.org/protobuf/proto"
)

var marshalOptions = proto.MarshalOptions{UseCachedSize: true}

type prollyChunkerFactory[TNode proto.Message, TMetadata model_core.ReferenceMetadata] struct {
	minimumSizeBytes int
	maximumSizeBytes int
	isParent         func(TNode) bool
}

// NewProllyChunkerFactory returns a ChunkerFactory that is capable of
// creating Chunkers that perform probabilistic chunking, leading to the
// creation of a Prolly Tree:
//
// https://docs.dolthub.com/architecture/storage-engine/prolly-tree
//
// For each node that is inserted, an FNV-1a hash is computed based on
// the node's message contents and outgoing references. A cut is made
// after the node where the hash is maximal.
//
// The size computation that is used by this type assumes that the
// resulting nodes are stored in an object where each node is prefixed
// with a variable length integer indicating the node's size.
func NewProllyChunkerFactory[TMetadata model_core.ReferenceMetadata, TNode proto.Message](
	minimumSizeBytes, maximumSizeBytes int,
	isParent func(TNode) bool,
) ChunkerFactory[TNode, TMetadata] {
	return &prollyChunkerFactory[TNode, TMetadata]{
		minimumSizeBytes: minimumSizeBytes,
		maximumSizeBytes: maximumSizeBytes,
		isParent:         isParent,
	}
}

func (cf *prollyChunkerFactory[TNode, TMetadata]) NewChunker() Chunker[TNode, TMetadata] {
	return &prollyChunker[TNode, TMetadata]{
		factory: cf,
		cuts:    make([]prollyCut, 2),
	}
}

type prollyCutPoint struct {
	index               int
	cumulativeSizeBytes int
}

type prollyCut struct {
	point prollyCutPoint
	hash  uint64
}

type prollyChunker[TNode proto.Message, TMetadata model_core.ReferenceMetadata] struct {
	factory *prollyChunkerFactory[TNode, TMetadata]

	nodes               []model_core.PatchedMessage[TNode, TMetadata]
	uncutSizesBytes     []int
	totalUncutSizeBytes int
	cuts                []prollyCut
}

func (c *prollyChunker[TNode, TMetadata]) isLargeEnough(start, end prollyCutPoint) bool {
	cf := c.factory
	count := end.index - start.index
	return end.cumulativeSizeBytes-start.cumulativeSizeBytes >= cf.minimumSizeBytes &&
		(count >= 2 || (count == 1 && !cf.isParent(c.nodes[start.index].Message)))
}

func (c *prollyChunker[TNode, TMetadata]) PushSingle(node model_core.PatchedMessage[TNode, TMetadata]) error {
	// Schedule the nodes for insertion into objects. Don't insert
	// them immediately, as we need to keep some at hand to ensure
	// the final object on this level respects the minimum number of
	// objects and size.
	messageSizeBytes := marshalOptions.Size(node.Message)
	sizeBytes := node.Patcher.GetReferencesSizeBytes() + varint.SizeBytes(messageSizeBytes) + messageSizeBytes
	c.nodes = append(c.nodes, node)
	c.uncutSizesBytes = append(c.uncutSizesBytes, sizeBytes)
	c.totalUncutSizeBytes += sizeBytes

	cf := c.factory
	for {
		lastCutPoint := c.cuts[len(c.cuts)-1].point
		newCutPoint := prollyCutPoint{
			index:               lastCutPoint.index + 1,
			cumulativeSizeBytes: lastCutPoint.cumulativeSizeBytes + c.uncutSizesBytes[0],
		}
		if !c.isLargeEnough(
			newCutPoint,
			prollyCutPoint{
				index:               len(c.nodes),
				cumulativeSizeBytes: lastCutPoint.cumulativeSizeBytes + c.totalUncutSizeBytes,
			},
		) {
			return nil
		}

		// We've got enough nodes to fill the last object on
		// this level. Add a new potential cut.
		var hash uint64
		if c.isLargeEnough(prollyCutPoint{}, newCutPoint) {
			// This node lies within a range where we can
			// eventually expect cuts to appear. Compute an
			// FNV-1a hash of the node, so that we can
			// perform the cut at the position where the
			// hash is maximized.
			hasher := fnv.New64a()
			node := &c.nodes[lastCutPoint.index]
			references, _ := node.Patcher.SortAndSetReferences()
			for _, reference := range references {
				hasher.Write(reference.GetRawReference())
			}
			m, err := marshalOptions.Marshal(node.Message)
			if err != nil {
				return err
			}
			hasher.Write(m)
			hash = hasher.Sum64()
		}
		c.totalUncutSizeBytes -= c.uncutSizesBytes[0]
		c.uncutSizesBytes = c.uncutSizesBytes[1:]

		// Insert the new cut, potentially collapsing some of
		// the cuts we added previously.
		cutsToKeep := len(c.cuts)
		for cutsToKeep >= 2 &&
			(!c.isLargeEnough(c.cuts[cutsToKeep-2].point, c.cuts[cutsToKeep-1].point) ||
				(c.cuts[cutsToKeep-1].hash < hash &&
					newCutPoint.cumulativeSizeBytes-c.cuts[cutsToKeep-2].point.cumulativeSizeBytes <= cf.maximumSizeBytes)) {
			cutsToKeep--
		}
		c.cuts = append(c.cuts[:cutsToKeep], prollyCut{
			point: newCutPoint,
			hash:  hash,
		})
	}
}

func (c *prollyChunker[TNode, TMetadata]) PopMultiple(finalize bool) model_core.PatchedMessage[[]TNode, TMetadata] {
	// Determine whether we've collected enough nodes to be able to
	// create a new object, and how many nodes should go into it.
	cf := c.factory
	cutPoint := c.cuts[1].point
	if finalize {
		if len(c.nodes) == 0 {
			return model_core.PatchedMessage[[]TNode, TMetadata]{}
		}
		if !c.isLargeEnough(prollyCutPoint{}, cutPoint) {
			// We've reached the end. Add all nodes for
			// which we didn't compute cuts yet, thereby
			// ensuring that the last object also respects
			// the minimum node count and size limit.
			if len(c.cuts) > 2 {
				panic("can't have multiple cuts if the first cut is still below limits")
			}
			cutPoint = prollyCutPoint{
				index:               len(c.nodes),
				cumulativeSizeBytes: c.cuts[len(c.cuts)-1].point.cumulativeSizeBytes + c.totalUncutSizeBytes,
			}
			c.uncutSizesBytes = c.uncutSizesBytes[:0]
			c.totalUncutSizeBytes = 0
		}
	} else {
		if !c.isLargeEnough(prollyCutPoint{}, cutPoint) || c.cuts[len(c.cuts)-1].point.cumulativeSizeBytes < cf.maximumSizeBytes {
			return model_core.PatchedMessage[[]TNode, TMetadata]{}
		}
	}

	// Remove nodes and cuts.
	nodes := c.nodes[:cutPoint.index]
	c.nodes = c.nodes[cutPoint.index:]
	if len(c.cuts) > 2 {
		for i := 1; i < len(c.cuts)-1; i++ {
			c.cuts[i] = prollyCut{
				point: prollyCutPoint{
					index:               c.cuts[i+1].point.index - cutPoint.index,
					cumulativeSizeBytes: c.cuts[i+1].point.cumulativeSizeBytes - cutPoint.cumulativeSizeBytes,
				},
				hash: c.cuts[i+1].hash,
			}
		}
		c.cuts = c.cuts[:len(c.cuts)-1]
	} else {
		c.cuts = append(c.cuts[:1], prollyCut{})
	}

	// Combine the nodes into a single list.
	return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) []TNode {
		messages := make([]TNode, 0, len(nodes))
		for _, node := range nodes {
			messages = append(messages, node.Message)
			patcher.Merge(node.Patcher)
		}
		return messages
	})
}

func (c *prollyChunker[TNode, TMetadata]) Discard() {
	for i := 0; i < len(c.nodes); i++ {
		c.nodes[i].Discard()
	}
}
