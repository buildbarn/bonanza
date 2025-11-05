package main

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"os"
	"runtime"
	"strconv"

	model_executewithstorage "bonanza.build/pkg/model/executewithstorage"
	model_fetch "bonanza.build/pkg/model/fetch"
	model_parser "bonanza.build/pkg/model/parser"
	"bonanza.build/pkg/proto/configuration/bonanza_fetcher"
	remoteworker_pb "bonanza.build/pkg/proto/remoteworker"
	dag_pb "bonanza.build/pkg/proto/storage/dag"
	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/remoteworker"
	object_existenceprecondition "bonanza.build/pkg/storage/object/existenceprecondition"
	object_grpc "bonanza.build/pkg/storage/object/grpc"
	object_local "bonanza.build/pkg/storage/object/local"
	object_readcaching "bonanza.build/pkg/storage/object/readcaching"

	"github.com/buildbarn/bb-remote-execution/pkg/filesystem/pool"
	"github.com/buildbarn/bb-storage/pkg/clock"
	"github.com/buildbarn/bb-storage/pkg/filesystem"
	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
	"github.com/buildbarn/bb-storage/pkg/global"
	http_client "github.com/buildbarn/bb-storage/pkg/http/client"
	"github.com/buildbarn/bb-storage/pkg/program"
	"github.com/buildbarn/bb-storage/pkg/random"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/buildbarn/bb-storage/pkg/x509"

	"golang.org/x/sync/semaphore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func main() {
	program.RunMain(func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
		if len(os.Args) != 2 {
			return status.Error(codes.InvalidArgument, "Usage: bonanza_fetcher bonanza_fetcher.jsonnet")
		}
		var configuration bonanza_fetcher.ApplicationConfiguration
		if err := util.UnmarshalConfigurationFromFile(os.Args[1], &configuration); err != nil {
			return util.StatusWrapf(err, "Failed to read configuration from %s", os.Args[1])
		}
		lifecycleState, grpcClientFactory, err := global.ApplyConfiguration(configuration.Global, dependenciesGroup)
		if err != nil {
			return util.StatusWrap(err, "Failed to apply global configuration options")
		}

		storageGRPCClient, err := grpcClientFactory.NewClientFromConfiguration(configuration.StorageGrpcClient, dependenciesGroup)
		if err != nil {
			return util.StatusWrap(err, "Failed to create storage gRPC client")
		}
		objectDownloader := object_existenceprecondition.NewDownloader(
			object_grpc.NewDownloader(
				object_pb.NewDownloaderClient(storageGRPCClient),
			),
		)
		if configuration.LocalObjectStore != nil {
			localObjectStore, err := object_local.NewStoreFromConfiguration(
				dependenciesGroup,
				configuration.LocalObjectStore,
			)
			if err != nil {
				return util.StatusWrap(err, "Failed to create local object store")
			}
			objectDownloader = object_readcaching.NewDownloader(
				objectDownloader,
				localObjectStore,
			)
		}

		parsedObjectPool, err := model_parser.NewParsedObjectPoolFromConfiguration(configuration.ParsedObjectPool)
		if err != nil {
			return util.StatusWrap(err, "Failed to create parsed object pool")
		}
		dagUploaderClient := dag_pb.NewUploaderClient(storageGRPCClient)
		objectContentsWalkerSemaphore := semaphore.NewWeighted(int64(runtime.NumCPU()))

		roundTripper, err := http_client.NewRoundTripperFromConfiguration(configuration.HttpClient)
		if err != nil {
			return util.StatusWrap(err, "Failed to create HTTP client")
		}

		filePool, err := pool.NewFilePoolFromConfiguration(configuration.FilePool)
		if err != nil {
			return util.StatusWrap(err, "Failed to create file pool")
		}

		cacheDirectory, err := filesystem.NewLocalDirectory(path.LocalFormat.NewParser(configuration.CacheDirectoryPath))
		if err != nil {
			return util.StatusWrap(err, "Failed to create cache directory")
		}

		remoteWorkerConnection, err := grpcClientFactory.NewClientFromConfiguration(configuration.RemoteWorkerGrpcClient, dependenciesGroup)
		if err != nil {
			return util.StatusWrap(err, "Failed to create remote worker RPC client")
		}
		remoteWorkerClient := remoteworker_pb.NewOperationQueueClient(remoteWorkerConnection)
		platformPrivateKeys, err := remoteworker.ParsePlatformPrivateKeys(configuration.PlatformPrivateKeys)
		if err != nil {
			return err
		}
		clientCertificateVerifier, err := x509.NewClientCertificateVerifierFromConfiguration(configuration.ClientCertificateVerifier, dependenciesGroup)
		if err != nil {
			return err
		}

		concurrencyLength := len(strconv.FormatUint(configuration.Concurrency-1, 10))
		for threadID := uint64(0); threadID < configuration.Concurrency; threadID++ {
			workerID := map[string]string{}
			if configuration.Concurrency > 1 {
				workerID["thread"] = fmt.Sprintf("%0*d", concurrencyLength, threadID)
			}
			maps.Copy(workerID, configuration.WorkerId)
			workerName, err := json.Marshal(workerID)
			if err != nil {
				return util.StatusWrap(err, "Failed to marshal worker ID")
			}

			client, err := remoteworker.NewClient(
				remoteWorkerClient,
				remoteworker.NewProtoExecutor(
					model_executewithstorage.NewExecutor(
						model_fetch.NewLocalExecutor(
							objectDownloader,
							parsedObjectPool,
							dagUploaderClient,
							objectContentsWalkerSemaphore,
							&http.Client{Transport: roundTripper},
							filePool,
							cacheDirectory,
						),
					),
				),
				clock.SystemClock,
				random.CryptoThreadSafeGenerator,
				platformPrivateKeys,
				clientCertificateVerifier,
				workerID,
				/* sizeClass = */ 0,
				/* isLargestSizeClass = */ true,
			)
			if err != nil {
				return util.StatusWrap(err, "Failed to create remote worker client")
			}
			remoteworker.LaunchWorkerThread(siblingsGroup, client.Run, string(workerName))
		}

		lifecycleState.MarkReadyAndWait(siblingsGroup)
		return nil
	})
}
