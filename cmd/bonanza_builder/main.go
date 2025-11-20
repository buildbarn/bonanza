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
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_executewithstorage_pb "bonanza.build/pkg/proto/model/executewithstorage"
	remoteexecution_pb "bonanza.build/pkg/proto/remoteexecution"
	remoteworker_pb "bonanza.build/pkg/proto/remoteworker"
	dag_pb "bonanza.build/pkg/proto/storage/dag"
	object_pb "bonanza.build/pkg/proto/storage/object"
	remoteexecution "bonanza.build/pkg/remoteexecution"
	"bonanza.build/pkg/remoteworker"
	dag_grpc "bonanza.build/pkg/storage/dag/grpc"
	"bonanza.build/pkg/storage/object"
	object_existenceprecondition "bonanza.build/pkg/storage/object/existenceprecondition"
	object_grpc "bonanza.build/pkg/storage/object/grpc"
	object_local "bonanza.build/pkg/storage/object/local"
	object_readcaching "bonanza.build/pkg/storage/object/readcaching"

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
	"google.golang.org/protobuf/proto"
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
		executionClientCertificateChain, err := crypto.ParsePEMWithCertificateChain([]byte(configuration.ExecutionClientCertificateChain))
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
						&queuesFactory[buffered.Reference, buffered.ReferenceMetadata]{
							local:  model_evaluation.NewSimpleRecursiveComputerQueuesFactory[buffered.Reference, buffered.ReferenceMetadata](configuration.LocalEvaluationConcurrency),
							remote: model_evaluation.NewSimpleRecursiveComputerQueuesFactory[buffered.Reference, buffered.ReferenceMetadata](configuration.RemoteEvaluationConcurrency),
						},
						parsedObjectPool,
						dag_grpc.NewUploader(
							dag_pb.NewUploaderClient(storageGRPCClient),
							semaphore.NewWeighted(int64(runtime.NumCPU())),
							// Assume everything we attempt to upload is memory backed.
							object.Unlimited,
						),
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

// queuesFactory is responsible for creating scheduling queues used by
// RecursiveComputer. In our case we want to let it be backed by two
// queues: one for running local evaluation steps (having a lower
// concurrency) and one for running remote evaluation steps (having a
// higher concurrency).
type queuesFactory[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	local  model_evaluation.RecursiveComputerQueuesFactory[TReference, TMetadata]
	remote model_evaluation.RecursiveComputerQueuesFactory[TReference, TMetadata]
}

func (qf *queuesFactory[TReference, TMetadata]) NewQueues() model_evaluation.RecursiveComputerQueues[TReference, TMetadata] {
	return &queues[TReference, TMetadata]{
		local:  qf.local.NewQueues(),
		remote: qf.remote.NewQueues(),
	}
}

type queues[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	local  model_evaluation.RecursiveComputerQueues[TReference, TMetadata]
	remote model_evaluation.RecursiveComputerQueues[TReference, TMetadata]
}

func (q *queues[TReference, TMetadata]) PickQueue(key model_core.Message[proto.Message, TReference]) *model_evaluation.RecursiveComputerQueue[TReference, TMetadata] {
	switch key.Message.(type) {
	case *model_analysis_pb.HttpFileContents_Key:
	case *model_analysis_pb.RawActionResult_Key:
		// Run evaluation steps that call into the remote
		// execution client with a higher concurrency.
		return q.remote.PickQueue(key)
	}

	// Run all other evaluation steps that run locally with a lower
	// concurrency.
	return q.local.PickQueue(key)
}

func (q *queues[TReference, TMetadata]) ProcessAllQueuedKeys(group program.Group, computer *model_evaluation.RecursiveComputer[TReference, TMetadata]) {
	q.local.ProcessAllQueuedKeys(group, computer)
	q.remote.ProcessAllQueuedKeys(group, computer)
}
