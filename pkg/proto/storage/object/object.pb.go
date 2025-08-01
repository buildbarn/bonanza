// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.36.6
// 	protoc        v6.31.1
// source: pkg/proto/storage/object/object.proto

package object

import (
	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
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

type ReferenceFormat_Value int32

const (
	ReferenceFormat_UNKNOWN   ReferenceFormat_Value = 0
	ReferenceFormat_SHA256_V1 ReferenceFormat_Value = 1
)

// Enum value maps for ReferenceFormat_Value.
var (
	ReferenceFormat_Value_name = map[int32]string{
		0: "UNKNOWN",
		1: "SHA256_V1",
	}
	ReferenceFormat_Value_value = map[string]int32{
		"UNKNOWN":   0,
		"SHA256_V1": 1,
	}
)

func (x ReferenceFormat_Value) Enum() *ReferenceFormat_Value {
	p := new(ReferenceFormat_Value)
	*p = x
	return p
}

func (x ReferenceFormat_Value) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (ReferenceFormat_Value) Descriptor() protoreflect.EnumDescriptor {
	return file_pkg_proto_storage_object_object_proto_enumTypes[0].Descriptor()
}

func (ReferenceFormat_Value) Type() protoreflect.EnumType {
	return &file_pkg_proto_storage_object_object_proto_enumTypes[0]
}

func (x ReferenceFormat_Value) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use ReferenceFormat_Value.Descriptor instead.
func (ReferenceFormat_Value) EnumDescriptor() ([]byte, []int) {
	return file_pkg_proto_storage_object_object_proto_rawDescGZIP(), []int{0, 0}
}

type ReferenceFormat struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ReferenceFormat) Reset() {
	*x = ReferenceFormat{}
	mi := &file_pkg_proto_storage_object_object_proto_msgTypes[0]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ReferenceFormat) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ReferenceFormat) ProtoMessage() {}

func (x *ReferenceFormat) ProtoReflect() protoreflect.Message {
	mi := &file_pkg_proto_storage_object_object_proto_msgTypes[0]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ReferenceFormat.ProtoReflect.Descriptor instead.
func (*ReferenceFormat) Descriptor() ([]byte, []int) {
	return file_pkg_proto_storage_object_object_proto_rawDescGZIP(), []int{0}
}

type Namespace struct {
	state           protoimpl.MessageState `protogen:"open.v1"`
	InstanceName    string                 `protobuf:"bytes,1,opt,name=instance_name,json=instanceName,proto3" json:"instance_name,omitempty"`
	ReferenceFormat ReferenceFormat_Value  `protobuf:"varint,2,opt,name=reference_format,json=referenceFormat,proto3,enum=bonanza.storage.object.ReferenceFormat_Value" json:"reference_format,omitempty"`
	unknownFields   protoimpl.UnknownFields
	sizeCache       protoimpl.SizeCache
}

func (x *Namespace) Reset() {
	*x = Namespace{}
	mi := &file_pkg_proto_storage_object_object_proto_msgTypes[1]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Namespace) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Namespace) ProtoMessage() {}

func (x *Namespace) ProtoReflect() protoreflect.Message {
	mi := &file_pkg_proto_storage_object_object_proto_msgTypes[1]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Namespace.ProtoReflect.Descriptor instead.
func (*Namespace) Descriptor() ([]byte, []int) {
	return file_pkg_proto_storage_object_object_proto_rawDescGZIP(), []int{1}
}

func (x *Namespace) GetInstanceName() string {
	if x != nil {
		return x.InstanceName
	}
	return ""
}

func (x *Namespace) GetReferenceFormat() ReferenceFormat_Value {
	if x != nil {
		return x.ReferenceFormat
	}
	return ReferenceFormat_UNKNOWN
}

type DownloadObjectRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Namespace     *Namespace             `protobuf:"bytes,1,opt,name=namespace,proto3" json:"namespace,omitempty"`
	Reference     []byte                 `protobuf:"bytes,2,opt,name=reference,proto3" json:"reference,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *DownloadObjectRequest) Reset() {
	*x = DownloadObjectRequest{}
	mi := &file_pkg_proto_storage_object_object_proto_msgTypes[2]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *DownloadObjectRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*DownloadObjectRequest) ProtoMessage() {}

func (x *DownloadObjectRequest) ProtoReflect() protoreflect.Message {
	mi := &file_pkg_proto_storage_object_object_proto_msgTypes[2]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use DownloadObjectRequest.ProtoReflect.Descriptor instead.
func (*DownloadObjectRequest) Descriptor() ([]byte, []int) {
	return file_pkg_proto_storage_object_object_proto_rawDescGZIP(), []int{2}
}

func (x *DownloadObjectRequest) GetNamespace() *Namespace {
	if x != nil {
		return x.Namespace
	}
	return nil
}

func (x *DownloadObjectRequest) GetReference() []byte {
	if x != nil {
		return x.Reference
	}
	return nil
}

type DownloadObjectResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Contents      []byte                 `protobuf:"bytes,1,opt,name=contents,proto3" json:"contents,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *DownloadObjectResponse) Reset() {
	*x = DownloadObjectResponse{}
	mi := &file_pkg_proto_storage_object_object_proto_msgTypes[3]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *DownloadObjectResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*DownloadObjectResponse) ProtoMessage() {}

