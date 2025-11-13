package encoding_test

import (
	"crypto/rand"
	"testing"

	"bonanza.build/pkg/model/encoding"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/secure-io/siv-go"
	"github.com/stretchr/testify/require"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestEncryptingBinaryEncoder(t *testing.T) {
	aead, err := siv.NewGCM([]byte{
		0x10, 0xc2, 0xfe, 0xfd, 0x4b, 0x11, 0x43, 0x9b,
		0x1e, 0x73, 0xda, 0x12, 0xef, 0x53, 0xd1, 0x31,
		0xf7, 0x7e, 0x6c, 0xb8, 0x2f, 0xdf, 0x79, 0xb0,
		0x90, 0x6b, 0x23, 0x3c, 0x4a, 0x32, 0xa0, 0x14,
	})
	require.NoError(t, err)
	binaryEncoder := encoding.NewEncryptingBinaryEncoder(
		aead,
		/* additionalData = */ []byte{
			0xfb, 0x76, 0x80, 0x6e, 0xcb, 0x9d, 0x42, 0xd7, 0xbf, 0xa0, 0x3c, 0xf6,
			0x14, 0x3f, 0x0b, 0x1e, 0xc4, 0x94, 0x5c, 0x44, 0xe0, 0xb3, 0xa7, 0xfa,
			0x2c, 0xd1, 0xe3, 0x06, 0x35, 0x55, 0xb5, 0xda,
		},
	)

	t.Run("EncodeBinary", func(t *testing.T) {
		t.Run("Empty", func(t *testing.T) {
			encodedData, tag, err := binaryEncoder.EncodeBinary(nil)
			require.NoError(t, err)
			require.Empty(t, encodedData)
			require.Equal(t, []byte{
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
			}, tag)
		})

		t.Run("HelloWorld", func(t *testing.T) {
			encodedData, tag, err := binaryEncoder.EncodeBinary([]byte("Hello world"))
			require.NoError(t, err)
			require.Equal(t, []byte{
				// Encrypted payload.
				0x84, 0x3e, 0x2b, 0xb2, 0xde, 0x7a, 0x97, 0x63,
				0x4c, 0x35, 0x8a,
				// Padding.
				0x6a,
			}, encodedData)
			require.Equal(t, []byte{
				0x8e, 0xe3, 0xc3, 0xff, 0xce, 0x1e, 0x6c, 0x5a,
				0xd5, 0x87, 0x8b, 0xf1, 0xde, 0x23, 0x5e, 0xf9,
			}, tag)
		})
	})

	t.Run("DecodeBinary", func(t *testing.T) {
		t.Run("Empty", func(t *testing.T) {
			decodedData, err := binaryEncoder.DecodeBinary(
				/* in = */ nil,
				/* tag = */ []byte{
					0x7e, 0x2d, 0x56, 0x18, 0xc7, 0x3b, 0xda, 0x73,
					0x0a, 0x97, 0xef, 0xfd, 0xaf, 0x6d, 0xec, 0x96,
				},
			)
			require.NoError(t, err)
			require.Empty(t, decodedData)
		})

		t.Run("HelloWorld", func(t *testing.T) {
			decodedData, err := binaryEncoder.DecodeBinary(
				/* in = */ []byte{
					// Encrypted payload.
					0x84, 0x3e, 0x2b, 0xb2, 0xde, 0x7a, 0x97, 0x63,
					0x4c, 0x35, 0x8a,
					// Padding.
					0x6a,
				},
				/* tag = */ []byte{
					0x8e, 0xe3, 0xc3, 0xff, 0xce, 0x1e, 0x6c, 0x5a,
					0xd5, 0x87, 0x8b, 0xf1, 0xde, 0x23, 0x5e, 0xf9,
				},
			)
			require.NoError(t, err)
			require.Equal(t, []byte("Hello world"), decodedData)
		})

		t.Run("DecodingParametersTooShort", func(t *testing.T) {
			_, err := binaryEncoder.DecodeBinary([]byte("Hello"), nil)
			testutil.RequireEqualStatus(t, status.Error(codes.InvalidArgument, "Decoding parameters are 0 bytes in size, while the tag was expected to be 16 bytes in size"), err)
		})

		t.Run("DecryptionFailure", func(t *testing.T) {
			_, err := binaryEncoder.DecodeBinary(
				/* in = */ []byte{
					0x4c, 0x4b, 0x7a, 0x24, 0x46, 0x08, 0x15, 0x62,
					0x3a, 0xc6, 0x45, 0x02, 0xca, 0xc3, 0xe4, 0xd3,
				},
				/* tag = */ []byte{
					0xad, 0x38, 0x0e, 0x0e, 0xfc, 0x7b, 0x2e, 0x2b,
					0x6c, 0x08, 0x61, 0xd8, 0x1d, 0x20, 0xb4, 0xc9,
				},
			)
			testutil.RequirePrefixedStatus(t, status.Error(codes.InvalidArgument, "Decryption failed: "), err)
		})

		t.Run("BadPadding", func(t *testing.T) {
			_, err := binaryEncoder.DecodeBinary(
				/* in = */ []byte{
					// Encrypted payload.
					0x1f, 0xe5, 0x5b, 0x05, 0xa7, 0x56, 0xf4, 0x90,
					0x0d, 0x6a, 0x60,

					// Padding.
					0x2c,
					// Invalid padding.
					0xce,
				},
				/* tag = */ []byte{
					0x5c, 0x4f, 0x3f, 0x1b, 0xf6, 0x42, 0x2f, 0x17,
					0x95, 0x44, 0xd3, 0x33, 0xf9, 0xea, 0x06, 0x03,
				},
			)
			testutil.RequireEqualStatus(t, status.Error(codes.InvalidArgument, "Padding contains invalid byte with value 118"), err)
		})

		t.Run("TooMuchPadding", func(t *testing.T) {
			// Additional check to ensure that other
			// implementations encode data the same way.
			// Using different amounts of padding may
			// introduce information leakage.
			_, err := binaryEncoder.DecodeBinary(
				/* in = */ []byte{
					// Encrypted payload.
					0x7c, 0x91, 0xe0, 0x2b, 0xf8, 0xfe, 0xcf, 0xd8,
					0xc6, 0xae, 0x8e,
					// Padding.
					0x4e, 0x7b,
				},
				/* tag = */ []byte{
					0x1e, 0xf5, 0xf8, 0xc3, 0xad, 0xd1, 0x80, 0x08,
					0x6f, 0x23, 0x4b, 0xeb, 0x9e, 0xeb, 0x48, 0xc3,
				},
			)
			testutil.RequireEqualStatus(t, status.Error(codes.InvalidArgument, "Encoded data is 13 bytes in size, while 12 bytes were expected for a payload of 11 bytes"), err)
		})
	})

	t.Run("RandomEncodeDecode", func(t *testing.T) {
		original := make([]byte, 10000)
		for length := 0; length < len(original); length++ {
			n, err := rand.Read(original[:length])
			require.NoError(t, err)
			require.Equal(t, length, n)

			encoded, tag, err := binaryEncoder.EncodeBinary(original[:length])
			require.NoError(t, err)

			decoded, err := binaryEncoder.DecodeBinary(encoded, tag)
			require.NoError(t, err)
			require.Equal(t, original[:length], decoded)
		}
	})
}
