package encoding

import (
	"crypto/cipher"
	"math/bits"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type encryptingDeterministicBinaryEncoder struct {
	aead           cipher.AEAD
	additionalData []byte
	tagSizeBytes   int
	nonce          []byte
}

// NewEncryptingDeterministicBinaryEncoder creates a
// DeterministicBinaryEncoder that is capable of encrypting and
// decrypting data. The encryption process is deterministic, in that
// encrypting the same data twice results in the same encoded version of
// the data. It uses Authenticating Encryption with Associated Data
// (AEAD), meaning that any objects encrypted with a different key will
// fail validation.
func NewEncryptingDeterministicBinaryEncoder(aead cipher.AEAD, additionalData []byte) DeterministicBinaryEncoder {
	return &encryptingDeterministicBinaryEncoder{
		aead:           aead,
		additionalData: additionalData,
		tagSizeBytes:   aead.Overhead(),
		nonce:          make([]byte, aead.NonceSize()),
	}
}

// getPaddedSizeBytes computes the size of the encrypted output, with
// padding in place. Because we use AES-GCM-SIV, we don't need any
// padding to encrypt the data itself. However, adding it reduces
// information leakage by obfuscating the original size.
//
// Use the same structure as Padded Uniform Random Blobs (PURBs), where
// the length is rounded up to a floating point number whose mantissa is
// no longer than its exponent.
//
// More details: Reducing Metadata Leakage from Encrypted Files and
// Communication with PURBs, Algorithm 1 "PADMÉ".
// https://petsymposium.org/popets/2019/popets-2019-0056.pdf
func getPaddedSizeBytes(dataSizeBytes int) int {
	e := bits.Len(uint(dataSizeBytes)) - 1
	bitsToClear := e - bits.Len(uint(e))
	return (dataSizeBytes>>bitsToClear + 1) << bitsToClear
}

// Pad the plaintext data according to the PADMÉ algorithm. Also ensure
// that enough space is present after the plaintext to store any
// overhead induced by the AEAD.
func pad(plaintext []byte, overheadSizeBytes int) []byte {
	paddedPlaintextSize := getPaddedSizeBytes(len(plaintext))
	paddedPlaintext := make([]byte, paddedPlaintextSize, paddedPlaintextSize+overheadSizeBytes)
	copy(paddedPlaintext, plaintext)
	paddedPlaintext[len(plaintext)] = 0x80
	return paddedPlaintext
}

// Unpad removes padding that was previously added by the pad() function.
func unpad(paddedPlaintext []byte) ([]byte, error) {
	plaintext := paddedPlaintext
	for l := len(plaintext) - 1; l > 0; l-- {
		switch plaintext[l] {
		case 0x00:
		case 0x80:
			plaintext = plaintext[:l]
			if paddedSizeBytes := getPaddedSizeBytes(len(plaintext)); len(paddedPlaintext) != paddedSizeBytes {
				return nil, status.Errorf(
					codes.InvalidArgument,
					"Encoded data is %d bytes in size, while %d bytes were expected for a payload of %d bytes",
					len(paddedPlaintext),
					paddedSizeBytes,
					len(plaintext),
				)
			}
			return plaintext, nil
		default:
			return nil, status.Errorf(codes.InvalidArgument, "Padding contains invalid byte with value %d", int(plaintext[l]))
		}
	}
	return nil, status.Error(codes.InvalidArgument, "No data remains after removing padding")
}

func (be *encryptingDeterministicBinaryEncoder) EncodeBinary(in []byte) ([]byte, []byte, error) {
	if len(in) == 0 {
		return []byte{}, make([]byte, be.tagSizeBytes), nil
	}

	paddedPlaintext := pad(in, be.tagSizeBytes)
	ciphertext := be.aead.Seal(paddedPlaintext[:0], be.nonce, paddedPlaintext, be.additionalData)

	// As AEAD.Seal() concatenates the ciphertext and the tag, split
	// it up again. We want to store the tag separately, so that
	// divulging the key does not permit immediate decryption of all
	// objects.
	return ciphertext[:len(paddedPlaintext)], ciphertext[len(paddedPlaintext):], nil
}

func (be *encryptingDeterministicBinaryEncoder) DecodeBinary(in, tag []byte) ([]byte, error) {
	if len(tag) != be.tagSizeBytes {
		return nil, status.Errorf(
			codes.InvalidArgument,
			"Decoding parameters are %d bytes in size, while the tag was expected to be %d bytes in size",
			len(tag),
			be.tagSizeBytes,
		)
	}

	if len(in) == 0 {
		return []byte{}, nil
	}

	// Re-attach the tag to the ciphertext and decrypt it.
	ciphertext := append(append([]byte(nil), in...), tag...)
	plaintext, err := be.aead.Open(ciphertext[:0], be.nonce, ciphertext, be.additionalData)
	if err != nil {
		return nil, util.StatusWrapWithCode(err, codes.InvalidArgument, "Decryption failed")
	}
	return unpad(plaintext)
}

func (be *encryptingDeterministicBinaryEncoder) GetDecodingParametersSizeBytes() int {
	return be.tagSizeBytes
}
