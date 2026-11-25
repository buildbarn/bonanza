package local_test

import (
	"path/filepath"
	"testing"

	configuration_pb "bonanza.build/pkg/proto/configuration/storage/object/local"
	"bonanza.build/pkg/storage/object/local"

	blockdevice_pb "github.com/buildbarn/bb-storage/pkg/proto/configuration/blockdevice"
	lossymap_pb "github.com/buildbarn/bb-storage/pkg/proto/configuration/lossymap"
	"github.com/buildbarn/bb-storage/pkg/testutil"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestNewStoreFromConfigurationPersistentInMemory(t *testing.T) {
	// Data stored in memory does not survive restarts. Enabling
	// persistency would cause the store to reuse its incarnation
	// and report existing leases as valid, even though the data is
	// gone. Such configurations must therefore be rejected. This
	// should happen before any goroutines are launched.
	directory := t.TempDir()
	onBlockDevice := &configuration_pb.StoreConfiguration{
		ReferenceLocationMap: &lossymap_pb.HashMapConfiguration{
			Backend: &lossymap_pb.HashMapConfiguration_OnBlockDevice{
				OnBlockDevice: &blockdevice_pb.Configuration{
					Source: &blockdevice_pb.Configuration_File{
						File: &blockdevice_pb.FileConfiguration{
							Path:      filepath.Join(directory, "reference_location_map"),
							SizeBytes: 1 << 20,
						},
					},
				},
			},
			MaximumGetAttempts: 16,
			MaximumPutAttempts: 64,
		},
		LocationBlobMapBackend: &configuration_pb.StoreConfiguration_LocationBlobMapOnBlockDevice{
			LocationBlobMapOnBlockDevice: &blockdevice_pb.Configuration{
				Source: &blockdevice_pb.Configuration_File{
					File: &blockdevice_pb.FileConfiguration{
						Path:      filepath.Join(directory, "location_blob_map"),
						SizeBytes: 1 << 20,
					},
				},
			},
		},
		OldRegionSizeRatio:     1,
		CurrentRegionSizeRatio: 1,
		NewRegionSizeRatio:     3,
		Persistent: &configuration_pb.StoreConfiguration_Persistent{
			StateDirectoryPath: directory,
		},
	}

	t.Run("ReferenceLocationMapInMemory", func(t *testing.T) {
		configuration := proto.Clone(onBlockDevice).(*configuration_pb.StoreConfiguration)
		configuration.ReferenceLocationMap.Backend = &lossymap_pb.HashMapConfiguration_InMemory_{
			InMemory: &lossymap_pb.HashMapConfiguration_InMemory{
				Entries: 1000,
			},
		}
		_, err := local.NewStoreFromConfiguration( /* terminationGroup = */ nil, configuration, nil)
		testutil.RequireEqualStatus(t, status.Error(codes.InvalidArgument, "Persistency can only be enabled if the reference-location map is stored on a block device"), err)
	})

	t.Run("LocationBlobMapInMemory", func(t *testing.T) {
		configuration := proto.Clone(onBlockDevice).(*configuration_pb.StoreConfiguration)
		configuration.LocationBlobMapBackend = &configuration_pb.StoreConfiguration_LocationBlobMapInMemory_{
			LocationBlobMapInMemory: &configuration_pb.StoreConfiguration_LocationBlobMapInMemory{
				SizeBytes: 1 << 20,
			},
		}
		_, err := local.NewStoreFromConfiguration( /* terminationGroup = */ nil, configuration, nil)
		testutil.RequireEqualStatus(t, status.Error(codes.InvalidArgument, "Persistency can only be enabled if the location-blob map is stored on a block device"), err)
	})
}
