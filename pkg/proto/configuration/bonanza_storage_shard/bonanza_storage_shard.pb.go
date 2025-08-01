// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.36.6
// 	protoc        v6.31.1
// source: pkg/proto/configuration/bonanza_storage_shard/bonanza_storage_shard.proto

package bonanza_storage_shard

import (
	local "bonanza.build/pkg/proto/configuration/storage/object/local"
	global "github.com/buildbarn/bb-storage/pkg/proto/configuration/global"
	grpc "github.com/buildbarn/bb-storage/pkg/proto/configuration/grpc"
	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	durationpb "google.golang.org/protobuf/types/known/durationpb"
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
	state                              protoimpl.MessageState      `protogen:"open.v1"`
	Global                             *global.Configuration       `protobuf:"bytes,1,opt,name=global,proto3" json:"global,omitempty"`
	GrpcServers                        []*grpc.ServerConfiguration `protobuf:"bytes,2,rep,name=grpc_servers,json=grpcServers,proto3" json:"grpc_servers,omitempty"`
	LeasesMapRecordsCount              uint64                      `protobuf:"varint,3,opt,name=leases_map_records_count,json=leasesMapRecordsCount,proto3" json:"leases_map_records_count,omitempty"`
	LeasesMapLeaseCompletenessDuration *durationpb.Duration        `protobuf:"bytes,4,opt,name=leases_map_lease_completeness_duration,json=leasesMapLeaseCompletenessDuration,proto3" json:"leases_map_lease_completeness_duration,omitempty"`
	LeasesMapMaximumGetAttempts        uint32                      `protobuf:"varint,5,opt,name=leases_map_maximum_get_attempts,json=leasesMapMaximumGetAttempts,proto3" json:"leases_map_maximum_get_attempts,omitempty"`
	LeasesMapMaximumPutAttempts        int64                       `protobuf:"varint,6,opt,name=leases_map_maximum_put_attempts,json=leasesMapMaximumPutAttempts,proto3" json:"leases_map_maximum_put_attempts,omitempty"`
	LocalObjectStore                   *local.StoreConfiguration   `protobuf:"bytes,7,opt,name=local_object_store,json=localObjectStore,proto3" json:"local_object_store,omitempty"`
	unknownFields                      protoimpl.UnknownFields
	sizeCache                          protoimpl.SizeCache
}

func (x *ApplicationConfiguration) Reset() {
	*x = ApplicationConfiguration{}
	mi := &file_pkg_proto_configuration_bonanza_storage_shard_bonanza_storage_shard_proto_msgTypes[0]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ApplicationConfiguration) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ApplicationConfiguration) ProtoMessage() {}

func (x *ApplicationConfiguration) ProtoReflect() protoreflect.Message {
	mi := &file_pkg_proto_configuration_bonanza_storage_shard_bonanza_storage_shard_proto_msgTypes[0]
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
	return file_pkg_proto_configuration_bonanza_storage_shard_bonanza_storage_shard_proto_rawDescGZIP(), []int{0}
}

func (x *ApplicationConfiguration) GetGlobal() *global.Configuration {
	if x != nil {
		return x.Global
	}
	return nil
}

func (x *ApplicationConfiguration) GetGrpcServers() []*grpc.ServerConfiguration {
	if x != nil {
		return x.GrpcServers
	}
	return nil
}

func (x *ApplicationConfiguration) GetLeasesMapRecordsCount() uint64 {
	if x != nil {
		return x.LeasesMapRecordsCount
	}
	return 0
}

func (x *ApplicationConfiguration) GetLeasesMapLeaseCompletenessDuration() *durationpb.Duration {
	if x != nil {
		return x.LeasesMapLeaseCompletenessDuration
	}
	return nil
}

func (x *ApplicationConfiguration) GetLeasesMapMaximumGetAttempts() uint32 {
	if x != nil {
		return x.LeasesMapMaximumGetAttempts
	}
	return 0
}

func (x *ApplicationConfiguration) GetLeasesMapMaximumPutAttempts() int64 {
	if x != nil {
		return x.LeasesMapMaximumPutAttempts
	}
	return 0
}

func (x *ApplicationConfiguration) GetLocalObjectStore() *local.StoreConfiguration {
	if x != nil {
		return x.LocalObjectStore
	}
	return nil
}

var File_pkg_proto_configuration_bonanza_storage_shard_bonanza_storage_shard_proto protoreflect.FileDescriptor

const file_pkg_proto_configuration_bonanza_storage_shard_bonanza_storage_shard_proto_rawDesc = "" +
	"\n" +
	"Ipkg/proto/configuration/bonanza_storage_shard/bonanza_storage_shard.proto\x12+bonanza.configuration.bonanza_storage_shard\x1a\x1egoogle/protobuf/duration.proto\x1a+pkg/proto/configuration/global/global.proto\x1a'pkg/proto/configuration/grpc/grpc.proto\x1a8pkg/proto/configuration/storage/object/local/local.proto\"\xd9\x04\n" +
	"\x18ApplicationConfiguration\x12E\n" +
	"\x06global\x18\x01 \x01(\v2-.buildbarn.configuration.global.ConfigurationR\x06global\x12T\n" +
	"\fgrpc_servers\x18\x02 \x03(\v21.buildbarn.configuration.grpc.ServerConfigurationR\vgrpcServers\x127\n" +
	"\x18leases_map_records_count\x18\x03 \x01(\x04R\x15leasesMapRecordsCount\x12m\n" +
	"&leases_map_lease_completeness_duration\x18\x04 \x01(\v2\x19.google.protobuf.DurationR\"leasesMapLeaseCompletenessDuration\x12D\n" +
	"\x1fleases_map_maximum_get_attempts\x18\x05 \x01(\rR\x1bleasesMapMaximumGetAttempts\x12D\n" +
	"\x1fleases_map_maximum_put_attempts\x18\x06 \x01(\x03R\x1bleasesMapMaximumPutAttempts\x12l\n" +
	"\x12local_object_store\x18\a \x01(\v2>.bonanza.configuration.storage.object.local.StoreConfigurationR\x10localObjectStoreB=Z;bonanza.build/pkg/proto/configuration/bonanza_storage_shardb\x06proto3"

