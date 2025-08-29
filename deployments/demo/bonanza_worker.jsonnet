local os = std.extVar('OS');
local statePath = std.extVar('STATE_PATH');

{
  // If enabled, use NFSv4 instead of FUSE.
  useNFSv4:: os == 'Darwin',

  global: { diagnosticsHttpServer: {
    httpServers: [{
      listenAddresses: [':9983'],
      authenticationPolicy: { allow: {} },
    }],
    enablePrometheus: true,
    enablePprof: true,
  } },

  storageGrpcClient: {
    address: 'unix://%s/bonanza_storage_frontend.sock' % statePath,
  },
  schedulerGrpcClient: {
    address: 'unix://%s/bonanza_scheduler_workers.sock' % statePath,
  },
  parsedObjectPool: {
    cacheReplacementPolicy: 'LEAST_RECENTLY_USED',
    count: 1e6,
    sizeBytes: 1e9,
  },
  filePool: { blockDevice: { file: {
    path: statePath + '/bonanza_worker_filepool',
    sizeBytes: 1e9,
  } } },
  buildDirectories: [{
    runners: [{
      endpoint: {
        address: 'unix://%s/bb_runner.sock' % statePath,
      },
      concurrency: std.extVar('NCPU'),
      platformPrivateKeys: [
        |||
          -----BEGIN PRIVATE KEY-----
          MC4CAQAwBQYDK2VuBCIEIOgTxCpcEulRlM07gyhE3ydECoP915a6wj6SR6W62QZ4
          -----END PRIVATE KEY-----
        |||,
      ],
      clientCertificateVerifier: {
        clientCertificateAuthorities: |||
          -----BEGIN CERTIFICATE-----
          MIIBFjCByaADAgECAhQehgruMx6qQ/4f985cRD+3B5tBJDAFBgMrZXAwADAgFw0y
          NTA3MjExNDAwNDNaGA8yMDUyMTIwNjE0MDA0M1owADAqMAUGAytlcAMhAOXY2Z9C
          AQ0fEdGOBVNQNnpkSR/kDd6B/rvYogriKaTVo1MwUTAdBgNVHQ4EFgQUhxs9zWAB
          5Hk8jy6sNKZd8ykNcd0wHwYDVR0jBBgwFoAUhxs9zWAB5Hk8jy6sNKZd8ykNcd0w
          DwYDVR0TAQH/BAUwAwEB/zAFBgMrZXADQQC+/DhfeldkbnqWg0fBPV9HY39kL2lT
          253seXn65SwVp5Kryf9bfFEfF715YIrcQjsyg3EDD8jw+qQho5bZ/pMF
          -----END CERTIFICATE-----
        |||,
        validationJmespathExpression: { expression: '`true`' },
        metadataExtractionJmespathExpression: { expression: '`{}`' },
      },
      sizeClass: 1,
      isLargestSizeClass: true,
      hiddenFilesPattern: '^\\._|^\\.nfs\\.[0-9a-f]{8}\\.[0-9a-f]{4}$',
      maximumExecutionTimeoutCompensation: '3600s',
      maximumWritableFileUploadDelay: '60s',
      maximumFilePoolFileCount: 1e5,
      maximumFilePoolSizeBytes: 1e9,
      workerId: { host: std.extVar('HOSTNAME') },
      buildDirectoryOwnerUserId: std.extVar('USER_ID'),
      buildDirectoryOwnerGroupId: std.extVar('GROUP_ID'),
    }],
    mount: {
      mountPath: statePath + '/bonanza_worker_mount',
    } + if $.useNFSv4 then {
      nfsv4: {
        enforcedLeaseTime: '120s',
        announcedLeaseTime: '60s',
      } + {
        Darwin: { darwin: { socketPath: statePath + '/bonanza_worker_mount.sock' } },
        Linux: { linux: { mountOptions: ['vers=4.1'] } },
      }[os],
    } else {
      fuse: {
        directoryEntryValidity: '300s',
        inodeAttributeValidity: '300s',
      },
    },
  }],
}
