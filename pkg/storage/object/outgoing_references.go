package object

// OutgoingReferences is a list of outgoing references of an object. It
// may either be implemented by a simple slice, or it's possible to
// provide an implementation that reads references directly out of the
// object's contents.
type OutgoingReferences[TReference any] interface {
	GetDegree() int
	GetOutgoingReference(index int) TReference

	// If the OutgoingReferences object is part of a larger object
	// (e.g., part of object.Contents), copy it, so that the
	// original instance may be garbage collected.
	DetachOutgoingReferences() OutgoingReferences[TReference]
}

// OutgoingReferencesList is a list of outgoing references of an object
// that is backed by a simple slice.
type OutgoingReferencesList[T any] []T

var _ OutgoingReferences[LocalReference] = OutgoingReferencesList[LocalReference]{}

// GetDegree returns the number of outgoing references in the list.
func (l OutgoingReferencesList[T]) GetDegree() int {
	return len(l)
}

// GetOutgoingReference returns the reference at a given index in the
// outgoing references list. It is the caller's responsibility to ensure
// the index is within bounds.
func (l OutgoingReferencesList[T]) GetOutgoingReference(index int) T {
	return l[index]
}

// DetachOutgoingReferences does nothing, as it is assumed that an
// OutgoingReferencesList is already stored in a separate memory
// allocation, and is not part of a larger object.
func (l OutgoingReferencesList[T]) DetachOutgoingReferences() OutgoingReferences[T] {
	return l
}
