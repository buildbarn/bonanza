package format

import (
	"encoding"

	model_format_pb "bonanza.build/pkg/proto/model/format"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
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

// ToProto returns the Protobuf message describing the format.
func (f DynamicProto) ToProto() *model_format_pb.Format {
	return &model_format_pb.Format{
		Format: &model_format_pb.Format_ProtoTypeName{
			ProtoTypeName: string(f.messageType.Descriptor().FullName()),
		},
	}
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