func (x *DownloadObjectResponse) ProtoReflect() protoreflect.Message {
	mi := &file_pkg_proto_storage_object_object_proto_msgTypes[3]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use DownloadObjectResponse.ProtoReflect.Descriptor instead.
func (*DownloadObjectResponse) Descriptor() ([]byte, []int) {
	return file_pkg_proto_storage_object_object_proto_rawDescGZIP(), []int{3}
}

func (x *DownloadObjectResponse) GetContents() []byte {
	if x != nil {
		return x.Contents
	}
	return nil
}

type UploadObjectRequest struct {
	state                    protoimpl.MessageState `protogen:"open.v1"`
	Namespace                *Namespace             `protobuf:"bytes,1,opt,name=namespace,proto3" json:"namespace,omitempty"`
	Reference                []byte                 `protobuf:"bytes,2,opt,name=reference,proto3" json:"reference,omitempty"`
	Contents                 []byte                 `protobuf:"bytes,3,opt,name=contents,proto3" json:"contents,omitempty"`
	OutgoingReferencesLeases [][]byte               `protobuf:"bytes,4,rep,name=outgoing_references_leases,json=outgoingReferencesLeases,proto3" json:"outgoing_references_leases,omitempty"`
	WantContentsIfIncomplete bool                   `protobuf:"varint,5,opt,name=want_contents_if_incomplete,json=wantContentsIfIncomplete,proto3" json:"want_contents_if_incomplete,omitempty"`
	unknownFields            protoimpl.UnknownFields
	sizeCache                protoimpl.SizeCache
}

func (x *UploadObjectRequest) Reset() {
	*x = UploadObjectRequest{}
	mi := &file_pkg_proto_storage_object_object_proto_msgTypes[4]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *UploadObjectRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*UploadObjectRequest) ProtoMessage() {}

func (x *UploadObjectRequest) ProtoReflect() protoreflect.Message {
	mi := &file_pkg_proto_storage_object_object_proto_msgTypes[4]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use UploadObjectRequest.ProtoReflect.Descriptor instead.
func (*UploadObjectRequest) Descriptor() ([]byte, []int) {
	return file_pkg_proto_storage_object_object_proto_rawDescGZIP(), []int{4}
}

func (x *UploadObjectRequest) GetNamespace() *Namespace {
	if x != nil {
		return x.Namespace
	}
	return nil
}

func (x *UploadObjectRequest) GetReference() []byte {
	if x != nil {
		return x.Reference
	}
	return nil
}

func (x *UploadObjectRequest) GetContents() []byte {
	if x != nil {
		return x.Contents
	}
	return nil
}

func (x *UploadObjectRequest) GetOutgoingReferencesLeases() [][]byte {
	if x != nil {
		return x.OutgoingReferencesLeases
	}
	return nil
}

func (x *UploadObjectRequest) GetWantContentsIfIncomplete() bool {
	if x != nil {
		return x.WantContentsIfIncomplete
	}
	return false
}

type UploadObjectResponse struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Types that are valid to be assigned to Type:
	//
	//	*UploadObjectResponse_Complete_
	//	*UploadObjectResponse_Incomplete_
	Type          isUploadObjectResponse_Type `protobuf_oneof:"type"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *UploadObjectResponse) Reset() {
	*x = UploadObjectResponse{}
	mi := &file_pkg_proto_storage_object_object_proto_msgTypes[5]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *UploadObjectResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*UploadObjectResponse) ProtoMessage() {}

func (x *UploadObjectResponse) ProtoReflect() protoreflect.Message {
	mi := &file_pkg_proto_storage_object_object_proto_msgTypes[5]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use UploadObjectResponse.ProtoReflect.Descriptor instead.
func (*UploadObjectResponse) Descriptor() ([]byte, []int) {
	return file_pkg_proto_storage_object_object_proto_rawDescGZIP(), []int{5}
}

func (x *UploadObjectResponse) GetType() isUploadObjectResponse_Type {
	if x != nil {
		return x.Type
	}
	return nil
}

func (x *UploadObjectResponse) GetComplete() *UploadObjectResponse_Complete {
	if x != nil {
		if x, ok := x.Type.(*UploadObjectResponse_Complete_); ok {
			return x.Complete
		}
	}
	return nil
}

func (x *UploadObjectResponse) GetIncomplete() *UploadObjectResponse_Incomplete {
	if x != nil {
		if x, ok := x.Type.(*UploadObjectResponse_Incomplete_); ok {
			return x.Incomplete
		}
	}
	return nil
}

type isUploadObjectResponse_Type interface {
	isUploadObjectResponse_Type()
}

type UploadObjectResponse_Complete_ struct {
	Complete *UploadObjectResponse_Complete `protobuf:"bytes,1,opt,name=complete,proto3,oneof"`
}

type UploadObjectResponse_Incomplete_ struct {
	Incomplete *UploadObjectResponse_Incomplete `protobuf:"bytes,2,opt,name=incomplete,proto3,oneof"`
}

func (*UploadObjectResponse_Complete_) isUploadObjectResponse_Type() {}

func (*UploadObjectResponse_Incomplete_) isUploadObjectResponse_Type() {}

type Limit struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Count         uint32                 `protobuf:"varint,1,opt,name=count,proto3" json:"count,omitempty"`
	SizeBytes     uint64                 `protobuf:"varint,2,opt,name=size_bytes,json=sizeBytes,proto3" json:"size_bytes,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *Limit) Reset() {
	*x = Limit{}
	mi := &file_pkg_proto_storage_object_object_proto_msgTypes[6]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Limit) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Limit) ProtoMessage() {}

