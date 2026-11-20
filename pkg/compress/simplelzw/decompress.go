package simplelzw

import (
	"encoding/binary"
	"math/bits"

	"bonanza.build/pkg/encoding/varint"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Decompress data that was compressed using Compress() or
// MaybeCompress().
func Decompress(compressed []byte, maximumSizeBytes uint32) ([]byte, error) {
	if len(compressed) == 0 {
		// Empty input decompresses to empty output.
		return []byte{}, nil
	}

	// Compressed data is prefixed with the decompressed size in
	// bytes, which makes it possible to allocate an output buffer.
	// If the size is zero, the data didn't compress well, causing
	// it to be stored in decompressed form.
	decompressedSizeBytes, n := varint.ConsumeForward[uint32](compressed)
	if n < 0 {
		return nil, status.Error(codes.InvalidArgument, "Invalid leading decompressed size in header")
	}
	compressed = compressed[n:]
	if decompressedSizeBytes == 0 {
		if uint64(len(compressed)) > uint64(maximumSizeBytes) {
			return nil, status.Errorf(
				codes.InvalidArgument,
				"Decompressed data is %d bytes in size, which exceeds the permitted maximum of %d bytes",
				len(compressed),
				maximumSizeBytes,
			)
		}
		return compressed, nil
	}
	if decompressedSizeBytes > maximumSizeBytes {
		return nil, status.Errorf(
			codes.InvalidArgument,
			"Decompressed data is %d bytes in size, which exceeds the permitted maximum of %d bytes",
			decompressedSizeBytes,
			maximumSizeBytes,
		)
	}

	// Given the size of the compressed data, determine the
	// number of codes that are stored.
	remainingBits := len(compressed) * 8
	const initialHighestCode = 255
	highestCode := initialHighestCode
	for {
		bitsPerCode := bits.Len(uint(highestCode))
		codesUntilNextPow2 := 1<<bitsPerCode - highestCode
		bitsUntilNextPow2 := bitsPerCode * codesUntilNextPow2
		if remainingBits < bitsUntilNextPow2 {
			codesUntilEnd := remainingBits / bitsPerCode
			remainingBits -= codesUntilEnd * bitsPerCode
			highestCode += codesUntilEnd
			break
		}
		remainingBits -= bitsUntilNextPow2
		highestCode = 1 << bitsPerCode
	}
	if remainingBytes := remainingBits / 8; remainingBytes > 0 {
		return nil, status.Errorf(codes.InvalidArgument, "Compressed input contains %d unnecessary trailing bytes", remainingBytes)
	}

	// Mapping of codes to monotonically increasing offsets
	// within the decompressed data, allowing prefixes to be
	// obtained. The length of the prefix can be obtained
	// by considering the next offset.
	offsets := make([]uint32, 0, highestCode-initialHighestCode)

	pendingInput := uint64(0)
	pendingBits := 0
	decompressed := make([]byte, 0, decompressedSizeBytes)
	for i := initialHighestCode; i < highestCode; i++ {
		// Consume the next code from the input.
		codeLength := bits.Len32(uint32(i))
		if pendingBits < codeLength {
			if len(compressed) >= 4 {
				pendingInput |= uint64(binary.LittleEndian.Uint32(compressed)) << pendingBits
				pendingBits += 32
				compressed = compressed[4:]
			} else {
				for _, b := range compressed {
					pendingInput |= uint64(b) << pendingBits
					pendingBits += 8
				}
			}
		}
		currentCode := pendingInput & ((1 << codeLength) - 1)
		pendingInput >>= codeLength
		pendingBits -= codeLength

		offsets = append(offsets, uint32(len(decompressed)))
		if currentCode <= initialHighestCode {
			// Literal value.
			decompressed = append(decompressed, byte(currentCode))
		} else if index := currentCode - initialHighestCode; index < uint64(len(offsets)) {
			// Repetition of previously observed data.
			firstOffset, lastOffset := offsets[index-1], offsets[index]
			decompressed = append(decompressed, decompressed[firstOffset:lastOffset]...)
			decompressed = append(decompressed, decompressed[lastOffset])
		} else {
			return nil, status.Errorf(codes.InvalidArgument, "Input contains unexpected code %d", currentCode)
		}
		if uint64(len(decompressed)) > uint64(decompressedSizeBytes) {
			return nil, status.Errorf(
				codes.InvalidArgument,
				"Size of decompressed output exceeds size in header of %d bytes, with %d of %d codes processed",
				decompressedSizeBytes,
				i-initialHighestCode,
				highestCode-initialHighestCode,
			)
		}
	}

	if pendingInput != 0 {
		return nil, status.Error(codes.InvalidArgument, "Trailing bits are not zero")
	}
	if uint64(len(decompressed)) != uint64(decompressedSizeBytes) {
		return nil, status.Errorf(
			codes.InvalidArgument,
			"Size of decompressed output is %d bytes, which differs from size in header of %d bytes",
			len(decompressed),
			decompressedSizeBytes,
		)
	}
	return decompressed, nil
}
