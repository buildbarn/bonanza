package analysis

import (
	"context"
	"encoding"
	"errors"
	"fmt"
	"sort"
	"strings"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_parser "bonanza.build/pkg/model/parser"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_command_pb "bonanza.build/pkg/proto/model/command"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
	"github.com/buildbarn/bb-storage/pkg/util"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
	"google.golang.org/protobuf/types/known/durationpb"
)

func (c *baseComputer[TReference, TMetadata]) ComputeRepoPlatformHostPathValue(ctx context.Context, key *model_analysis_pb.RepoPlatformHostPath_Key, e RepoPlatformHostPathEnvironment[TReference, TMetadata]) (PatchedRepoPlatformHostPathValue[TMetadata], error) {
	actionEncoder, gotActionEncoder := e.GetActionEncoderObjectValue(&model_analysis_pb.ActionEncoderObject_Key{})
	directoryCreationParameters, gotDirectoryCreationParameters := e.GetDirectoryCreationParametersObjectValue(&model_analysis_pb.DirectoryCreationParametersObject_Key{})
	directoryCreationParametersMessage := e.GetDirectoryCreationParametersValue(&model_analysis_pb.DirectoryCreationParameters_Key{})
	directoryReaders, gotDirectoryReaders := e.GetDirectoryReadersValue(&model_analysis_pb.DirectoryReaders_Key{})
	fileCreationParametersMessage := e.GetFileCreationParametersValue(&model_analysis_pb.FileCreationParameters_Key{})
	repoPlatform := e.GetRegisteredRepoPlatformValue(&model_analysis_pb.RegisteredRepoPlatform_Key{})
	if !gotActionEncoder ||
		!gotDirectoryCreationParameters ||
		!directoryCreationParametersMessage.IsSet() ||
		!gotDirectoryReaders ||
		!fileCreationParametersMessage.IsSet() ||
		!repoPlatform.IsSet() {
		return PatchedRepoPlatformHostPathValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	environment := map[string]string{}
	for _, environmentVariable := range repoPlatform.Message.RepositoryOsEnviron {
		if _, ok := environment[environmentVariable.Name]; !ok {
			environment[environmentVariable.Name] = environmentVariable.Value
		}
	}
	referenceFormat := c.referenceFormat
	environmentVariableList, _, err := convertDictToEnvironmentVariableList(
		ctx,
		environment,
		actionEncoder,
		referenceFormat,
		e,
	)
	if err != nil {
		return PatchedRepoPlatformHostPathValue[TMetadata]{}, err
	}

	// Request that the worker captures a given path by copying it
	// into its input root directory, using "cp -RH".
	// TODO: This should use inlinedtree.Build().
	const capturedFilename = "captured"
	createdCommand, err := model_core.MarshalAndEncode(
		model_core.NewPatchedMessage(
			model_core.NewProtoBinaryMarshaler(&model_command_pb.Command{
				Arguments: []*model_command_pb.ArgumentList_Element{
					{
						Level: &model_command_pb.ArgumentList_Element_Leaf{
							Leaf: "cp",
						},
					},
					{
						Level: &model_command_pb.ArgumentList_Element_Leaf{
							Leaf: "-RH",
						},
					},
					{
						Level: &model_command_pb.ArgumentList_Element_Leaf{
							Leaf: key.AbsolutePath,
						},
					},
					{
						Level: &model_command_pb.ArgumentList_Element_Leaf{
							Leaf: capturedFilename,
						},
					},
				},
				EnvironmentVariables:        environmentVariableList.Message,
				DirectoryCreationParameters: directoryCreationParametersMessage.Message.DirectoryCreationParameters,
				FileCreationParameters:      fileCreationParametersMessage.Message.FileCreationParameters,
				OutputPathPattern: &model_command_pb.PathPattern{
					Children: &model_command_pb.PathPattern_ChildrenInline{
						ChildrenInline: &model_command_pb.PathPattern_Children{
							Children: []*model_command_pb.PathPattern_Child{{
								Name:    capturedFilename,
								Pattern: &model_command_pb.PathPattern{},
							}},
						},
					},
				},
				WorkingDirectory: (*path.Trace)(nil).GetUNIXString(),
			}),
			environmentVariableList.Patcher,
		),
		referenceFormat,
		actionEncoder,
	)
	if err != nil {
		return PatchedRepoPlatformHostPathValue[TMetadata]{}, fmt.Errorf("failed to create command: %w", err)
	}

	// We can assume that paths outside the input root do not
	// resolve to any location inside the input root. It should
	// therefore be fine to run these actions with an empty input
	// root.
	inputRootReference, err := c.createMerkleTreeFromChangeTrackingDirectory(
		ctx,
		e,
		&changeTrackingDirectory[TReference, TMetadata]{},
		directoryCreationParameters,
		directoryReaders,
		/* fileCreationParameters = */ nil,
		/* patchedFiles = */ nil,
	)
	if err != nil {
		return PatchedRepoPlatformHostPathValue[TMetadata]{}, fmt.Errorf("failed to create Merkle tree of root directory: %w", err)
	}

	action, err := model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (encoding.BinaryMarshaler, error) {
		patcher.Merge(inputRootReference.Patcher)
		commandReference, err := patcher.CaptureAndAddDecodableReference(ctx, createdCommand, e)
		if err != nil {
			return nil, err
		}
		return model_core.NewProtoBinaryMarshaler(&model_command_pb.Action{
			CommandReference:   commandReference,
			InputRootReference: inputRootReference.Message,
		}), nil
	})
	if err != nil {
		return PatchedRepoPlatformHostPathValue[TMetadata]{}, fmt.Errorf("failed to create action: %w", err)
	}
	createdAction, err := model_core.MarshalAndEncode(action, referenceFormat, actionEncoder)
	if err != nil {
		return PatchedRepoPlatformHostPathValue[TMetadata]{}, fmt.Errorf("failed to encode action: %w", err)
	}

	actionResultKey, err := model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (*model_analysis_pb.SuccessfulActionResult_Key, error) {
		actionReference, err := patcher.CaptureAndAddDecodableReference(ctx, createdAction, e)
		if err != nil {
			return nil, err
		}
		return &model_analysis_pb.SuccessfulActionResult_Key{
			ExecuteRequest: &model_analysis_pb.ExecuteRequest{
				PlatformPkixPublicKey: repoPlatform.Message.ExecPkixPublicKey,
				ActionReference:       actionReference,
				ExecutionTimeout:      &durationpb.Duration{Seconds: 600},
			},
		}, nil
	})
	if err != nil {
		return PatchedRepoPlatformHostPathValue[TMetadata]{}, fmt.Errorf("failed to create action result key: %w", err)
	}
	actionResult := e.GetSuccessfulActionResultValue(actionResultKey)
	if !actionResult.IsSet() {
		return PatchedRepoPlatformHostPathValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	outputs, err := model_parser.MaybeDereference(ctx, directoryReaders.CommandOutputs, model_core.Nested(actionResult, actionResult.Message.OutputsReference))
	if err != nil {
		return PatchedRepoPlatformHostPathValue[TMetadata]{}, fmt.Errorf("failed to obtain outputs from action result: %w", err)
	}
	outputRoot := outputs.Message.OutputRoot
	if outputRoot == nil {
		return PatchedRepoPlatformHostPathValue[TMetadata]{}, errors.New("action did not yield an output root")
	}

	directories := outputRoot.Directories
	if index, ok := sort.Find(
		len(directories),
		func(i int) int { return strings.Compare(capturedFilename, directories[i].Name) },
	); ok {
		// The captured path is a directory. Check whether it
		// contains any more symlinks that point to locations
		// outside this directory. If so, invoke
		// RepoPlatformHostPath recursively.
		rootDirectory := changeTrackingDirectory[TReference, TMetadata]{
			unmodifiedDirectory: model_core.Nested(outputs, directories[index].Directory),
		}

		virtualRootScopeWalkerFactory, err := path.NewVirtualRootScopeWalkerFactory(path.UNIXFormat.NewParser(key.AbsolutePath), nil)
		if err != nil {
			return PatchedRepoPlatformHostPathValue[TMetadata]{}, err
		}
		sr := changeTrackingDirectorySymlinksRelativizer[TReference, TMetadata]{
			context:     ctx,
			environment: e,
			directoryLoadOptions: &changeTrackingDirectoryLoadOptions[TReference]{
				context:                 ctx,
				directoryContentsReader: directoryReaders.DirectoryContents,
				leavesReader:            directoryReaders.Leaves,
			},
			virtualRootScopeWalkerFactory: virtualRootScopeWalkerFactory,
		}
		if err := sr.relativizeSymlinksRecursively(
			util.NewNonEmptyStack(&rootDirectory),
			/* dPath = */ nil,
			/* maximumEscapementLevels = */ 0,
		); err != nil {
			return PatchedRepoPlatformHostPathValue[TMetadata]{}, err
		}

		group, groupCtx := errgroup.WithContext(ctx)
		var createdRootDirectory model_filesystem.CreatedDirectory[TMetadata]
		group.Go(func() error {
			return model_filesystem.CreateDirectoryMerkleTree(
				groupCtx,
				semaphore.NewWeighted(1),
				group,
				directoryCreationParameters,
				&capturableChangeTrackingDirectory[TReference, TMetadata]{
					options: &capturableChangeTrackingDirectoryOptions[TReference, TMetadata]{
						context:                 groupCtx,
						directoryContentsReader: directoryReaders.DirectoryContents,
						objectCapturer:          e,
					},
					directory: &rootDirectory,
				},
				model_filesystem.NewSimpleDirectoryMerkleTreeCapturer(e),
				&createdRootDirectory,
			)
		})
		if err := group.Wait(); err != nil {
			return PatchedRepoPlatformHostPathValue[TMetadata]{}, err
		}

		return model_core.NewPatchedMessage(
			&model_analysis_pb.RepoPlatformHostPath_Value{
				CapturedPath: &model_analysis_pb.RepoPlatformHostPath_Value_Directory{
					Directory: createdRootDirectory.Message.Message,
				},
			},
			createdRootDirectory.Message.Patcher,
		), nil
	}

	leaves, err := model_filesystem.DirectoryGetLeaves(ctx, directoryReaders.Leaves, model_core.Nested(outputs, outputRoot))
	if err != nil {
		return PatchedRepoPlatformHostPathValue[TMetadata]{}, fmt.Errorf("failed to read leaves of output root: %w", err)
	}

	files := leaves.Message.Files
	if index, ok := sort.Find(
		len(files),
		func(i int) int { return strings.Compare(capturedFilename, files[i].Name) },
	); ok {
		// The captured path is a regular file.
		patchedFileProperties := model_core.Patch(e, model_core.Nested(leaves, files[index].Properties))
		return model_core.NewPatchedMessage(
			&model_analysis_pb.RepoPlatformHostPath_Value{
				CapturedPath: &model_analysis_pb.RepoPlatformHostPath_Value_File{
					File: patchedFileProperties.Message,
				},
			},
			patchedFileProperties.Patcher,
		), nil
	}

	return PatchedRepoPlatformHostPathValue[TMetadata]{}, errors.New("action did not capture host path")
}

