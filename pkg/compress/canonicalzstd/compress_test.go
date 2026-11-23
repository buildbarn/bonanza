package canonicalzstd_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"bonanza.build/pkg/compress/canonicalzstd"

	"github.com/stretchr/testify/require"
)

// generateTestData deterministically generates data that contains a
// mixture of skewed literals and repetitions at varying distances.
// It uses its own random number generator, so that the output remains
// stable across Go releases.
func generateTestData(size int, seed uint64) []byte {
	state := seed
	next := func(n uint64) uint64 {
		state = state*6364136223846793005 + 1442695040888963407
		return (state >> 33) % n
	}
	data := make([]byte, 0, size)
	for len(data) < size {
		if len(data) > 16 && next(3) == 0 {
			offset := 1 + int(next(uint64(min(len(data), 1+int(next(200000))))))
			length := 3 + int(next(60))
			for i := 0; i < length && len(data) < size; i++ {
				data = append(data, data[len(data)-offset])
			}
		} else {
			// Approximately geometric distribution.
			b := byte(0)
			for b < 255 && next(8) != 0 {
				b++
			}
			data = append(data, b)
		}
	}
	return data
}

func TestCompress(t *testing.T) {
	t.Run("Golden", func(t *testing.T) {
		// Exact outputs for small inputs. Any change to these
		// values constitutes a change to the format.
		for input, expected := range map[string]string{
			"":                            "",
			"a":                           "28b52ffd200109000061",
			"aaaaaaaa":                    "28b52ffd200843000061",
			"abcdabcdabcdabcd":            "28b52ffd201055000020616263640100b84b04",
			"Hello, Hello, Hello, World!": "28b52ffd201b9d00006848656c6c6f2c20576f726c64210100928b11",
		} {
			require.Equal(t, expected, hex.EncodeToString(canonicalzstd.Compress([]byte(input))), "Input %#v", input)
		}
	})

	t.Run("GoldenHashes", func(t *testing.T) {
		// SHA-256 hashes of outputs for larger inputs, covering
		// multiple blocks and Huffman coded literals.
		for _, tc := range []struct {
			size     int
			seed     uint64
			expected string
		}{
			{1000, 1, "b3c4bea2732fa33d7c0ea127a5343b7daaf113042134792068861c35ed2b12c4"},
			{100000, 2, "a799ff1b758a9855e0ff3cab2fcefe20eae1a7941681a44661aef75ff5da31c1"},
			{2 << 20, 3, "fbb5f18f1637f0d8d26daf5bb4866584aec8245072950bfd45182e32846f1d0f"},
		} {
			hash := sha256.Sum256(canonicalzstd.Compress(generateTestData(tc.size, tc.seed)))
			require.Equal(t, tc.expected, hex.EncodeToString(hash[:]), "Size %d", tc.size)
		}
	})

	t.Run("RoundTrip", func(t *testing.T) {
		for _, size := range []int{0, 1, 2, 3, 4, 5, 31, 32, 1023, 1024, 4095, 4096, 65791, 65792, 131072, 131073, 1 << 20} {
			for seed := uint64(0); seed < 3; seed++ {
				input := generateTestData(size, seed)
				compressed := canonicalzstd.Compress(input)
				decompressed, err := canonicalzstd.Decompress(compressed, uint32(size))
				require.NoError(t, err)
				require.True(t, bytes.Equal(input, decompressed), "Size %d, seed %d", size, seed)
			}
		}
	})

	t.Run("CompressibleAfterIncompressible", func(t *testing.T) {
		// Skipping during long runs of literals must not prevent
		// the parser from finding matches in compressible data
		// that follows.
		input := make([]byte, 16384, 16384+48*1024)
		state := uint64(1)
		for i := range input {
			state = state*6364136223846793005 + 1442695040888963407
			input[i] = byte(state >> 56)
		}
		block := input[:1024]
		for range 48 {
			input = append(input, block...)
		}
		compressed := canonicalzstd.Compress(input)
		require.Equal(t, 16419, len(compressed))
		hash := sha256.Sum256(compressed)
		require.Equal(t, "6d1fce63004f85683265acc9de39291c0d13cbbf800784f70b0cd9369af717aa", hex.EncodeToString(hash[:]))
	})

	t.Run("Incompressible", func(t *testing.T) {
		// Incompressible data should be stored in raw blocks,
		// adding only a small amount of overhead.
		input := make([]byte, 300000)
		state := uint64(1)
		for i := range input {
			state = state*6364136223846793005 + 1442695040888963407
			input[i] = byte(state >> 56)
		}
		compressed := canonicalzstd.Compress(input)
		require.Equal(t, len(input)+4+5+3*3, len(compressed))
	})
}

