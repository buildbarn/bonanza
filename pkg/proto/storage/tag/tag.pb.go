// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.36.5
// 	protoc        v5.29.3
// source: pkg/proto/storage/tag/tag.proto

package tag

import (
	object "github.com/buildbarn/bonanza/pkg/proto/storage/object"
	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	anypb "google.golang.org/protobuf/types/known/anypb"
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

type ResolveTagRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Namespace     *object.Namespace      `protobuf:"bytes,1,opt,name=namespace,proto3" json:"namespace,omitempty"`
	Tag           *anypb.Any             `protobuf:"bytes,2,opt,name=tag,proto3" json:"tag,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ResolveTagRequest) Reset() {
	*x = ResolveTagRequest{}
	mi := &file_pkg_proto_storage_tag_tag_proto_msgTypes[0]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ResolveTagRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ResolveTagRequest) ProtoMessage() {}

func (x *ResolveTagRequest) ProtoReflect() protoreflect.Message {
	mi := &file_pkg_proto_storage_tag_tag_proto_msgTypes[0]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ResolveTagRequest.ProtoReflect.Descriptor instead.
func (*ResolveTagRequest) Descriptor() ([]byte, []int) {
	return file_pkg_proto_storage_tag_tag_proto_rawDescGZIP(), []int{0}
}

func (x *ResolveTagRequest) GetNamespace() *object.Namespace {
	if x != nil {
		return x.Namespace
	}
	return nil
}

func (x *ResolveTagRequest) GetTag() *anypb.Any {
	if x != nil {
		return x.Tag
	}
	return nil
}

type ResolveTagResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Reference     []byte                 `protobuf:"bytes,1,opt,name=reference,proto3" json:"reference,omitempty"`
	Complete      bool                   `protobuf:"varint,2,opt,name=complete,proto3" json:"complete,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ResolveTagResponse) Reset() {
	*x = ResolveTagResponse{}
	mi := &file_pkg_proto_storage_tag_tag_proto_msgTypes[1]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ResolveTagResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ResolveTagResponse) ProtoMessage() {}

