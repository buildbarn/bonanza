package main

import (
	"context"
	"encoding/json"
	"os"
	"runtime"

	"bonanza.build/pkg/crypto"
	model_analysis "bonanza.build/pkg/model/analysis"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/buffered"
	model_evaluation "bonanza.build/pkg/model/evaluation"
	model_executewithstorage "bonanza.build/pkg/model/executewithstorage"
	model_parser "bonanza.build/pkg/model/parser"
	model_starlark "bonanza.build/pkg/model/starlark"
	"bonanza.build/pkg/proto/configuration/bonanza_builder"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_executewithstorage_pb "bonanza.build/pkg/proto/model/executewithstorage"
	remoteexecution_pb "bonanza.build/pkg/proto/remoteexecution"
	remoteworker_pb "bonanza.build/pkg/proto/remoteworker"
	dag_pb "bonanza.build/pkg/proto/storage/dag"
	object_pb "bonanza.build/pkg/proto/storage/object"
	remoteexecution "bonanza.build/pkg/remoteexecution"
	"bonanza.build/pkg/remoteworker"
	object_existenceprecondition "bonanza.build/pkg/storage/object/existenceprecondition"
	object_grpc "bonanza.build/pkg/storage/object/grpc"

	"github.com/buildbarn/bb-remote-execution/pkg/filesystem/pool"
	"github.com/buildbarn/bb-storage/pkg/clock"
	"github.com/buildbarn/bb-storage/pkg/global"
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
			return status.Error(codes.InvalidArgument, "Usage: bonanza_builder bonanza_builder.jsonnet")
		}
		var configuration bonanza_builder.ApplicationConfiguration
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
			object_grpc.NewGRPCDownloader(
				object_pb.NewDownloaderClient(storageGRPCClient),
			),
		)
		parsedObjectPool, err := model_parser.NewParsedObjectPoolFromConfiguration(configuration.ParsedObjectPool)
		if err != nil {
			return util.StatusWrap(err, "Failed to create parsed object pool")
		}

		filePool, err := pool.NewFilePoolFromConfiguration(configuration.FilePool)
		if err != nil {
			return util.StatusWrap(err, "Failed to create file pool")
		}

		executionGRPCClient, err := grpcClientFactory.NewClientFromConfiguration(configuration.ExecutionGrpcClient, dependenciesGroup)
		if err != nil {
			return util.StatusWrap(err, "Failed to create execution gRPC client")
		}

		executionClientPrivateKey, err := crypto.ParsePEMWithPKCS8ECDHPrivateKey([]byte(configuration.ExecutionClientPrivateKey))
		if err != nil {
			return util.StatusWrap(err, "Failed to parse execution client private key")
		}
		executionClientCertificateChain, err := remoteexecution.ParseCertificateChain([]byte(configuration.ExecutionClientCertificateChain))
		if err != nil {
			return util.StatusWrap(err, "Failed to parse execution client certificate chain")
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
		workerName, err := json.Marshal(configuration.WorkerId)
		if err != nil {
			return util.StatusWrap(err, "Failed to marshal worker ID")
		}

		bzlFileBuiltins, buildFileBuiltins := model_starlark.GetBuiltins[buffered.Reference, *model_core.LeakCheckingReferenceMetadata[buffered.ReferenceMetadata]]()
		client, err := remoteworker.NewClient(
			remoteWorkerClient,
			remoteworker.NewProtoExecutor(
				model_executewithstorage.NewExecutor(
					model_evaluation.NewExecutor(
						objectDownloader,
						model_analysis.NewBaseComputerFactory[buffered.Reference, *model_core.LeakCheckingReferenceMetadata[buffered.ReferenceMetadata]](
							filePool,
							model_executewithstorage.NewProtoClient(
								remoteexecution.NewProtoClient[*model_executewithstorage_pb.Action, model_core_pb.WeakDecodableReference, model_core_pb.WeakDecodableReference](
									remoteexecution.NewRemoteClient(
										remoteexecution_pb.NewExecutionClient(executionGRPCClient),
										executionClientPrivateKey,
										executionClientCertificateChain,
									),
								),
							),
							bzlFileBuiltins,
							buildFileBuiltins,
						),
						parsedObjectPool,
						dag_pb.NewUploaderClient(storageGRPCClient),
						semaphore.NewWeighted(int64(runtime.NumCPU())),
						configuration.EvaluationConcurrency,
						clock.SystemClock,
					),
				),
			),
			clock.SystemClock,
			random.CryptoThreadSafeGenerator,
			platformPrivateKeys,
			clientCertificateVerifier,
			configuration.WorkerId,
			/* sizeClass = */ 0,
			/* isLargestSizeClass = */ true,
		)
		if err != nil {
			return util.StatusWrap(err, "Failed to create remote worker client")
		}
		remoteworker.LaunchWorkerThread(siblingsGroup, client.Run, string(workerName))

		lifecycleState.MarkReadyAndWait(siblingsGroup)
		return nil
	})
}
