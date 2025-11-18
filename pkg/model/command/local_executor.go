package command

import (
	"context"
	"errors"
	"io/fs"
	"maps"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_executewithstorage "bonanza.build/pkg/model/executewithstorage"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_filesystem_virtual "bonanza.build/pkg/model/filesystem/virtual"
	model_parser "bonanza.build/pkg/model/parser"
	model_command_pb "bonanza.build/pkg/proto/model/command"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	remoteworker_pb "bonanza.build/pkg/proto/remoteworker"
	"bonanza.build/pkg/remoteworker"
	"bonanza.build/pkg/storage/dag"
	"bonanza.build/pkg/storage/object"
	object_namespacemapping "bonanza.build/pkg/storage/object/namespacemapping"
	object_suspending "bonanza.build/pkg/storage/object/suspending"

	re_clock "github.com/buildbarn/bb-remote-execution/pkg/clock"
	"github.com/buildbarn/bb-remote-execution/pkg/filesystem/pool"
	"github.com/buildbarn/bb-remote-execution/pkg/filesystem/virtual"
	runner_pb "github.com/buildbarn/bb-remote-execution/pkg/proto/runner"
	"github.com/buildbarn/bb-storage/pkg/clock"
	"github.com/buildbarn/bb-storage/pkg/filesystem"
	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/google/uuid"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// Filenames of objects to be created inside the build directory.
var (
	stdoutComponent              = path.MustNewComponent("stdout")
	stderrComponent              = path.MustNewComponent("stderr")
	inputRootDirectoryComponent  = path.MustNewComponent("root")
	serverLogsDirectoryComponent = path.MustNewComponent("server_logs")
	temporaryDirectoryComponent  = path.MustNewComponent("tmp")
	checkReadinessComponent      = path.MustNewComponent("check_readiness")
	stableInputRootComponent     = path.MustNewComponent("stable")
)

// capturingErrorLogger is an error logger that stores up to a single
// error. When the error is stored, a context cancelation function is
// invoked. This is used by localBuildExecutor to kill a build action in
// case an I/O error occurs on the FUSE file system.
type capturingErrorLogger struct {
	lock   sync.Mutex
	cancel context.CancelFunc
	error  error
}

func (el *capturingErrorLogger) Log(err error) {
	el.lock.Lock()
	defer el.lock.Unlock()

	if el.cancel != nil {
		el.error = err
		el.cancel()
		el.cancel = nil
	}
}

func (el *capturingErrorLogger) GetError() error {
	el.lock.Lock()
	defer el.lock.Unlock()

	return el.error
}

// TopLevelDirectory represents the top-level directory that is exposed
// as a virtual file system in which builds take place.
//
// Whenever an action needs to be run, the executor creates a new
// virtual file system directory and attaches it to the top-level
// directory. It is removed after the action has finished executing.
type TopLevelDirectory interface {
	AddChild(ctx context.Context, name path.Component, child virtual.DirectoryChild) error
	RemoveChild(name path.Component)
}

type localExecutor struct {
	objectDownloader                    object.Downloader[object.GlobalReference]
	parsedObjectPool                    *model_parser.ParsedObjectPool
	dagUploader                         dag.Uploader[object.InstanceName, object.GlobalReference]
	objectContentsWalkerSemaphore       *semaphore.Weighted
	topLevelDirectory                   TopLevelDirectory
	handleAllocator                     virtual.StatefulHandleAllocator
	filePool                            pool.FilePool
	symlinkFactory                      virtual.SymlinkFactory
	initialContentsSorter               virtual.Sorter
	hiddenFilesMatcher                  virtual.StringMatcher
	runner                              runner_pb.RunnerClient
	clock                               clock.Clock
	uuidGenerator                       util.UUIDGenerator
	maximumWritableFileUploadDelay      time.Duration
	environmentVariables                map[string]string
	buildDirectoryOwnerUserID           uint32
	buildDirectoryOwnerGroupID          uint32
	readinessCheckingDirectory          virtual.Directory
	maximumExecutionTimeoutCompensation time.Duration
	workerID                            map[string]string
}

func NewLocalExecutor(
	objectDownloader object.Downloader[object.GlobalReference],
	parsedObjectPool *model_parser.ParsedObjectPool,
	dagUploader dag.Uploader[object.InstanceName, object.GlobalReference],
	objectContentsWalkerSemaphore *semaphore.Weighted,
	topLevelDirectory TopLevelDirectory,
	handleAllocator virtual.StatefulHandleAllocator,
	filePool pool.FilePool,
	symlinkFactory virtual.SymlinkFactory,
	initialContentsSorter virtual.Sorter,
	hiddenFilesMatcher virtual.StringMatcher,
	runner runner_pb.RunnerClient,
	clock clock.Clock,
	uuidGenerator util.UUIDGenerator,
	maximumWritableFileUploadDelay time.Duration,
	environmentVariables map[string]string,
	buildDirectoryOwnerUserID uint32,
	buildDirectoryOwnerGroupID uint32,
	maximumExecutionTimeoutCompensation time.Duration,
	workerID map[string]string,
) remoteworker.Executor[*model_executewithstorage.Action[object.GlobalReference], model_core.Decodable[object.LocalReference], model_core.Decodable[object.LocalReference]] {
	return &localExecutor{
		objectDownloader:               objectDownloader,
		parsedObjectPool:               parsedObjectPool,
		dagUploader:                    dagUploader,
		objectContentsWalkerSemaphore:  objectContentsWalkerSemaphore,
		topLevelDirectory:              topLevelDirectory,
		handleAllocator:                handleAllocator,
		filePool:                       filePool,
		symlinkFactory:                 symlinkFactory,
		initialContentsSorter:          initialContentsSorter,
		hiddenFilesMatcher:             hiddenFilesMatcher,
		runner:                         runner,
		clock:                          clock,
		uuidGenerator:                  uuidGenerator,
		maximumWritableFileUploadDelay: maximumWritableFileUploadDelay,
		environmentVariables:           environmentVariables,
		buildDirectoryOwnerUserID:      buildDirectoryOwnerUserID,
		buildDirectoryOwnerGroupID:     buildDirectoryOwnerGroupID,
		readinessCheckingDirectory: handleAllocator.New().AsStatelessDirectory(
			virtual.NewStaticDirectory(
				virtual.CaseSensitiveComponentNormalizer,
				nil,
			),
		),
		maximumExecutionTimeoutCompensation: maximumExecutionTimeoutCompensation,
		workerID:                            workerID,
	}
}

func (e *localExecutor) CheckReadiness(ctx context.Context) error {
	// Create a randomly named directory.
	directoryName := path.MustNewComponent(util.Must(e.uuidGenerator()).String())
	if err := e.topLevelDirectory.AddChild(
		ctx,
		directoryName,
		virtual.DirectoryChild{}.FromDirectory(e.readinessCheckingDirectory),
	); err != nil {
		return util.StatusWrap(err, "Failed to attach readiness checking directory")
	}
	defer e.topLevelDirectory.RemoveChild(directoryName)

	// Ask the runner to validate its existence.
	_, err := e.runner.CheckReadiness(ctx, &runner_pb.CheckReadinessRequest{
		Path: directoryName.String(),
	})
	return err
}

func captureLog(ctx context.Context, buildDirectory virtual.PrepopulatedDirectory, name path.Component, writableFileUploadDelay <-chan struct{}, fileCreationParameters *model_filesystem.FileCreationParameters) (model_core.PatchedMessage[*model_filesystem_pb.FileContents, dag.ObjectContentsWalker], error) {
	stdoutFile, err := buildDirectory.LookupChild(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return model_core.PatchedMessage[*model_filesystem_pb.FileContents, dag.ObjectContentsWalker]{}, nil
		}
		return model_core.PatchedMessage[*model_filesystem_pb.FileContents, dag.ObjectContentsWalker]{}, util.StatusWrap(err, "Failed to look up file")
	}

	openReadFrozen := virtual.ApplyOpenReadFrozen{
		WritableFileDelay: writableFileUploadDelay,
	}
	if stdoutFile.GetNode().VirtualApply(&openReadFrozen) {
		if openReadFrozen.Err != nil {
			return model_core.PatchedMessage[*model_filesystem_pb.FileContents, dag.ObjectContentsWalker]{}, util.StatusWrap(openReadFrozen.Err, "Failed to open file")
		}
		fileContents, err := model_filesystem.CreateChunkDiscardingFileMerkleTree(ctx, fileCreationParameters, openReadFrozen.Reader)
		if err != nil {
			return model_core.PatchedMessage[*model_filesystem_pb.FileContents, dag.ObjectContentsWalker]{}, util.StatusWrap(err, "Failed to create file Merkle tree")
		}
		return fileContents, nil
	}

	return model_core.PatchedMessage[*model_filesystem_pb.FileContents, dag.ObjectContentsWalker]{}, status.Error(codes.InvalidArgument, "File is of an incorrect type")
}

