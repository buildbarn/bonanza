package main

import (
	"context"
	"os"

	"bonanza.build/pkg/proto/configuration/bonanza_storage_frontend"
	dag_pb "bonanza.build/pkg/proto/storage/dag"
	object_pb "bonanza.build/pkg/proto/storage/object"
	tag_pb "bonanza.build/pkg/proto/storage/tag"
	"bonanza.build/pkg/storage/dag"
	"bonanza.build/pkg/storage/object"
	object_grpc "bonanza.build/pkg/storage/object/grpc"
	object_leaserenewing "bonanza.build/pkg/storage/object/leaserenewing"
	object_mirrored "bonanza.build/pkg/storage/object/mirrored"
	object_sharded "bonanza.build/pkg/storage/object/sharded"
	"bonanza.build/pkg/storage/tag"
	tag_grpc "bonanza.build/pkg/storage/tag/grpc"
	tag_leaserenewing "bonanza.build/pkg/storage/tag/leaserenewing"
	tag_mirrored "bonanza.build/pkg/storage/tag/mirrored"
	tag_sharded "bonanza.build/pkg/storage/tag/sharded"

	"github.com/buildbarn/bb-storage/pkg/global"
	bb_grpc "github.com/buildbarn/bb-storage/pkg/grpc"
	"github.com/buildbarn/bb-storage/pkg/program"
	"github.com/buildbarn/bb-storage/pkg/util"

	"golang.org/x/sync/semaphore"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func main() {
	program.RunMain(func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
		if len(os.Args) != 2 {
			return status.Error(codes.InvalidArgument, "Usage: bonanza_storage_frontend bonanza_storage_frontend.jsonnet")
		}
		var configuration bonanza_storage_frontend.ApplicationConfiguration
		if err := util.UnmarshalConfigurationFromFile(os.Args[1], &configuration); err != nil {
			return util.StatusWrapf(err, "Failed to read configuration from %s", os.Args[1])
		}
		lifecycleState, grpcClientFactory, err := global.ApplyConfiguration(configuration.Global, dependenciesGroup)
		if err != nil {
			return util.StatusWrap(err, "Failed to apply global configuration options")
		}

		if configuration.MaximumUnfinalizedParentsLimit == nil {
			return status.Error(codes.InvalidArgument, "No maximum unfinalized parents limit provided")
		}
		maximumUnfinalizedParentsLimit := object.NewLimit(configuration.MaximumUnfinalizedParentsLimit)

		// Construct object and tag stores for mirrored replicas.
		objectStoreA, tagStoreA, err := createShardsForReplica(grpcClientFactory, configuration.ShardsReplicaA, dependenciesGroup)
		if err != nil {
			return util.StatusWrap(err, "Failed to create storage shards for replica A")
		}
		objectStoreB, tagStoreB, err := createShardsForReplica(grpcClientFactory, configuration.ShardsReplicaB, dependenciesGroup)
		if err != nil {
			return util.StatusWrap(err, "Failed to create storage shards for replica B")
		}

		// Combine mirrored replicas together.
		objectDownloader := object_mirrored.NewDownloader(objectStoreA, objectStoreB)
		objectUploader := object_leaserenewing.NewUploader(
			object_mirrored.NewUploader(objectStoreA, objectStoreB),
			semaphore.NewWeighted(configuration.ObjectStoreConcurrency),
			maximumUnfinalizedParentsLimit,
		)
		dependenciesGroup.Go(func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
			for objectUploader.ProcessSingleObject(ctx) {
			}
			return nil
		})

		tagUpdater := tag_mirrored.NewUpdater(tagStoreA, tagStoreB)
		tagResolver := tag_leaserenewing.NewResolver(
			tag.NewStore(
				tag_mirrored.NewResolver(tagStoreA, tagStoreB),
				tagUpdater,
			),
			objectUploader,
		)

		if err := bb_grpc.NewServersFromConfigurationAndServe(
			configuration.GrpcServers,
			func(s grpc.ServiceRegistrar) {
				// Services for downloading DAGs.
				object_pb.RegisterDownloaderServer(
					s,
					object.NewDownloaderServer(objectDownloader),
				)
				tag_pb.RegisterResolverServer(
					s,
					tag.NewResolverServer(tagResolver),
				)

				// Services for uploading DAGs.
				dag_pb.RegisterUploaderServer(
					s,
					dag.NewUploaderServer(
						objectUploader,
						semaphore.NewWeighted(configuration.ObjectStoreConcurrency),
						tagUpdater,
						configuration.MaximumUnfinalizedDagsCount,
						maximumUnfinalizedParentsLimit,
					),
				)
			},
			siblingsGroup,
			grpcClientFactory,
		); err != nil {
			return util.StatusWrap(err, "gRPC server failure")
		}

		lifecycleState.MarkReadyAndWait(siblingsGroup)
		return nil
	})
}

func createShardsForReplica(grpcClientFactory bb_grpc.ClientFactory, shards map[string]*bonanza_storage_frontend.ApplicationConfiguration_Shard, dependenciesGroup program.Group) (object.Store[object.GlobalReference, []byte], tag.Store[object.Namespace, []byte], error) {
	// Create object & tag stores for each shard.
	shardNames := make([]string, 0, len(shards))
	weightedShards := make([]object_sharded.WeightedShard, 0, len(shards))
	objectDownloaders := make([]object.Downloader[object.GlobalReference], 0, len(shards))
	objectUploaders := make([]object.Uploader[object.GlobalReference, []byte], 0, len(shards))
	tagResolvers := make([]tag.Resolver[object.Namespace], 0, len(shards))
	tagUpdaters := make([]tag.Updater[object.Namespace, []byte], 0, len(shards))
	for key, shard := range shards {
		grpcClient, err := grpcClientFactory.NewClientFromConfiguration(shard.Client, dependenciesGroup)
		if err != nil {
			return nil, nil, util.StatusWrapf(err, "Failed to create gRPC client for shard with key %#v", key)
		}

		shardNames = append(shardNames, key)
		weightedShards = append(weightedShards, object_sharded.WeightedShard{
			Key:    []byte(key),
			Weight: shard.Weight,
		})
		objectDownloaders = append(objectDownloaders, object_grpc.NewGRPCDownloader(
			object_pb.NewDownloaderClient(grpcClient),
		))
		objectUploaders = append(objectUploaders, object_grpc.NewGRPCUploader(
			object_pb.NewUploaderClient(grpcClient),
		))
		tagResolvers = append(tagResolvers, tag_grpc.NewResolver(
			tag_pb.NewResolverClient(grpcClient),
		))
		tagUpdaters = append(tagUpdaters, tag_grpc.NewUpdater(
			tag_pb.NewUpdaterClient(grpcClient),
		))
	}

	// If we have multiple stores, instantiate the sharded backend.
	switch len(shards) {
	case 0:
		return nil, nil, status.Error(codes.InvalidArgument, "No shards provided")
	case 1:
		return object.NewStore(objectDownloaders[0], objectUploaders[0]),
			tag.NewStore(tagResolvers[0], tagUpdaters[0]),
			nil
	default:
		picker := object_sharded.NewWeightedRendezvousPicker(weightedShards)
		return object.NewStore(
				object_sharded.NewShardedDownloader(objectDownloaders, shardNames, picker),
				object_sharded.NewShardedUploader[object.GlobalReference, []byte](objectUploaders, shardNames, picker),
			),
			tag.NewStore(
				tag_sharded.NewResolver(tagResolvers, shardNames, picker),
				tag_sharded.NewUpdater[object.Namespace, []byte](tagUpdaters, shardNames, picker),
			),
			nil
	}
}
