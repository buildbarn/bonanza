package filesystem

import (
	"context"

	model_core "bonanza.build/pkg/model/core"
)

type DirectoryMerkleTreeCapturer[TDirectory, TFile any] interface {
	CaptureFileNode(TFile) TDirectory
	CaptureDirectory(ctx context.Context, createdObject model_core.CreatedObject[TDirectory]) (TDirectory, error)
	CaptureLeaves(ctx context.Context, createdObject model_core.CreatedObject[TDirectory]) (TDirectory, error)
}

type fileDiscardingDirectoryMerkleTreeCapturer struct{}

// FileDiscardingDirectoryMerkleTreeCapturer is an instance of
// DirectoryMerkleTreeCapturer that keeps any Directory and Leaves
// objects, but discards FileContents list and file chunk objects.
//
// Discarding the contents of files is typically the right approach for
// uploading directory structures with changes to only a small number of
// files. The Merkle trees of files can be recomputed if it turns out
// they still need to be uploaded.
var FileDiscardingDirectoryMerkleTreeCapturer DirectoryMerkleTreeCapturer[model_core.CreatedObjectTree, model_core.NoopReferenceMetadata] = fileDiscardingDirectoryMerkleTreeCapturer{}

func (fileDiscardingDirectoryMerkleTreeCapturer) CaptureFileNode(model_core.NoopReferenceMetadata) model_core.CreatedObjectTree {
	return model_core.CreatedObjectTree{}
}

func (fileDiscardingDirectoryMerkleTreeCapturer) CaptureDirectory(ctx context.Context, createdObject model_core.CreatedObject[model_core.CreatedObjectTree]) (model_core.CreatedObjectTree, error) {
	return model_core.CreatedObjectTree(createdObject), nil
}

func (fileDiscardingDirectoryMerkleTreeCapturer) CaptureLeaves(ctx context.Context, createdObject model_core.CreatedObject[model_core.CreatedObjectTree]) (model_core.CreatedObjectTree, error) {
	return model_core.CreatedObjectTree(createdObject), nil
}

type simpleDirectoryMerkleTreeCapturer[TMetadata any] struct {
	capturer model_core.CreatedObjectCapturer[TMetadata]
}

// NewSimpleDirectoryMerkleTreeCapturer creates a
// DirectoryMerkleTreeCapturer that assumes that directories and leaves
// need to be captured the same way, and that file metadata uses the
// same type as directory metadata.
func NewSimpleDirectoryMerkleTreeCapturer[TMetadata any](capturer model_core.CreatedObjectCapturer[TMetadata]) DirectoryMerkleTreeCapturer[TMetadata, TMetadata] {
	return simpleDirectoryMerkleTreeCapturer[TMetadata]{
		capturer: capturer,
	}
}

func (simpleDirectoryMerkleTreeCapturer[TMetadata]) CaptureFileNode(metadata TMetadata) TMetadata {
	return metadata
}

func (c simpleDirectoryMerkleTreeCapturer[TMetadata]) CaptureDirectory(ctx context.Context, createdObject model_core.CreatedObject[TMetadata]) (TMetadata, error) {
	return c.capturer.CaptureCreatedObject(ctx, createdObject)
}

func (c simpleDirectoryMerkleTreeCapturer[TMetadata]) CaptureLeaves(ctx context.Context, createdObject model_core.CreatedObject[TMetadata]) (TMetadata, error) {
	return c.capturer.CaptureCreatedObject(ctx, createdObject)
}