type changeTrackingDirectoryNormalizingPathResolver[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	loadOptions *changeTrackingDirectoryLoadOptions[TReference]

	gotScope    bool
	directories util.NonEmptyStack[*changeTrackingDirectory[TReference, TMetadata]]
	components  []path.Component
}

func (r *changeTrackingDirectoryNormalizingPathResolver[TReference, TMetadata]) OnAbsolute() (path.ComponentWalker, error) {
	r.gotScope = true
	r.directories.PopAll()
	r.components = r.components[:0]
	return r, nil
}

func (r *changeTrackingDirectoryNormalizingPathResolver[TReference, TMetadata]) OnRelative() (path.ComponentWalker, error) {
	r.gotScope = true
	return r, nil
}

func (r *changeTrackingDirectoryNormalizingPathResolver[TReference, TMetadata]) OnDriveLetter(driveLetter rune) (path.ComponentWalker, error) {
	return nil, errors.New("drive letters are not supported")
}

func (r *changeTrackingDirectoryNormalizingPathResolver[TReference, TMetadata]) OnShare(server, share string) (path.ComponentWalker, error) {
	return nil, errors.New("shares are not supported")
}

func (r *changeTrackingDirectoryNormalizingPathResolver[TReference, TMetadata]) OnDirectory(name path.Component) (path.GotDirectoryOrSymlink, error) {
	d := r.directories.Peek()
	if err := d.maybeLoadContents(r.loadOptions); err != nil {
		return nil, err
	}

	if dChild, ok := d.directories[name]; ok {
		r.components = append(r.components, name)
		r.directories.Push(dChild)
		return path.GotDirectory{
			Child:        r,
			IsReversible: true,
		}, nil
	}

	return nil, fmt.Errorf("directory %#v does not exist", name.String())
}

