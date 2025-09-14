package core

import (
	"context"
)

// NoopReferenceMetadata is a trivial implementation of
// ReferenceMetadata that does not capture anything. This can be used if
// the goal is to simply compute the root node of the Merkle tree,
// discarding any children that were created in the process.
type NoopReferenceMetadata struct{}

// Discard all resources owned by this instance of
// NoopReferenceMetadata. This method is merely provided to satisfy the
// ReferenceMetadata interface.
func (NoopReferenceMetadata) Discard() {}

type discardingCreatedObjectCapturer struct{}

// DiscardingCreatedObjectCapturer is an implementation of
// CreatedObjectCapturer that discards any created objects. This can be
// used if the goal is to simply compute the root node of the Merkle
// tree, discarding any children that were created in the process.
var DiscardingCreatedObjectCapturer CreatedObjectCapturer[NoopReferenceMetadata] = discardingCreatedObjectCapturer{}

func (discardingCreatedObjectCapturer) CaptureCreatedObject(ctx context.Context, createdObject CreatedObject[NoopReferenceMetadata]) (NoopReferenceMetadata, error) {
	return NoopReferenceMetadata{}, nil
}

type discardingObjectCapturer[TReference any] struct {
	discardingCreatedObjectCapturer
}

// NewDiscardingObjectCapturer creates is an implementation of
// ObjectCapturer that discards any created objects, and stores no
// metadata for any existing objects. This can be used if the goal is to
// simply compute the root node of the Merkle tree, discarding any
// children that were created in the process.
func NewDiscardingObjectCapturer[TReference any]() ObjectCapturer[TReference, NoopReferenceMetadata] {
	return discardingObjectCapturer[TReference]{}
}

func (discardingObjectCapturer[TReference]) CaptureExistingObject(TReference) NoopReferenceMetadata {
	return NoopReferenceMetadata{}
}
