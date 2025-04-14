// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.36.5
// 	protoc        v5.29.3
// source: pkg/proto/model/core/core.proto

package core

import (
	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	descriptorpb "google.golang.org/protobuf/types/descriptorpb"
	emptypb "google.golang.org/protobuf/types/known/emptypb"
	reflect "reflect"
	sync "sync"
	unsafe "unsafe"
)

const (
	// Verify that this generated code is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(20 - protoimpl.MinVersion)
	// Verify that runtime/protoimpl is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(protoimpl.MaxVersion - 20)
)

type ObjectFormat struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Types that are valid to be assigned to Format:
	//
	//	*ObjectFormat_Raw
	//	*ObjectFormat_MessageTypeName
	//	*ObjectFormat_MessageListTypeName
	Format        isObjectFormat_Format `protobuf_oneof:"format"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ObjectFormat) Reset() {
	*x = ObjectFormat{}
	mi := &file_pkg_proto_model_core_core_proto_msgTypes[0]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ObjectFormat) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ObjectFormat) ProtoMessage() {}

func (x *ObjectFormat) ProtoReflect() protoreflect.Message {
	mi := &file_pkg_proto_model_core_core_proto_msgTypes[0]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ObjectFormat.ProtoReflect.Descriptor instead.
func (*ObjectFormat) Descriptor() ([]byte, []int) {
	return file_pkg_proto_model_core_core_proto_rawDescGZIP(), []int{0}
}

func (x *ObjectFormat) GetFormat() isObjectFormat_Format {
	if x != nil {
		return x.Format
	}
	return nil
}

func (x *ObjectFormat) GetRaw() *emptypb.Empty {
	if x != nil {
		if x, ok := x.Format.(*ObjectFormat_Raw); ok {
			return x.Raw
		}
	}
	return nil
}

func (x *ObjectFormat) GetMessageTypeName() string {
	if x != nil {
		if x, ok := x.Format.(*ObjectFormat_MessageTypeName); ok {
			return x.MessageTypeName
		}
	}
	return ""
}

func (x *ObjectFormat) GetMessageListTypeName() string {
	if x != nil {
		if x, ok := x.Format.(*ObjectFormat_MessageListTypeName); ok {
			return x.MessageListTypeName
		}
	}
	return ""
}

type isObjectFormat_Format interface {
	isObjectFormat_Format()
}

type ObjectFormat_Raw struct {
	Raw *emptypb.Empty `protobuf:"bytes,1,opt,name=raw,proto3,oneof"`
}

type ObjectFormat_MessageTypeName struct {
	MessageTypeName string `protobuf:"bytes,2,opt,name=message_type_name,json=messageTypeName,proto3,oneof"`
}

type ObjectFormat_MessageListTypeName struct {
	MessageListTypeName string `protobuf:"bytes,3,opt,name=message_list_type_name,json=messageListTypeName,proto3,oneof"`
}

func (*ObjectFormat_Raw) isObjectFormat_Format() {}

func (*ObjectFormat_MessageTypeName) isObjectFormat_Format() {}

func (*ObjectFormat_MessageListTypeName) isObjectFormat_Format() {}

type Reference struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Index         uint32                 `protobuf:"fixed32,1,opt,name=index,proto3" json:"index,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Reference) Reset() {
	*x = Reference{}
	mi := &file_pkg_proto_model_core_core_proto_msgTypes[1]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Reference) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Reference) ProtoMessage() {}

