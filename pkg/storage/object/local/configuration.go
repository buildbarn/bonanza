package local

import (
	"cmp"
	"context"
	"math/rand/v2"
	"sync"
	"time"

	"bonanza.build/pkg/ds/lossymap"
	configuration_pb "bonanza.build/pkg/proto/configuration/storage/object/local"
	pb "bonanza.build/pkg/proto/storage/object/local"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/blockdevice"
	"github.com/buildbarn/bb-storage/pkg/clock"
	"github.com/buildbarn/bb-storage/pkg/filesystem"
	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
	"github.com/buildbarn/bb-storage/pkg/program"
	"github.com/buildbarn/bb-storage/pkg/random"
	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// NewStoreFromConfiguration creates a new local object store that uses
// the block devices and parameters specified in a Protobuf
// configuration message.
func NewStoreFromConfiguration(terminationGroup program.Group, configuration *configuration_pb.StoreConfiguration) (object.Store[object.FlatReference, struct{}], error) {
	if configuration == nil {
		return nil, status.Error(codes.InvalidArgument, "No configuration provided")
	}

	// If persistency is enabled, reload the persistent state from
	// disk so that we can resume where the previous run left off.
	var persistentStateStore PersistentStateStore
	var initialPersistentState *pb.PersistentState
	if pcfg := configuration.Persistent; pcfg != nil {
		stateDirectory, err := filesystem.NewLocalDirectory(path.LocalFormat.NewParser(pcfg.StateDirectoryPath))
		if err != nil {
			return nil, util.StatusWrap(err, "Failed to open persistent state directory")
		}
		persistentStateStore = NewDirectoryBackedPersistentStateStore(stateDirectory)

		initialPersistentState, err = persistentStateStore.ReadPersistentState()
		if err != nil {
			return nil, util.StatusWrap(err, "Failed to read persistent state")
		}
	} else {
		initialPersistentState = &pb.PersistentState{
			ReferenceLocationMapHashInitialization: rand.Uint64(),
		}
	}

	// Construct the reference-location map that stores the location
	// at which object contents are stored.
	referenceLocationMapHashInitialization := initialPersistentState.ReferenceLocationMapHashInitialization
	referenceLocationMap, err := lossymap.NewHashMapFromConfiguration(
		configuration.ReferenceLocationMap,
		"ReferenceLocationMap",
		LocationRecordArrayFactory,
		/* recordKeyHasher = */
		func(k *lossymap.RecordKey[object.FlatReference]) uint64 {
			// Compute a FNV-1a hash of the record key.
			h := referenceLocationMapHashInitialization
			for _, c := range k.Key.GetRawFlatReference() {
				h ^= uint64(c)
				h *= 1099511628211
			}
			attempt := k.Attempt
			for i := 0; i < 4; i++ {
				h ^= uint64(attempt & 0xff)
				h *= 1099511628211
				attempt >>= 8
			}
			return h
		},
		/* valueComparator = */ func(a, b *uint64) int {
			return cmp.Compare(*a, *b)
		},
		configuration.Persistent != nil,
	)
	if err != nil {
		return nil, util.StatusWrap(err, "Failed to create reference-location map")
	}

	// Construct the location-blob map that stores the contents of
	// the objects.
	var dataSyncer DataSyncer = func() error { return nil }
	var locationBlobMap LocationBlobMap
	var maximumLocationSpan uint64
	switch backendConfiguration := configuration.LocationBlobMapBackend.(type) {
	case *configuration_pb.StoreConfiguration_LocationBlobMapInMemory_:
		sizeBytes := backendConfiguration.LocationBlobMapInMemory.SizeBytes
		locationBlobMap = NewInMemoryLocationBlobMap(int(sizeBytes))
		maximumLocationSpan = sizeBytes
	case *configuration_pb.StoreConfiguration_LocationBlobMapOnBlockDevice:
		blockDevice, sectorSizeBytes, sectorCount, err := blockdevice.NewBlockDeviceFromConfiguration(
			backendConfiguration.LocationBlobMapOnBlockDevice,
			/* mayZeroInitialize = */ configuration.Persistent == nil,
		)
		if err != nil {
			return nil, util.StatusWrap(err, "Failed to create location-blob map block device")
		}

		initialLocation := initialPersistentState.MinimumLocation
		for _, epoch := range initialPersistentState.Epochs {
			initialLocation += epoch.LocationIncrease
		}
		dataSyncer = blockDevice.Sync
		locationBlobMap = NewBlockDeviceBackedLocationBlobMap(
			blockDevice,
			sectorSizeBytes,
			sectorCount,
			initialLocation,
		)

		// BlockDeviceBackedLocationBlobMap always performs
		// sector aligned writes. If an object ends in the
		// middle of a sector, it may pad the sector with null
		// bytes. This means that the effective amount of
		// storage is one sector less than the size of the
		// underlying block device.
		maximumLocationSpan = uint64(sectorSizeBytes) * uint64(sectorCount-1)
	default:
		return nil, status.Error(codes.InvalidArgument, "No reference-location map backend provided")
	}

	// Construct the epoch list that tracks which objects whose
	// contents have been synced to disk properly. This is necessary
	// to reliably continue after restarts.
	var epochList EpochList
	var globalLock sync.RWMutex
	if pcfg := configuration.Persistent; pcfg != nil {
		persistentEpochList := NewPersistentEpochList(
			maximumLocationSpan,
			random.NewFastSingleThreadedGenerator(),
			initialPersistentState.MinimumEpochId,
			initialPersistentState.MinimumLocation,
			initialPersistentState.Epochs,
		)
		epochList = persistentEpochList

		minimumEpochInterval := pcfg.MinimumEpochInterval
		if err := minimumEpochInterval.CheckValid(); err != nil {
			return nil, util.StatusWrap(err, "Invalid minimum epoch interval")
		}

		periodicSyncer := NewPeriodicSyncer(
			persistentEpochList,
			&globalLock,
			persistentStateStore,
			clock.SystemClock,
			util.DefaultErrorLogger,
			/* errorRetryInterval = */ 10*time.Second,
			minimumEpochInterval.AsDuration(),
			referenceLocationMapHashInitialization,
			dataSyncer,
		)
		terminationGroup.Go(func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
			for periodicSyncer.ProcessLocationsChanged(ctx) {
			}
			// TODO: Let PeriodicSyncer propagate errors
			// upwards in case they occur after the context
			// has been cancelled.
			return nil
		})
	} else {
		epochList = NewVolatileEpochList(
			maximumLocationSpan,
			random.NewFastSingleThreadedGenerator(),
		)
	}

	// Validate and compute the sizes of the "old" and "current"
	// regions based on the configured ratios.
	oldRegionSizeRatio := configuration.OldRegionSizeRatio
	if oldRegionSizeRatio < 1 {
		return nil, status.Error(codes.InvalidArgument, "Old region size ratio must be positive")
	}
	currentRegionSizeRatio := configuration.CurrentRegionSizeRatio
	if currentRegionSizeRatio < 1 {
		return nil, status.Error(codes.InvalidArgument, "Current region size ratio must be positive")
	}
	newRegionSizeRatio := configuration.NewRegionSizeRatio
	if newRegionSizeRatio < 1 {
		return nil, status.Error(codes.InvalidArgument, "New region size ratio must be positive")
	}
	totalRatio := uint64(oldRegionSizeRatio + currentRegionSizeRatio + newRegionSizeRatio)
	currentRegionSizeBytes := maximumLocationSpan * uint64(currentRegionSizeRatio) / totalRatio
	newRegionSizeBytes := maximumLocationSpan * uint64(newRegionSizeRatio) / totalRatio

	return NewStore(
		&globalLock,
		referenceLocationMap,
		locationBlobMap,
		epochList,
		currentRegionSizeBytes,
		newRegionSizeBytes,
	), nil
}
