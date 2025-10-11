package varint

import (
	"encoding/binary"
	"io"
	"math/bits"

	"golang.org/x/exp/constraints"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// AppendForward appends an integer value to a byte slice using a
// variable length encoding, which can be read back in a forward parsing
// manner.
//
// The encoding used by this function is similar to UTF-8, except that
// it is simplified to no longer be self-synchronizing. The number of
// leading 0-bits corresponds to the number of trailing bytes. Trailing
// bytes do not have any special prefix. Values are sign extended.
func AppendForward[T constraints.Integer](dst []byte, value T) []byte {
	if value < 0 {
		v := int64(value)
		switch {
		case v >= -(1 << 6):
			// Negative 7 bit value stored in 1 byte.
			return append(dst, byte(v))
		case v >= -(1 << 13):
			// Negative 14 bit value stored in 2 bytes.
			return binary.BigEndian.AppendUint16(dst, uint16(v)&0x7fff)
		case v >= -(1 << 20):
			// Negative 21 bit value stored in 3 bytes.
			return binary.BigEndian.AppendUint16(
				append(dst, byte(v>>16)&0x3f),
				uint16(v),
			)
		case v >= -(1 << 27):
			// Negative 28 bit value stored in 4 bytes.
			return binary.BigEndian.AppendUint32(dst, uint32(v)&0x1fffffff)
		case v >= -(1 << 34):
			// Negative 35 bit value stored in 5 bytes.
			return binary.BigEndian.AppendUint32(
				append(dst, byte(v>>32)&0x0f),
				uint32(v),
			)
		case v >= -(1 << 41):
			// Negative 42 bit value stored in 6 bytes.
			return binary.BigEndian.AppendUint32(
				binary.BigEndian.AppendUint16(dst, uint16(v>>32)&0x07ff),
				uint32(v),
			)
		case v >= -(1 << 48):
			// Negative 49 bit value stored in 7 bytes.
			return binary.BigEndian.AppendUint32(
				binary.BigEndian.AppendUint16(
					append(dst, 0x03),
					uint16(v>>32),
				),
				uint32(v),
			)
		case v >= -(1 << 55):
			// Negative 56 bit value stored in 8 bytes.
			return binary.BigEndian.AppendUint64(dst, uint64(v)&0x01ffffffffffffff)
		case v >= -(1 << 62):
			// Negative 63 bit value stored in 9 bytes.
			return binary.BigEndian.AppendUint64(
				append(dst, 0x00),
				uint64(v),
			)
		default:
			// Negative 70 bit value stored in 10 bytes.
			return binary.BigEndian.AppendUint64(
				append(dst, 0x00, 0x7f),
				uint64(v),
			)
		}
	} else {
		v := uint64(value)
		switch {
		case v < 1<<6:
			// Non-negative 7 bit value stored in 1 byte.
			return append(dst, byte(v)|0x80)
		case v < 1<<13:
			// Non-negative 14 bit value stored in 2 bytes.
			return binary.BigEndian.AppendUint16(dst, uint16(v)|0x4000)
		case v < 1<<20:
			// Non-negative 21 bit value stored in 3 bytes.
			return binary.BigEndian.AppendUint16(
				append(dst, byte(v>>16)|0x20),
				uint16(v),
			)
		case v < 1<<27:
			// Non-negative 28 bit value stored in 4 bytes.
			return binary.BigEndian.AppendUint32(dst, uint32(v)|0x10000000)
		case v < 1<<34:
			// Non-negative 35 bit value stored in 5 bytes.
			return binary.BigEndian.AppendUint32(
				append(dst, byte(v>>32)|0x08),
				uint32(v),
			)
		case v < 1<<41:
			// Non-negative 42 bit value stored in 6 bytes.
			return binary.BigEndian.AppendUint32(
				binary.BigEndian.AppendUint16(dst, uint16(v>>32)|0x0400),
				uint32(v),
			)
		case v < 1<<48:
			// Non-negative 49 bit value stored in 7 bytes.
			return binary.BigEndian.AppendUint32(
				binary.BigEndian.AppendUint16(
					append(dst, 0x02),
					uint16(v>>32),
				),
				uint32(v),
			)
		case v < 1<<55:
			// Non-negative 56 bit value stored in 8 bytes.
			return binary.BigEndian.AppendUint64(dst, v|0x0100000000000000)
		case v < 1<<62:
			// Non-negative 63 bit value stored in 9 bytes.
			return binary.BigEndian.AppendUint64(
				append(dst, 0x00),
				v|0x8000000000000000,
			)
		default:
			// Non-negative 70 bit value stored in 10 bytes.
			return binary.BigEndian.AppendUint64(
				append(dst, 0x00, 0x40),
				v,
			)
		}
	}
}

// ConsumeForward consumes an integer value from the head of a byte
// slice, which was encoded using AppendForward().
func ConsumeForward[T constraints.Integer](src []byte) (T, int) {
	if len(src) < 1 {
		return 0, -1
	}
	leadingZeros := bits.LeadingZeros8(uint8(src[0]))
	length := 1 + leadingZeros
	if len(src) < length {
		return 0, -1
	}

	isUnsigned := ^T(0) > 0
	var v int64
	switch leadingZeros {
	case 0:
		// 7 bit value stored in 1 byte.
		v = int64(src[0]) << 57 >> 57
	case 1:
		// 14 bit value stored in 2 bytes.
		v = int64(binary.BigEndian.Uint16(src)) << 50 >> 50
	case 2:
		// 21 bit value stored in 3 bytes.
		v = (int64(src[0]) << 59 >> 43) |
			int64(binary.BigEndian.Uint16(src[1:]))
	case 3:
		// 28 bit value stored in 4 bytes.
		v = int64(binary.BigEndian.Uint32(src)) << 36 >> 36
	case 4:
		// 35 bit value stored in 5 bytes.
		v = (int64(src[0]) << 61 >> 29) |
			int64(binary.BigEndian.Uint32(src[1:]))
	case 5:
		// 42 bit value stored in 6 bytes.
		v = (int64(binary.BigEndian.Uint16(src)) << 54 >> 22) |
			int64(binary.BigEndian.Uint32(src[2:]))
	case 6:
		// 49 byte value stored in 7 bytes.
		v = (int64(src[0]) << 63 >> 15) |
			(int64(binary.BigEndian.Uint16(src[1:])) << 32) |
			int64(binary.BigEndian.Uint32(src[3:]))
	case 7:
		// 56 bit value stored in 8 bytes.
		v = int64(binary.BigEndian.Uint64(src)) << 8 >> 8
	case 8:
		leadingZeros := bits.LeadingZeros8(uint8(src[1]))
		length += leadingZeros
		if len(src) < length {
			return 0, -1
		}

		switch {
		case leadingZeros == 0:
			// 63 bit values stored in 9 bytes.
			v = int64(binary.BigEndian.Uint64(src[1:])) << 1 >> 1
		case isUnsigned && src[1] == 0x40:
			// 70 bit value stored in 10 bytes, converted to uint64.
			v := binary.BigEndian.Uint64(src[2:])
			converted := T(v)
			if uint64(converted) != v {
				return 0, -1
			}
			return converted, length
		case !isUnsigned && (src[1] == 0x40 && src[2] < 0x80) || (src[1] == 0x7f && src[2] >= 0x80):
			// 70 bit value stored in 10 bytes, converted to int64.
			v = int64(binary.BigEndian.Uint64(src[2:]))
		default:
			return 0, -1
		}
	}

	// Bounds checking against the desired integer type.
	if isUnsigned && v < 0 {
		return 0, -1
	}
	converted := T(v)
	if int64(converted) != v {
		return 0, -1
	}
	return converted, length
}

// ReadForward consumes an integer value from the start of a ByteReader,
// which was encoded using AppendForward().
func ReadForward[T constraints.Integer](r io.ByteReader) (T, error) {
	b, err := r.ReadByte()
	if err != nil {
		return 0, err
	}
	leadingZeros := bits.LeadingZeros8(uint8(b))
	length := leadingZeros

	isUnsigned := ^T(0) > 0
	if leadingZeros == 8 {
		// Special handling for sequences that are 9 or 10 bytes.
		b, err = r.ReadByte()
		if err != nil {
			if err == io.EOF {
				return 0, io.ErrUnexpectedEOF
			}
		}
		length--

		leadingZeros = bits.LeadingZeros8(uint8(b))
		length += leadingZeros
		switch {
		case leadingZeros == 0:
			// 63 bit value stored in 9 bytes.
		case isUnsigned && b == 0x40:
			// 70 bit value stored in 10 bytes, converted to uint64.
			var v uint64
			for i := 0; i < length; i++ {
				b, err = r.ReadByte()
				if err != nil {
					if err == io.EOF {
						return 0, io.ErrUnexpectedEOF
					}
				}
				v = v<<8 | uint64(b)
			}
			converted := T(v)
			if uint64(converted) != v {
				return 0, status.Errorf(codes.InvalidArgument, "Value is %d, which is out of range", v)
			}
			return converted, nil
		case !isUnsigned && (b == 0x40 || b == 0x7f):
			// 70 bit value stored in 10 bytes, converted to int64.
			positive := b == 0x40
			b, err = r.ReadByte()
			if err != nil {
				if err == io.EOF {
					return 0, io.ErrUnexpectedEOF
				}
			}
			length--
			leadingZeros = -1
			if (positive && b >= 0x80) || (!positive && b < 0x80) {
				return 0, status.Error(codes.InvalidArgument, "Value is out of range for int64")
			}
		default:
			return 0, status.Error(codes.InvalidArgument, "Value is out of range for int64 and uint64")
		}
	}

	shift := 64 - 7 + leadingZeros
	v := int64(b) << shift >> shift
	for i := 0; i < length; i++ {
		b, err = r.ReadByte()
		if err != nil {
			if err == io.EOF {
				return 0, io.ErrUnexpectedEOF
			}
		}
		v = v<<8 | int64(b)
	}

	// Bounds checking against the desired integer type.
	if isUnsigned && v < 0 {
		return 0, status.Errorf(codes.InvalidArgument, "Value is %d, while a non-negative value was expected", v)
	}
	converted := T(v)
	if int64(converted) != v {
		return 0, status.Errorf(codes.InvalidArgument, "Value is %d, which is out of range", v)
	}
	return converted, nil
}