func (x *Reference) ProtoReflect() protoreflect.Message {
	mi := &file_pkg_proto_model_core_core_proto_msgTypes[1]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Reference.ProtoReflect.Descriptor instead.
func (*Reference) Descriptor() ([]byte, []int) {
	return file_pkg_proto_model_core_core_proto_rawDescGZIP(), []int{1}
}

func (x *Reference) GetIndex() uint32 {
	if x != nil {
		return x.Index
	}
	return 0
}

var file_pkg_proto_model_core_core_proto_extTypes = []protoimpl.ExtensionInfo{
	{
		ExtendedType:  (*descriptorpb.FieldOptions)(nil),
		ExtensionType: (*ObjectFormat)(nil),
		Field:         66941,
		Name:          "bonanza.model.core.object_format",
		Tag:           "bytes,66941,opt,name=object_format",
		Filename:      "pkg/proto/model/core/core.proto",
	},
}

// Extension fields to descriptorpb.FieldOptions.
var (
	// optional bonanza.model.core.ObjectFormat object_format = 66941;
	E_ObjectFormat = &file_pkg_proto_model_core_core_proto_extTypes[0]
)

var File_pkg_proto_model_core_core_proto protoreflect.FileDescriptor

var file_pkg_proto_model_core_core_proto_rawDesc = string([]byte{
	0x0a, 0x1f, 0x70, 0x6b, 0x67, 0x2f, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x2f, 0x6d, 0x6f, 0x64, 0x65,
	0x6c, 0x2f, 0x63, 0x6f, 0x72, 0x65, 0x2f, 0x63, 0x6f, 0x72, 0x65, 0x2e, 0x70, 0x72, 0x6f, 0x74,
	0x6f, 0x12, 0x12, 0x62, 0x6f, 0x6e, 0x61, 0x6e, 0x7a, 0x61, 0x2e, 0x6d, 0x6f, 0x64, 0x65, 0x6c,
	0x2e, 0x63, 0x6f, 0x72, 0x65, 0x1a, 0x20, 0x67, 0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x2f, 0x70, 0x72,
	0x6f, 0x74, 0x6f, 0x62, 0x75, 0x66, 0x2f, 0x64, 0x65, 0x73, 0x63, 0x72, 0x69, 0x70, 0x74, 0x6f,
	0x72, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x1a, 0x1b, 0x67, 0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x2f,
	0x70, 0x72, 0x6f, 0x74, 0x6f, 0x62, 0x75, 0x66, 0x2f, 0x65, 0x6d, 0x70, 0x74, 0x79, 0x2e, 0x70,
	0x72, 0x6f, 0x74, 0x6f, 0x22, 0xa9, 0x01, 0x0a, 0x0c, 0x4f, 0x62, 0x6a, 0x65, 0x63, 0x74, 0x46,
	0x6f, 0x72, 0x6d, 0x61, 0x74, 0x12, 0x2a, 0x0a, 0x03, 0x72, 0x61, 0x77, 0x18, 0x01, 0x20, 0x01,
	0x28, 0x0b, 0x32, 0x16, 0x2e, 0x67, 0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x2e, 0x70, 0x72, 0x6f, 0x74,
	0x6f, 0x62, 0x75, 0x66, 0x2e, 0x45, 0x6d, 0x70, 0x74, 0x79, 0x48, 0x00, 0x52, 0x03, 0x72, 0x61,
	0x77, 0x12, 0x2c, 0x0a, 0x11, 0x6d, 0x65, 0x73, 0x73, 0x61, 0x67, 0x65, 0x5f, 0x74, 0x79, 0x70,
	0x65, 0x5f, 0x6e, 0x61, 0x6d, 0x65, 0x18, 0x02, 0x20, 0x01, 0x28, 0x09, 0x48, 0x00, 0x52, 0x0f,
	0x6d, 0x65, 0x73, 0x73, 0x61, 0x67, 0x65, 0x54, 0x79, 0x70, 0x65, 0x4e, 0x61, 0x6d, 0x65, 0x12,
	0x35, 0x0a, 0x16, 0x6d, 0x65, 0x73, 0x73, 0x61, 0x67, 0x65, 0x5f, 0x6c, 0x69, 0x73, 0x74, 0x5f,
	0x74, 0x79, 0x70, 0x65, 0x5f, 0x6e, 0x61, 0x6d, 0x65, 0x18, 0x03, 0x20, 0x01, 0x28, 0x09, 0x48,
	0x00, 0x52, 0x13, 0x6d, 0x65, 0x73, 0x73, 0x61, 0x67, 0x65, 0x4c, 0x69, 0x73, 0x74, 0x54, 0x79,
	0x70, 0x65, 0x4e, 0x61, 0x6d, 0x65, 0x42, 0x08, 0x0a, 0x06, 0x66, 0x6f, 0x72, 0x6d, 0x61, 0x74,
	0x22, 0x21, 0x0a, 0x09, 0x52, 0x65, 0x66, 0x65, 0x72, 0x65, 0x6e, 0x63, 0x65, 0x12, 0x14, 0x0a,
	0x05, 0x69, 0x6e, 0x64, 0x65, 0x78, 0x18, 0x01, 0x20, 0x01, 0x28, 0x07, 0x52, 0x05, 0x69, 0x6e,
	0x64, 0x65, 0x78, 0x3a, 0x66, 0x0a, 0x0d, 0x6f, 0x62, 0x6a, 0x65, 0x63, 0x74, 0x5f, 0x66, 0x6f,
	0x72, 0x6d, 0x61, 0x74, 0x12, 0x1d, 0x2e, 0x67, 0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x2e, 0x70, 0x72,
	0x6f, 0x74, 0x6f, 0x62, 0x75, 0x66, 0x2e, 0x46, 0x69, 0x65, 0x6c, 0x64, 0x4f, 0x70, 0x74, 0x69,
	0x6f, 0x6e, 0x73, 0x18, 0xfd, 0x8a, 0x04, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x20, 0x2e, 0x62, 0x6f,
	0x6e, 0x61, 0x6e, 0x7a, 0x61, 0x2e, 0x6d, 0x6f, 0x64, 0x65, 0x6c, 0x2e, 0x63, 0x6f, 0x72, 0x65,
	0x2e, 0x4f, 0x62, 0x6a, 0x65, 0x63, 0x74, 0x46, 0x6f, 0x72, 0x6d, 0x61, 0x74, 0x52, 0x0c, 0x6f,
	0x62, 0x6a, 0x65, 0x63, 0x74, 0x46, 0x6f, 0x72, 0x6d, 0x61, 0x74, 0x42, 0x33, 0x5a, 0x31, 0x67,
	0x69, 0x74, 0x68, 0x75, 0x62, 0x2e, 0x63, 0x6f, 0x6d, 0x2f, 0x62, 0x75, 0x69, 0x6c, 0x64, 0x62,
	0x61, 0x72, 0x6e, 0x2f, 0x62, 0x6f, 0x6e, 0x61, 0x6e, 0x7a, 0x61, 0x2f, 0x70, 0x6b, 0x67, 0x2f,
	0x70, 0x72, 0x6f, 0x74, 0x6f, 0x2f, 0x6d, 0x6f, 0x64, 0x65, 0x6c, 0x2f, 0x63, 0x6f, 0x72, 0x65,
	0x62, 0x06, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x33,
})

var (
	file_pkg_proto_model_core_core_proto_rawDescOnce sync.Once
	file_pkg_proto_model_core_core_proto_rawDescData []byte
)

func file_pkg_proto_model_core_core_proto_rawDescGZIP() []byte {
	file_pkg_proto_model_core_core_proto_rawDescOnce.Do(func() {
		file_pkg_proto_model_core_core_proto_rawDescData = protoimpl.X.CompressGZIP(unsafe.Slice(unsafe.StringData(file_pkg_proto_model_core_core_proto_rawDesc), len(file_pkg_proto_model_core_core_proto_rawDesc)))
	})
	return file_pkg_proto_model_core_core_proto_rawDescData
}

var file_pkg_proto_model_core_core_proto_msgTypes = make([]protoimpl.MessageInfo, 2)
var file_pkg_proto_model_core_core_proto_goTypes = []any{
	(*ObjectFormat)(nil),              // 0: bonanza.model.core.ObjectFormat
	(*Reference)(nil),                 // 1: bonanza.model.core.Reference
	(*emptypb.Empty)(nil),             // 2: google.protobuf.Empty
	(*descriptorpb.FieldOptions)(nil), // 3: google.protobuf.FieldOptions
}
var file_pkg_proto_model_core_core_proto_depIdxs = []int32{
	2, // 0: bonanza.model.core.ObjectFormat.raw:type_name -> google.protobuf.Empty
	3, // 1: bonanza.model.core.object_format:extendee -> google.protobuf.FieldOptions
	0, // 2: bonanza.model.core.object_format:type_name -> bonanza.model.core.ObjectFormat
	3, // [3:3] is the sub-list for method output_type
	3, // [3:3] is the sub-list for method input_type
	2, // [2:3] is the sub-list for extension type_name
	1, // [1:2] is the sub-list for extension extendee
	0, // [0:1] is the sub-list for field type_name
}

func init() { file_pkg_proto_model_core_core_proto_init() }
func file_pkg_proto_model_core_core_proto_init() {
	if File_pkg_proto_model_core_core_proto != nil {
		return
	}
	file_pkg_proto_model_core_core_proto_msgTypes[0].OneofWrappers = []any{
		(*ObjectFormat_Raw)(nil),
		(*ObjectFormat_MessageTypeName)(nil),
		(*ObjectFormat_MessageListTypeName)(nil),
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: unsafe.Slice(unsafe.StringData(file_pkg_proto_model_core_core_proto_rawDesc), len(file_pkg_proto_model_core_core_proto_rawDesc)),
			NumEnums:      0,
			NumMessages:   2,
			NumExtensions: 1,
			NumServices:   0,
		},
		GoTypes:           file_pkg_proto_model_core_core_proto_goTypes,
		DependencyIndexes: file_pkg_proto_model_core_core_proto_depIdxs,
		MessageInfos:      file_pkg_proto_model_core_core_proto_msgTypes,
		ExtensionInfos:    file_pkg_proto_model_core_core_proto_extTypes,
	}.Build()
	File_pkg_proto_model_core_core_proto = out.File
	file_pkg_proto_model_core_core_proto_goTypes = nil
	file_pkg_proto_model_core_core_proto_depIdxs = nil
}
