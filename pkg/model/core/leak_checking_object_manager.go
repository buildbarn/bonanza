package core

import (
	"context"
	"log"
	"sync"

	"bonanza.build/pkg/storage/object"
)

type leakCheckingListEntry struct {
	previous *leakCheckingListEntry
	next     *leakCheckingListEntry
	stack    []byte
}

// leakCheckingState holds the bookkeeping that is shared between
// LeakCheckingObjectManager and all LeakCheckingReferenceMetadata that
// are created by it.
type leakCheckingState struct {
	lock sync.Mutex
	list leakCheckingListEntry
}

// LeakCheckingReferenceMetadata is a wrapper around ReferenceMetadata
// that keeps track of whether it's still valid. When discarded, it
// decrements the valid metadata count.
type LeakCheckingReferenceMetadata[TMetadata ReferenceMetadata] struct {
	base  TMetadata
	state *leakCheckingState
	entry leakCheckingListEntry
}

var _ ReferenceMetadata = (*LeakCheckingReferenceMetadata[ReferenceMetadata])(nil)

func (rm *LeakCheckingReferenceMetadata[TMetadata]) removeFromList() {
	s := rm.state
	s.lock.Lock()
	e := &rm.entry
	e.previous.next, e.next.previous = e.next, e.previous
	s.lock.Unlock()
	rm.state = nil
	e.previous = nil
	e.next = nil
}

func (rm *LeakCheckingReferenceMetadata[TMetadata]) Discard() {
	rm.removeFromList()
	rm.base.Discard()
}

// Unwrap a LeakCheckingReferenceMetadata, returning the
// ReferenceMetadata that it contains. This destroys the
// LeakCheckingReferenceMetadata in the process.
func (rm *LeakCheckingReferenceMetadata[TMetadata]) Unwrap() TMetadata {
	rm.removeFromList()
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
	om := &LeakCheckingObjectManager[TReference, TMetadata]{
		base: base,
	}
	l := &om.state.list
	l.previous = l
	l.next = l
	return om
}

var _ ObjectManager[object.LocalReference, *LeakCheckingReferenceMetadata[ReferenceMetadata]] = (*LeakCheckingObjectManager[object.LocalReference, ReferenceMetadata])(nil)

func (om *LeakCheckingObjectManager[TReference, TMetadata]) CaptureCreatedObject(ctx context.Context, createdObject CreatedObject[*LeakCheckingReferenceMetadata[TMetadata]]) (*LeakCheckingReferenceMetadata[TMetadata], error) {
	unwrappedMetadata := make([]TMetadata, 0, len(createdObject.Metadata))
	for _, metadata := range createdObject.Metadata {
		unwrappedMetadata = append(unwrappedMetadata, metadata.Unwrap())
	}
	base, err := om.base.CaptureCreatedObject(
		ctx,
		CreatedObject[TMetadata]{
			Contents: createdObject.Contents,
			Metadata: unwrappedMetadata,
		},
	)
	if err != nil {
		return nil, err
	}
	return om.wrap(base), nil
}

func (om *LeakCheckingObjectManager[TReference, TMetadata]) wrap(base TMetadata) *LeakCheckingReferenceMetadata[TMetadata] {
	rm := &LeakCheckingReferenceMetadata[TMetadata]{
		base:  base,
		state: &om.state,
		entry: leakCheckingListEntry{
			// TODO: This is too heavy to always leave enabled.
			// stack: debug.Stack(),
		},
	}
	s := &om.state
	e := &rm.entry
	s.lock.Lock()
	e.previous = s.list.previous
	e.next = &s.list
	e.previous.next = e
	e.next.previous = e
	s.lock.Unlock()
	return rm
}

func (om *LeakCheckingObjectManager[TReference, TMetadata]) CaptureExistingObject(reference TReference) *LeakCheckingReferenceMetadata[TMetadata] {
	return om.wrap(om.base.CaptureExistingObject(reference))
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
func (om *LeakCheckingObjectManager[TReference, TMetadata]) GetCurrentValidMetadataCount() int {
	s := &om.state
	count := 0
	s.lock.Lock()
	seen := map[string]struct{}{}
	for e := s.list.next; e != &s.list; e = e.next {
		if count == 0 {
			log.Print("---- START OF LEAKS ---")
		}
		count++
		stack := string(e.stack)
		if _, ok := seen[stack]; !ok {
			log.Print(stack)
			seen[stack] = struct{}{}
		}
	}
	s.lock.Unlock()
	return count
}