func (e *localExecutor) Execute(ctx context.Context, action *model_executewithstorage.Action[object.GlobalReference], executionTimeout time.Duration, executionEvents chan<- model_core.Decodable[object.LocalReference]) (model_core.Decodable[object.LocalReference], time.Duration, remoteworker_pb.CurrentState_Completed_Result, error) {
	// Reject actions that this worker can't process.
	if !proto.Equal(action.Format, &model_core_pb.ObjectFormat{
		Format: &model_core_pb.ObjectFormat_ProtoTypeName{
			ProtoTypeName: "bonanza.model.command.Action",
		},
	}) {
		var badReference model_core.Decodable[object.LocalReference]
		return badReference, 0, 0, status.Error(codes.InvalidArgument, "This worker cannot execute actions of this type")
	}

	// Create a clock that compensates for time that's spent
	// downloading objects from storage. This is needed to
	// accurately enforce execution timeouts.
	suspendableClock := re_clock.NewSuspendableClock(
		e.clock,
		e.maximumExecutionTimeoutCompensation,
		/* timeoutThreshold = */ time.Second/10,
	)
	parsedObjectPoolIngester := model_parser.NewParsedObjectPoolIngester(
		e.parsedObjectPool,
		model_parser.NewDownloadingObjectReader(
			object_suspending.NewDownloader(
				object_namespacemapping.NewNamespaceAddingDownloader(
					e.objectDownloader,
					action.Reference.Value.InstanceName,
				),
				suspendableClock,
			),
		),
	)

	referenceFormat := action.Reference.Value.GetReferenceFormat()
	actionEncoder, err := model_encoding.NewDeterministicBinaryEncoderFromProto(
		action.Encoders,
		uint32(referenceFormat.GetMaximumObjectSizeBytes()),
	)
	if err != nil {
		var badReference model_core.Decodable[object.LocalReference]
		return badReference, 0, 0, status.Error(codes.InvalidArgument, "Invalid action encoders")
	}

	var virtualExecutionDuration time.Duration
	result := model_core.MustBuildPatchedMessage(func(resultPatcher *model_core.ReferenceMessagePatcher[dag.ObjectContentsWalker]) *model_command_pb.Result {
		// Fetch the Command message, so that we know the arguments
		// and environment variables of the process to spawn.
		result := model_command_pb.Result{
			WorkerId: e.workerID,
		}
		actionReader := model_parser.LookupParsedObjectReader[object.LocalReference](
			parsedObjectPoolIngester,
			model_parser.NewChainedObjectParser(
				model_parser.NewEncodedObjectParser[object.LocalReference](actionEncoder),
				model_parser.NewProtoObjectParser[object.LocalReference, model_command_pb.Action](),
			),
		)
		actionMessage, err := actionReader.ReadParsedObject(ctx, model_core.CopyDecodable(action.Reference, action.Reference.Value.GetLocalReference()))
		if err != nil {
			result.Status = status.Convert(util.StatusWrap(err, "Failed to read action")).Proto()
			return &result
		}

		commandReader := model_parser.LookupParsedObjectReader[object.LocalReference](
			parsedObjectPoolIngester,
			model_parser.NewChainedObjectParser(
				model_parser.NewEncodedObjectParser[object.LocalReference](actionEncoder),
				model_parser.NewProtoObjectParser[object.LocalReference, model_command_pb.Command](),
			),
		)
		command, err := model_parser.Dereference(ctx, commandReader, model_core.Nested(actionMessage, actionMessage.Message.CommandReference))
		if err != nil {
			result.Status = status.Convert(util.StatusWrap(err, "Failed to read command")).Proto()
			return &result
		}

		// Convert arguments and environment variables stored in B-trees
		// backed by storage to plain lists, so that they can be sent to
		// the runner.
		var arguments []string
		var errIter error
		for element := range btree.AllLeaves(
			ctx,
			model_parser.LookupParsedObjectReader(
				parsedObjectPoolIngester,
				model_parser.NewChainedObjectParser(
					model_parser.NewEncodedObjectParser[object.LocalReference](actionEncoder),
					model_parser.NewProtoListObjectParser[object.LocalReference, model_command_pb.ArgumentList_Element](),
				),
			),
			model_core.Nested(command, command.Message.Arguments),
			func(element model_core.Message[*model_command_pb.ArgumentList_Element, object.LocalReference]) (*model_core_pb.DecodableReference, error) {
				return element.Message.GetParent(), nil
			},
			&errIter,
		) {
			level, ok := element.Message.Level.(*model_command_pb.ArgumentList_Element_Leaf)
			if !ok {
				result.Status = status.New(codes.InvalidArgument, "Invalid leaf element in arguments").Proto()
				return &result
			}
			arguments = append(arguments, level.Leaf)
		}
		if errIter != nil {
			result.Status = status.Convert(util.StatusWrap(err, "Failed to iterate arguments")).Proto()
			return &result
		}

		environmentVariables := maps.Clone(e.environmentVariables)
		for entry := range btree.AllLeaves(
			ctx,
			model_parser.LookupParsedObjectReader(
				parsedObjectPoolIngester,
				model_parser.NewChainedObjectParser(
					model_parser.NewEncodedObjectParser[object.LocalReference](actionEncoder),
					model_parser.NewProtoListObjectParser[object.LocalReference, model_command_pb.EnvironmentVariableList_Element](),
				),
			),
			model_core.Nested(command, command.Message.EnvironmentVariables),
			func(entry model_core.Message[*model_command_pb.EnvironmentVariableList_Element, object.LocalReference]) (*model_core_pb.DecodableReference, error) {
				return entry.Message.GetParent(), nil
			},
			&errIter,
		) {
			level, ok := entry.Message.Level.(*model_command_pb.EnvironmentVariableList_Element_Leaf_)
			if !ok {
				result.Status = status.New(codes.InvalidArgument, "Invalid leaf entry in environment variables").Proto()
				return &result
			}
			if environmentVariables == nil {
				environmentVariables = map[string]string{}
			}
			environmentVariables[level.Leaf.Name] = level.Leaf.Value
		}
		if errIter != nil {
			result.Status = status.Convert(util.StatusWrap(err, "Failed to iterate environment variables")).Proto()
			return &result
		}

		// Error logger to terminate execution and capture I/O error events.
		ctxWithIOError, cancelIOError := context.WithCancel(ctx)
		defer cancelIOError()
		ioErrorCapturer := &capturingErrorLogger{cancel: cancelIOError}

		fileCreationParameters, err := model_filesystem.NewFileCreationParametersFromProto(
			command.Message.FileCreationParameters,
			referenceFormat,
		)
		if err != nil {
			result.Status = status.Convert(util.StatusWrap(err, "Invalid file creation parameters")).Proto()
			return &result
		}
		directoryCreationParameters, err := model_filesystem.NewDirectoryCreationParametersFromProto(
			command.Message.DirectoryCreationParameters,
			referenceFormat,
		)
		if err != nil {
			result.Status = status.Convert(util.StatusWrap(err, "Invalid directory creation parameters")).Proto()
			return &result
		}
		directoryEncoder := directoryCreationParameters.GetEncoder()

		// Create build directory and expose it via the virtual file system.
		defaultAttributesSetter := func(requested virtual.AttributesMask, attributes *virtual.Attributes) {
			attributes.SetOwnerUserID(e.buildDirectoryOwnerUserID)
			attributes.SetOwnerGroupID(e.buildDirectoryOwnerGroupID)
		}
		namedAttributesFactory := virtual.NewInMemoryNamedAttributesFactory(
			virtual.NewHandleAllocatingFileAllocator(
				virtual.NewPoolBackedFileAllocator(
					e.filePool,
					ioErrorCapturer,
					defaultAttributesSetter,
					virtual.InNamedAttributeDirectoryNamedAttributesFactory,
				),
				e.handleAllocator,
			),
			e.symlinkFactory,
			ioErrorCapturer,
			e.handleAllocator,
			e.clock,
		)
		fileAllocator := virtual.NewPoolBackedFileAllocator(
			e.filePool,
			ioErrorCapturer,
			defaultAttributesSetter,
			namedAttributesFactory,
		)
		buildDirectory := virtual.NewInMemoryPrepopulatedDirectory(
			virtual.NewHandleAllocatingFileAllocator(fileAllocator, e.handleAllocator),
			e.symlinkFactory,
			ioErrorCapturer,
			e.handleAllocator,
			e.initialContentsSorter,
			e.hiddenFilesMatcher,
			e.clock,
			virtual.CaseSensitiveComponentNormalizer,
			defaultAttributesSetter,
			namedAttributesFactory,
		)
		defer buildDirectory.RemoveAllChildren(true)

		// For regular build actions we want input files to be
		// read-only. This matches the behavior of existing
		// tools like Bazel and Buildbarn. However, repository
		// rules require input files to be writable.
		inputFileReader := model_filesystem.NewFileReader(
			model_parser.LookupParsedObjectReader(
				parsedObjectPoolIngester,
				model_parser.NewChainedObjectParser(
					model_parser.NewEncodedObjectParser[object.LocalReference](fileCreationParameters.GetFileContentsListEncoder()),
					model_filesystem.NewFileContentsListObjectParser[object.LocalReference](),
				),
			),
			model_parser.LookupParsedObjectReader(
				parsedObjectPoolIngester,
				model_parser.NewChainedObjectParser(
					model_parser.NewEncodedObjectParser[object.LocalReference](fileCreationParameters.GetChunkEncoder()),
					model_parser.NewRawObjectParser[object.LocalReference](),
				),
			),
		)
		var inputFileFactory model_filesystem_virtual.FileFactory
		if command.Message.NeedsWritableInputFiles {
			inputFileFactory = model_filesystem_virtual.NewStatefulHandleAllocatingFileFactory(
				model_filesystem_virtual.NewMutationTrackingFileFactory(
					model_filesystem_virtual.NewObjectInitializedFileFactory(
						ctxWithIOError,
						inputFileReader,
						fileAllocator,
					),
				),
				e.handleAllocator,
			)
		} else {
			inputFileFactory = model_filesystem_virtual.NewStatelessHandleAllocatingFileFactory(
				model_filesystem_virtual.NewObjectBackedFileFactory(
					ctxWithIOError,
					inputFileReader,
					ioErrorCapturer,
				),
				e.handleAllocator.New(),
			)
		}

		// Create subdirectories that should be present when the command
		// is executed, such as the input root directory.
		inputRootReference, err := model_core.FlattenDecodableReference(model_core.Nested(actionMessage, actionMessage.Message.InputRootReference.GetReference()))
		if err != nil {
			result.Status = status.Convert(util.StatusWrap(err, "Invalid input root reference")).Proto()
			return &result
		}
		if err := buildDirectory.CreateChildren(map[path.Component]virtual.InitialChild{
			inputRootDirectoryComponent: virtual.InitialChild{}.FromDirectory(
				model_filesystem_virtual.NewObjectBackedInitialContentsFetcher(
					ctxWithIOError,
					model_parser.LookupParsedObjectReader(
						parsedObjectPoolIngester,
						model_parser.NewChainedObjectParser(
							model_parser.NewEncodedObjectParser[object.LocalReference](directoryEncoder),
							model_filesystem.NewDirectoryClusterObjectParser[object.LocalReference](),
						),
					),
					model_parser.LookupParsedObjectReader(
						parsedObjectPoolIngester,
						model_parser.NewChainedObjectParser(
							model_parser.NewEncodedObjectParser[object.LocalReference](directoryEncoder),
							model_parser.NewProtoObjectParser[object.LocalReference, model_filesystem_pb.Leaves](),
						),
					),
					inputFileFactory,
					e.symlinkFactory,
					inputRootReference,
				),
			),
			serverLogsDirectoryComponent: virtual.InitialChild{}.FromDirectory(virtual.EmptyInitialContentsFetcher),
			temporaryDirectoryComponent:  virtual.InitialChild{}.FromDirectory(virtual.EmptyInitialContentsFetcher),
		}, false); err != nil {
			result.Status = status.Convert(util.StatusWrap(err, "Failed to create initial children of build directory")).Proto()
			return &result
		}

		// If the command requires a stable input root path, we
		// should attach the build directory name under a fixed
		// name. We ideally don't want to do this, because it
		// limits prevents any form of parallelism.
		var buildDirectoryUUID uuid.UUID
		if u := command.Message.StableInputRootPathUuid; u != "" {
			buildDirectoryUUID, err = uuid.Parse(u)
			if err != nil {
				result.Status = status.Convert(util.StatusWrap(err, "Invalid stable input root path UUID")).Proto()
				return &result
			}
		} else {
			buildDirectoryUUID = util.Must(e.uuidGenerator())
		}

		buildDirectoryName := path.MustNewComponent(buildDirectoryUUID.String())
		if err := e.topLevelDirectory.AddChild(ctx, buildDirectoryName, virtual.DirectoryChild{}.FromDirectory(buildDirectory)); err != nil {
			result.Status = status.Convert(util.StatusWrap(err, "Failed to attach build directory")).Proto()
			return &result
		}
		defer e.topLevelDirectory.RemoveChild(buildDirectoryName)

		// TODO: This is currently needed, because NFSv4 caches
		// directory entries even if we fully disable any form of
		// caching. Figure out what's going on here, so we can get rid
		// of this unnecessary delay.
		if command.Message.StableInputRootPathUuid != "" {
			time.Sleep(1)
		}

		// Invoke the command.
		buildDirectoryPath := (*path.Trace)(nil).Append(buildDirectoryName)
		ctxWithTimeout, cancelTimeout := suspendableClock.NewContextWithTimeout(ctxWithIOError, executionTimeout)
		runResponse, runErr := e.runner.Run(ctxWithTimeout, &runner_pb.RunRequest{
			Arguments:            arguments,
			EnvironmentVariables: environmentVariables,
			WorkingDirectory:     command.Message.WorkingDirectory,
			StdoutPath:           buildDirectoryPath.Append(stdoutComponent).GetUNIXString(),
			StderrPath:           buildDirectoryPath.Append(stderrComponent).GetUNIXString(),
			InputRootDirectory:   buildDirectoryPath.Append(inputRootDirectoryComponent).GetUNIXString(),
			TemporaryDirectory:   buildDirectoryPath.Append(temporaryDirectoryComponent).GetUNIXString(),
			ServerLogsDirectory:  buildDirectoryPath.Append(serverLogsDirectoryComponent).GetUNIXString(),
		})

		// Determine the amount of time the action ran, minus the time
		// it was delayed reading data from storage.
		cancelTimeout()
		<-ctxWithTimeout.Done()
		if d, ok := ctxWithTimeout.Value(re_clock.UnsuspendedDurationKey{}).(time.Duration); ok {
			virtualExecutionDuration = d
		}

		setError := func(err error) {
			if result.Status == nil {
				result.Status = status.Convert(err).Proto()
			}
		}

		// If an I/O error occurred during execution, attach any errors
		// related to it to the response first. These errors should be
		// preferred over the cancelation errors that are a result of it.
		if err := ioErrorCapturer.GetError(); err != nil {
			setError(err)
		}

		// Attach the exit code or execution error.
		if runErr == nil {
			result.ExitCode = runResponse.ExitCode
			result.AuxiliaryMetadata = append(result.AuxiliaryMetadata, runResponse.ResourceUsage...)
		} else {
			setError(util.StatusWrap(runErr, "Failed to run command"))
		}

		writableFileUploadDelayCtx, writableFileUploadDelayCancel := e.clock.NewContextWithTimeout(ctx, e.maximumWritableFileUploadDelay)
		defer writableFileUploadDelayCancel()
		writableFileUploadDelayChan := writableFileUploadDelayCtx.Done()

		// Capture output files.
		var outputs model_command_pb.Outputs
		outputsPatcher := model_core.NewReferenceMessagePatcher[dag.ObjectContentsWalker]()

		if stdoutContents, err := captureLog(ctx, buildDirectory, stdoutComponent, writableFileUploadDelayChan, fileCreationParameters); err == nil {
			if stdoutContents.IsSet() {
				outputs.Stdout = stdoutContents.Message
				outputsPatcher.Merge(stdoutContents.Patcher)
			}
		} else {
			setError(util.StatusWrap(err, "Failed to capture standard output"))
		}

		if stderrContents, err := captureLog(ctx, buildDirectory, stderrComponent, writableFileUploadDelayChan, fileCreationParameters); err == nil {
			if stderrContents.IsSet() {
				outputs.Stderr = stderrContents.Message
				outputsPatcher.Merge(stderrContents.Patcher)
			}
		} else {
			setError(util.StatusWrap(err, "Failed to capture standard error"))
		}

		if pattern := command.Message.OutputPathPattern; pattern != nil {
			if inputRoot, err := buildDirectory.LookupChild(inputRootDirectoryComponent); err != nil {
				setError(util.StatusWrap(err, "Failed to look up input root directory"))
			} else if inputRootDirectory, _ := inputRoot.GetPair(); inputRootDirectory == nil {
				setError(util.StatusWrap(err, "Input root is not a directory"))
			} else {
				group, groupCtx := errgroup.WithContext(ctx)
				var outputRoot model_filesystem.CreatedDirectory[dag.ObjectContentsWalker]
				group.Go(func() error {
					return model_filesystem.CreateDirectoryMerkleTree(
						groupCtx,
						// TODO: Should this be a separate semaphore?
						e.objectContentsWalkerSemaphore,
						group,
						directoryCreationParameters,
						&prepopulatedCapturableDirectory{
							options: &prepopulatedCapturableDirectoryOptions{
								context: groupCtx,
								pathPatternChildrenReader: model_parser.LookupParsedObjectReader[object.LocalReference](
									parsedObjectPoolIngester,
									model_parser.NewChainedObjectParser(
										model_parser.NewEncodedObjectParser[object.LocalReference](actionEncoder),
										model_parser.NewProtoObjectParser[object.LocalReference, model_command_pb.PathPattern_Children](),
									),
								),
								writableFileUploadDelay: writableFileUploadDelayChan,
								fileCreationParameters:  fileCreationParameters,
							},
							directory: inputRootDirectory,
							pattern:   model_core.Nested(command, pattern),
						},
						model_filesystem.NewSimpleDirectoryMerkleTreeCapturer(model_core.WalkableCreatedObjectCapturer),
						&outputRoot,
					)
				})
				if err := group.Wait(); err == nil {
					outputs.OutputRoot = outputRoot.Message.Message
					outputsPatcher.Merge(outputRoot.Message.Patcher)
				} else {
					setError(util.StatusWrap(err, "Failed to capture output root"))
				}
			}
		}

		if proto.Size(&outputs) > 0 {
			// Action has one or more outputs. Upload them and
			// attach a reference to the result message.
			if createdObject, err := model_core.MarshalAndEncodeDeterministic(
				model_core.NewPatchedMessage(model_core.NewProtoBinaryMarshaler(&outputs), outputsPatcher),
				referenceFormat,
				directoryEncoder,
			); err != nil {
				// TODO: Does this properly release all resources?
				setError(util.StatusWrap(err, "Failed to marshal outputs"))
			} else if createdObjectReference, err := resultPatcher.CaptureAndAddDecodableReference(
				ctx,
				createdObject,
				model_core.WalkableCreatedObjectCapturer,
			); err != nil {
				setError(util.StatusWrap(err, "Failed to capture outputs"))
			} else {
				result.OutputsReference = createdObjectReference
			}
		}
		return &result
	})

	createdResult, err := model_core.MarshalAndEncodeDeterministic(
		model_core.ProtoToBinaryMarshaler(result),
		referenceFormat,
		actionEncoder,
	)
	if err != nil {
		var badReference model_core.Decodable[object.LocalReference]
		return badReference, 0, 0, util.StatusWrap(err, "Failed to create marshal and encode result")
	}
	resultReference := createdResult.Value.GetLocalReference()
	if err := e.dagUploader.UploadDAG(
		ctx,
		action.Reference.Value.WithLocalReference(resultReference),
		dag.NewSimpleObjectContentsWalker(
			createdResult.Value.Contents,
			createdResult.Value.Metadata,
		),
	); err != nil {
		var badReference model_core.Decodable[object.LocalReference]
		return badReference, 0, 0, util.StatusWrap(err, "Failed to upload result")
	}

	resultCode := remoteworker_pb.CurrentState_Completed_SUCCEEDED
	if grpcCode := codes.Code(result.Message.Status.GetCode()); grpcCode != codes.OK {
		if grpcCode == codes.DeadlineExceeded {
			resultCode = remoteworker_pb.CurrentState_Completed_TIMED_OUT
		} else {
			resultCode = remoteworker_pb.CurrentState_Completed_FAILED
		}
	} else if result.Message.ExitCode != 0 {
		resultCode = remoteworker_pb.CurrentState_Completed_FAILED
	}
	return model_core.CopyDecodable(createdResult, resultReference), virtualExecutionDuration, resultCode, nil
}

