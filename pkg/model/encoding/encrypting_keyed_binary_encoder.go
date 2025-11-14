package encoding

import (
	"crypto/cipher"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type encryptingKeyedBinaryEncoder struct {
	aead              cipher.AEAD
	additionalData    []byte
	nonceSizeBytes    int
	overheadSizeBytes int
}

// NewEncryptingKeyedBinaryEncoder creates a KeyedBinaryEncoder that is
// capable of encrypting and decrypting data.
//
// Whereas NewEncryptingDeterministicBinaryEncoder() creates an encoder
// that is fully deterministic (i.e., always yielding the same data if
// the same input is provided), this implementation allows the decoding
// parameters to act as AES-GCM-SIV's nonce.
//
// This implementation should only be used in case there is no way to
// explicitly store decoding parameters, such as the Tag Store.
func NewEncryptingKeyedBinaryEncoder(aead cipher.AEAD, additionalData []byte) KeyedBinaryEncoder {
	return &encryptingKeyedBinaryEncoder{
		aead:              aead,
		additionalData:    additionalData,
		nonceSizeBytes:    aead.NonceSize(),
		overheadSizeBytes: aead.Overhead(),
	}
}

func (be *encryptingKeyedBinaryEncoder) validateNonce(nonce []byte) error {
	if len(nonce) != be.nonceSizeBytes {
		return status.Errorf(
			codes.InvalidArgument,
			"Decoding parameters are %d bytes in size, while the nonce was expected to be %d bytes in size",
			len(nonce),
			be.nonceSizeBytes,
		)
	}
	return nil
}

func (be *encryptingKeyedBinaryEncoder) EncodeBinary(in, nonce []byte) ([]byte, error) {
	if err := be.validateNonce(nonce); err != nil {
		return nil, err
	}
	if len(in) == 0 {
		return []byte{}, nil
	}

	paddedPlaintext := pad(in, be.overheadSizeBytes)
	return be.aead.Seal(paddedPlaintext[:0], nonce, paddedPlaintext, be.additionalData), nil
}

func (be *encryptingKeyedBinaryEncoder) DecodeBinary(in, nonce []byte) ([]byte, error) {
	if err := be.validateNonce(nonce); err != nil {
		return nil, err
	}
	if len(in) == 0 {
		return []byte{}, nil
	}

	plaintext, err := be.aead.Open(nil, nonce, in, be.additionalData)
	if err != nil {
		return nil, util.StatusWrapWithCode(err, codes.InvalidArgument, "Decryption failed")
	}
	return unpad(plaintext)
}

func (be *encryptingKeyedBinaryEncoder) GetDecodingParametersSizeBytes() int {
	return be.nonceSizeBytes
}
