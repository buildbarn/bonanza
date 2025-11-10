local statePath = std.extVar('STATE_PATH');
local replica = std.extVar('REPLICA');
local shard = std.extVar('SHARD');

{
  grpcServers: [{
    listenPaths: ['%s/bonanza_storage_shard_%s%s.sock' % [statePath, replica, shard]],
    authenticationPolicy: { allow: {} },
  }],

  local dataPath = '%s/bonanza_storage_shard_%s%s' % [statePath, replica, shard],

  localObjectStore: {
    referenceLocationMap: {
      onBlockDevice: { file: {
        path: dataPath + '/reference_location_map',
        sizeBytes: 1e8,
      } },
      maximumGetAttempts: 16,
      maximumPutAttempts: 64,
    },

    locationBlobMapOnBlockDevice: { file: {
      path: dataPath + '/location_blob_map',
      sizeBytes: 1e10,
    } },

    oldRegionSizeRatio: 1,
    currentRegionSizeRatio: 1,
    newRegionSizeRatio: 3,

    persistent: {
      stateDirectoryPath: dataPath + '/persistent_state',
      minimumEpochInterval: '300s',
    },
  },

  leasesMap: {
    inMemory: { entries: 1e6 },
    maximumGetAttempts: 16,
    maximumPutAttempts: 64,
  },
  leasesMapLeaseCompletenessDuration: '120s',

  tagsMap: {
    onBlockDevice: { file: {
      path: dataPath + '/tags',
      sizeBytes: 1e7,
    } },
    maximumGetAttempts: 16,
    maximumPutAttempts: 64,
  },
}