type prepopulatedCapturableDirectoryOptions struct {
	context                   context.Context
	pathPatternChildrenReader model_parser.MessageObjectReader[object.LocalReference, *model_command_pb.PathPattern_Children]

	writableFileUploadDelay <-chan struct{}
	fileCreationParameters  *model_filesystem.FileCreationParameters
}

type prepopulatedCapturableDirectory struct {
	options         *prepopulatedCapturableDirectoryOptions
	directory       virtual.PrepopulatedDirectory
	pattern         model_core.Message[*model_command_pb.PathPattern, object.LocalReference]
	patternChildren atomic.Pointer[model_core.Message[*model_command_pb.PathPattern_Children, object.LocalReference]]
}

func (d *prepopulatedCapturableDirectory) getPatternChildren() (model_core.Message[*model_command_pb.PathPattern_Children, object.LocalReference], error) {
	if patternChildren := d.patternChildren.Load(); patternChildren != nil {
		return *patternChildren, nil
	}

	patternChildren, err := PathPatternGetChildren(d.options.context, d.options.pathPatternChildrenReader, d.pattern)
	if err != nil {
		return model_core.Message[*model_command_pb.PathPattern_Children, object.LocalReference]{}, err
	}

	d.patternChildren.Store(&patternChildren)
	return patternChildren, nil
}

