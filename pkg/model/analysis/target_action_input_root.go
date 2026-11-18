package analysis

import (
	"context"
	"errors"
	"fmt"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	"bonanza.build/pkg/model/evaluation"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_core_pb "bonanza.build/pkg/proto/model/core"

	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
)

var componentMainWorkspaceName = path.MustNewComponent("_main")

func (*baseComputer[TReference, TMetadata]) ComputeTargetActionInputRootValue(ctx context.Context, key model_core.Message[*model_analysis_pb.TargetActionInputRoot_Key, TReference], e TargetActionInputRootEnvironment[TReference, TMetadata]) (PatchedTargetActionInputRootValue[TMetadata], error) {
	analysisReaders, gotAnalysisReaders := getAnalysisReaders(e)

	id := model_core.Nested(key, key.Message.Id)
	if id.Message == nil {
		return PatchedTargetActionInputRootValue[TMetadata]{}, errors.New("no target action identifier specified")
	}
	targetLabel, err := label.NewCanonicalLabel(id.Message.Label)
	if err != nil {
		return PatchedTargetActionInputRootValue[TMetadata]{}, errors.New("invalid target label")
	}

	patchedID := model_core.Patch(e, id)
	action := e.GetTargetActionValue(
		model_core.NewPatchedMessage(
			&model_analysis_pb.TargetAction_Key{
				Id: patchedID.Message,
			},
			patchedID.Patcher,
		),
	)
	directoryCreationParameters, gotDirectoryCreationParameters := e.GetDirectoryCreationParametersObject(&model_analysis_pb.DirectoryCreationParameters_Key{})
	directoryReaders, gotDirectoryReaders := e.GetDirectoryAccessParametersObject(&model_analysis_pb.DirectoryAccessParameters_Key{})
	fileCreationParametersMessage := e.GetFileCreationParametersValue(&model_analysis_pb.FileCreationParameters_Key{})
	if !action.IsSet() ||
		!gotDirectoryCreationParameters ||
		!gotDirectoryReaders ||
		!fileCreationParametersMessage.IsSet() {
		return PatchedTargetActionInputRootValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	actionDefinition := action.Message.Definition
	if actionDefinition == nil {
		return PatchedTargetActionInputRootValue[TMetadata]{}, errors.New("action definition missing")
	}

	var rootDirectory model_filesystem.MergedDirectory[TReference]

	// Add empty directories for the output directory of the current
	// package and configuration.
	components, err := getPackageOutputDirectoryComponents(
		model_core.Nested(id, id.Message.Variant.GetConfigurationReference()),
		targetLabel.GetCanonicalPackage(),
		model_analysis_pb.DirectoryLayout_INPUT_ROOT,
	)
	if err != nil {
		return PatchedTargetActionInputRootValue[TMetadata]{}, fmt.Errorf("failed to get package output directory: %w", err)
	}
	outputDirectory := &rootDirectory
	for _, component := range components {
		outputDirectory = outputDirectory.GetOrCreateDirectory(component)
	}
	outputDirectory.MergeDirectoryContents(model_core.Nested(action, actionDefinition.InitialOutputDirectory))

	// Add input files.
	if err := addFilesToMergedDirectory(
		e,
		model_core.Nested(action, actionDefinition.Inputs),
		&rootDirectory,
		model_analysis_pb.DirectoryLayout_INPUT_ROOT,
	); err != nil {
		return PatchedTargetActionInputRootValue[TMetadata]{}, fmt.Errorf("failed to add input files to input root: %w", err)
	}

	if !gotAnalysisReaders {
		return PatchedTargetActionInputRootValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	// Add tools.
	var errIter error
	for tool := range btree.AllLeaves(
		ctx,
		analysisReaders.FilesToRunProvider,
		model_core.Nested(action, actionDefinition.Tools),
		func(element model_core.Message[*model_analysis_pb.FilesToRunProvider, TReference]) (*model_core_pb.DecodableReference, error) {
			return element.Message.GetParent().GetReference(), nil
		},
		&errIter,
	) {
		toolLevel, ok := tool.Message.Level.(*model_analysis_pb.FilesToRunProvider_Leaf_)
		if !ok {
			return PatchedTargetActionInputRootValue[TMetadata]{}, errors.New("not a valid leaf entry for tool")
		}
		toolLeaf := toolLevel.Leaf

		// Add the tool's executable to the input root.
		executable := model_core.Nested(tool, toolLeaf.Executable)
		if err := addFileToMergedDirectory(
			e,
			executable,
			&rootDirectory,
			model_analysis_pb.DirectoryLayout_INPUT_ROOT,
		); err != nil {
			return PatchedTargetActionInputRootValue[TMetadata]{}, fmt.Errorf("failed to add tool executable to input root: %w", err)
		}

		// Create the tool's runfiles directory.
		executablePath, err := model_starlark.FileGetInputRootPath(executable, nil)
		if err != nil {
			return PatchedTargetActionInputRootValue[TMetadata]{}, fmt.Errorf("failed to get path of tool executable: %w", err)
		}
		runfilesDirectoryResolver := mergedDirectoryResolver[TReference]{
			directory: &rootDirectory,
		}
		if err := path.Resolve(
			path.UNIXFormat.NewParser(executablePath+".runfiles"),
			path.NewRelativeScopeWalker(&runfilesDirectoryResolver),
		); err != nil {
			return PatchedTargetActionInputRootValue[TMetadata]{}, fmt.Errorf("failed to create runfiles directory of tool with path %#v: %w", executablePath, err)
		}
		runfilesDirectory := runfilesDirectoryResolver.directory
		if runfilesDirectoryResolver.terminalName != nil {
			runfilesDirectory = runfilesDirectory.GetOrCreateDirectory(*runfilesDirectoryResolver.terminalName)
		}
		if err := addFilesToMergedDirectory(
			e,
			model_core.Nested(tool, toolLeaf.RunfilesFiles),
			runfilesDirectory,
			model_analysis_pb.DirectoryLayout_RUNFILES,
		); err != nil {
			return PatchedTargetActionInputRootValue[TMetadata]{}, fmt.Errorf("failed to add runfiles files of tool with path %#v to input root: %w", executablePath, err)
		}

		// Create a ctx.workspace_name == "_main" directory.
		// This is needed to make path lookups of the form
		// "${RUNFILES_DIR}/_main/../${path}" work.
		runfilesDirectory.GetOrCreateDirectory(componentMainWorkspaceName)

		if len(toolLeaf.RunfilesSymlinks) > 0 {
			return PatchedTargetActionInputRootValue[TMetadata]{}, errors.New("TODO: add runfiles symlinks to the input root")
		}
		if len(toolLeaf.RunfilesRootSymlinks) > 0 {
			return PatchedTargetActionInputRootValue[TMetadata]{}, errors.New("TODO: add runfiles root symlinks to the input root")
		}
	}
	if errIter != nil {
		return PatchedTargetActionInputRootValue[TMetadata]{}, errIter
	}

	createdRootDirectory, err := model_filesystem.CreateMergedDirectory(
		ctx,
		directoryReaders.DirectoryEntries,
		directoryCreationParameters,
		e,
		model_filesystem.MergeConflictPolicyError,
		&rootDirectory,
	)
	if err != nil {
		return PatchedTargetActionInputRootValue[TMetadata]{}, err
	}

	inputRootReference, err := createdRootDirectory.CaptureDirectoryReference(ctx, directoryCreationParameters, e)
	if err != nil {
		return PatchedTargetActionInputRootValue[TMetadata]{}, err
	}
	return model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (*model_analysis_pb.TargetActionInputRoot_Value, error) {
		return &model_analysis_pb.TargetActionInputRoot_Value{
			InputRootReference: inputRootReference.Merge(patcher),
		}, nil
	})
}
