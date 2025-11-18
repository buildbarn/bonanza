package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"os"
	"runtime"
	"time"

	model_encoding "bonanza.build/pkg/model/encoding"
	model_parser "bonanza.build/pkg/model/parser"
	model_tag "bonanza.build/pkg/model/tag"
	buildqueuestate_pb "bonanza.build/pkg/proto/buildqueuestate"
	"bonanza.build/pkg/proto/configuration/bonanza_scheduler"
	model_initialsizeclass_pb "bonanza.build/pkg/proto/model/initialsizeclass"
	remoteexecution_pb "bonanza.build/pkg/proto/remoteexecution"
	remoteworker_pb "bonanza.build/pkg/proto/remoteworker"
	dag_pb "bonanza.build/pkg/proto/storage/dag"
	object_pb "bonanza.build/pkg/proto/storage/object"
	tag_pb "bonanza.build/pkg/proto/storage/tag"
	"bonanza.build/pkg/scheduler"
	"bonanza.build/pkg/scheduler/initialsizeclass"
	"bonanza.build/pkg/scheduler/routing"
	dag_grpc "bonanza.build/pkg/storage/dag/grpc"
	dag_namespacemapping "bonanza.build/pkg/storage/dag/namespacemapping"
	"bonanza.build/pkg/storage/object"
	object_grpc "bonanza.build/pkg/storage/object/grpc"
	object_namespacemapping "bonanza.build/pkg/storage/object/namespacemapping"
	tag_grpc "bonanza.build/pkg/storage/tag/grpc"
	tag_namespacemapping "bonanza.build/pkg/storage/tag/namespacemapping"

	"github.com/buildbarn/bb-storage/pkg/clock"
	"github.com/buildbarn/bb-storage/pkg/global"
	bb_grpc "github.com/buildbarn/bb-storage/pkg/grpc"
	"github.com/buildbarn/bb-storage/pkg/program"
	"github.com/buildbarn/bb-storage/pkg/random"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/google/uuid"

	"golang.org/x/sync/semaphore"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func main() {
	program.RunMain(func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
		if len(os.Args) != 2 {
			return status.Error(codes.InvalidArgument, "Usage: bonanza_scheduler bonanza_scheduler.jsonnet")
		}
		var configuration bonanza_scheduler.ApplicationConfiguration
		if err := util.UnmarshalConfigurationFromFile(os.Args[1], &configuration); err != nil {
			return util.StatusWrapf(err, "Failed to read configuration from %s", os.Args[1])
		}
		lifecycleState, grpcClientFactory, err := global.ApplyConfiguration(configuration.Global, dependenciesGroup)
		if err != nil {
			return util.StatusWrap(err, "Failed to apply global configuration options")
		}

		var previousExecutionStatsStore initialsizeclass.PreviousExecutionStatsStore
		var previousExecutionStatsCommonKeyHash [sha256.Size]byte
		if storeConfiguration := configuration.PreviousExecutionStatsStore; storeConfiguration != nil {
			grpcClient, err := grpcClientFactory.NewClientFromConfiguration(storeConfiguration.GrpcClient, dependenciesGroup)
			if err != nil {
				return util.StatusWrap(err, "Failed to create gRPC client for PreviousExecutionStats store")
			}
			namespace, err := object.NewNamespace(storeConfiguration.Namespace)
			if err != nil {
				return util.StatusWrap(err, "Invalid namespace for PreviousExecutionStats store")
			}

			tagSignaturePrivateKeyBlock, _ := pem.Decode(storeConfiguration.TagSignaturePrivateKey)
			if tagSignaturePrivateKeyBlock == nil {
				return status.Error(codes.InvalidArgument, "Tag signature private key store does not contain a PEM block")
			}
			if tagSignaturePrivateKeyBlock.Type != "PRIVATE KEY" {
				return status.Error(codes.InvalidArgument, "Tag signature private key PEM block is not of type PRIVATE KEY")
			}
			tagSignaturePrivateKey, err := x509.ParsePKCS8PrivateKey(tagSignaturePrivateKeyBlock.Bytes)
			if err != nil {
				return util.StatusWrapWithCode(err, codes.InvalidArgument, "Invalid tag signature private key")
			}
			tagSignatureEd25519PrivateKey, ok := tagSignaturePrivateKey.(ed25519.PrivateKey)
			if !ok {
				return status.Error(codes.InvalidArgument, "Tag signature private key is not of type Ed25519")
			}

			referenceFormat := namespace.ReferenceFormat
			objectEncoder, err := model_encoding.NewKeyedBinaryEncoderFromProto(
				storeConfiguration.ObjectEncoders,
				uint32(referenceFormat.GetMaximumObjectSizeBytes()),
			)
			if err != nil {
				return util.StatusWrap(err, "Failed to create object encoder for PreviousExecutionStats store")
			}

			previousExecutionStatsStore = model_tag.NewStorageBackedMutableProtoStore(
				referenceFormat,
				tag_namespacemapping.NewNamespaceAddingResolver(
					tag_grpc.NewResolver(tag_pb.NewResolverClient(grpcClient)),
					namespace.InstanceName,
				),
				tagSignatureEd25519PrivateKey,
				model_parser.NewParsedObjectReader(
					model_parser.NewDownloadingObjectReader(
						object_namespacemapping.NewNamespaceAddingDownloader(
							object_grpc.NewDownloader(object_pb.NewDownloaderClient(grpcClient)),
							namespace.InstanceName,
						),
					),
					model_parser.NewChainedObjectParser(
						model_parser.NewEncodedObjectParser[object.LocalReference](objectEncoder),
						model_parser.NewProtoObjectParser[object.LocalReference, model_initialsizeclass_pb.PreviousExecutionStats](),
					),
				),
				objectEncoder,
				dag_namespacemapping.NewNamespaceAddingUploader(
					dag_grpc.NewUploader(
						dag_pb.NewUploaderClient(grpcClient),
						semaphore.NewWeighted(int64(runtime.NumCPU())),
						// Assume everything we attempt to upload is memory backed.
						object.Unlimited,
					),
					namespace.InstanceName,
				),
				clock.SystemClock,
			)
		}

		// Create an action router that is responsible for analyzing
		// incoming execution requests and determining how they are
		// scheduled.
		actionRouter, err := routing.NewActionRouterFromConfiguration(configuration.ActionRouter, previousExecutionStatsStore, previousExecutionStatsCommonKeyHash)
		if err != nil {
			return util.StatusWrap(err, "Failed to create action router")
		}

		platformQueueWithNoWorkersTimeout := configuration.PlatformQueueWithNoWorkersTimeout
		if err := platformQueueWithNoWorkersTimeout.CheckValid(); err != nil {
			return util.StatusWrap(err, "Invalid platform queue with no workers timeout")
		}

		// Create in-memory build queue.
		generator := random.NewFastSingleThreadedGenerator()
		buildQueue := scheduler.NewInMemoryBuildQueue(
			clock.SystemClock,
			uuid.NewRandom,
			random.CryptoThreadSafeGenerator,
			&scheduler.InMemoryBuildQueueConfiguration{
				ExecutionUpdateInterval:           time.Minute,
				OperationWithNoWaitersTimeout:     time.Minute,
				PlatformQueueWithNoWorkersTimeout: platformQueueWithNoWorkersTimeout.AsDuration(),
				BusyWorkerSynchronizationInterval: 10 * time.Second,
				GetIdleWorkerSynchronizationInterval: func() time.Duration {
					// Let synchronization calls block somewhere
					// between 0 and 2 minutes. Add jitter to
					// prevent recurring traffic spikes.
					return random.Duration(generator, 2*time.Minute)
				},
				WorkerTaskRetryCount:                  9,
				WorkerWithNoSynchronizationsTimeout:   time.Minute,
				VerificationPrivateKeyRefreshInterval: time.Hour,
			},
			actionRouter,
		)

		// Create predeclared platform queues.
		for platformQueueIndex, platformQueue := range configuration.PredeclaredPlatformQueues {
			workerInvocationStickinessLimits := make([]time.Duration, 0, len(platformQueue.WorkerInvocationStickinessLimits))
			for i, d := range platformQueue.WorkerInvocationStickinessLimits {
				if err := d.CheckValid(); err != nil {
					return util.StatusWrapf(err, "Invalid worker invocation stickiness limit at index %d: %s", i)
				}
				workerInvocationStickinessLimits = append(workerInvocationStickinessLimits, d.AsDuration())
			}

			if err := buildQueue.RegisterPredeclaredPlatformQueue(
				platformQueue.PkixPublicKeys,
				workerInvocationStickinessLimits,
				int(platformQueue.MaximumQueuedBackgroundLearningOperations),
				platformQueue.BackgroundLearningOperationPriority,
				platformQueue.SizeClasses,
			); err != nil {
				return util.StatusWrapf(err, "Failed to register predeclared platform queue at index %d", platformQueueIndex)
			}
		}

		// Spawn gRPC servers for client and worker traffic.
		if err := bb_grpc.NewServersFromConfigurationAndServe(
			configuration.ClientGrpcServers,
			func(s grpc.ServiceRegistrar) {
				remoteexecution_pb.RegisterExecutionServer(s, buildQueue)
			},
			siblingsGroup,
			grpcClientFactory,
		); err != nil {
			return util.StatusWrap(err, "Client gRPC server failure")
		}
		if err := bb_grpc.NewServersFromConfigurationAndServe(
			configuration.WorkerGrpcServers,
			func(s grpc.ServiceRegistrar) {
				remoteworker_pb.RegisterOperationQueueServer(s, buildQueue)
			},
			siblingsGroup,
			grpcClientFactory,
		); err != nil {
			return util.StatusWrap(err, "Worker gRPC server failure")
		}
		if err := bb_grpc.NewServersFromConfigurationAndServe(
			configuration.BuildQueueStateGrpcServers,
			func(s grpc.ServiceRegistrar) {
				buildqueuestate_pb.RegisterBuildQueueStateServer(s, buildQueue)
			},
			siblingsGroup,
			grpcClientFactory,
		); err != nil {
			return util.StatusWrap(err, "Build queue state gRPC server failure")
		}

		lifecycleState.MarkReadyAndWait(siblingsGroup)
		return nil
	})
}