func (prepopulatedCapturableDirectory) Close() error {
	return nil
}

func (d *prepopulatedCapturableDirectory) ReadDir() ([]filesystem.FileInfo, error) {
	entries, err := d.directory.ReadDir()
	if err != nil {
		return nil, err
	}

	patternChildren, err := d.getPatternChildren()
	if err != nil {
		return nil, err
	}
	if !patternChildren.IsSet() {
		// We should capture the entire directory.
		return entries, nil
	}

	// We should only capture certain children. Filter the results
	// of ReadDir() by name.
	permittedEntries := patternChildren.Message.Children
	var filteredEntries []filesystem.FileInfo
	for len(entries) > 0 && len(permittedEntries) > 0 {
		if cmp := strings.Compare(entries[0].Name().String(), permittedEntries[0].Name); cmp < 0 {
			entries = entries[1:]
		} else if cmp > 0 {
			permittedEntries = permittedEntries[1:]
		} else {
			filteredEntries = append(filteredEntries, entries[0])
			entries = entries[1:]
			permittedEntries = permittedEntries[1:]
		}
	}
	return filteredEntries, nil
}

func (d *prepopulatedCapturableDirectory) Readlink(name path.Component) (path.Parser, error) {
	child, err := d.directory.LookupChild(name)
	if err != nil {
		return nil, err
	}
	if _, leaf := child.GetPair(); leaf != nil {
		p := virtual.ApplyReadlink{}
		if !child.GetNode().VirtualApply(&p) {
			panic("build directory contains leaves that don't handle ApplyReadlink")
		}
		return p.Target, p.Err
	}
	return nil, syscall.EISDIR
}

