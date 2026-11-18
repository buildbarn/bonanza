package btree

import (
	model_core "bonanza.build/pkg/model/core"

	"google.golang.org/protobuf/proto"
)

// Builder of B-trees.
type Builder[TNode proto.Message, TMetadata model_core.ReferenceMetadata] interface {
	PushChild(node model_core.PatchedMessage[TNode, TMetadata]) error

	// FinalizeList returns the nodes of the root of the B-tree.
	// The root is either smaller than the minimum object size, or
	// only contains a single node.
	//
	// This makes the root suitable for being embedded into the
	// nodes of another B-tree, without causing the objects of that
	// B-tree to exceed the maximum object size. As the root is
	// computed deterministically, embedded and non-embedded
	// instances of the same B-tree have identical roots.
	FinalizeList() (model_core.PatchedMessage[[]TNode, TMetadata], error)

	// FinalizeSingle returns the root of the B-tree as a single
	// node.
	FinalizeSingle() (model_core.PatchedMessage[TNode, TMetadata], error)

	Discard()
}
