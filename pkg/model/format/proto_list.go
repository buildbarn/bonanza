package format

import (
	"encoding"

	"bonanza.build/pkg/encoding/varint"
	model_format_pb "bonanza.build/pkg/proto/model/format"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

// ProtoListMarshaler is a Marshaler for payloads
// containing a list of Protobuf messages. Each message is prefixed with
// its size, encoded as a variable length integer.
//
// As this type only takes the pointer type as a type parameter, it can
// be used by generic code that does not have access to the message
// type itself, such as B-tree construction. T may even be
// proto.Message, allowing lists of messages of arbitrary types to be
// marshaled. Use ProtoList if unmarshaling is needed.
type ProtoListMarshaler[T proto.Message] struct{}

var _ Marshaler[[]proto.Message] = ProtoListMarshaler[proto.Message]{}

// NewMarshaler converts a list of Protobuf messages to a
// BinaryMarshaler.
func (ProtoListMarshaler[T]) NewMarshaler(value []T) encoding.BinaryMarshaler {
	return protoListBinaryMarshaler[T]{messages: value}
}

// ProtoList is a Format for payloads containing a
// list of Protobuf messages. Each message is prefixed with its size,
// encoded as a variable length integer.
type ProtoList[
	TMessage any,
	TMessagePtr interface {
		*TMessage
		proto.Message
	},
] struct {
	ProtoListMarshaler[TMessagePtr]
}

var _ Format[[]*emptypb.Empty] = ProtoList[emptypb.Empty, *emptypb.Empty]{}

// NewProtoList returns a ProtoList for a given
// Protobuf message type. Unlike ProtoList's composite
// literal, this function only requires the message type to be
// provided, as the pointer type can be inferred.
func NewProtoList[
	TMessage any,
	TMessagePtr interface {
		*TMessage
		proto.Message
	},
]() ProtoList[TMessage, TMessagePtr] {
	return ProtoList[TMessage, TMessagePtr]{}
}

// ToProto returns the Protobuf message describing the format.
func (ProtoList[TMessage, TMessagePtr]) ToProto() *model_format_pb.Format {
	return &model_format_pb.Format{
		Format: &model_format_pb.Format_ProtoListTypeName{
			ProtoListTypeName: string(TMessagePtr(nil).ProtoReflect().Descriptor().FullName()),
		},
	}
}

// Unmarshal a list of Protobuf messages.
func (ProtoList[TMessage, TMessagePtr]) Unmarshal(data []byte) ([]TMessagePtr, error) {
	originalDataLength := len(data)
	var elements []TMessagePtr
	for len(data) > 0 {
		// Extract the size of the element.
		offset := originalDataLength - len(data)
		length, lengthLength := varint.ConsumeForward[uint](data)
		if lengthLength < 0 {
			return nil, status.Errorf(codes.InvalidArgument, "Invalid element length at offset %d", offset)
		}

		// Validate the size.
		data = data[lengthLength:]
		if length > uint(len(data)) {
			return nil, status.Errorf(codes.InvalidArgument, "Length of element at offset %d is %d bytes, which exceeds maximum permitted size of %d bytes", offset, length, len(data))
		}

		// Unmarshal the element.
		var element TMessage
		if err := proto.Unmarshal(data[:length], TMessagePtr(&element)); err != nil {
			return nil, util.StatusWrapfWithCode(err, codes.InvalidArgument, "Failed to unmarshal element at offset %d", offset)
		}
		elements = append(elements, &element)
		data = data[length:]
	}
	return elements, nil
}

type protoListBinaryMarshaler[T proto.Message] struct {
	messages []T
}

func (mlm protoListBinaryMarshaler[T]) MarshalBinary() ([]byte, error) {
	var data []byte
	for _, message := range mlm.messages {
		data = varint.AppendForward(data, marshalOptions.Size(message))
		var err error
		data, err = marshalOptions.MarshalAppend(data, message)
		if err != nil {
			return nil, err
		}
	}
	return data, nil
}
