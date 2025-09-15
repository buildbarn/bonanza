package core

import (
	"context"
	"sync/atomic"

	"bonanza.build/pkg/storage/object"
)

// leakCheckingState holds the bookkeeping that is shared between
// LeakCheckingObjectManager and all LeakCheckingReferenceMetadata that
// are created by it.
type leakCheckingState struct {
	validMetadataCount atomic.Uint64
}

// LeakCheckingReferenceMetadata is a wrapper around ReferenceMetadata
// that keeps track of whether it's still valid. When discarded, it
// decrements the valid metadata count.
type LeakCheckingReferenceMetadata[TMetadata ReferenceMetadata] struct {
	base  TMetadata
	state *leakCheckingState
}

var _ ReferenceMetadata = (*LeakCheckingReferenceMetadata[ReferenceMetadata])(nil)

func (rm *LeakCheckingReferenceMetadata[TMetadata]) Discard() {
	if rm.state.validMetadataCount.Add(^uint64(0)) == ^uint64(0) {
		panic("invalid valid metadata count")
	}
	rm.state = nil
	rm.base.Discard()
}

// Unwrap a LeakCheckingReferenceMetadata, returning the
// ReferenceMetadata that it contains. This destroys the
// LeakCheckingReferenceMetadata in the process.
func (rm *LeakCheckingReferenceMetadata[TMetadata]) Unwrap() TMetadata {
	if rm.state.validMetadataCount.Add(^uint64(0)) == ^uint64(0) {
		panic("invalid valid metadata count")
	}
	rm.state = nil
	return rm.base
}

// LeakCheckingObjectManager is a wrapper around ObjectManager that
// keeps track of the number of ReferenceMetadata values that are still
// valid. This can be used to detect and report leaks of
// ReferenceMetadata.
type LeakCheckingObjectManager[TReference any, TMetadata ReferenceMetadata] struct {
	base  ObjectManager[TReference, TMetadata]
	state leakCheckingState
}

// NewLeakCheckingObjectManager creates a LeakCheckingObjectManager that
// is in the initial state, meaning that it has zero valid
// ReferenceMetadata objects.
func NewLeakCheckingObjectManager[TReference any, TMetadata ReferenceMetadata](base ObjectManager[TReference, TMetadata]) *LeakCheckingObjectManager[TReference, TMetadata] {
	return &LeakCheckingObjectManager[TReference, TMetadata]{
		base: base,
	}
}

var _ ObjectManager[object.LocalReference, *LeakCheckingReferenceMetadata[ReferenceMetadata]] = (*LeakCheckingObjectManager[object.LocalReference, ReferenceMetadata])(nil)

func (om *LeakCheckingObjectManager[TReference, TMetadata]) CaptureCreatedObject(ctx context.Context, createdObject CreatedObject[*LeakCheckingReferenceMetadata[TMetadata]]) (*LeakCheckingReferenceMetadata[TMetadata], error) {
	unwrappedMetadata := make([]TMetadata, 0, len(createdObject.Metadata))
	for _, metadata := range createdObject.Metadata {
		if metadata.state != &om.state {
			panic("attempted to call ReferenceObject() against a metadata entry that is no longer valid")
		}
		metadata.state = nil
		unwrappedMetadata = append(unwrappedMetadata, metadata.base)
	}
	base, err := om.base.CaptureCreatedObject(
		ctx,
		CreatedObject[TMetadata]{
			Contents: createdObject.Contents,
			Metadata: unwrappedMetadata,
		},
	)
	if err != nil {
		om.state.validMetadataCount.Add(-uint64(len(unwrappedMetadata)))
		return nil, err
	}
	om.state.validMetadataCount.Add(1 - uint64(len(unwrappedMetadata)))
	return &LeakCheckingReferenceMetadata[TMetadata]{
		base:  base,
		state: &om.state,
	}, nil
}

func (om *LeakCheckingObjectManager[TReference, TMetadata]) CaptureExistingObject(reference TReference) *LeakCheckingReferenceMetadata[TMetadata] {
	if om.state.validMetadataCount.Add(1) == 0 {
		panic("invalid valid metadata count")
	}
	return &LeakCheckingReferenceMetadata[TMetadata]{
		base:  om.base.CaptureExistingObject(reference),
		state: &om.state,
	}
}

func (om *LeakCheckingObjectManager[TReference, TMetadata]) ReferenceObject(entry MetadataEntry[*LeakCheckingReferenceMetadata[TMetadata]]) TReference {
	return om.base.ReferenceObject(
		MetadataEntry[TMetadata]{
			LocalReference: entry.LocalReference,
			Metadata:       entry.Metadata.Unwrap(),
		},
	)
}

// GetCurrentValidMetadataCount returns the current valid number of
// ReferenceMetadata objects. When non-zero, ReferenceMetadata objects
// may have been leaked.
func (om *LeakCheckingObjectManager[TReference, TMetadata]) GetCurrentValidMetadataCount() uint64 {
	return om.state.validMetadataCount.Load()
}
