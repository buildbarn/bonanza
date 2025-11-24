local statePath = std.extVar('STATE_PATH');

{
  global: { diagnosticsHttpServer: {
    httpServers: [{
      listenAddresses: [':9984'],
      authenticationPolicy: { allow: {} },
    }],
    enablePrometheus: true,
    enablePprof: true,
  } },

  storageGrpcClient: {
    address: 'unix://%s/bonanza_storage_frontend.sock' % statePath,
  },

  parsedObjectPool: {
    cacheReplacementPolicy: 'LEAST_RECENTLY_USED',
    count: 1e6,
    sizeBytes: 1e9,
  },
  filePool: { blockDevice: { file: {
    path: statePath + '/bonanza_fetcher_filepool',
    sizeBytes: 1e9,
  } } },

  // Connection to scheduler to pick up fetch requests from clients.
  remoteWorkerGrpcClient: {
    address: 'unix://%s/bonanza_scheduler_workers.sock' % statePath,
  },
  platformPrivateKeys: [
    |||
      -----BEGIN PRIVATE KEY-----
      MC4CAQAwBQYDK2VuBCIEIDCV7CmgpxW5TOkay7iqxen7AbB7nYhfExOwmCZCFm9q
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
  concurrency: 10,
  workerId: { host: std.extVar('HOSTNAME') },
}
