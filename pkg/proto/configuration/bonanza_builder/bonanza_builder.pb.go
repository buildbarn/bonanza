// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.36.6
// 	protoc        v6.31.1
// source: pkg/proto/configuration/bonanza_builder/bonanza_builder.proto

package bonanza_builder

import (
	parser "bonanza.build/pkg/proto/configuration/model/parser"
	filesystem "github.com/buildbarn/bb-remote-execution/pkg/proto/configuration/filesystem"
	global "github.com/buildbarn/bb-storage/pkg/proto/configuration/global"
	grpc "github.com/buildbarn/bb-storage/pkg/proto/configuration/grpc"
	x509 "github.com/buildbarn/bb-storage/pkg/proto/configuration/x509"
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

type ApplicationConfiguration struct {
	state                           protoimpl.MessageState                       `protogen:"open.v1"`
	Global                          *global.Configuration                        `protobuf:"bytes,1,opt,name=global,proto3" json:"global,omitempty"`
	StorageGrpcClient               *grpc.ClientConfiguration                    `protobuf:"bytes,3,opt,name=storage_grpc_client,json=storageGrpcClient,proto3" json:"storage_grpc_client,omitempty"`
	FilePool                        *filesystem.FilePoolConfiguration            `protobuf:"bytes,5,opt,name=file_pool,json=filePool,proto3" json:"file_pool,omitempty"`
	ExecutionGrpcClient             *grpc.ClientConfiguration                    `protobuf:"bytes,7,opt,name=execution_grpc_client,json=executionGrpcClient,proto3" json:"execution_grpc_client,omitempty"`
	ExecutionClientPrivateKey       string                                       `protobuf:"bytes,8,opt,name=execution_client_private_key,json=executionClientPrivateKey,proto3" json:"execution_client_private_key,omitempty"`
	ExecutionClientCertificateChain string                                       `protobuf:"bytes,9,opt,name=execution_client_certificate_chain,json=executionClientCertificateChain,proto3" json:"execution_client_certificate_chain,omitempty"`
	RemoteWorkerGrpcClient          *grpc.ClientConfiguration                    `protobuf:"bytes,10,opt,name=remote_worker_grpc_client,json=remoteWorkerGrpcClient,proto3" json:"remote_worker_grpc_client,omitempty"`
	PlatformPrivateKeys             []string                                     `protobuf:"bytes,11,rep,name=platform_private_keys,json=platformPrivateKeys,proto3" json:"platform_private_keys,omitempty"`
	ClientCertificateVerifier       *x509.ClientCertificateVerifierConfiguration `protobuf:"bytes,12,opt,name=client_certificate_verifier,json=clientCertificateVerifier,proto3" json:"client_certificate_verifier,omitempty"`
	WorkerId                        map[string]string                            `protobuf:"bytes,13,rep,name=worker_id,json=workerId,proto3" json:"worker_id,omitempty" protobuf_key:"bytes,1,opt,name=key" protobuf_val:"bytes,2,opt,name=value"`
	ParsedObjectPool                *parser.ParsedObjectPool                     `protobuf:"bytes,14,opt,name=parsed_object_pool,json=parsedObjectPool,proto3" json:"parsed_object_pool,omitempty"`
	unknownFields                   protoimpl.UnknownFields
	sizeCache                       protoimpl.SizeCache
}

func (x *ApplicationConfiguration) Reset() {
	*x = ApplicationConfiguration{}
	mi := &file_pkg_proto_configuration_bonanza_builder_bonanza_builder_proto_msgTypes[0]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ApplicationConfiguration) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ApplicationConfiguration) ProtoMessage() {}

