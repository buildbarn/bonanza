local shardsCount = 4;
local statePath = std.extVar('STATE_PATH');

{
  global: { diagnosticsHttpServer: {
    httpServers: [{
      listenAddresses: [':9981'],
      authenticationPolicy: { allow: {} },
    }],
    enablePrometheus: true,
    enablePprof: true,
  } },
  grpcServers: [{
    listenPaths: [statePath + '/bonanza_storage_frontend.sock'],
    authenticationPolicy: { allow: {} },
  }],

  objectStoreConcurrency: 100,
  maximumUnfinalizedDagsCount: 100,
  maximumUnfinalizedParentsLimit: {
    count: 1000,
    sizeBytes: 16 * 1024 * 1024,
  },
  watermarkPollInterval: '10s',

  shardsReplicaA: {
    [std.toString(shard)]: {
      client: { address: 'unix://%s/bonanza_storage_shard_a%s.sock' % [statePath, shard] },
      weight: 1,
    }
    for shard in std.range(0, shardsCount - 1)
  },
  shardsReplicaB: {
    [std.toString(shard)]: {
      client: { address: 'unix://%s/bonanza_storage_shard_b%s.sock' % [statePath, shard] },
      weight: 1,
    }
    for shard in std.range(0, shardsCount - 1)
  },
}
