package canonicalzstd

import (
	"encoding/binary"
)

// bitWriter writes a little-endian bitstream, where values are packed
// starting at the least significant bit of each byte. This is the
// order used by Zstandard for both forward and backward bitstreams.
type bitWriter struct {
	out   []byte
	acc   uint64
	nBits uint
}

// addBits appends the lowest n bits of v to the bitstream. n may not
// exceed 32.
func (w *bitWriter) addBits(v uint64, n uint) {
	w.acc |= (v & (1<<n - 1)) << w.nBits
	w.nBits += n
	if w.nBits >= 32 {
		w.out = binary.LittleEndian.AppendUint32(w.out, uint32(w.acc))
		w.acc >>= 32
		w.nBits -= 32
	}
}

// flush pads the bitstream with zero bits up to the next byte
// boundary, and returns its contents.
func (w *bitWriter) flush() []byte {
	for w.nBits > 0 {
		w.out = append(w.out, byte(w.acc))
		w.acc >>= 8
		w.nBits = max(w.nBits, 8) - 8
	}
	w.acc = 0
	return w.out
}

// close terminates a backward bitstream by appending a single one bit
// and padding it to the next byte boundary. Decoders use the position
// of the highest one bit in the final byte to locate the start of the
// stream.
func (w *bitWriter) close() []byte {
	w.addBits(1, 1)
	return w.flush()
}
