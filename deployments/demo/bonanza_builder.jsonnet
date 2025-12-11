local statePath = std.extVar('STATE_PATH');

{
  global: { diagnosticsHttpServer: {
    httpServers: [{
      listenAddresses: [':9980'],
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
    path: statePath + '/bonanza_builder_filepool',
    sizeBytes: 1e9,
  } } },

  // Connection to the scheduler to run actions on workers.
  executionGrpcClient: {
    address: 'unix://%s/bonanza_scheduler_clients.sock' % statePath,
  },
  executionClientPrivateKey: |||
    -----BEGIN PRIVATE KEY-----
    MC4CAQAwBQYDK2VwBCIEIC/NAtKkG4U54eK681lDSjLihao6xb4f0Z7klOddF4wb
    -----END PRIVATE KEY-----
  |||,
  executionClientCertificateChain: |||
    -----BEGIN CERTIFICATE-----
    MIIBFjCByaADAgECAhQehgruMx6qQ/4f985cRD+3B5tBJDAFBgMrZXAwADAgFw0y
    NTA3MjExNDAwNDNaGA8yMDUyMTIwNjE0MDA0M1owADAqMAUGAytlcAMhAOXY2Z9C
    AQ0fEdGOBVNQNnpkSR/kDd6B/rvYogriKaTVo1MwUTAdBgNVHQ4EFgQUhxs9zWAB
    5Hk8jy6sNKZd8ykNcd0wHwYDVR0jBBgwFoAUhxs9zWAB5Hk8jy6sNKZd8ykNcd0w
    DwYDVR0TAQH/BAUwAwEB/zAFBgMrZXADQQC+/DhfeldkbnqWg0fBPV9HY39kL2lT
    253seXn65SwVp5Kryf9bfFEfF715YIrcQjsyg3EDD8jw+qQho5bZ/pMF
    -----END CERTIFICATE-----
  |||,

  // Connection to scheduler to pick up build requests from clients.
  remoteWorkerGrpcClient: {
    address: 'unix://%s/bonanza_scheduler_workers.sock' % statePath,
  },
  platformPrivateKeys: [
    |||
      -----BEGIN PRIVATE KEY-----
      MC4CAQAwBQYDK2VuBCIEIPANGDz3SIhhQJdIQ/7w4Uq9DYUwyH/fw36A1j2aviBW
      -----END PRIVATE KEY-----
    |||,
  ],
  clientCertificateVerifier: {
    clientCertificateAuthorities: |||
      -----BEGIN CERTIFICATE-----
      MIIBFjCByaADAgECAhQlTQG2VhL0NaJ6YQNG1PTV9ToC9TAFBgMrZXAwADAgFw0y
      NTA3MjExMzU5NDVaGA8yMDUyMTIwNjEzNTk0NVowADAqMAUGAytlcAMhAAKWO7Ag
      zpOFze4lA2eNnk/pfBQPJWo89n8A3p6tbhx/o1MwUTAdBgNVHQ4EFgQUrxStcD6E
      18Zcj7250S3HUujNZW0wHwYDVR0jBBgwFoAUrxStcD6E18Zcj7250S3HUujNZW0w
      DwYDVR0TAQH/BAUwAwEB/zAFBgMrZXADQQDgJdQPpgGm+UDER+Kc/HzXJOyzWqAA
      sQ1zyHaR2bN9rxbDuTRSgsIZxvw/3UAyjEkwWz8uCI28kf2K+GWOcb0K
      -----END CERTIFICATE-----
    |||,
    validationJmespathExpression: { expression: '`true`' },
    metadataExtractionJmespathExpression: { expression: '`{}`' },
  },
  workerId: { host: std.extVar('HOSTNAME') },
  localEvaluationConcurrency: std.extVar('NCPU'),
  remoteEvaluationConcurrency: 100,
  objectStoreConcurrency: std.extVar('NCPU'),
  cacheTagSignaturePrivateKey: |||
    -----BEGIN PRIVATE KEY-----
    MC4CAQAwBQYDK2VwBCIEIB02dlibU4cQ7kQaoTg3f4VeXtrM0aM5q6VYslB+1UNE
    -----END PRIVATE KEY-----
  |||,
}
