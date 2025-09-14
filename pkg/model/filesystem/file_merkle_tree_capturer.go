package filesystem

import (
	"context"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/storage/object"
)

// FileMerkleTreeCapturer is provided by callers of CreateFileMerkleTree
// to provide logic for how the resulting Merkle tree of the file should
// be captured.
//
// A no-op implementation can be used by the caller to simply compute a
// reference of the file. An implementation that actually captures the
// provided contents can be used to prepare a Merkle tree for uploading.
//
// The methods below return metadata. The metadata for the root object
// will be returned by CreateFileMerkleTree.
type FileMerkleTreeCapturer[TMetadata any] interface {
	CaptureChunk(ctx context.Context, contents *object.Contents) (TMetadata, error)
	CaptureFileContentsList(ctx context.Context, createdObject model_core.CreatedObject[TMetadata]) (TMetadata, error)
}

type chunkDiscardingFileMerkleTreeCapturer struct{}

// ChunkDiscardingFileMerkleTreeCapturer is an implementation of
// FileMerkleTreeCapturer that only preserves the FileContents messages
// of the Merkle tree. This can be of use when incrementally replicating
// the contents of a file. In those cases it's wasteful to store the
// full contents of a file in memory.
var ChunkDiscardingFileMerkleTreeCapturer FileMerkleTreeCapturer[model_core.CreatedObjectTree] = chunkDiscardingFileMerkleTreeCapturer{}

func (chunkDiscardingFileMerkleTreeCapturer) CaptureChunk(ctx context.Context, contents *object.Contents) (model_core.CreatedObjectTree, error) {
	return model_core.CreatedObjectTree{}, nil
}

func (chunkDiscardingFileMerkleTreeCapturer) CaptureFileContentsList(ctx context.Context, createdObject model_core.CreatedObject[model_core.CreatedObjectTree]) (model_core.CreatedObjectTree, error) {
	o := model_core.CreatedObjectTree{
		Contents: createdObject.Contents,
	}
	if createdObject.GetHeight() > 1 {
		o.Metadata = createdObject.Metadata
	}
	return o, nil
}

type simpleFileMerkleTreeCapturer[TMetadata any] struct {
	capturer model_core.CreatedObjectCapturer[TMetadata]
}

// NewSimpleFileMerkleTreeCapturer creates a FileMerkleTreeCapturer that
// assumes that chunks and file contents lists need to be captured the
// same way.
func NewSimpleFileMerkleTreeCapturer[TMetadata any](capturer model_core.CreatedObjectCapturer[TMetadata]) FileMerkleTreeCapturer[TMetadata] {
	return simpleFileMerkleTreeCapturer[TMetadata]{
		capturer: capturer,
	}
}

func (c simpleFileMerkleTreeCapturer[TMetadata]) CaptureChunk(ctx context.Context, contents *object.Contents) (TMetadata, error) {
	return c.capturer.CaptureCreatedObject(ctx, model_core.CreatedObject[TMetadata]{Contents: contents})
}

func (c simpleFileMerkleTreeCapturer[TMetadata]) CaptureFileContentsList(ctx context.Context, createdObject model_core.CreatedObject[TMetadata]) (TMetadata, error) {
	return c.capturer.CaptureCreatedObject(ctx, createdObject)
}

type FileMerkleTreeCapturerForTesting FileMerkleTreeCapturer[model_core.ReferenceMetadata]
