package object

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"

	"bonanza.build/pkg/encoding/float16"

	"github.com/buildbarn/bb-storage/pkg/util"
)

const (
	minimumObjectSizeBytes     = 1
	maximumObjectSizeBytes     = 1 << 21
	SHA256V1ReferenceSizeBytes = 40
)

type maximumTotalParentsSizeBytesBoundsEntry struct {
	minimum uint16
	maximum uint16
}

var maximumTotalParentsSizeBytesBounds [1 << 8]maximumTotalParentsSizeBytesBoundsEntry

func init() {
	var bounds maximumTotalParentsSizeBytesBoundsEntry
	for i := 2; i <= math.MaxUint8; i++ {
		var ok bool
		bounds.minimum, ok = float16.FromUint64RoundUp(float16.ToUint64(bounds.minimum) + SHA256V1ReferenceSizeBytes)
		if !ok {
			panic("float16 is too small to express all possible maximum total parents sizes")
		}
		bounds.maximum, ok = float16.FromUint64RoundUp(float16.ToUint64(bounds.maximum) + maximumObjectSizeBytes)
		if !ok {
			panic("float16 is too small to express all possible maximum total parents sizes")
		}
		maximumTotalParentsSizeBytesBounds[i] = bounds
	}
}

// LocalReference uniquely identifies an object stored within a single
// storage namespace.
type LocalReference struct {
	rawReference [SHA256V1ReferenceSizeBytes]byte
}

var _ BasicReference = LocalReference{}

// MustNewSHA256V1LocalReference creates a local reference that uses
// reference format SHA256_V1. This function can be used as part of
// tests.
func MustNewSHA256V1LocalReference(hash string, sizeBytes uint32, height uint8, degree uint16, maximumTotalParentsSizeBytes uint64) LocalReference {
	var rawReference [40]byte
	if n, err := hex.Decode(rawReference[:], []byte(hash)); err != nil {
		panic(err)
	} else if n != 32 {
		panic("Wrong hash length")
	}
	binary.LittleEndian.PutUint32(rawReference[32:], sizeBytes)
	rawReference[35] = byte(height)
	binary.LittleEndian.PutUint16(rawReference[36:], degree)
	f16, ok := float16.FromUint64RoundUp(maximumTotalParentsSizeBytes)
	if !ok {
		panic("maximumTotalParentsSizeBytes is too large")
	}
	binary.LittleEndian.PutUint16(rawReference[38:], f16)

	return util.Must(SHA256V1ReferenceFormat.NewLocalReference(rawReference[:]))
}

// GetReferenceFormat returns the reference format that was used to
// generate the reference.
func (LocalReference) GetReferenceFormat() ReferenceFormat {
	return ReferenceFormat{}
}

// GetRawReference returns the reference in binary form, so that it may
// be embedded into gRPC request bodies.
func (r LocalReference) GetRawReference() []byte {
	return r.rawReference[:]
}

func (r LocalReference) String() string {
	return fmt.Sprintf(
		"SHA256=%s:S=%d:H=%d:D=%d:M=%d",
		hex.EncodeToString(r.GetHash()),
		r.GetSizeBytes(),
		r.GetHeight(),
		r.GetDegree(),
		r.GetMaximumTotalParentsSizeBytes(false),
	)
}

// GetHash returns the hash of the contents of the object associated
// with the reference.
func (r LocalReference) GetHash() []byte {
	return r.rawReference[:32]
}

// GetSizeBytes returns the size of the object associated with the
// reference. The size comprises both the outgoing references and the
// data payload.
func (r LocalReference) GetSizeBytes() int {
	return int(binary.LittleEndian.Uint32(r.rawReference[32:]) & 0xffffff)
}

// GetHeight returns the maximum length of all paths to leaves
// underneath the current object. Objects without any children have
// height zero.
func (r LocalReference) GetHeight() int {
	return int(r.rawReference[35])
}

// GetDegree returns the number of children the object associated with
// the reference has.
func (r LocalReference) GetDegree() int {
	return int(binary.LittleEndian.Uint16(r.rawReference[36:]))
}

func (r LocalReference) getRawMaximumTotalParentsSizeBytes() uint16 {
	return binary.LittleEndian.Uint16(r.rawReference[38:])
}

// GetMaximumTotalParentsSizeBytes returns the maximum total size of
// objects along all paths to leaves underneath the current object,
// excluding the leaves themselves. Whether the size of the current
// object is taken into consideration is controlled through the
// includeSelf parameter.
//
// This method can be used to determine the amount of memory that's
// needed to traverse the full graph. It is used by the DAG uploading
// and lease renewing code to traverse graphs in parallel, in such a way
// that memory usage is bounded.
func (r LocalReference) GetMaximumTotalParentsSizeBytes(includeSelf bool) int {
	sizeBytes := int(float16.ToUint64(r.getRawMaximumTotalParentsSizeBytes()))
	if includeSelf && r.GetHeight() > 0 {
		sizeBytes += r.GetSizeBytes()
	}
	return sizeBytes
}

// Flatten a reference, so that its height and degree are both zero.
//
// This is used by the read caching backend, where we want to cache
// individual objects as opposed to graphs. When writing objects to the
// local cache, we set the height and degree to zero, so that there is
// no need to track any leases.
func (r LocalReference) Flatten() (flatReference FlatReference) {
	copy(flatReference.rawReference[:], r.rawReference[:])
	return flatReference
}

// GetLocalReference trims all properties of the reference, so that only
// a LocalReference remains.
//
// This method is provided to ensure that all types that are derived
// from LocalReference are easy to convert back to a LocalReference.
func (r LocalReference) GetLocalReference() LocalReference {
	return r
}

// WithLocalReference returns a new LocalReference that has its value
// replaced with another.
//
// This method is merely provided to satisfy some of the constraints of
// some generic functions and types that can operate both on local and
// global references.
func (LocalReference) WithLocalReference(localReference LocalReference) LocalReference {
	return localReference
}

// CompareByHeight returns -1 if the reference comes before another
// reference along a total order based on the references' height and
// maximum total parents size. Conversely, it returns 1 if the reference
// comes after another reference. 0 is returned if both references are
// equal.
//
// This order can be used to perform bounded parallel traversal of DAGs
// in such a way that forward progress is guaranteed.
func (r LocalReference) CompareByHeight(other LocalReference) int {
	if c := cmp.Compare(r.GetHeight(), other.GetHeight()); c != 0 {
		return c
	}
	if c := cmp.Compare(r.GetMaximumTotalParentsSizeBytes(true), other.GetMaximumTotalParentsSizeBytes(true)); c != 0 {
		return c
	}

	// Tie breaker to ensure a total order.
	return bytes.Compare(r.GetRawReference(), other.GetRawReference())
}
