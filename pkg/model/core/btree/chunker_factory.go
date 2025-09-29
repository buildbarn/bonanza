package btree

import (
	model_core "bonanza.build/pkg/model/core"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"

	"google.golang.org/protobuf/proto"
)

// ChunkerFactory is a factory type for creating chunkers of individual
// levels of a B-tree.
type ChunkerFactory[TNode proto.Message, TMetadata model_core.ReferenceMetadata] interface {
	NewChunker() Chunker[TNode, TMetadata]
}

// PopThreshold can be provided to Chunker.PopMultiple() to control how
// up to which node that was pushed chunks may be returned.
type PopThreshold int

const (
	// Only pop chunks of nodes if it is certain that no future
	// pushes will affect the chunking process up to that point.
	// This can be used to flush definitive parts of a B-tree to
	// storage, so that memory usage remains bounded.
	PopDefinitive PopThreshold = iota
	// Only pop chunks of nodes if it is certain that the current
	// level of the B-tree is not going to be the root.
	PopChild
	// Pop chunks containing all nodes that were pushed, even if it
	// leads to chunks that are smaller than the desired minimum
	// size. This can be used to finalize a B-tree.
	PopAll
)

// Chunker is responsible for determining how nodes at a given level in
// the B-tree are chunked and spread out across sibling objects at the
// same level.
type Chunker[TNode proto.Message, TMetadata model_core.ReferenceMetadata] interface {
	PushSingle(node model_core.PatchedMessage[TNode, TMetadata]) error
	PopMultiple(threshold PopThreshold) model_core.PatchedMessageList[TNode, TMetadata]
	Discard()
}

type (
	// ChunkerFactoryForTesting is an instantiation of
	// ChunkerFactory for generating mocks to be used by tests.
	ChunkerFactoryForTesting ChunkerFactory[*model_filesystem_pb.FileContents, model_core.ReferenceMetadata]
	// ChunkerForTesting is an instantiation of Chunker for
	// generating mocks to be used by tests.
	ChunkerForTesting Chunker[*model_filesystem_pb.FileContents, model_core.ReferenceMetadata]
)
