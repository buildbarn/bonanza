package btree

import (
	model_core "bonanza.build/pkg/model/core"

	"google.golang.org/protobuf/proto"
)

type splitBuilder[TNode proto.Message, TMetadata model_core.ReferenceMetadata] struct {
	leavesChunker    Chunker[TNode, TMetadata]
	leavesNodeMerger NodeMerger[TNode, TMetadata]
	parentsBuilder   Builder[TNode, TMetadata]

	multipleLevels bool
	firstChildren  model_core.PatchedMessage[[]TNode, TMetadata]
}

// NewSplitBuilder creates a Builder that can use a different Chunker
// for leaf and parent objects. This can, for example, be used to make
// leaf nodes larger than parents.
func NewSplitBuilder[TNode proto.Message, TMetadata model_core.ReferenceMetadata](leavesChunker Chunker[TNode, TMetadata], leavesNodeMerger NodeMerger[TNode, TMetadata], parentsBuilder Builder[TNode, TMetadata]) Builder[TNode, TMetadata] {
	return &splitBuilder[TNode, TMetadata]{
		leavesChunker:    leavesChunker,
		leavesNodeMerger: leavesNodeMerger,
		parentsBuilder:   parentsBuilder,
	}
}

func (b *splitBuilder[TNode, TMetadata]) pushChildrenToParent(children model_core.PatchedMessage[[]TNode, TMetadata]) error {
	if !b.multipleLevels {
		if !b.firstChildren.IsSet() {
			b.firstChildren = children
			return nil
		}

		rootNode, err := b.leavesNodeMerger(b.firstChildren.Move())
		if err != nil {
			return err
		}
		if err := b.parentsBuilder.PushChild(rootNode); err != nil {
			return err
		}
		b.multipleLevels = true
	}

	node, err := b.leavesNodeMerger(children)
	if err != nil {
		return err
	}
	return b.parentsBuilder.PushChild(node)
}

func (b *splitBuilder[TNode, TMetadata]) maybePushChildrenToParent(finalize bool) error {
	for {
		children := b.leavesChunker.PopMultiple(finalize)
		if !children.IsSet() {
			return nil
		}
		if err := b.pushChildrenToParent(children); err != nil {
			return err
		}
	}
}

func (b *splitBuilder[TNode, TMetadata]) PushChild(node model_core.PatchedMessage[TNode, TMetadata]) error {
	if err := b.leavesChunker.PushSingle(node); err != nil {
		return err
	}
	return b.maybePushChildrenToParent(false)
}

func (b *splitBuilder[TNode, TMetadata]) FinalizeList() (model_core.PatchedMessage[[]TNode, TMetadata], error) {
	if err := b.maybePushChildrenToParent(true); err != nil {
		return model_core.PatchedMessage[[]TNode, TMetadata]{}, err
	}
	if b.multipleLevels {
		return b.parentsBuilder.FinalizeList()
	}
	if b.firstChildren.IsSet() {
		return b.firstChildren.Move(), nil
	}
	return model_core.NewSimplePatchedMessage[TMetadata, []TNode](nil), nil
}

func (b *splitBuilder[TNode, TMetadata]) FinalizeSingle() (model_core.PatchedMessage[TNode, TMetadata], error) {
	if err := b.maybePushChildrenToParent(true); err != nil {
		return model_core.PatchedMessage[TNode, TMetadata]{}, err
	}
	if b.multipleLevels {
		return b.parentsBuilder.FinalizeSingle()
	}
	switch len(b.firstChildren.Message) {
	case 0:
		return model_core.PatchedMessage[TNode, TMetadata]{}, nil
	case 1:
		return model_core.FlattenPatchedSingletonList(b.firstChildren.Move()), nil
	default:
		return b.leavesNodeMerger(b.firstChildren.Move())
	}
}

func (b *splitBuilder[TNode, TMetadata]) Discard() {
	b.parentsBuilder.Discard()
	b.firstChildren.Discard()
}
