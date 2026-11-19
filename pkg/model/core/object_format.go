package core

import (
	model_format_pb "bonanza.build/pkg/proto/model/format"

	"google.golang.org/protobuf/proto"
)

// ObjectFormatToPath converts an object format to a set of pathname
// components. This corresponds to the paths accepted by tools like
// bonanza_browser.
func ObjectFormatToPath(objectFormat *model_format_pb.Format) ([]string, bool) {
	switch format := objectFormat.GetFormat().(type) {
	case *model_format_pb.Format_Raw:
		return []string{"raw"}, true
	case *model_format_pb.Format_ProtoTypeName:
		return []string{"proto", format.ProtoTypeName}, true
	case *model_format_pb.Format_ProtoListTypeName:
		return []string{"proto_list", format.ProtoListTypeName}, true
	default:
		return nil, false
	}
}

// NewProtoObjectFormat creates a new ObjectFormat message that
// corresponds to the type of the provided Protobuf message.
func NewProtoObjectFormat(m proto.Message) *model_format_pb.Format {
	return &model_format_pb.Format{
		Format: &model_format_pb.Format_ProtoTypeName{
			ProtoTypeName: string(m.ProtoReflect().Descriptor().FullName()),
		},
	}
}

// NewProtoListObjectFormat creates a new ObjectFormat message that
// corresponds to a list of the type of the provided Protobuf message.
func NewProtoListObjectFormat(m proto.Message) *model_format_pb.Format {
	return &model_format_pb.Format{
		Format: &model_format_pb.Format_ProtoListTypeName{
			ProtoListTypeName: string(m.ProtoReflect().Descriptor().FullName()),
		},
	}
}
