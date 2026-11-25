package local

import (
	"cmp"
	"context"
	"math/rand/v2"
	"sync"
	"time"

	configuration_pb "bonanza.build/pkg/proto/configuration/storage/object/local"
	pb "bonanza.build/pkg/proto/storage/object/local"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/blockdevice"
	"github.com/buildbarn/bb-storage/pkg/clock"
	"github.com/buildbarn/bb-storage/pkg/filesystem"
	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
	"github.com/buildbarn/bb-storage/pkg/lossymap"
	"github.com/buildbarn/bb-storage/pkg/program"
	"github.com/buildbarn/bb-storage/pkg/random"
	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// NewStoreFromConfiguration creates a new local object store that uses
// the block devices and parameters specified in a Protobuf
// configuration message.
//
// If leaseTracking is provided, the store tracks the points in time at
// which data is written, so that leases of objects and the watermark of
// the store can be computed. This is only needed when the store is used
// by storage shards. Otherwise, all leases and the watermark are the
// zero time.
func NewStoreFromConfiguration(terminationGroup program.Group, configuration *configuration_pb.StoreConfiguration, leaseTracking *LeaseTrackingConfiguration) (Store, error) {
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

		// State needed to compute leases is only valid if no
		// data was written after it. Remove it from disk before
		// handing out any leases, so that it isn't reused if
		// this process does not shut down cleanly.
		if initialPersistentState.LeaseTracking != nil {
			clearedPersistentState := proto.Clone(initialPersistentState).(*pb.PersistentState)
			clearedPersistentState.LeaseTracking = nil
			if err := persistentStateStore.WritePersistentState(clearedPersistentState); err != nil {
				return nil, util.StatusWrap(err, "Failed to clear lease tracking state")
			}
		}
	} else {
		initialPersistentState = &pb.PersistentState{
			ReferenceLocationMapHashInitialization: rand.Uint64(),
		}
	}

	// Construct the reference-location map that stores the location
	// at which object contents are stored.
	referenceLocationMapHashInitialization := initialPersistentState.ReferenceLocationMapHashInitialization
	referenceLocationMap, syncReferenceLocationMap, err := lossymap.NewHashMapFromConfiguration(
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
			h ^= uint64(k.Attempt)
			h *= 1099511628211
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

	// Data stored in memory is lost across restarts. Using the
	// persistent state would cause the store to report the existing
	// incarnation and leases as valid, even though the data is gone.
	if configuration.Persistent != nil && syncReferenceLocationMap == nil {
		return nil, status.Error(codes.InvalidArgument, "Persistency can only be enabled if the reference-location map is stored on a block device")
	}

	// Construct the location-blob map that stores the contents of
	// the objects.
	var dataSyncer DataSyncer = func(isFinalSync bool) error { return nil }
	var locationBlobMap LocationBlobMap
	var maximumLocationSpan uint64
	switch backendConfiguration := configuration.LocationBlobMapBackend.(type) {
	case *configuration_pb.StoreConfiguration_LocationBlobMapInMemory_:
		if configuration.Persistent != nil {
			return nil, status.Error(codes.InvalidArgument, "Persistency can only be enabled if the location-blob map is stored on a block device")
		}
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
		dataSyncer = func(isFinalSync bool) error {
			if err := blockDevice.Sync(); err != nil {
				return util.StatusWrap(err, "Failed to synchronize location-blob map")
			}

			// Writes to the reference-location map are not
			// synchronized during regular operation, as the
			// persistent state written afterwards causes
			// all leases to be invalidated upon restart.
			// Upon clean shutdown leases remain valid,
			// meaning all entries must be persisted. The
			// same holds for any other data structures
			// containing leases.
			if isFinalSync {
				if err := syncReferenceLocationMap(); err != nil {
					return util.StatusWrap(err, "Failed to synchronize reference-location map")
				}
				if leaseTracking != nil && leaseTracking.SyncOnCleanShutdown != nil {
					if err := leaseTracking.SyncOnCleanShutdown(); err != nil {
						return err
					}
				}
			}
			return nil
		}
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
			leaseTracking,
			initialPersistentState,
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
			leaseTracking,
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

	var minimumSafeSince func() time.Time
	if leaseTracking != nil {
		minimumSafeSince = leaseTracking.MinimumSafeSince
	}
	return NewStore(
		&globalLock,
		referenceLocationMap,
		locationBlobMap,
		epochList,
		currentRegionSizeBytes,
		newRegionSizeBytes,
		minimumSafeSince,
	), nil
}
