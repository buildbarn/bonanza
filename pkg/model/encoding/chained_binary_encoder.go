package encoding

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type chainedBinaryEncoder[TBinaryEncoder BinaryDecoder] struct {
	encoders []TBinaryEncoder
}

func (be *chainedBinaryEncoder[TBinaryEncoder]) DecodeBinary(in, parameters []byte) ([]byte, error) {
	// Invoke decoders the other way around.
	for i := len(be.encoders); i > 0; i-- {
		var err error
		in, err = be.encoders[i-1].DecodeBinary(in, parameters)
		if err != nil {
			return nil, err
		}
		parameters = nil
	}
	if len(parameters) > 0 {
		return nil, status.Error(codes.InvalidArgument, "Unexpected decoding parameters")
	}
	return in, nil
}

func (be *chainedBinaryEncoder[TBinaryEncoder]) GetDecodingParametersSizeBytes() int {
	if len(be.encoders) == 0 {
		return 0
	}
	return be.encoders[0].GetDecodingParametersSizeBytes()
}

type chainedDeterministicBinaryEncoder struct {
	chainedBinaryEncoder[DeterministicBinaryEncoder]
}

// NewChainedDeterministicBinaryEncoder creates a
// DeterministicBinaryEncoder that is capable of applying multiple
// encoding/decoding steps. It can be used to, for example, apply both
// compression and encryption.
func NewChainedDeterministicBinaryEncoder(encoders []DeterministicBinaryEncoder) DeterministicBinaryEncoder {
	if len(encoders) == 1 {
		return encoders[0]
	}
	return &chainedDeterministicBinaryEncoder{
		chainedBinaryEncoder: chainedBinaryEncoder[DeterministicBinaryEncoder]{
			encoders: encoders,
		},
	}
}

func (be *chainedDeterministicBinaryEncoder) EncodeBinary(in []byte) ([]byte, []byte, error) {
	// Invoke encoders in forward order.
	var parameters []byte
	for _, encoder := range be.encoders {
		if len(parameters) > 0 {
			return nil, nil, status.Error(codes.InvalidArgument, "Binary encoders that yield decoding parameters must be the last in the chain")
		}
		var err error
		in, parameters, err = encoder.EncodeBinary(in)
		if err != nil {
			return nil, nil, err
		}
	}
	return in, parameters, nil
}

type chainedKeyedBinaryEncoder struct {
	chainedBinaryEncoder[KeyedBinaryEncoder]
}

// NewChainedKeyedBinaryEncoder creates a KeyedBinaryEncoder that is
// capable of applying multiple encoding/decoding steps. It can be used
// to, for example, apply both compression and encryption.
func NewChainedKeyedBinaryEncoder(encoders []KeyedBinaryEncoder) KeyedBinaryEncoder {
	if len(encoders) == 1 {
		return encoders[0]
	}
	return &chainedKeyedBinaryEncoder{
		chainedBinaryEncoder: chainedBinaryEncoder[KeyedBinaryEncoder]{
			encoders: encoders,
		},
	}
}

func (be *chainedKeyedBinaryEncoder) EncodeBinary(in, parameters []byte) ([]byte, error) {
	// Invoke encoders in forward order, providing the decoding
	// parameters to the last encoding step.
	for _, encoder := range be.encoders[:len(be.encoders)-1] {
		var err error
		in, err = encoder.EncodeBinary(in, nil)
		if err != nil {
			return nil, err
		}
	}
	return be.encoders[len(be.encoders)-1].EncodeBinary(in, parameters)
}