func (r *changeTrackingDirectoryNormalizingPathResolver[TReference, TMetadata]) OnTerminal(name path.Component) (*path.GotSymlink, error) {
	d := r.directories.Peek()
	if err := d.maybeLoadContents(r.loadOptions); err != nil {
		return nil, err
	}
	r.components = append(r.components, name)
	return nil, nil
}

func (r *changeTrackingDirectoryNormalizingPathResolver[TReference, TMetadata]) OnUp() (path.ComponentWalker, error) {
	if _, ok := r.directories.PopSingle(); !ok {
		r.components = r.components[:len(r.components)-1]
		return nil, errors.New("path resolves to a location above the root directory")
	}
	return r, nil
}

type changeTrackingDirectorySymlinksRelativizerEnvironment[TReference any] interface {
	GetRepoPlatformHostPathValue(*model_analysis_pb.RepoPlatformHostPath_Key) model_core.Message[*model_analysis_pb.RepoPlatformHostPath_Value, TReference]
}

// changeTrackingDirectorySymlinksRelativizer is a helper that is used
// by ComputeRepoPlatformHostPath() that recursively traverses a
// directory and replaces all symlinks to refer to relative paths, or
// replace them with the object that is referenced.
type changeTrackingDirectorySymlinksRelativizer[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	context                       context.Context
	environment                   changeTrackingDirectorySymlinksRelativizerEnvironment[TReference]
	directoryLoadOptions          *changeTrackingDirectoryLoadOptions[TReference]
	virtualRootScopeWalkerFactory *path.VirtualRootScopeWalkerFactory
}

