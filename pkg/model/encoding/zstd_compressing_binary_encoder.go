package encoding

import (
	"unique"

	"bonanza.build/pkg/compress/canonicalzstd"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type zstdCompressingBinaryEncoder struct {
	maximumDecodedSizeBytes uint32
}

func (be *zstdCompressingBinaryEncoder) DecodeBinary(in, parameters []byte) ([]byte, error) {
	if len(parameters) > 0 {
		return nil, status.Error(codes.InvalidArgument, "Unexpected decoding parameters")
	}
	return canonicalzstd.Decompress(in, be.maximumDecodedSizeBytes)
}

type zstdCompressingBinaryEncoderKey struct {
	maximumDecodedSizeBytes uint32
}

func (be *zstdCompressingBinaryEncoder) AppendUniqueDecodingKeys(keys []unique.Handle[any]) []unique.Handle[any] {
	return append(keys, unique.Make[any](zstdCompressingBinaryEncoderKey{
		maximumDecodedSizeBytes: be.maximumDecodedSizeBytes,
	}))
}

// IsAutoDetecting returns false. Even though decompressing data that
// was not compressed is likely to fail, canonical Zstandard frames
// contain no checksum. Data that happens to be a valid frame is
// decompressed successfully.
func (zstdCompressingBinaryEncoder) IsAutoDetecting() bool {
	return false
}

func (zstdCompressingBinaryEncoder) GetDecodingParametersSizeBytes() int {
	return 0
}

type zstdCompressingDeterministicBinaryEncoder struct {
	zstdCompressingBinaryEncoder
}

// NewZstdCompressingDeterministicBinaryEncoder creates a
// DeterministicBinaryEncoder that encodes data by compressing it using
// canonical Zstandard.
func NewZstdCompressingDeterministicBinaryEncoder(maximumDecodedSizeBytes uint32) DeterministicBinaryEncoder {
	return &zstdCompressingDeterministicBinaryEncoder{
		zstdCompressingBinaryEncoder: zstdCompressingBinaryEncoder{
			maximumDecodedSizeBytes: maximumDecodedSizeBytes,
		},
	}
}

func (zstdCompressingDeterministicBinaryEncoder) EncodeBinary(in []byte) ([]byte, []byte, error) {
	return canonicalzstd.Compress(in), nil, nil
}

type zstdCompressingKeyedBinaryEncoder struct {
	zstdCompressingBinaryEncoder
}

// NewZstdCompressingKeyedBinaryEncoder creates a KeyedBinaryEncoder
// that encodes data by compressing it using canonical Zstandard.
func NewZstdCompressingKeyedBinaryEncoder(maximumDecodedSizeBytes uint32) KeyedBinaryEncoder {
	return &zstdCompressingKeyedBinaryEncoder{
		zstdCompressingBinaryEncoder: zstdCompressingBinaryEncoder{
			maximumDecodedSizeBytes: maximumDecodedSizeBytes,
		},
	}
}

func (zstdCompressingKeyedBinaryEncoder) EncodeBinary(in, parameters []byte) ([]byte, error) {
	if len(parameters) > 0 {
		return nil, status.Error(codes.InvalidArgument, "Unexpected decoding parameters")
	}
	return canonicalzstd.Compress(in), nil
}
