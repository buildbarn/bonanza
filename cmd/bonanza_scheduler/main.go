package main

import (
	"context"
	"crypto/sha256"
	"os"
	"time"

	buildqueuestate_pb "bonanza.build/pkg/proto/buildqueuestate"
	"bonanza.build/pkg/proto/configuration/bonanza_scheduler"
	remoteexecution_pb "bonanza.build/pkg/proto/remoteexecution"
	remoteworker_pb "bonanza.build/pkg/proto/remoteworker"
	"bonanza.build/pkg/scheduler"
	"bonanza.build/pkg/scheduler/initialsizeclass"
	"bonanza.build/pkg/scheduler/routing"

	"github.com/buildbarn/bb-storage/pkg/clock"
	"github.com/buildbarn/bb-storage/pkg/global"
	bb_grpc "github.com/buildbarn/bb-storage/pkg/grpc"
	"github.com/buildbarn/bb-storage/pkg/program"
	"github.com/buildbarn/bb-storage/pkg/random"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/google/uuid"

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

		// TODO: Let the scheduler talk to storage, so it can
		// store previous execution stats.
		var previousExecutionStatsStore initialsizeclass.PreviousExecutionStatsStore
		var previousExecutionStatsCommonKeyHash [sha256.Size]byte

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