func (x *ApplicationConfiguration) ProtoReflect() protoreflect.Message {
	mi := &file_pkg_proto_configuration_bonanza_builder_bonanza_builder_proto_msgTypes[0]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ApplicationConfiguration.ProtoReflect.Descriptor instead.
func (*ApplicationConfiguration) Descriptor() ([]byte, []int) {
	return file_pkg_proto_configuration_bonanza_builder_bonanza_builder_proto_rawDescGZIP(), []int{0}
}

func (x *ApplicationConfiguration) GetGlobal() *global.Configuration {
	if x != nil {
		return x.Global
	}
	return nil
}

func (x *ApplicationConfiguration) GetStorageGrpcClient() *grpc.ClientConfiguration {
	if x != nil {
		return x.StorageGrpcClient
	}
	return nil
}

func (x *ApplicationConfiguration) GetFilePool() *filesystem.FilePoolConfiguration {
	if x != nil {
		return x.FilePool
	}
	return nil
}

func (x *ApplicationConfiguration) GetExecutionGrpcClient() *grpc.ClientConfiguration {
	if x != nil {
		return x.ExecutionGrpcClient
	}
	return nil
}

func (x *ApplicationConfiguration) GetExecutionClientPrivateKey() string {
	if x != nil {
		return x.ExecutionClientPrivateKey
	}
	return ""
}

func (x *ApplicationConfiguration) GetExecutionClientCertificateChain() string {
	if x != nil {
		return x.ExecutionClientCertificateChain
	}
	return ""
}

func (x *ApplicationConfiguration) GetRemoteWorkerGrpcClient() *grpc.ClientConfiguration {
	if x != nil {
		return x.RemoteWorkerGrpcClient
	}
	return nil
}

func (x *ApplicationConfiguration) GetPlatformPrivateKeys() []string {
	if x != nil {
		return x.PlatformPrivateKeys
	}
	return nil
}

func (x *ApplicationConfiguration) GetClientCertificateVerifier() *x509.ClientCertificateVerifierConfiguration {
	if x != nil {
		return x.ClientCertificateVerifier
	}
	return nil
}

func (x *ApplicationConfiguration) GetWorkerId() map[string]string {
	if x != nil {
		return x.WorkerId
	}
	return nil
}

func (x *ApplicationConfiguration) GetParsedObjectPool() *parser.ParsedObjectPool {
	if x != nil {
		return x.ParsedObjectPool
	}
	return nil
}

var File_pkg_proto_configuration_bonanza_builder_bonanza_builder_proto protoreflect.FileDescriptor

const file_pkg_proto_configuration_bonanza_builder_bonanza_builder_proto_rawDesc = "" +
	"\n" +
	"=pkg/proto/configuration/bonanza_builder/bonanza_builder.proto\x12%bonanza.configuration.bonanza_builder\x1a3pkg/proto/configuration/filesystem/filesystem.proto\x1a+pkg/proto/configuration/global/global.proto\x1a'pkg/proto/configuration/grpc/grpc.proto\x1a1pkg/proto/configuration/model/parser/parser.proto\x1a'pkg/proto/configuration/x509/x509.proto\"\xc7\b\n" +
	"\x18ApplicationConfiguration\x12E\n" +
	"\x06global\x18\x01 \x01(\v2-.buildbarn.configuration.global.ConfigurationR\x06global\x12a\n" +
	"\x13storage_grpc_client\x18\x03 \x01(\v21.buildbarn.configuration.grpc.ClientConfigurationR\x11storageGrpcClient\x12V\n" +
	"\tfile_pool\x18\x05 \x01(\v29.buildbarn.configuration.filesystem.FilePoolConfigurationR\bfilePool\x12e\n" +
	"\x15execution_grpc_client\x18\a \x01(\v21.buildbarn.configuration.grpc.ClientConfigurationR\x13executionGrpcClient\x12?\n" +
	"\x1cexecution_client_private_key\x18\b \x01(\tR\x19executionClientPrivateKey\x12K\n" +
	"\"execution_client_certificate_chain\x18\t \x01(\tR\x1fexecutionClientCertificateChain\x12l\n" +
	"\x19remote_worker_grpc_client\x18\n" +
	" \x01(\v21.buildbarn.configuration.grpc.ClientConfigurationR\x16remoteWorkerGrpcClient\x122\n" +
	"\x15platform_private_keys\x18\v \x03(\tR\x13platformPrivateKeys\x12\x84\x01\n" +
	"\x1bclient_certificate_verifier\x18\f \x01(\v2D.buildbarn.configuration.x509.ClientCertificateVerifierConfigurationR\x19clientCertificateVerifier\x12j\n" +
	"\tworker_id\x18\r \x03(\v2M.bonanza.configuration.bonanza_builder.ApplicationConfiguration.WorkerIdEntryR\bworkerId\x12b\n" +
	"\x12parsed_object_pool\x18\x0e \x01(\v24.bonanza.configuration.model.parser.ParsedObjectPoolR\x10parsedObjectPool\x1a;\n" +
	"\rWorkerIdEntry\x12\x10\n" +
	"\x03key\x18\x01 \x01(\tR\x03key\x12\x14\n" +
	"\x05value\x18\x02 \x01(\tR\x05value:\x028\x01B7Z5bonanza.build/pkg/proto/configuration/bonanza_builderb\x06proto3"

var (
	file_pkg_proto_configuration_bonanza_builder_bonanza_builder_proto_rawDescOnce sync.Once
	file_pkg_proto_configuration_bonanza_builder_bonanza_builder_proto_rawDescData []byte
)

func file_pkg_proto_configuration_bonanza_builder_bonanza_builder_proto_rawDescGZIP() []byte {
	file_pkg_proto_configuration_bonanza_builder_bonanza_builder_proto_rawDescOnce.Do(func() {
		file_pkg_proto_configuration_bonanza_builder_bonanza_builder_proto_rawDescData = protoimpl.X.CompressGZIP(unsafe.Slice(unsafe.StringData(file_pkg_proto_configuration_bonanza_builder_bonanza_builder_proto_rawDesc), len(file_pkg_proto_configuration_bonanza_builder_bonanza_builder_proto_rawDesc)))
	})
	return file_pkg_proto_configuration_bonanza_builder_bonanza_builder_proto_rawDescData
}

var file_pkg_proto_configuration_bonanza_builder_bonanza_builder_proto_msgTypes = make([]protoimpl.MessageInfo, 2)
var file_pkg_proto_configuration_bonanza_builder_bonanza_builder_proto_goTypes = []any{
	(*ApplicationConfiguration)(nil),         // 0: bonanza.configuration.bonanza_builder.ApplicationConfiguration
	nil,                                      // 1: bonanza.configuration.bonanza_builder.ApplicationConfiguration.WorkerIdEntry
	(*global.Configuration)(nil),             // 2: buildbarn.configuration.global.Configuration
	(*grpc.ClientConfiguration)(nil),         // 3: buildbarn.configuration.grpc.ClientConfiguration
	(*filesystem.FilePoolConfiguration)(nil), // 4: buildbarn.configuration.filesystem.FilePoolConfiguration
	(*x509.ClientCertificateVerifierConfiguration)(nil), // 5: buildbarn.configuration.x509.ClientCertificateVerifierConfiguration
	(*parser.ParsedObjectPool)(nil),                     // 6: bonanza.configuration.model.parser.ParsedObjectPool
}
var file_pkg_proto_configuration_bonanza_builder_bonanza_builder_proto_depIdxs = []int32{
	2, // 0: bonanza.configuration.bonanza_builder.ApplicationConfiguration.global:type_name -> buildbarn.configuration.global.Configuration
	3, // 1: bonanza.configuration.bonanza_builder.ApplicationConfiguration.storage_grpc_client:type_name -> buildbarn.configuration.grpc.ClientConfiguration
	4, // 2: bonanza.configuration.bonanza_builder.ApplicationConfiguration.file_pool:type_name -> buildbarn.configuration.filesystem.FilePoolConfiguration
	3, // 3: bonanza.configuration.bonanza_builder.ApplicationConfiguration.execution_grpc_client:type_name -> buildbarn.configuration.grpc.ClientConfiguration
	3, // 4: bonanza.configuration.bonanza_builder.ApplicationConfiguration.remote_worker_grpc_client:type_name -> buildbarn.configuration.grpc.ClientConfiguration
	5, // 5: bonanza.configuration.bonanza_builder.ApplicationConfiguration.client_certificate_verifier:type_name -> buildbarn.configuration.x509.ClientCertificateVerifierConfiguration
	1, // 6: bonanza.configuration.bonanza_builder.ApplicationConfiguration.worker_id:type_name -> bonanza.configuration.bonanza_builder.ApplicationConfiguration.WorkerIdEntry
	6, // 7: bonanza.configuration.bonanza_builder.ApplicationConfiguration.parsed_object_pool:type_name -> bonanza.configuration.model.parser.ParsedObjectPool
	8, // [8:8] is the sub-list for method output_type
	8, // [8:8] is the sub-list for method input_type
	8, // [8:8] is the sub-list for extension type_name
	8, // [8:8] is the sub-list for extension extendee
	0, // [0:8] is the sub-list for field type_name
}

func init() { file_pkg_proto_configuration_bonanza_builder_bonanza_builder_proto_init() }
func file_pkg_proto_configuration_bonanza_builder_bonanza_builder_proto_init() {
	if File_pkg_proto_configuration_bonanza_builder_bonanza_builder_proto != nil {
		return
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: unsafe.Slice(unsafe.StringData(file_pkg_proto_configuration_bonanza_builder_bonanza_builder_proto_rawDesc), len(file_pkg_proto_configuration_bonanza_builder_bonanza_builder_proto_rawDesc)),
			NumEnums:      0,
			NumMessages:   2,
			NumExtensions: 0,
			NumServices:   0,
		},
		GoTypes:           file_pkg_proto_configuration_bonanza_builder_bonanza_builder_proto_goTypes,
		DependencyIndexes: file_pkg_proto_configuration_bonanza_builder_bonanza_builder_proto_depIdxs,
		MessageInfos:      file_pkg_proto_configuration_bonanza_builder_bonanza_builder_proto_msgTypes,
	}.Build()
	File_pkg_proto_configuration_bonanza_builder_bonanza_builder_proto = out.File
	file_pkg_proto_configuration_bonanza_builder_bonanza_builder_proto_goTypes = nil
	file_pkg_proto_configuration_bonanza_builder_bonanza_builder_proto_depIdxs = nil
}
