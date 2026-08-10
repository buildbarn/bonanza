package object

import (
	"math"

	"bonanza.build/pkg/proto/storage/object"
)

// Limit on the maximum number of objects to process in parallel.
type Limit struct {
	count     uint32
	sizeBytes uint64
}

// Unlimited concurrency when attempting to traverse a graph of objects.
var Unlimited = Limit{
	count:     math.MaxUint32,
	sizeBytes: math.MaxUint64,
}

// NewLimit converts limits on the maximum number of objects to process
// in parallel from a Protobuf message format to a native type.
func NewLimit(m *object.Limit) Limit {
	return Limit{
		count:     m.Count,
		sizeBytes: m.SizeBytes,
	}
}

// Min computes the lower bound on the maximum number of objects to
// process in parallel. This method can be used during client/server
// negotiation to pick a resource limit that can be respected by both
// sides.
func (l Limit) Min(other Limit) Limit {
	return Limit{
		count:     min(l.count, other.count),
		sizeBytes: min(l.sizeBytes, other.sizeBytes),
	}
}

// ToProto converts a limit on the maximum number of objects to process
// in parallel to a Protobuf message, so that it can be embedded into a
// gRPC request message.
func (l Limit) ToProto() *object.Limit {
	return &object.Limit{
		Count:     l.count,
		SizeBytes: l.sizeBytes,
	}
}

// CanAcquireSingleObject returns whether the current limits allow
// processing the referenced object.
func (l *Limit) CanAcquireSingleObject(reference LocalReference) bool {
	return l.count >= 1 && l.sizeBytes >= uint64(reference.GetSizeBytes())
}

// CanAcquireParentAndChildren returns whether the current limits allow
// processing the subgraph rooted at the referenced object.
func (l *Limit) CanAcquireParentAndChildren(reference LocalReference) bool {
	count := uint32(reference.GetHeight())
	sizeBytes := uint64(reference.GetMaximumTotalParentsSizeBytes(true))
	return l.count >= count && l.sizeBytes >= sizeBytes
}

// AcquireSingleObject reduces the limits, so that resources are
// allocated for processing the referenced object.
func (l *Limit) AcquireSingleObject(reference LocalReference) bool {
	sizeBytes := uint64(reference.GetSizeBytes())
	if l.count < 1 || l.sizeBytes < sizeBytes {
		return false
	}

	l.count--
	l.sizeBytes -= sizeBytes
	return true
}

// AcquireParentAndChildren reduces the limits, so that resources are
// allocated for processing the subgraph rooted at the referenced
// object.
func (l *Limit) AcquireParentAndChildren(reference LocalReference) bool {
	count := uint32(reference.GetHeight())
	sizeBytes := uint64(reference.GetMaximumTotalParentsSizeBytes(true))
	if l.count < count || l.sizeBytes < sizeBytes {
		return false
	}

	l.count -= count
	l.sizeBytes -= sizeBytes
	return true
}

// ReleaseSingleObject increases the limits, so that resources are released
// that were acquired for processing the referenced object.
func (l *Limit) ReleaseSingleObject(reference LocalReference) {
	l.count++
	l.sizeBytes += uint64(reference.GetSizeBytes())
}

// ReleaseParent increases the limits, so that resources are released
// that were acquired for processing the referenced parent object (but
// not its children).
func (l *Limit) ReleaseParent(reference LocalReference) {
	if reference.GetHeight() > 0 {
		l.ReleaseSingleObject(reference)
	}
}

// ReleaseChildren increases the limits, so that resources are released
// that were acquired for processing the children of the referenced
// parent object.
func (l *Limit) ReleaseChildren(reference LocalReference) {
	if h := reference.GetHeight(); h > 0 {
		l.count += uint32(h - 1)
		l.sizeBytes += uint64(reference.GetMaximumTotalParentsSizeBytes(false))
	}
}
