package format

import (
	"encoding"

	model_format_pb "bonanza.build/pkg/proto/model/format"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

var marshalOptions = proto.MarshalOptions{
	Deterministic: true,
	UseCachedSize: true,
}

// ProtoMarshaler is a Marshaler for payloads containing a single
// Protobuf message.
//
// As this type only takes the pointer type as a type parameter, it can
// be used by generic code that does not have access to the message
// type itself. T may even be proto.Message, allowing messages of
// arbitrary types to be marshaled. Use Proto if unmarshaling is needed.
type ProtoMarshaler[T proto.Message] struct{}

var _ Marshaler[proto.Message] = ProtoMarshaler[proto.Message]{}

// NewMarshaler converts a Protobuf message to a BinaryMarshaler.
func (ProtoMarshaler[T]) NewMarshaler(value T) encoding.BinaryMarshaler {
	return protoBinaryMarshaler{message: value}
}

// Proto is a Format for payloads containing a
// single Protobuf message.
type Proto[
	TMessage any,
	TMessagePtr interface {
		*TMessage
		proto.Message
	},
] struct {
	ProtoMarshaler[TMessagePtr]
}

var _ Format[*emptypb.Empty] = Proto[emptypb.Empty, *emptypb.Empty]{}

// NewProto returns a Proto for a given
// Protobuf message type. Unlike Proto's composite literal,
// this function only requires the message type to be provided, as the
// pointer type can be inferred.
func NewProto[
	TMessage any,
	TMessagePtr interface {
		*TMessage
		proto.Message
	},
]() Proto[TMessage, TMessagePtr] {
	return Proto[TMessage, TMessagePtr]{}
}

// ToProto returns the Protobuf message describing the format.
func (Proto[TMessage, TMessagePtr]) ToProto() *model_format_pb.Format {
	return model_format_pb.Format_builder{
		ProtoTypeName: proto.String(string(TMessagePtr(nil).ProtoReflect().Descriptor().FullName())),
	}.Build()
}

// Unmarshal a Protobuf message.
func (Proto[TMessage, TMessagePtr]) Unmarshal(data []byte) (TMessagePtr, error) {
	var m TMessage
	if err := proto.Unmarshal(data, TMessagePtr(&m)); err != nil {
		return nil, util.StatusWrapWithCode(err, codes.InvalidArgument, "Failed to unmarshal message")
	}
	return &m, nil
}

type protoBinaryMarshaler struct {
	message proto.Message
}

func (mm protoBinaryMarshaler) MarshalBinary() ([]byte, error) {
	return marshalOptions.Marshal(mm.message)
}
