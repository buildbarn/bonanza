package encoding

import (
	"bonanza.build/pkg/compress/simplelzw"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type lzwCompressingBinaryEncoder struct {
	maximumDecodedSizeBytes uint32
}

func (be *lzwCompressingBinaryEncoder) DecodeBinary(in, parameters []byte) ([]byte, error) {
	if len(parameters) > 0 {
		return nil, status.Error(codes.InvalidArgument, "Unexpected decoding parameters")
	}
	return simplelzw.Decompress(in, be.maximumDecodedSizeBytes)
}

func (lzwCompressingBinaryEncoder) GetDecodingParametersSizeBytes() int {
	return 0
}

type lzwCompressingDeterministicBinaryEncoder struct {
	lzwCompressingBinaryEncoder
}

// NewLZWCompressingDeterministicBinaryEncoder creates a
// DeterministicBinaryEncoder that encodes data by compressing data
// using the "simple LZW" algorithm.
func NewLZWCompressingDeterministicBinaryEncoder(maximumDecodedSizeBytes uint32) DeterministicBinaryEncoder {
	return &lzwCompressingDeterministicBinaryEncoder{
		lzwCompressingBinaryEncoder: lzwCompressingBinaryEncoder{
			maximumDecodedSizeBytes: maximumDecodedSizeBytes,
		},
	}
}

func (lzwCompressingDeterministicBinaryEncoder) EncodeBinary(in []byte) ([]byte, []byte, error) {
	compressed, err := simplelzw.MaybeCompress(in)
	return compressed, nil, err
}
