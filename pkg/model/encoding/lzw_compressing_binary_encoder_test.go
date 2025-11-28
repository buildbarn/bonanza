package encoding_test

import (
	"crypto/rand"
	"testing"

	model_encoding "bonanza.build/pkg/model/encoding"

	"github.com/stretchr/testify/require"
)

func TestLZWCompressingDeterministicBinaryEncoder(t *testing.T) {
	binaryEncoder := model_encoding.NewLZWCompressingDeterministicBinaryEncoder(1 << 20)

	t.Run("EncodeBinary", func(t *testing.T) {
		t.Run("Empty", func(t *testing.T) {
			encodedData, decodingState, err := binaryEncoder.EncodeBinary(nil)
			require.NoError(t, err)
			require.Empty(t, encodedData)
			require.Empty(t, decodingState)
		})
	})

	t.Run("DecodeBinary", func(t *testing.T) {
		t.Run("Empty", func(t *testing.T) {
			decodedData, err := binaryEncoder.DecodeBinary(nil, nil)
			require.NoError(t, err)
			require.Empty(t, decodedData)
		})
	})

	t.Run("RandomEncodeDecode", func(t *testing.T) {
		original := make([]byte, 10000)
		for length := 0; length < len(original); length++ {
			n, err := rand.Read(original[:length])
			require.NoError(t, err)
			require.Equal(t, length, n)

			encoded, decodingState, err := binaryEncoder.EncodeBinary(original[:length])
			require.NoError(t, err)
			require.Empty(t, decodingState)

			decoded, err := binaryEncoder.DecodeBinary(encoded, decodingState)
			require.NoError(t, err)
			require.Equal(t, original[:length], decoded)
		}
	})

	t.Run("AppendUniqueDecodingKeys", func(t *testing.T) {
		// AppendUniqueDecodingKeys returns a list of keys that
		// should only be equal to each other if the options
		// provided to the decoder are the same.
		t.Run("Equal", func(t *testing.T) {
			require.Equal(
				t,
				model_encoding.NewLZWCompressingDeterministicBinaryEncoder(1<<20).
					AppendUniqueDecodingKeys(nil),
				model_encoding.NewLZWCompressingDeterministicBinaryEncoder(1<<20).
					AppendUniqueDecodingKeys(nil),
			)
		})

		t.Run("NotEqual", func(t *testing.T) {
			require.NotEqual(
				t,
				model_encoding.NewLZWCompressingDeterministicBinaryEncoder(1<<19).
					AppendUniqueDecodingKeys(nil),
				model_encoding.NewLZWCompressingDeterministicBinaryEncoder(1<<20).
					AppendUniqueDecodingKeys(nil),
			)
		})

		t.Run("DeterministicAndKeyed", func(t *testing.T) {
			// With respect to LZW compression, there is no
			// difference between deterministic and keyed
			// encoding.
			require.Equal(
				t,
				model_encoding.NewLZWCompressingDeterministicBinaryEncoder(1<<20).
					AppendUniqueDecodingKeys(nil),
				model_encoding.NewLZWCompressingKeyedBinaryEncoder(1<<20).
					AppendUniqueDecodingKeys(nil),
			)
		})
	})
}
