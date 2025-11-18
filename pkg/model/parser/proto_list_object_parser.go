package parser

import (
	"bonanza.build/pkg/encoding/varint"
	model_core "bonanza.build/pkg/model/core"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type protoListObjectParser[
	TReference any,
	TMessage any,
	TMessagePtr interface {
		*TMessage
		proto.Message
	},
] struct{}

// NewProtoListObjectParser is capable of unmarshaling objects
// containing a list of Protobuf messages. Messages are prefixed with
// their size, encoded as a variable length integer.
func NewProtoListObjectParser[
	TReference any,
	TMessage any,
	TMessagePtr interface {
		*TMessage
		proto.Message
	},
]() ObjectParser[TReference, model_core.Message[[]TMessagePtr, TReference]] {
	return &protoListObjectParser[TReference, TMessage, TMessagePtr]{}
}

func (protoListObjectParser[TReference, TMessage, TMessagePtr]) ParseObject(in model_core.Message[[]byte, TReference], decodingParameters []byte) (model_core.Message[[]TMessagePtr, TReference], error) {
	if len(decodingParameters) > 0 {
		return model_core.Message[[]TMessagePtr, TReference]{}, status.Error(codes.InvalidArgument, "Unexpected decoding parameters")
	}

	data := in.Message
	originalDataLength := len(data)
	var elements []TMessagePtr
	for len(data) > 0 {
		// Extract the size of the element.
		offset := originalDataLength - len(data)
		length, lengthLength := varint.ConsumeForward[uint](data)
		if lengthLength < 0 {
			return model_core.Message[[]TMessagePtr, TReference]{}, status.Errorf(codes.InvalidArgument, "Invalid element length at offset %d", offset)
		}

		// Validate the size.
		data = data[lengthLength:]
		if length > uint(len(data)) {
			return model_core.Message[[]TMessagePtr, TReference]{}, status.Errorf(codes.InvalidArgument, "Length of element at offset %d is %d bytes, which exceeds maximum permitted size of %d bytes", offset, length, len(data))
		}

		// Unmarshal the element.
		var element TMessage
		if err := proto.Unmarshal(data[:length], TMessagePtr(&element)); err != nil {
			return model_core.Message[[]TMessagePtr, TReference]{}, util.StatusWrapfWithCode(err, codes.InvalidArgument, "Failed to unmarshal element at offset %d", offset)
		}
		elements = append(elements, &element)
		data = data[length:]
	}

	return model_core.NewMessage(elements, in.OutgoingReferences), nil
}

func (protoListObjectParser[TReference, TMessage, TMessagePtr]) GetDecodingParametersSizeBytes() int {
	return 0
}
