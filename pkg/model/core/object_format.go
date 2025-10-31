package core

import (
	model_core_pb "bonanza.build/pkg/proto/model/core"
)

func ObjectFormatToPath(objectFormat *model_core_pb.ObjectFormat, rawReference string) ([]string, bool) {
	switch format := objectFormat.GetFormat().(type) {
	case *model_core_pb.ObjectFormat_Raw:
		return []string{rawReference, "raw"}, true

	case *model_core_pb.ObjectFormat_ProtoTypeName:
		return []string{rawReference, "proto", format.ProtoTypeName}, true

	case *model_core_pb.ObjectFormat_ProtoListTypeName:
		return []string{rawReference, "proto_list", format.ProtoListTypeName}, true

	default:
		return nil, false
	}
}
