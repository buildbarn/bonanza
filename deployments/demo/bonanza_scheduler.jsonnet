local statePath = std.extVar('STATE_PATH');

{
  global: { diagnosticsHttpServer: {
    httpServers: [{
      listenAddresses: [':9985'],
      authenticationPolicy: { allow: {} },
    }],
    enablePrometheus: true,
    enablePprof: true,
  } },

  clientGrpcServers: [{
    listenPaths: [statePath + '/bonanza_scheduler_clients.sock'],
    authenticationPolicy: { allow: {} },
  }],
  workerGrpcServers: [{
    listenPaths: [statePath + '/bonanza_scheduler_workers.sock'],
    authenticationPolicy: { allow: {} },
  }],
  buildQueueStateGrpcServers: [{
    listenPaths: [statePath + '/bonanza_scheduler_buildqueuestate.sock'],
    authenticationPolicy: { allow: {} },
  }],
  actionRouter: {
    simple: {
      initialSizeClassAnalyzer: {
        maximumExecutionTimeout: '86400s',
        feedbackDriven: {
          failureCacheDuration: '86400s',
          historySize: 32,
        },
      },
    },
  },
  previousExecutionStatsStore: {
    grpcClient: {
      address: 'unix://%s/bonanza_storage_frontend.sock' % statePath,
    },
    namespace: { referenceFormat: 'SHA256_V1' },
    tagSignaturePrivateKey: |||
      -----BEGIN PRIVATE KEY-----
      MC4CAQAwBQYDK2VwBCIEIHrwHIyuzYLw9vemhKYfBKHwpPyivLDXJBMeS7Q+bL3x
      -----END PRIVATE KEY-----
    |||,
    objectEncoders: [{ encrypting: {
      encryptionKey: 'c3LrGufjguOjXvxrAv5mAq+mqMkAstWlN/lwBFMGItQ=',
    } }],
  },
  platformQueueWithNoWorkersTimeout: '900s',
}
