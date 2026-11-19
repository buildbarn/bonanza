package core

import (
	"context"
	"fmt"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"

	"bonanza.build/pkg/storage/object"
)

type leakCheckingListEntry struct {
	previous *leakCheckingListEntry
	next     *leakCheckingListEntry
	callers  []uintptr
}

// maximumLeakCheckingStackDepth is the maximum number of stack frames
// that are captured when creating LeakCheckingReferenceMetadata.
// Program counters are captured without symbolizing them, as that is
// relatively cheap. Symbolization only happens when reporting leaks.
const maximumLeakCheckingStackDepth = 32

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

// Discard the reference metadata, releasing any resources associated
// with it.
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

// CaptureCreatedObject captures the contents of an object that was just
// created, returning reference metadata that tracks any leaks.
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
	// Skip runtime.Callers() and wrap(), so that the stack starts
	// at CaptureCreatedObject() or CaptureExistingObject().
	var pcs [maximumLeakCheckingStackDepth]uintptr
	n := runtime.Callers(2, pcs[:])
	rm := &LeakCheckingReferenceMetadata[TMetadata]{
		base:  base,
		state: &om.state,
		entry: leakCheckingListEntry{
			callers: slices.Clone(pcs[:n]),
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

// CaptureExistingObject creates reference metadata for an object that
// already exists in storage. The reference metadata that is returned
// performs tracking of leaks.
func (om *LeakCheckingObjectManager[TReference, TMetadata]) CaptureExistingObject(reference TReference) *LeakCheckingReferenceMetadata[TMetadata] {
	return om.wrap(om.base.CaptureExistingObject(reference))
}

// ReferenceObject converts the reference metadata back to a reference.
// As references don't have explicit lifetimes, this means that no leak
// tracking is performed from this point on.
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
	for e := s.list.next; e != &s.list; e = e.next {
		count++
	}
	s.lock.Unlock()
	return count
}

// FormatLeaks returns a human readable description of all
// ReferenceMetadata objects that are still valid, grouped by the stack
// trace at which they were created. Groups are sorted by the number of
// objects leaked, in descending order.
func (om *LeakCheckingObjectManager[TReference, TMetadata]) FormatLeaks() string {
	type leakGroup struct {
		callers []uintptr
		count   int
	}
	groups := map[string]*leakGroup{}

	s := &om.state
	s.lock.Lock()
	for e := s.list.next; e != &s.list; e = e.next {
		key := fmt.Sprint(e.callers)
		if g, ok := groups[key]; ok {
			g.count++
		} else {
			groups[key] = &leakGroup{
				callers: e.callers,
				count:   1,
			}
		}
	}
	s.lock.Unlock()

	sortedGroups := make([]*leakGroup, 0, len(groups))
	for _, g := range groups {
		sortedGroups = append(sortedGroups, g)
	}
	sort.SliceStable(sortedGroups, func(i, j int) bool {
		return sortedGroups[i].count > sortedGroups[j].count
	})

	var sb strings.Builder
	for _, g := range sortedGroups {
		fmt.Fprintf(&sb, "%d metadata values created at:\n", g.count)
		frames := runtime.CallersFrames(g.callers)
		for {
			frame, more := frames.Next()
			fmt.Fprintf(&sb, "\t%s\n\t\t%s:%d\n", frame.Function, frame.File, frame.Line)
			if !more {
				break
			}
		}
	}
	return sb.String()
}
