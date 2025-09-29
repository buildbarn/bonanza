package btree

import (
	model_core "bonanza.build/pkg/model/core"

	"google.golang.org/protobuf/proto"
)

type heightAwareBuilder[TNode proto.Message, TMetadata model_core.ReferenceMetadata] struct {
	chunkerFactory ChunkerFactory[TNode, TMetadata]
	nodeMerger     NodeMerger[TNode, TMetadata]

	levels []Chunker[TNode, TMetadata]
}

// NewHeightAwareBuilder creates a B-tree builder that is in the initial
// state (i.e., does not contain any nodes).
//
// Regular B-trees have a uniform depth, meaning that all leaves are
// stored at the same distance from the root. However, this
// implementation differs from that in that it stores leaves may be
// stored at higher levels in the tree, depending on their own height.
// This ensures that the overall height of the tree is minimized.
func NewHeightAwareBuilder[TNode proto.Message, TMetadata model_core.ReferenceMetadata](chunkerFactory ChunkerFactory[TNode, TMetadata], nodeMerger NodeMerger[TNode, TMetadata]) Builder[TNode, TMetadata] {
	return &heightAwareBuilder[TNode, TMetadata]{
		chunkerFactory: chunkerFactory,
		nodeMerger:     nodeMerger,
	}
}

func (b *heightAwareBuilder[TNode, TMetadata]) pushChildrenToParent(parentLevel int, children model_core.PatchedMessageList[TNode, TMetadata]) error {
	if parentLevel == len(b.levels) {
		b.levels = append(b.levels, b.chunkerFactory.NewChunker())
	}
	node, err := b.nodeMerger(children.Merge())
	if err != nil {
		return err
	}
	return b.levels[parentLevel].PushSingle(node)
}

// drainUpToLevel ensures that all chunkers up to a given level no
// longer contain any nodes. This is needed to ensure that if a node is
// inserted at a higher level in the B-tree, that the overall order of
// nodes remains consistent.
func (b *heightAwareBuilder[TNode, TMetadata]) drainUpToLevel(height int) error {
	for lowerLevel, lowerChunker := range b.levels[:height] {
		// First try to store as many nodes at the lower level
		// within their own objects.
		upperChunker := b.levels[lowerLevel+1]
		for {
			lowerNodes := lowerChunker.PopMultiple(PopLargeEnough)
			if len(lowerNodes) == 0 {
				break
			}
			upperNode, err := b.nodeMerger(lowerNodes.Merge())
			if err != nil {
				return err
			}
			if err := upperChunker.PushSingle(upperNode); err != nil {
				return err
			}
		}

		// If we don't have enough nodes at the lower level to
		// store within its own object, promote them to the
		// level above.
		for {
			lowerNodes := lowerChunker.PopMultiple(PopAll)
			if len(lowerNodes) == 0 {
				break
			}
			for index := range lowerNodes {
				if err := upperChunker.PushSingle(lowerNodes[index].Move()); err != nil {
					lowerNodes.Discard()
					return err
				}
			}
		}
	}
	return nil
}

// PushChild inserts a new node at the very end of the B-tree.
func (b *heightAwareBuilder[TNode, TMetadata]) PushChild(node model_core.PatchedMessage[TNode, TMetadata]) error {
	// If this node is preceded by nodes having a lower height, we
	// should ensure that those are either written to the tree.
	nodeLevel := node.Patcher.GetHeight()
	for len(b.levels) <= nodeLevel {
		b.levels = append(b.levels, b.chunkerFactory.NewChunker())
	}
	if err := b.drainUpToLevel(nodeLevel); err != nil {
		node.Discard()
		return err
	}

	// Insert the node at the level corresponding to its height.
	if err := b.levels[nodeLevel].PushSingle(node); err != nil {
		return err
	}

	// Inserting this node may yield new B-tree objects at this
	// level. Yield those and add a parent node in the level above.
	for childLevel := nodeLevel; childLevel < len(b.levels); childLevel++ {
		children := b.levels[childLevel].PopMultiple(PopDefinitive)
		if len(children) == 0 {
			return nil
		}
		parentLevel := childLevel + 1
		for {
			if err := b.pushChildrenToParent(parentLevel, children); err != nil {
				return err
			}
			children = b.levels[childLevel].PopMultiple(PopDefinitive)
			if len(children) == 0 {
				break
			}
		}
	}
	return nil
}

// FinalizeList finalizes the B-tree by returning the list of nodes to
// be contained in the root node. If the B-tree contains no entries, an
// empty list is returned.
func (b *heightAwareBuilder[TNode, TMetadata]) FinalizeList() (model_core.PatchedMessage[[]TNode, TMetadata], error) {
	if len(b.levels) == 0 {
		return model_core.NewSimplePatchedMessage[TMetadata, []TNode](nil), nil
	}

	// If the final nodes in the tree were preceded by nodes having
	// a larger height, then we may still have some objects at the
	// lower levels that don't contain enough nodes to reach the
	// minimum size. Ensure these are promoted to the higher levels.
	bottomLevels := len(b.levels) - 1
	if err := b.drainUpToLevel(bottomLevels); err != nil {
		return model_core.PatchedMessage[[]TNode, TMetadata]{}, err
	}

	// Drain parent nodes at the top until a single node root remains.
	for childLevel := bottomLevels; childLevel < len(b.levels); childLevel++ {
		parentLevel := childLevel + 1
		for {
			threshold := PopAll
			if parentLevel == len(b.levels) {
				// Only create an additional level at the top
				// of the tree if it has multiple children.
				threshold = PopChild
			}
			children := b.levels[childLevel].PopMultiple(threshold)
			if len(children) == 0 {
				break
			}
			if err := b.pushChildrenToParent(parentLevel, children); err != nil {
				return model_core.PatchedMessage[[]TNode, TMetadata]{}, err
			}
		}
	}

	return b.levels[len(b.levels)-1].PopMultiple(PopAll).Merge(), nil
}

// FinalizeSingle finalizes the B-tree by returning the root node. If
// the B-tree contains no entries, nothing is returned.
func (b *heightAwareBuilder[TNode, TMetadata]) FinalizeSingle() (model_core.PatchedMessage[TNode, TMetadata], error) {
	rootChildren, err := b.FinalizeList()
	if err != nil {
		return model_core.PatchedMessage[TNode, TMetadata]{}, err
	}
	switch len(rootChildren.Message) {
	case 0:
		return model_core.PatchedMessage[TNode, TMetadata]{}, nil
	case 1:
		return model_core.FlattenPatchedSingletonList(rootChildren.Move()), nil
	default:
		return b.nodeMerger(rootChildren.Move())
	}
}

func (b *heightAwareBuilder[TNode, TMetadata]) Discard() {
	for _, level := range b.levels {
		level.Discard()
	}
}
