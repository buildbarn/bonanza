package core

import (
	model_core_pb "bonanza.build/pkg/proto/model/core"

	"google.golang.org/protobuf/proto"
)

// ObjectFormatToPath converts an object format to a set of pathname
// components. This corresponds to the paths accepted by tools like
// bonanza_browser.
func ObjectFormatToPath(objectFormat *model_core_pb.ObjectFormat) ([]string, bool) {
	switch format := objectFormat.GetFormat().(type) {
	case *model_core_pb.ObjectFormat_Raw:
		return []string{"raw"}, true
	case *model_core_pb.ObjectFormat_ProtoTypeName:
		return []string{"proto", format.ProtoTypeName}, true
	case *model_core_pb.ObjectFormat_ProtoListTypeName:
		return []string{"proto_list", format.ProtoListTypeName}, true
	default:
		return nil, false
	}
}

// NewProtoObjectFormat creates a new ObjectFormat message that
// corresponds to the type of the provided Protobuf message.
func NewProtoObjectFormat(m proto.Message) *model_core_pb.ObjectFormat {
	return &model_core_pb.ObjectFormat{
		Format: &model_core_pb.ObjectFormat_ProtoTypeName{
			ProtoTypeName: string(m.ProtoReflect().Descriptor().FullName()),
		},
	}
}

// NewProtoListObjectFormat creates a new ObjectFormat message that
// corresponds to a list of the type of the provided Protobuf message.
func NewProtoListObjectFormat(m proto.Message) *model_core_pb.ObjectFormat {
	return &model_core_pb.ObjectFormat{
		Format: &model_core_pb.ObjectFormat_ProtoListTypeName{
			ProtoListTypeName: string(m.ProtoReflect().Descriptor().FullName()),
		},
	}
}