func (x *ResolveTagResponse) ProtoReflect() protoreflect.Message {
	mi := &file_pkg_proto_storage_tag_tag_proto_msgTypes[1]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ResolveTagResponse.ProtoReflect.Descriptor instead.
func (*ResolveTagResponse) Descriptor() ([]byte, []int) {
	return file_pkg_proto_storage_tag_tag_proto_rawDescGZIP(), []int{1}
}

func (x *ResolveTagResponse) GetReference() []byte {
	if x != nil {
		return x.Reference
	}
	return nil
}

func (x *ResolveTagResponse) GetComplete() bool {
	if x != nil {
		return x.Complete
	}
	return false
}

type UpdateTagRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Namespace     *object.Namespace      `protobuf:"bytes,1,opt,name=namespace,proto3" json:"namespace,omitempty"`
	Tag           *anypb.Any             `protobuf:"bytes,2,opt,name=tag,proto3" json:"tag,omitempty"`
	Reference     []byte                 `protobuf:"bytes,3,opt,name=reference,proto3" json:"reference,omitempty"`
	Lease         []byte                 `protobuf:"bytes,4,opt,name=lease,proto3" json:"lease,omitempty"`
	Overwrite     bool                   `protobuf:"varint,5,opt,name=overwrite,proto3" json:"overwrite,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *UpdateTagRequest) Reset() {
	*x = UpdateTagRequest{}
	mi := &file_pkg_proto_storage_tag_tag_proto_msgTypes[2]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *UpdateTagRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*UpdateTagRequest) ProtoMessage() {}

func (x *UpdateTagRequest) ProtoReflect() protoreflect.Message {
	mi := &file_pkg_proto_storage_tag_tag_proto_msgTypes[2]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use UpdateTagRequest.ProtoReflect.Descriptor instead.
func (*UpdateTagRequest) Descriptor() ([]byte, []int) {
	return file_pkg_proto_storage_tag_tag_proto_rawDescGZIP(), []int{2}
}

func (x *UpdateTagRequest) GetNamespace() *object.Namespace {
	if x != nil {
		return x.Namespace
	}
	return nil
}

func (x *UpdateTagRequest) GetTag() *anypb.Any {
	if x != nil {
		return x.Tag
	}
	return nil
}

func (x *UpdateTagRequest) GetReference() []byte {
	if x != nil {
		return x.Reference
	}
	return nil
}

func (x *UpdateTagRequest) GetLease() []byte {
	if x != nil {
		return x.Lease
	}
	return nil
}

func (x *UpdateTagRequest) GetOverwrite() bool {
	if x != nil {
		return x.Overwrite
	}
	return false
}

var File_pkg_proto_storage_tag_tag_proto protoreflect.FileDescriptor

var file_pkg_proto_storage_tag_tag_proto_rawDesc = string([]byte{
	0x0a, 0x1f, 0x70, 0x6b, 0x67, 0x2f, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x2f, 0x73, 0x74, 0x6f, 0x72,
	0x61, 0x67, 0x65, 0x2f, 0x74, 0x61, 0x67, 0x2f, 0x74, 0x61, 0x67, 0x2e, 0x70, 0x72, 0x6f, 0x74,
	0x6f, 0x12, 0x13, 0x62, 0x6f, 0x6e, 0x61, 0x6e, 0x7a, 0x61, 0x2e, 0x73, 0x74, 0x6f, 0x72, 0x61,
	0x67, 0x65, 0x2e, 0x74, 0x61, 0x67, 0x1a, 0x19, 0x67, 0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x2f, 0x70,
	0x72, 0x6f, 0x74, 0x6f, 0x62, 0x75, 0x66, 0x2f, 0x61, 0x6e, 0x79, 0x2e, 0x70, 0x72, 0x6f, 0x74,
	0x6f, 0x1a, 0x1b, 0x67, 0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x2f, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x62,
	0x75, 0x66, 0x2f, 0x65, 0x6d, 0x70, 0x74, 0x79, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x1a, 0x25,
	0x70, 0x6b, 0x67, 0x2f, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x2f, 0x73, 0x74, 0x6f, 0x72, 0x61, 0x67,
	0x65, 0x2f, 0x6f, 0x62, 0x6a, 0x65, 0x63, 0x74, 0x2f, 0x6f, 0x62, 0x6a, 0x65, 0x63, 0x74, 0x2e,
	0x70, 0x72, 0x6f, 0x74, 0x6f, 0x22, 0x7c, 0x0a, 0x11, 0x52, 0x65, 0x73, 0x6f, 0x6c, 0x76, 0x65,
	0x54, 0x61, 0x67, 0x52, 0x65, 0x71, 0x75, 0x65, 0x73, 0x74, 0x12, 0x3f, 0x0a, 0x09, 0x6e, 0x61,
	0x6d, 0x65, 0x73, 0x70, 0x61, 0x63, 0x65, 0x18, 0x01, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x21, 0x2e,
	0x62, 0x6f, 0x6e, 0x61, 0x6e, 0x7a, 0x61, 0x2e, 0x73, 0x74, 0x6f, 0x72, 0x61, 0x67, 0x65, 0x2e,
	0x6f, 0x62, 0x6a, 0x65, 0x63, 0x74, 0x2e, 0x4e, 0x61, 0x6d, 0x65, 0x73, 0x70, 0x61, 0x63, 0x65,
	0x52, 0x09, 0x6e, 0x61, 0x6d, 0x65, 0x73, 0x70, 0x61, 0x63, 0x65, 0x12, 0x26, 0x0a, 0x03, 0x74,
	0x61, 0x67, 0x18, 0x02, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x14, 0x2e, 0x67, 0x6f, 0x6f, 0x67, 0x6c,
	0x65, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x62, 0x75, 0x66, 0x2e, 0x41, 0x6e, 0x79, 0x52, 0x03,
	0x74, 0x61, 0x67, 0x22, 0x4e, 0x0a, 0x12, 0x52, 0x65, 0x73, 0x6f, 0x6c, 0x76, 0x65, 0x54, 0x61,
	0x67, 0x52, 0x65, 0x73, 0x70, 0x6f, 0x6e, 0x73, 0x65, 0x12, 0x1c, 0x0a, 0x09, 0x72, 0x65, 0x66,
	0x65, 0x72, 0x65, 0x6e, 0x63, 0x65, 0x18, 0x01, 0x20, 0x01, 0x28, 0x0c, 0x52, 0x09, 0x72, 0x65,
	0x66, 0x65, 0x72, 0x65, 0x6e, 0x63, 0x65, 0x12, 0x1a, 0x0a, 0x08, 0x63, 0x6f, 0x6d, 0x70, 0x6c,
	0x65, 0x74, 0x65, 0x18, 0x02, 0x20, 0x01, 0x28, 0x08, 0x52, 0x08, 0x63, 0x6f, 0x6d, 0x70, 0x6c,
	0x65, 0x74, 0x65, 0x22, 0xcd, 0x01, 0x0a, 0x10, 0x55, 0x70, 0x64, 0x61, 0x74, 0x65, 0x54, 0x61,
	0x67, 0x52, 0x65, 0x71, 0x75, 0x65, 0x73, 0x74, 0x12, 0x3f, 0x0a, 0x09, 0x6e, 0x61, 0x6d, 0x65,
	0x73, 0x70, 0x61, 0x63, 0x65, 0x18, 0x01, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x21, 0x2e, 0x62, 0x6f,
	0x6e, 0x61, 0x6e, 0x7a, 0x61, 0x2e, 0x73, 0x74, 0x6f, 0x72, 0x61, 0x67, 0x65, 0x2e, 0x6f, 0x62,
	0x6a, 0x65, 0x63, 0x74, 0x2e, 0x4e, 0x61, 0x6d, 0x65, 0x73, 0x70, 0x61, 0x63, 0x65, 0x52, 0x09,
	0x6e, 0x61, 0x6d, 0x65, 0x73, 0x70, 0x61, 0x63, 0x65, 0x12, 0x26, 0x0a, 0x03, 0x74, 0x61, 0x67,
	0x18, 0x02, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x14, 0x2e, 0x67, 0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x2e,
	0x70, 0x72, 0x6f, 0x74, 0x6f, 0x62, 0x75, 0x66, 0x2e, 0x41, 0x6e, 0x79, 0x52, 0x03, 0x74, 0x61,
	0x67, 0x12, 0x1c, 0x0a, 0x09, 0x72, 0x65, 0x66, 0x65, 0x72, 0x65, 0x6e, 0x63, 0x65, 0x18, 0x03,
	0x20, 0x01, 0x28, 0x0c, 0x52, 0x09, 0x72, 0x65, 0x66, 0x65, 0x72, 0x65, 0x6e, 0x63, 0x65, 0x12,
	0x14, 0x0a, 0x05, 0x6c, 0x65, 0x61, 0x73, 0x65, 0x18, 0x04, 0x20, 0x01, 0x28, 0x0c, 0x52, 0x05,
	0x6c, 0x65, 0x61, 0x73, 0x65, 0x12, 0x1c, 0x0a, 0x09, 0x6f, 0x76, 0x65, 0x72, 0x77, 0x72, 0x69,
	0x74, 0x65, 0x18, 0x05, 0x20, 0x01, 0x28, 0x08, 0x52, 0x09, 0x6f, 0x76, 0x65, 0x72, 0x77, 0x72,
	0x69, 0x74, 0x65, 0x32, 0x69, 0x0a, 0x08, 0x52, 0x65, 0x73, 0x6f, 0x6c, 0x76, 0x65, 0x72, 0x12,
	0x5d, 0x0a, 0x0a, 0x52, 0x65, 0x73, 0x6f, 0x6c, 0x76, 0x65, 0x54, 0x61, 0x67, 0x12, 0x26, 0x2e,
	0x62, 0x6f, 0x6e, 0x61, 0x6e, 0x7a, 0x61, 0x2e, 0x73, 0x74, 0x6f, 0x72, 0x61, 0x67, 0x65, 0x2e,
	0x74, 0x61, 0x67, 0x2e, 0x52, 0x65, 0x73, 0x6f, 0x6c, 0x76, 0x65, 0x54, 0x61, 0x67, 0x52, 0x65,
	0x71, 0x75, 0x65, 0x73, 0x74, 0x1a, 0x27, 0x2e, 0x62, 0x6f, 0x6e, 0x61, 0x6e, 0x7a, 0x61, 0x2e,
	0x73, 0x74, 0x6f, 0x72, 0x61, 0x67, 0x65, 0x2e, 0x74, 0x61, 0x67, 0x2e, 0x52, 0x65, 0x73, 0x6f,
	0x6c, 0x76, 0x65, 0x54, 0x61, 0x67, 0x52, 0x65, 0x73, 0x70, 0x6f, 0x6e, 0x73, 0x65, 0x32, 0x55,
	0x0a, 0x07, 0x55, 0x70, 0x64, 0x61, 0x74, 0x65, 0x72, 0x12, 0x4a, 0x0a, 0x09, 0x55, 0x70, 0x64,
	0x61, 0x74, 0x65, 0x54, 0x61, 0x67, 0x12, 0x25, 0x2e, 0x62, 0x6f, 0x6e, 0x61, 0x6e, 0x7a, 0x61,
	0x2e, 0x73, 0x74, 0x6f, 0x72, 0x61, 0x67, 0x65, 0x2e, 0x74, 0x61, 0x67, 0x2e, 0x55, 0x70, 0x64,
	0x61, 0x74, 0x65, 0x54, 0x61, 0x67, 0x52, 0x65, 0x71, 0x75, 0x65, 0x73, 0x74, 0x1a, 0x16, 0x2e,
	0x67, 0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x62, 0x75, 0x66, 0x2e,
	0x45, 0x6d, 0x70, 0x74, 0x79, 0x42, 0x34, 0x5a, 0x32, 0x67, 0x69, 0x74, 0x68, 0x75, 0x62, 0x2e,
	0x63, 0x6f, 0x6d, 0x2f, 0x62, 0x75, 0x69, 0x6c, 0x64, 0x62, 0x61, 0x72, 0x6e, 0x2f, 0x62, 0x6f,
	0x6e, 0x61, 0x6e, 0x7a, 0x61, 0x2f, 0x70, 0x6b, 0x67, 0x2f, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x2f,
	0x73, 0x74, 0x6f, 0x72, 0x61, 0x67, 0x65, 0x2f, 0x74, 0x61, 0x67, 0x62, 0x06, 0x70, 0x72, 0x6f,
	0x74, 0x6f, 0x33,
})

var (
	file_pkg_proto_storage_tag_tag_proto_rawDescOnce sync.Once
	file_pkg_proto_storage_tag_tag_proto_rawDescData []byte
)

func file_pkg_proto_storage_tag_tag_proto_rawDescGZIP() []byte {
	file_pkg_proto_storage_tag_tag_proto_rawDescOnce.Do(func() {
		file_pkg_proto_storage_tag_tag_proto_rawDescData = protoimpl.X.CompressGZIP(unsafe.Slice(unsafe.StringData(file_pkg_proto_storage_tag_tag_proto_rawDesc), len(file_pkg_proto_storage_tag_tag_proto_rawDesc)))
	})
	return file_pkg_proto_storage_tag_tag_proto_rawDescData
}

var file_pkg_proto_storage_tag_tag_proto_msgTypes = make([]protoimpl.MessageInfo, 3)
var file_pkg_proto_storage_tag_tag_proto_goTypes = []any{
	(*ResolveTagRequest)(nil),  // 0: bonanza.storage.tag.ResolveTagRequest
	(*ResolveTagResponse)(nil), // 1: bonanza.storage.tag.ResolveTagResponse
	(*UpdateTagRequest)(nil),   // 2: bonanza.storage.tag.UpdateTagRequest
	(*object.Namespace)(nil),   // 3: bonanza.storage.object.Namespace
	(*anypb.Any)(nil),          // 4: google.protobuf.Any
	(*emptypb.Empty)(nil),      // 5: google.protobuf.Empty
}
var file_pkg_proto_storage_tag_tag_proto_depIdxs = []int32{
	3, // 0: bonanza.storage.tag.ResolveTagRequest.namespace:type_name -> bonanza.storage.object.Namespace
	4, // 1: bonanza.storage.tag.ResolveTagRequest.tag:type_name -> google.protobuf.Any
	3, // 2: bonanza.storage.tag.UpdateTagRequest.namespace:type_name -> bonanza.storage.object.Namespace
	4, // 3: bonanza.storage.tag.UpdateTagRequest.tag:type_name -> google.protobuf.Any
	0, // 4: bonanza.storage.tag.Resolver.ResolveTag:input_type -> bonanza.storage.tag.ResolveTagRequest
	2, // 5: bonanza.storage.tag.Updater.UpdateTag:input_type -> bonanza.storage.tag.UpdateTagRequest
	1, // 6: bonanza.storage.tag.Resolver.ResolveTag:output_type -> bonanza.storage.tag.ResolveTagResponse
	5, // 7: bonanza.storage.tag.Updater.UpdateTag:output_type -> google.protobuf.Empty
	6, // [6:8] is the sub-list for method output_type
	4, // [4:6] is the sub-list for method input_type
	4, // [4:4] is the sub-list for extension type_name
	4, // [4:4] is the sub-list for extension extendee
	0, // [0:4] is the sub-list for field type_name
}

func init() { file_pkg_proto_storage_tag_tag_proto_init() }
func file_pkg_proto_storage_tag_tag_proto_init() {
	if File_pkg_proto_storage_tag_tag_proto != nil {
		return
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: unsafe.Slice(unsafe.StringData(file_pkg_proto_storage_tag_tag_proto_rawDesc), len(file_pkg_proto_storage_tag_tag_proto_rawDesc)),
			NumEnums:      0,
			NumMessages:   3,
			NumExtensions: 0,
			NumServices:   2,
		},
		GoTypes:           file_pkg_proto_storage_tag_tag_proto_goTypes,
		DependencyIndexes: file_pkg_proto_storage_tag_tag_proto_depIdxs,
		MessageInfos:      file_pkg_proto_storage_tag_tag_proto_msgTypes,
	}.Build()
	File_pkg_proto_storage_tag_tag_proto = out.File
	file_pkg_proto_storage_tag_tag_proto_goTypes = nil
	file_pkg_proto_storage_tag_tag_proto_depIdxs = nil
}
