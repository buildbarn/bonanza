package main

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"time"

	model_command "bonanza.build/pkg/model/command"
	model_executewithstorage "bonanza.build/pkg/model/executewithstorage"
	model_filesystem_virtual "bonanza.build/pkg/model/filesystem/virtual"
	model_parser "bonanza.build/pkg/model/parser"
	"bonanza.build/pkg/proto/configuration/bonanza_worker"
	remoteworker_pb "bonanza.build/pkg/proto/remoteworker"
	dag_pb "bonanza.build/pkg/proto/storage/dag"
	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/remoteworker"
	dag_grpc "bonanza.build/pkg/storage/dag/grpc"
	"bonanza.build/pkg/storage/object"
	object_existenceprecondition "bonanza.build/pkg/storage/object/existenceprecondition"
	object_grpc "bonanza.build/pkg/storage/object/grpc"
	object_local "bonanza.build/pkg/storage/object/local"
	object_readcaching "bonanza.build/pkg/storage/object/readcaching"

	re_clock "github.com/buildbarn/bb-remote-execution/pkg/clock"
	"github.com/buildbarn/bb-remote-execution/pkg/filesystem/pool"
	"github.com/buildbarn/bb-remote-execution/pkg/filesystem/virtual"
	virtual_configuration "github.com/buildbarn/bb-remote-execution/pkg/filesystem/virtual/configuration"
	runner_pb "github.com/buildbarn/bb-remote-execution/pkg/proto/runner"
	"github.com/buildbarn/bb-storage/pkg/clock"
	"github.com/buildbarn/bb-storage/pkg/global"
	"github.com/buildbarn/bb-storage/pkg/program"
	"github.com/buildbarn/bb-storage/pkg/random"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/buildbarn/bb-storage/pkg/x509"
	"github.com/google/uuid"

	"golang.org/x/sync/semaphore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func main() {
	program.RunMain(func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
		if len(os.Args) != 2 {
			return status.Error(codes.InvalidArgument, "Usage: bonanza_worker bonanza_worker.jsonnet")
		}
		var configuration bonanza_worker.ApplicationConfiguration
		if err := util.UnmarshalConfigurationFromFile(os.Args[1], &configuration); err != nil {
			return util.StatusWrapf(err, "Failed to read configuration from %s", os.Args[1])
		}
		lifecycleState, grpcClientFactory, err := global.ApplyConfiguration(configuration.Global, dependenciesGroup)
		if err != nil {
			return util.StatusWrap(err, "Failed to apply global configuration options")
		}

		// Storage access for reading commands and input files.
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
		objectContentsWalkerSemaphore := semaphore.NewWeighted(int64(runtime.NumCPU()))
		dagUploader := dag_grpc.NewUploader(
			dag_pb.NewUploaderClient(storageGRPCClient),
			objectContentsWalkerSemaphore,
			// Assume everything we attempt to upload is memory backed.
			object.Unlimited,
		)

		// Create connection with scheduler.
		schedulerConnection, err := grpcClientFactory.NewClientFromConfiguration(configuration.SchedulerGrpcClient, dependenciesGroup)
		if err != nil {
			return util.StatusWrap(err, "Failed to create scheduler RPC client")
		}
		schedulerClient := remoteworker_pb.NewOperationQueueClient(schedulerConnection)

		// Location for storing temporary file objects. This is
		// currently only used by the virtual file system to store
		// output files of build actions.
		filePool, err := pool.NewFilePoolFromConfiguration(configuration.FilePool)
		if err != nil {
			return util.StatusWrap(err, "Failed to create file pool")
		}

		objectStoreSemaphore := semaphore.NewWeighted(configuration.ObjectStoreConcurrency)
		for _, buildDirectoryConfiguration := range configuration.BuildDirectories {
			mount, handleAllocator, err := virtual_configuration.NewMountFromConfiguration(
				buildDirectoryConfiguration.Mount,
				"bonanza_worker",
				/* rootDirectory = */ virtual_configuration.NoAttributeCaching,
				/* childDirectories = */ virtual_configuration.LongAttributeCaching,
				/* leaves = */ virtual_configuration.LongAttributeCaching,
				/* caseSensitive = */ true,
			)
			if err != nil {
				return util.StatusWrap(err, "Failed to create build directory mount")
			}

			rootDirectory := model_filesystem_virtual.NewWorkerTopLevelDirectory(handleAllocator.New())
			symlinkFactory := virtual.NewHandleAllocatingSymlinkFactory(
				virtual.BaseSymlinkFactory,
				handleAllocator.New(),
			)

			if err := mount.Expose(dependenciesGroup, rootDirectory); err != nil {
				return util.StatusWrap(err, "Failed to expose build directory mount")
			}

			if len(buildDirectoryConfiguration.Runners) == 0 {
				return util.StatusWrap(err, "Cannot start worker without any runners")
			}
			for _, runnerConfiguration := range buildDirectoryConfiguration.Runners {
				if runnerConfiguration.Concurrency < 1 {
					return status.Error(codes.InvalidArgument, "Runner concurrency must be positive")
				}
				concurrencyLength := len(strconv.FormatUint(runnerConfiguration.Concurrency-1, 10))

				if err := runnerConfiguration.MaximumExecutionTimeoutCompensation.CheckValid(); err != nil {
					return util.StatusWrap(err, "Invalid maximum execution timeout compensation")
				}
				maximumExecutionTimeoutCompensation := runnerConfiguration.MaximumExecutionTimeoutCompensation.AsDuration()
				if err := runnerConfiguration.MaximumWritableFileUploadDelay.CheckValid(); err != nil {
					return util.StatusWrap(err, "Invalid maximum writable file upload delay")
				}
				maximumWritableFileUploadDelay := runnerConfiguration.MaximumWritableFileUploadDelay.AsDuration()

				platformPrivateKeys, err := remoteworker.ParsePlatformPrivateKeys(runnerConfiguration.PlatformPrivateKeys)
				if err != nil {
					return err
				}
				clientCertificateVerifier, err := x509.NewClientCertificateVerifierFromConfiguration(runnerConfiguration.ClientCertificateVerifier, dependenciesGroup)
				if err != nil {
					return err
				}

				hiddenFilesPattern := func(s string) bool { return false }
				if pattern := runnerConfiguration.HiddenFilesPattern; pattern != "" {
					hiddenFilesRegexp, err := regexp.Compile(pattern)
					if err != nil {
						return util.StatusWrap(err, "Failed to parse hidden files pattern")
					}
					hiddenFilesPattern = hiddenFilesRegexp.MatchString
				}

				initialContentsSorter := sort.Sort
				if runnerConfiguration.ShuffleDirectoryListings {
					initialContentsSorter = virtual.Shuffle
				}

				// Execute commands using a separate runner process. Due to the
				// interaction between threads, forking and execve() returning
				// ETXTBSY, concurrent execution of build actions can only be
				// used in combination with a runner process. Having a separate
				// runner process also makes it possible to apply privilege
				// separation.
				runnerConnection, err := grpcClientFactory.NewClientFromConfiguration(runnerConfiguration.Endpoint, dependenciesGroup)
				if err != nil {
					return util.StatusWrap(err, "Failed to create runner RPC client")
				}
				runnerClient := runner_pb.NewRunnerClient(runnerConnection)

				for threadID := uint64(0); threadID < runnerConfiguration.Concurrency; threadID++ {
					suspendableClock := re_clock.NewSuspendableClock(
						clock.SystemClock,
						maximumExecutionTimeoutCompensation,
						time.Second/10,
					)

					workerID := map[string]string{}
					if runnerConfiguration.Concurrency > 1 {
						workerID["thread"] = fmt.Sprintf("%0*d", concurrencyLength, threadID)
					}
					maps.Copy(workerID, runnerConfiguration.WorkerId)
					workerName, err := json.Marshal(workerID)
					if err != nil {
						return util.StatusWrap(err, "Failed to marshal worker ID")
					}

					executor := model_command.NewLocalExecutor(
						objectDownloader,
						objectStoreSemaphore,
						parsedObjectPool,
						dagUploader,
						objectContentsWalkerSemaphore,
						rootDirectory,
						handleAllocator,
						pool.NewQuotaEnforcingFilePool(
							filePool,
							runnerConfiguration.MaximumFilePoolFileCount,
							runnerConfiguration.MaximumFilePoolSizeBytes,
						),
						symlinkFactory,
						initialContentsSorter,
						hiddenFilesPattern,
						runnerClient,
						suspendableClock,
						uuid.NewRandom,
						maximumWritableFileUploadDelay,
						runnerConfiguration.EnvironmentVariables,
						runnerConfiguration.BuildDirectoryOwnerUserId,
						runnerConfiguration.BuildDirectoryOwnerGroupId,
						maximumExecutionTimeoutCompensation,
						workerID,
					)

					client, err := remoteworker.NewClient(
						schedulerClient,
						remoteworker.NewProtoExecutor(
							model_executewithstorage.NewExecutor(executor),
						),
						clock.SystemClock,
						random.CryptoThreadSafeGenerator,
						platformPrivateKeys,
						clientCertificateVerifier,
						workerID,
						runnerConfiguration.SizeClass,
						runnerConfiguration.IsLargestSizeClass,
					)
					if err != nil {
						return util.StatusWrap(err, "Failed to create remote worker client")
					}
					remoteworker.LaunchWorkerThread(siblingsGroup, client.Run, string(workerName))
				}
			}
		}

		lifecycleState.MarkReadyAndWait(siblingsGroup)
		return nil
	})
}
