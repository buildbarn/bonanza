package btree

import (
	model_core "bonanza.build/pkg/model/core"

	"google.golang.org/protobuf/proto"
)

type uniformBuilder[TNode proto.Message, TMetadata model_core.ReferenceMetadata] struct {
	chunkerFactory ChunkerFactory[TNode, TMetadata]
	nodeMerger     NodeMerger[TNode, TMetadata]

	levels []Chunker[TNode, TMetadata]
}

// NewUniformBuilder creates a B-tree builder that is in the initial
// state (i.e., does not contain any nodes). The resulting B-tree will
// be uniform, meaning that all layers will be constructed using the
// same Chunker.
func NewUniformBuilder[TNode proto.Message, TMetadata model_core.ReferenceMetadata](chunkerFactory ChunkerFactory[TNode, TMetadata], nodeMerger NodeMerger[TNode, TMetadata]) Builder[TNode, TMetadata] {
	return &uniformBuilder[TNode, TMetadata]{
		chunkerFactory: chunkerFactory,
		nodeMerger:     nodeMerger,
	}
}

func (b *uniformBuilder[TNode, TMetadata]) pushChildrenToParent(level int, children model_core.PatchedMessageList[TNode, TMetadata]) error {
	if level == len(b.levels) {
		b.levels = append(b.levels, b.chunkerFactory.NewChunker())
	}
	node, err := b.nodeMerger(children.Merge())
	if err != nil {
		return err
	}
	return b.levels[level].PushSingle(node)
}

// PushChild inserts a new node at the very end of the B-tree.
func (b *uniformBuilder[TNode, TMetadata]) PushChild(node model_core.PatchedMessage[TNode, TMetadata]) error {
	if len(b.levels) == 0 {
		b.levels = append(b.levels, b.chunkerFactory.NewChunker())
	}
	if err := b.levels[0].PushSingle(node); err != nil {
		return err
	}

	// See if there are any new parent nodes that we can propagate upward.
	for childLevel := 0; childLevel < len(b.levels); childLevel++ {
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

// Drain parent nodes at each level until a single node root remains.
func (b *uniformBuilder[TNode, TMetadata]) drain() error {
	for childLevel := 0; childLevel < len(b.levels); childLevel++ {
		parentLevel := childLevel + 1
		for {
			threshold := PopAll
			if parentLevel == len(b.levels) {
				// Only create an additional level at
				// the top of the tree if it has
				// multiple children.
				threshold = PopChild
			}
			children := b.levels[childLevel].PopMultiple(threshold)
			if len(children) == 0 {
				break
			}
			if err := b.pushChildrenToParent(parentLevel, children); err != nil {
				return err
			}
		}
	}
	return nil
}

// FinalizeList finalizes the B-tree by returning the list of nodes to
// be contained in the root node. If the B-tree contains no entries, an
// empty list is returned.
func (b *uniformBuilder[TNode, TMetadata]) FinalizeList() (model_core.PatchedMessage[[]TNode, TMetadata], error) {
	if len(b.levels) == 0 {
		return model_core.NewSimplePatchedMessage[TMetadata, []TNode](nil), nil
	}
	if err := b.drain(); err != nil {
		return model_core.PatchedMessage[[]TNode, TMetadata]{}, err
	}
	return b.levels[len(b.levels)-1].PopMultiple(PopAll).Merge(), nil
}

// FinalizeSingle finalizes the B-tree by returning the root node. If
// the B-tree contains no entries, nothing is returned.
func (b *uniformBuilder[TNode, TMetadata]) FinalizeSingle() (model_core.PatchedMessage[TNode, TMetadata], error) {
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

func (b *uniformBuilder[TNode, TMetadata]) Discard() {
	for _, level := range b.levels {
		level.Discard()
	}
}
