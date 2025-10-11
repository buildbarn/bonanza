package object

import (
	"encoding/hex"

	"github.com/buildbarn/bb-storage/pkg/util"
)

// SHA256V1FlatReferenceSizeBytes corresponds to the number of bytes
// that are needed to store a flat reference of type SHA256_V1.
const SHA256V1FlatReferenceSizeBytes = 35

// FlatReference is a simplified version of LocalReference that assumes
// that the height and degree of an object are zero. This means that
// only the hash and size need to be tracked, similar to REv2's Digest
// message.
//
// Because the hash and size are stored at the beginning of a
// LocalReference, FlatReference uses the same representation as
// LocalReference with the last couple of bytes removed.
//
// FlatReference is sufficient for the read caching and local storage
// backends, which are oblivious of references between objects.
type FlatReference struct {
	rawReference [SHA256V1FlatReferenceSizeBytes]byte
}

// MustNewSHA256V1FlatReference creates a flat reference that uses
// reference format SHA256_V1. This function can be used as part of
// tests.
func MustNewSHA256V1FlatReference(hash string, sizeBytes uint32) (flatReference FlatReference) {
	var rawReference [35]byte
	if n, err := hex.Decode(rawReference[:], []byte(hash)); err != nil {
		panic(err)
	} else if n != 32 {
		panic("Wrong hash length")
	}
	rawReference[32] = byte(sizeBytes)
	rawReference[33] = byte(sizeBytes >> 8)
	rawReference[34] = byte(sizeBytes >> 16)

	return util.Must(SHA256V1ReferenceFormat.NewFlatReference(rawReference[:]))
}

// GetReferenceFormat returns the reference format that was used to
// generate the reference.
func (FlatReference) GetReferenceFormat() ReferenceFormat {
	return ReferenceFormat{}
}

// GetRawFlatReference returns the flat reference in binary form, so
// that it may be embedded into gRPC request bodies.
func (r FlatReference) GetRawFlatReference() []byte {
	return r.rawReference[:]
}

// GetLocalReference converts a flat reference to a corresponding local
// reference having height zero.
func (r FlatReference) GetLocalReference() (localReference LocalReference) {
	copy(localReference.rawReference[:], r.rawReference[:])
	return localReference
}

// GetHash returns the hash of the contents of the object associated
// with the flat reference.
func (r FlatReference) GetHash() []byte {
	return r.rawReference[:32]
}

// GetSizeBytes returns the size of the object associated with the flat
// reference.
func (r FlatReference) GetSizeBytes() int {
	return int(r.rawReference[32]) |
		(int(r.rawReference[33]) << 8) |
		(int(r.rawReference[34]) << 16)
}
