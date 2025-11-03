package encoding

import (
	"bonanza.build/pkg/compress/simplelzw"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type lzwCompressingBinaryEncoder struct {
	maximumDecodedSizeBytes uint32
}

// NewLZWCompressingBinaryEncoder creates a BinaryEncoder that encodes
// data by compressing data using the "simple LZW" algorithm.
func NewLZWCompressingBinaryEncoder(maximumDecodedSizeBytes uint32) BinaryEncoder {
	return &lzwCompressingBinaryEncoder{
		maximumDecodedSizeBytes: maximumDecodedSizeBytes,
	}
}

func (lzwCompressingBinaryEncoder) EncodeBinary(in []byte) ([]byte, []byte, error) {
	compressed, err := simplelzw.MaybeCompress(in)
	return compressed, nil, err
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
