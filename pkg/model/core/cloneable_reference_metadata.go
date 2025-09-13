package core

import (
	"bonanza.build/pkg/storage/object"
)

// CloneableReferenceMetadata is a type of ReferenceMetadata that is
// safe to place in multiple ReferenceMessagePatchers. For example,
// ReferenceMetadata that stores the contents of an object in a file may
// not be cloneable. A simple in-memory implementation like
// CreatedObject[T] is cloneable.
type CloneableReferenceMetadata interface {
	ReferenceMetadata

	IsCloneable()
}

// CloneableReference is a basic implementation of object.BasicReference
// that holds on to any metadata that was extracted out of the
// ReferenceMessagePatcher by PatchedMessageToCloneable(). The metadata
// is later reinserted into one or more ReferenceMessagePatchers by
// calling Patch() using ClonedObjectManager.
type CloneableReference[TMetadata any] struct {
	object.LocalReference
	metadata TMetadata
}

type CloningObjectManager[TMetadata any] struct{}

var (
	_ ExistingObjectCapturer[CloneableReference[CloneableReferenceMetadata], CloneableReferenceMetadata] = CloningObjectManager[CloneableReferenceMetadata]{}
	_ ObjectReferencer[CloneableReference[CloneableReferenceMetadata], CloneableReferenceMetadata]       = CloningObjectManager[CloneableReferenceMetadata]{}
)

func (CloningObjectManager[TMetadata]) CaptureExistingObject(reference CloneableReference[TMetadata]) TMetadata {
	return reference.metadata
}

func (CloningObjectManager[TMetadata]) ReferenceObject(capturedObject MetadataEntry[TMetadata]) CloneableReference[TMetadata] {
	return CloneableReference[TMetadata]{
		LocalReference: capturedObject.LocalReference,
		metadata:       capturedObject.Metadata,
	}
}