func (sr *changeTrackingDirectorySymlinksRelativizer[TReference, TMetadata]) relativizeSymlinksRecursively(dStack util.NonEmptyStack[*changeTrackingDirectory[TReference, TMetadata]], dPath *path.Trace, maximumEscapementLevels uint32) error {
	d := dStack.Peek()
	if d.maximumSymlinkEscapementLevelsAtMost(maximumEscapementLevels) {
		// This directory is guaranteed to not contain any symlinks
		// that escape beyond the maximum number of permitted levels.
		// There is no need to traverse it.
		return nil
	}

	if err := d.maybeLoadContents(sr.directoryLoadOptions); err != nil {
		return err
	}
	for name, target := range d.symlinks {
		escapementCounter := model_filesystem.NewEscapementCountingScopeWalker()
		if err := path.Resolve(target, escapementCounter); err != nil {
			return fmt.Errorf("failed to resolve symlink %#v %w", name.String(), err)
		}
		if levels := escapementCounter.GetLevels(); levels == nil || levels.Value > maximumEscapementLevels {
			// Target of this symlink is absolute or has too
			// many ".." components.
			r := changeTrackingDirectoryNormalizingPathResolver[TReference, TMetadata]{
				loadOptions: sr.directoryLoadOptions,
				directories: dStack.Copy(),
				components:  append([]path.Component(nil), dPath.ToList()...),
			}
			normalizedPath, scopeWalker := path.EmptyBuilder.Join(sr.virtualRootScopeWalkerFactory.New(&r))
			if err := path.Resolve(target, scopeWalker); err != nil {
				return fmt.Errorf("failed to resolve symlink %#v: %w", dPath.Append(name).GetUNIXString(), err)
			}
			if r.gotScope {
				// Symlink points to a file that resides
				// inside the directory hierarchy.
				// Rewrite the target of the symlink to
				// be of shape "../../a/b/c".
				directoryComponents := dPath.ToList()
				targetComponents := r.components
				matchingCount := 0
				for matchingCount < len(directoryComponents) && matchingCount < len(targetComponents) && directoryComponents[matchingCount] == targetComponents[matchingCount] {
					matchingCount++
				}
				if matchingCount+int(maximumEscapementLevels) < len(directoryComponents) {
					// TODO: Copy files as well?
					return fmt.Errorf("Symlink %#v resolves to a path outside the repo", dPath.Append(name).GetUNIXString())
				}

				// Replace the symlink's target with a
				// relative path.
				// TODO: Any way we can cleanly implement this
				// on top of pkg/filesystem/path?
				dotDotsCount := len(directoryComponents) - matchingCount
				parts := make([]string, 0, dotDotsCount+len(targetComponents)-matchingCount)
				for i := 0; i < dotDotsCount; i++ {
					parts = append(parts, "..")
				}
				for _, component := range targetComponents[matchingCount:] {
					parts = append(parts, component.String())
				}
				d.symlinks[name] = path.UNIXFormat.NewParser(strings.Join(parts, "/"))
			} else {
				// Symlink points to a file that resides
				// outside the directory hierarchy,
				// meaning it refers to a file that is
				// part of the installation of the repo
				// platform worker. Ask the worker to
				// upload this file to storage, so that
				// we can replace the symlink with the
				// actual contents.
				replacement := sr.environment.GetRepoPlatformHostPathValue(
					&model_analysis_pb.RepoPlatformHostPath_Key{
						AbsolutePath: normalizedPath.GetUNIXString(),
					},
				)
				if !replacement.IsSet() {
					// TODO: Only return this error at the
					// very end, so that capturing can be
					// performed in parallel.
					return evaluation.ErrMissingDependency
				}

				delete(d.symlinks, name)
				switch capturedPath := replacement.Message.CapturedPath.(type) {
				case *model_analysis_pb.RepoPlatformHostPath_Value_File:
					f, err := newChangeTrackingFileFromFileProperties[TReference, TMetadata](model_core.Nested(replacement, capturedPath.File))
					if err != nil {
						return err
					}
					d.setFileSimple(name, f)
				case *model_analysis_pb.RepoPlatformHostPath_Value_Directory:
					d.setDirectorySimple(
						name,
						&changeTrackingDirectory[TReference, TMetadata]{
							unmodifiedDirectory: model_core.Nested(replacement, &model_filesystem_pb.Directory{
								Contents: &model_filesystem_pb.Directory_ContentsInline{
									ContentsInline: capturedPath.Directory,
								},
							}),
						},
					)
				default:
					return errors.New("captured host path has an unknown type")
				}
			}
		}
	}

	for name, dChild := range d.directories {
		dStack.Push(dChild)
		if err := sr.relativizeSymlinksRecursively(dStack, dPath.Append(name), maximumEscapementLevels+1); err != nil {
			return err
		}
		if _, ok := dStack.PopSingle(); !ok {
			panic("should have popped previously pushed directory")
		}
	}
	return nil
}
