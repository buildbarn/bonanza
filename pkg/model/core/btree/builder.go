package btree

import (
	model_core "bonanza.build/pkg/model/core"

	"google.golang.org/protobuf/proto"
)

// Builder of B-trees.
type Builder[TNode proto.Message, TMetadata model_core.ReferenceMetadata] interface {
	PushChild(node model_core.PatchedMessage[TNode, TMetadata]) error
	FinalizeList() (model_core.PatchedMessage[[]TNode, TMetadata], error)
	FinalizeSingle() (model_core.PatchedMessage[TNode, TMetadata], error)
	Discard()
}