func TestDecompress(t *testing.T) {
	t.Run("Empty", func(t *testing.T) {
		decompressed, err := canonicalzstd.Decompress(nil, 0)
		require.NoError(t, err)
		require.Empty(t, decompressed)
	})

	t.Run("TooLarge", func(t *testing.T) {
		_, err := canonicalzstd.Decompress(canonicalzstd.Compress([]byte("Hello")), 4)
		require.ErrorContains(t, err, "exceeds the permitted maximum of 4 bytes")
	})

	t.Run("InvalidMagicNumber", func(t *testing.T) {
		// Input that does not start with the regular frame magic
		// number, such as a skippable frame, is rejected.
		for _, input := range [][]byte{
			{0x28},
			{0x20, 0x01, 0x09, 0x00, 0x00, 'a'},
			{0x50, 0x2a, 0x4d, 0x18, 0x00, 0x00, 0x00, 0x00},
		} {
			_, err := canonicalzstd.Decompress(input, 100)
			require.ErrorContains(t, err, "does not start with the Zstandard frame magic number")
		}
	})

	t.Run("InvalidFrameHeader", func(t *testing.T) {
		// Frames that are not single segment are rejected.
		_, err := canonicalzstd.Decompress([]byte{0x28, 0xb5, 0x2f, 0xfd, 0x00, 0x58, 0x01, 0x00, 0x00}, 100)
		require.ErrorContains(t, err, "does not denote a single segment frame")
	})

	t.Run("TrailingFrame", func(t *testing.T) {
		// Additional frames following the first one must be
		// rejected. Otherwise small inputs could decompress to
		// large amounts of data.
		compressed := canonicalzstd.Compress([]byte("a"))
		compressed = append(compressed, canonicalzstd.Compress([]byte("b"))...)
		_, err := canonicalzstd.Decompress(compressed, 100)
		require.ErrorContains(t, err, "trailing data after the last block")
	})

	t.Run("TrailingByte", func(t *testing.T) {
		compressed := append(canonicalzstd.Compress([]byte("Hello")), 0)
		_, err := canonicalzstd.Decompress(compressed, 100)
		require.ErrorContains(t, err, "trailing data after the last block")
	})

	t.Run("BlockTooLarge", func(t *testing.T) {
		// Frame with a content size of 1 byte, containing a
		// Raw block of 2 bytes.
		_, err := canonicalzstd.Decompress([]byte{0x28, 0xb5, 0x2f, 0xfd, 0x20, 0x01, 0x11, 0x00, 0x00, 'a', 'b'}, 100)
		require.ErrorContains(t, err, "Block size of 2 bytes exceeds the maximum of 1 bytes")
	})

	t.Run("RLEBlocksExceedContentSize", func(t *testing.T) {
		// Frame with a content size of 300 bytes, containing
		// two RLE blocks of 200 bytes each.
		_, err := canonicalzstd.Decompress([]byte{0x28, 0xb5, 0x2f, 0xfd, 0x60, 0x2c, 0x00, 0x42, 0x06, 0x00, 'a', 0x43, 0x06, 0x00, 'b'}, 1000)
		require.ErrorContains(t, err, "Blocks decompress to more than the frame content size of 300 bytes")
	})

	t.Run("ReservedBlockType", func(t *testing.T) {
		_, err := canonicalzstd.Decompress([]byte{0x28, 0xb5, 0x2f, 0xfd, 0x20, 0x01, 0x07, 0x00, 0x00}, 100)
		require.ErrorContains(t, err, "Block has a reserved block type")
	})

	t.Run("Truncated", func(t *testing.T) {
		compressed := canonicalzstd.Compress(generateTestData(10000, 1))
		_, err := canonicalzstd.Decompress(compressed[:len(compressed)-1], 10000)
		require.Error(t, err)
	})
}