var (
	file_pkg_proto_configuration_bonanza_storage_shard_bonanza_storage_shard_proto_rawDescOnce sync.Once
	file_pkg_proto_configuration_bonanza_storage_shard_bonanza_storage_shard_proto_rawDescData []byte
)

func file_pkg_proto_configuration_bonanza_storage_shard_bonanza_storage_shard_proto_rawDescGZIP() []byte {
	file_pkg_proto_configuration_bonanza_storage_shard_bonanza_storage_shard_proto_rawDescOnce.Do(func() {
		file_pkg_proto_configuration_bonanza_storage_shard_bonanza_storage_shard_proto_rawDescData = protoimpl.X.CompressGZIP(unsafe.Slice(unsafe.StringData(file_pkg_proto_configuration_bonanza_storage_shard_bonanza_storage_shard_proto_rawDesc), len(file_pkg_proto_configuration_bonanza_storage_shard_bonanza_storage_shard_proto_rawDesc)))
	})
	return file_pkg_proto_configuration_bonanza_storage_shard_bonanza_storage_shard_proto_rawDescData
}

var file_pkg_proto_configuration_bonanza_storage_shard_bonanza_storage_shard_proto_msgTypes = make([]protoimpl.MessageInfo, 1)
var file_pkg_proto_configuration_bonanza_storage_shard_bonanza_storage_shard_proto_goTypes = []any{
	(*ApplicationConfiguration)(nil), // 0: bonanza.configuration.bonanza_storage_shard.ApplicationConfiguration
	(*global.Configuration)(nil),     // 1: buildbarn.configuration.global.Configuration
	(*grpc.ServerConfiguration)(nil), // 2: buildbarn.configuration.grpc.ServerConfiguration
	(*durationpb.Duration)(nil),      // 3: google.protobuf.Duration
	(*local.StoreConfiguration)(nil), // 4: bonanza.configuration.storage.object.local.StoreConfiguration
}
var file_pkg_proto_configuration_bonanza_storage_shard_bonanza_storage_shard_proto_depIdxs = []int32{
	1, // 0: bonanza.configuration.bonanza_storage_shard.ApplicationConfiguration.global:type_name -> buildbarn.configuration.global.Configuration
	2, // 1: bonanza.configuration.bonanza_storage_shard.ApplicationConfiguration.grpc_servers:type_name -> buildbarn.configuration.grpc.ServerConfiguration
	3, // 2: bonanza.configuration.bonanza_storage_shard.ApplicationConfiguration.leases_map_lease_completeness_duration:type_name -> google.protobuf.Duration
	4, // 3: bonanza.configuration.bonanza_storage_shard.ApplicationConfiguration.local_object_store:type_name -> bonanza.configuration.storage.object.local.StoreConfiguration
	4, // [4:4] is the sub-list for method output_type
	4, // [4:4] is the sub-list for method input_type
	4, // [4:4] is the sub-list for extension type_name
	4, // [4:4] is the sub-list for extension extendee
	0, // [0:4] is the sub-list for field type_name
}

func init() { file_pkg_proto_configuration_bonanza_storage_shard_bonanza_storage_shard_proto_init() }
func file_pkg_proto_configuration_bonanza_storage_shard_bonanza_storage_shard_proto_init() {
	if File_pkg_proto_configuration_bonanza_storage_shard_bonanza_storage_shard_proto != nil {
		return
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: unsafe.Slice(unsafe.StringData(file_pkg_proto_configuration_bonanza_storage_shard_bonanza_storage_shard_proto_rawDesc), len(file_pkg_proto_configuration_bonanza_storage_shard_bonanza_storage_shard_proto_rawDesc)),
			NumEnums:      0,
			NumMessages:   1,
			NumExtensions: 0,
			NumServices:   0,
		},
		GoTypes:           file_pkg_proto_configuration_bonanza_storage_shard_bonanza_storage_shard_proto_goTypes,
		DependencyIndexes: file_pkg_proto_configuration_bonanza_storage_shard_bonanza_storage_shard_proto_depIdxs,
		MessageInfos:      file_pkg_proto_configuration_bonanza_storage_shard_bonanza_storage_shard_proto_msgTypes,
	}.Build()
	File_pkg_proto_configuration_bonanza_storage_shard_bonanza_storage_shard_proto = out.File
	file_pkg_proto_configuration_bonanza_storage_shard_bonanza_storage_shard_proto_goTypes = nil
	file_pkg_proto_configuration_bonanza_storage_shard_bonanza_storage_shard_proto_depIdxs = nil
}