func (x *Limit) ProtoReflect() protoreflect.Message {
	mi := &file_pkg_proto_storage_object_object_proto_msgTypes[6]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Limit.ProtoReflect.Descriptor instead.
func (*Limit) Descriptor() ([]byte, []int) {
	return file_pkg_proto_storage_object_object_proto_rawDescGZIP(), []int{6}
}

func (x *Limit) GetCount() uint32 {
	if x != nil {
		return x.Count
	}
	return 0
}

func (x *Limit) GetSizeBytes() uint64 {
	if x != nil {
		return x.SizeBytes
	}
	return 0
}

type UploadObjectResponse_Complete struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Lease         []byte                 `protobuf:"bytes,1,opt,name=lease,proto3" json:"lease,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *UploadObjectResponse_Complete) Reset() {
	*x = UploadObjectResponse_Complete{}
	mi := &file_pkg_proto_storage_object_object_proto_msgTypes[7]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *UploadObjectResponse_Complete) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*UploadObjectResponse_Complete) ProtoMessage() {}

func (x *UploadObjectResponse_Complete) ProtoReflect() protoreflect.Message {
	mi := &file_pkg_proto_storage_object_object_proto_msgTypes[7]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use UploadObjectResponse_Complete.ProtoReflect.Descriptor instead.
func (*UploadObjectResponse_Complete) Descriptor() ([]byte, []int) {
	return file_pkg_proto_storage_object_object_proto_rawDescGZIP(), []int{5, 0}
}

func (x *UploadObjectResponse_Complete) GetLease() []byte {
	if x != nil {
		return x.Lease
	}
	return nil
}

type UploadObjectResponse_Incomplete struct {
	state                        protoimpl.MessageState `protogen:"open.v1"`
	Contents                     []byte                 `protobuf:"bytes,1,opt,name=contents,proto3" json:"contents,omitempty"`
	WantOutgoingReferencesLeases []uint32               `protobuf:"varint,2,rep,packed,name=want_outgoing_references_leases,json=wantOutgoingReferencesLeases,proto3" json:"want_outgoing_references_leases,omitempty"`
	unknownFields                protoimpl.UnknownFields
	sizeCache                    protoimpl.SizeCache
}

func (x *UploadObjectResponse_Incomplete) Reset() {
	*x = UploadObjectResponse_Incomplete{}
	mi := &file_pkg_proto_storage_object_object_proto_msgTypes[8]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *UploadObjectResponse_Incomplete) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*UploadObjectResponse_Incomplete) ProtoMessage() {}

func (x *UploadObjectResponse_Incomplete) ProtoReflect() protoreflect.Message {
	mi := &file_pkg_proto_storage_object_object_proto_msgTypes[8]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use UploadObjectResponse_Incomplete.ProtoReflect.Descriptor instead.
func (*UploadObjectResponse_Incomplete) Descriptor() ([]byte, []int) {
	return file_pkg_proto_storage_object_object_proto_rawDescGZIP(), []int{5, 1}
}

func (x *UploadObjectResponse_Incomplete) GetContents() []byte {
	if x != nil {
		return x.Contents
	}
	return nil
}

func (x *UploadObjectResponse_Incomplete) GetWantOutgoingReferencesLeases() []uint32 {
	if x != nil {
		return x.WantOutgoingReferencesLeases
	}
	return nil
}

var File_pkg_proto_storage_object_object_proto protoreflect.FileDescriptor

const file_pkg_proto_storage_object_object_proto_rawDesc = "" +
	"\n" +
	"%pkg/proto/storage/object/object.proto\x12\x16bonanza.storage.object\"6\n" +
	"\x0fReferenceFormat\"#\n" +
	"\x05Value\x12\v\n" +
	"\aUNKNOWN\x10\x00\x12\r\n" +
	"\tSHA256_V1\x10\x01\"\x8a\x01\n" +
	"\tNamespace\x12#\n" +
	"\rinstance_name\x18\x01 \x01(\tR\finstanceName\x12X\n" +
	"\x10reference_format\x18\x02 \x01(\x0e2-.bonanza.storage.object.ReferenceFormat.ValueR\x0freferenceFormat\"v\n" +
	"\x15DownloadObjectRequest\x12?\n" +
	"\tnamespace\x18\x01 \x01(\v2!.bonanza.storage.object.NamespaceR\tnamespace\x12\x1c\n" +
	"\treference\x18\x02 \x01(\fR\treference\"4\n" +
	"\x16DownloadObjectResponse\x12\x1a\n" +
	"\bcontents\x18\x01 \x01(\fR\bcontents\"\x8d\x02\n" +
	"\x13UploadObjectRequest\x12?\n" +
	"\tnamespace\x18\x01 \x01(\v2!.bonanza.storage.object.NamespaceR\tnamespace\x12\x1c\n" +
	"\treference\x18\x02 \x01(\fR\treference\x12\x1a\n" +
	"\bcontents\x18\x03 \x01(\fR\bcontents\x12<\n" +
	"\x1aoutgoing_references_leases\x18\x04 \x03(\fR\x18outgoingReferencesLeases\x12=\n" +
	"\x1bwant_contents_if_incomplete\x18\x05 \x01(\bR\x18wantContentsIfIncomplete\"\xe1\x02\n" +
	"\x14UploadObjectResponse\x12S\n" +
	"\bcomplete\x18\x01 \x01(\v25.bonanza.storage.object.UploadObjectResponse.CompleteH\x00R\bcomplete\x12Y\n" +
	"\n" +
	"incomplete\x18\x02 \x01(\v27.bonanza.storage.object.UploadObjectResponse.IncompleteH\x00R\n" +
	"incomplete\x1a \n" +
	"\bComplete\x12\x14\n" +
	"\x05lease\x18\x01 \x01(\fR\x05lease\x1ao\n" +
	"\n" +
	"Incomplete\x12\x1a\n" +
	"\bcontents\x18\x01 \x01(\fR\bcontents\x12E\n" +
	"\x1fwant_outgoing_references_leases\x18\x02 \x03(\rR\x1cwantOutgoingReferencesLeasesB\x06\n" +
	"\x04type\"<\n" +
	"\x05Limit\x12\x14\n" +
	"\x05count\x18\x01 \x01(\rR\x05count\x12\x1d\n" +
	"\n" +
	"size_bytes\x18\x02 \x01(\x04R\tsizeBytes2}\n" +
	"\n" +
	"Downloader\x12o\n" +
	"\x0eDownloadObject\x12-.bonanza.storage.object.DownloadObjectRequest\x1a..bonanza.storage.object.DownloadObjectResponse2u\n" +
	"\bUploader\x12i\n" +
	"\fUploadObject\x12+.bonanza.storage.object.UploadObjectRequest\x1a,.bonanza.storage.object.UploadObjectResponseB(Z&bonanza.build/pkg/proto/storage/objectb\x06proto3"

var (
	file_pkg_proto_storage_object_object_proto_rawDescOnce sync.Once
	file_pkg_proto_storage_object_object_proto_rawDescData []byte
)

func file_pkg_proto_storage_object_object_proto_rawDescGZIP() []byte {
	file_pkg_proto_storage_object_object_proto_rawDescOnce.Do(func() {
		file_pkg_proto_storage_object_object_proto_rawDescData = protoimpl.X.CompressGZIP(unsafe.Slice(unsafe.StringData(file_pkg_proto_storage_object_object_proto_rawDesc), len(file_pkg_proto_storage_object_object_proto_rawDesc)))
	})
	return file_pkg_proto_storage_object_object_proto_rawDescData
}

var file_pkg_proto_storage_object_object_proto_enumTypes = make([]protoimpl.EnumInfo, 1)
var file_pkg_proto_storage_object_object_proto_msgTypes = make([]protoimpl.MessageInfo, 9)
var file_pkg_proto_storage_object_object_proto_goTypes = []any{
	(ReferenceFormat_Value)(0),              // 0: bonanza.storage.object.ReferenceFormat.Value
	(*ReferenceFormat)(nil),                 // 1: bonanza.storage.object.ReferenceFormat
	(*Namespace)(nil),                       // 2: bonanza.storage.object.Namespace
	(*DownloadObjectRequest)(nil),           // 3: bonanza.storage.object.DownloadObjectRequest
	(*DownloadObjectResponse)(nil),          // 4: bonanza.storage.object.DownloadObjectResponse
	(*UploadObjectRequest)(nil),             // 5: bonanza.storage.object.UploadObjectRequest
	(*UploadObjectResponse)(nil),            // 6: bonanza.storage.object.UploadObjectResponse
	(*Limit)(nil),                           // 7: bonanza.storage.object.Limit
	(*UploadObjectResponse_Complete)(nil),   // 8: bonanza.storage.object.UploadObjectResponse.Complete
	(*UploadObjectResponse_Incomplete)(nil), // 9: bonanza.storage.object.UploadObjectResponse.Incomplete
}
var file_pkg_proto_storage_object_object_proto_depIdxs = []int32{
	0, // 0: bonanza.storage.object.Namespace.reference_format:type_name -> bonanza.storage.object.ReferenceFormat.Value
	2, // 1: bonanza.storage.object.DownloadObjectRequest.namespace:type_name -> bonanza.storage.object.Namespace
	2, // 2: bonanza.storage.object.UploadObjectRequest.namespace:type_name -> bonanza.storage.object.Namespace
	8, // 3: bonanza.storage.object.UploadObjectResponse.complete:type_name -> bonanza.storage.object.UploadObjectResponse.Complete
	9, // 4: bonanza.storage.object.UploadObjectResponse.incomplete:type_name -> bonanza.storage.object.UploadObjectResponse.Incomplete
	3, // 5: bonanza.storage.object.Downloader.DownloadObject:input_type -> bonanza.storage.object.DownloadObjectRequest
	5, // 6: bonanza.storage.object.Uploader.UploadObject:input_type -> bonanza.storage.object.UploadObjectRequest
	4, // 7: bonanza.storage.object.Downloader.DownloadObject:output_type -> bonanza.storage.object.DownloadObjectResponse
	6, // 8: bonanza.storage.object.Uploader.UploadObject:output_type -> bonanza.storage.object.UploadObjectResponse
	7, // [7:9] is the sub-list for method output_type
	5, // [5:7] is the sub-list for method input_type
	5, // [5:5] is the sub-list for extension type_name
	5, // [5:5] is the sub-list for extension extendee
	0, // [0:5] is the sub-list for field type_name
}

func init() { file_pkg_proto_storage_object_object_proto_init() }
func file_pkg_proto_storage_object_object_proto_init() {
	if File_pkg_proto_storage_object_object_proto != nil {
		return
	}
	file_pkg_proto_storage_object_object_proto_msgTypes[5].OneofWrappers = []any{
		(*UploadObjectResponse_Complete_)(nil),
		(*UploadObjectResponse_Incomplete_)(nil),
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: unsafe.Slice(unsafe.StringData(file_pkg_proto_storage_object_object_proto_rawDesc), len(file_pkg_proto_storage_object_object_proto_rawDesc)),
			NumEnums:      1,
			NumMessages:   9,
			NumExtensions: 0,
			NumServices:   2,
		},
		GoTypes:           file_pkg_proto_storage_object_object_proto_goTypes,
		DependencyIndexes: file_pkg_proto_storage_object_object_proto_depIdxs,
		EnumInfos:         file_pkg_proto_storage_object_object_proto_enumTypes,
		MessageInfos:      file_pkg_proto_storage_object_object_proto_msgTypes,
	}.Build()
	File_pkg_proto_storage_object_object_proto = out.File
	file_pkg_proto_storage_object_object_proto_goTypes = nil
	file_pkg_proto_storage_object_object_proto_depIdxs = nil
}