func (d *prepopulatedCapturableDirectory) EnterCapturableDirectory(name path.Component) (*model_filesystem.CreatedDirectory[dag.ObjectContentsWalker], model_filesystem.CapturableDirectory[dag.ObjectContentsWalker, dag.ObjectContentsWalker], error) {
	patternChildren, err := d.getPatternChildren()
	if err != nil {
		return nil, nil, err
	}

	var childPattern model_core.Message[*model_command_pb.PathPattern, object.LocalReference]
	if patternChildren.IsSet() {
		// Determine if the requested directory is part of the
		// path pattern. If not, hide it.
		nameStr := name.String()
		children := patternChildren.Message.Children
		index, ok := sort.Find(
			len(children),
			func(i int) int { return strings.Compare(nameStr, children[i].Name) },
		)
		if !ok {
			return nil, nil, syscall.ENOENT
		}

		// Extract the pattern to apply to the child.
		childPatternMessage := children[index].Pattern
		if childPatternMessage == nil {
			return nil, nil, status.Error(codes.InvalidArgument, "Missing path pattern")
		}
		childPattern = model_core.Nested(patternChildren, childPatternMessage)
	} else {
		// The current directory should be captured without any
		// filtering. Also don't apply any filtering in the
		// child directory.
		childPattern = model_core.NewSimpleMessage[object.LocalReference](&model_command_pb.PathPattern{})
	}

	child, err := d.directory.LookupChild(name)
	if err != nil {
		return nil, nil, err
	}
	childDirectory, _ := child.GetPair()
	if childDirectory == nil {
		return nil, nil, syscall.ENOTDIR
	}

	var getRawDirectory model_filesystem_virtual.ApplyGetRawDirectory
	if childDirectory.VirtualApply(&getRawDirectory) {
		// The current directory is still backed by an
		// InitialContentsFetcher, meaning it hasn't been
		// accessed yet.
		//
		// Instead of traversing into it and computing a Merkle
		// tree, use the original Directory message that backs
		// the InitialContentsFetcher.
		if err := getRawDirectory.Err; err != nil {
			return nil, nil, err
		}
		createdDirectory, err := model_filesystem.NewCreatedDirectoryBare(
			model_core.NewPatchedMessageFromExisting(
				getRawDirectory.RawDirectory,
				func(index int) dag.ObjectContentsWalker {
					return dag.ExistingObjectContentsWalker
				},
			),
		)
		return createdDirectory, nil, err
	}

	return nil,
		&prepopulatedCapturableDirectory{
			options:   d.options,
			directory: childDirectory,
			pattern:   childPattern,
		},
		nil
}

