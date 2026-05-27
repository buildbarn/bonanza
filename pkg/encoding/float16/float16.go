package float16

import (
	"math/bits"
)

const (
	significandBits       = 11
	significandMask       = (1 << significandBits) - 1
	significandLeadingBit = 1 << significandBits
	exponentBits          = 16 - significandBits
	exponentMax           = (1 << exponentBits) - 1
)

func float16GetExponent(v uint64) int {
	return bits.Len64(v) - significandBits - 1
}

// FromUint64RoundUp converts an integer value to its corresponding
// 16-bit floating-point value, rounding up if needed.
//
// Note that the format used differs from IEEE 754's half-precision
// floating-point format. There is no sign bit, meaning that the
// significand is 11 bits. The exponent bias is chosen so that the
// smallest subnormal value corresponds to integer value 1. There is no
// support for expressing infinity/NaN.
func FromUint64RoundUp(v uint64) (uint16, bool) {
	if v <= significandMask {
		// Value can be represented as a subnormal floating-point value.
		return uint16(v), true
	}

	if v > (significandLeadingBit+significandMask)<<(exponentMax-1) {
		// Number is too large to represent as a floating-point value.
		return 0, false
	}

	// Round the result upwards.
	v += ^(^uint64(0) << float16GetExponent(v))
	shift := uint16(float16GetExponent(v))
	return ((shift + 1) << significandBits) | (uint16(v>>shift) & significandMask), true
}

// ToUint64 converts a 16-bit floating-point value to its corresponding
// integer value. This function is the inverse of
// Uint64ToFloat16RoundUp.
func ToUint64(v uint16) uint64 {
	exponent := v >> significandBits
	if exponent == 0 {
		// Subnormal floating-point value.
		return uint64(v)
	}

	// Normal floating-point value.
	significand := uint64(v) & significandMask
	return (significand | significandLeadingBit) << (exponent - 1)
}
