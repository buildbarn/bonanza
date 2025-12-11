package lthash

import (
	"crypto/sha3"
	"encoding/binary"
	"io"
)

// Hasher of sets, yielding a hash that is independent of the order in
// which elements are added. This is achieved by implementing the LtHASH
// algorithm, as described in the following two papers:
//
//   - A New Paradigm for Collision-free Hashing: Incrementality at
//     Reduced Cost, by Bellare and Micciancio.
//     https://cseweb.ucsd.edu/~daniele/papers/IncHash.pdf
//   - Secure Update Propagation with Homomorphic Hashing, by Lewi, Kim,
//     Maykov, and Weis.
//     https://eprint.iacr.org/2019/227.pdf
//
// This implementation is similar to the one proposed in the second
// paper, with the main difference that the extendable-output function
// (XOF) that is used by this implementation is SHAKE128 instead of
// BLAKE2b.
type Hasher struct {
	shake      *sha3.SHAKE
	scratch    [2048]byte
	currentSum [2048]byte
}

// NewHasher creates a new Hasher that is in the initial state,
// representing the empty set.
func NewHasher() *Hasher {
	return &Hasher{
		shake: sha3.NewSHAKE128(),
	}
}

// Add an element to the set for which a hash is computed.
//
// Though it is possible to add the same element to the set multiple
// times, this can only be done up to 2^16 times, as it leads to trivial
// hash collisions otherwise.
func (h *Hasher) Add(element []byte) {
	// Hash the element to obtain 1024 16-bit integers.
	h.shake.Reset()
	if _, err := h.shake.Write(element); err != nil {
		panic(err)
	}
	if _, err := io.ReadFull(h.shake, h.scratch[:]); err != nil {
		panic(err)
	}

	// Add all the of the 16-bit integers to the values obtained
	// thus far.
	for off := 0; off < len(h.currentSum); off += 2 {
		binary.LittleEndian.PutUint16(
			h.currentSum[off:],
			binary.LittleEndian.Uint16(h.currentSum[off:])+binary.LittleEndian.Uint16(h.scratch[off:]),
		)
	}
}

// Sum returns a 256-bit hash for the elements contained in the set.
func (h *Hasher) Sum() [32]byte {
	return sha3.Sum256(h.currentSum[:])
}
