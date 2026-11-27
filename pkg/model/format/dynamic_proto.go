package format

import (
	"encoding"

	model_format_pb "bonanza.build/pkg/proto/model/format"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// DynamicProto is a Format for payloads containing
// a single Protobuf message, whose type is only known at runtime.
type DynamicProto struct {
	messageType protoreflect.MessageType
}

var _ Format[proto.Message] = DynamicProto{}

// NewDynamicProto creates a DynamicProto for
// a given Protobuf message type.
func NewDynamicProto(messageType protoreflect.MessageType) DynamicProto {
	return DynamicProto{messageType: messageType}
}

// NewDynamicProtoFromProto creates a DynamicProto from the Protobuf
// message describing the format. This requires that the format
// describes a single Protobuf message, whose type is registered.
func NewDynamicProtoFromProto(format *model_format_pb.Format) (DynamicProto, error) {
	if format.WhichFormat() != model_format_pb.Format_ProtoTypeName_case {
		return DynamicProto{}, status.Error(codes.InvalidArgument, "Format does not describe a single Protobuf message")
	}
	typeName := format.GetProtoTypeName()
	messageType, err := protoregistry.GlobalTypes.FindMessageByName(protoreflect.FullName(typeName))
	if err != nil {
		return DynamicProto{}, util.StatusWrapfWithCode(err, codes.InvalidArgument, "Unknown message type %#v", typeName)
	}
	return NewDynamicProto(messageType), nil
}

// ToProto returns the Protobuf message describing the format.
func (f DynamicProto) ToProto() *model_format_pb.Format {
	return model_format_pb.Format_builder{
		ProtoTypeName: proto.String(string(f.messageType.Descriptor().FullName())),
	}.Build()
}

// Unmarshal a Protobuf message.
func (f DynamicProto) Unmarshal(data []byte) (proto.Message, error) {
	m := f.messageType.New().Interface()
	if err := proto.Unmarshal(data, m); err != nil {
		return nil, util.StatusWrapWithCode(err, codes.InvalidArgument, "Failed to unmarshal message")
	}
	return m, nil
}

// NewMarshaler converts a Protobuf message to a BinaryMarshaler.
func (DynamicProto) NewMarshaler(value proto.Message) encoding.BinaryMarshaler {
	return protoBinaryMarshaler{message: value}
}