func (d *prepopulatedCapturableDirectory) OpenForFileMerkleTreeCreation(name path.Component) (model_filesystem.CapturableFile[dag.ObjectContentsWalker], error) {
	child, err := d.directory.LookupChild(name)
	if err != nil {
		return nil, err
	}
	return &prepopulatedCapturableFile{
		options: d.options,
		node:    child.GetNode(),
	}, nil
}

type prepopulatedCapturableFile struct {
	options *prepopulatedCapturableDirectoryOptions
	node    virtual.Node
}

func (f *prepopulatedCapturableFile) CreateFileMerkleTree(ctx context.Context) (model_core.PatchedMessage[*model_filesystem_pb.FileContents, dag.ObjectContentsWalker], error) {
	// Read-only object store backed files.
	var getFileContents model_filesystem_virtual.ApplyGetFileContents
	if f.node.VirtualApply(&getFileContents) {
		return model_filesystem.FileContentsEntryToProto(&getFileContents.FileContents), nil
	}

	// Mutable files created during execution.
	openReadFrozen := virtual.ApplyOpenReadFrozen{
		WritableFileDelay: f.options.writableFileUploadDelay,
	}
	if f.node.VirtualApply(&openReadFrozen) {
		if openReadFrozen.Err != nil {
			return model_core.PatchedMessage[*model_filesystem_pb.FileContents, dag.ObjectContentsWalker]{}, util.StatusWrap(openReadFrozen.Err, "Failed to open file")
		}
		fileContents, err := model_filesystem.CreateChunkDiscardingFileMerkleTree(ctx, f.options.fileCreationParameters, openReadFrozen.Reader)
		if err != nil {
			return model_core.PatchedMessage[*model_filesystem_pb.FileContents, dag.ObjectContentsWalker]{}, util.StatusWrap(err, "Failed to create file Merkle tree")
		}
		return fileContents, nil
	}

	return model_core.PatchedMessage[*model_filesystem_pb.FileContents, dag.ObjectContentsWalker]{}, status.Error(codes.InvalidArgument, "File is of an incorrect type")
}

func (prepopulatedCapturableFile) Discard() {}
