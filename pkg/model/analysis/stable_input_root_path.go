package analysis

import (
	"context"
	"encoding"
	"fmt"
	"strings"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_parser "bonanza.build/pkg/model/parser"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_command_pb "bonanza.build/pkg/proto/model/command"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"

	"github.com/buildbarn/bb-storage/pkg/filesystem/path"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func (c *baseComputer[TReference, TMetadata]) ComputeStableInputRootPathValue(ctx context.Context, key *model_analysis_pb.StableInputRootPath_Key, e StableInputRootPathEnvironment[TReference, TMetadata]) (PatchedStableInputRootPathValue[TMetadata], error) {
	actionEncoder, gotActionEncoder := e.GetActionEncoderObjectValue(&model_analysis_pb.ActionEncoderObject_Key{})
	directoryCreationParameters, gotDirectoryCreationParameters := e.GetDirectoryCreationParametersObjectValue(&model_analysis_pb.DirectoryCreationParametersObject_Key{})
	directoryCreationParametersValue := e.GetDirectoryCreationParametersValue(&model_analysis_pb.DirectoryCreationParameters_Key{})
	directoryReaders, gotDirectoryReaders := e.GetDirectoryReadersValue(&model_analysis_pb.DirectoryReaders_Key{})
	fileCreationParametersValue := e.GetFileCreationParametersValue(&model_analysis_pb.FileCreationParameters_Key{})
	fileReader, gotFileReader := e.GetFileReaderValue(&model_analysis_pb.FileReader_Key{})
	repoPlatform := e.GetRegisteredRepoPlatformValue(&model_analysis_pb.RegisteredRepoPlatform_Key{})
	if !gotActionEncoder ||
		!gotDirectoryCreationParameters ||
		!directoryCreationParametersValue.IsSet() ||
		!gotDirectoryReaders ||
		!fileCreationParametersValue.IsSet() ||
		!gotFileReader ||
		!repoPlatform.IsSet() {
		return PatchedStableInputRootPathValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	// Construct a command that simply invokes "pwd" inside of the
	// stable input root path.
	environment := map[string]string{}
	for _, environmentVariable := range repoPlatform.Message.RepositoryOsEnviron {
		environment[environmentVariable.Name] = environmentVariable.Value
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
		return PatchedStableInputRootPathValue[TMetadata]{}, err
	}

	// TODO: This should use inlinedtree.Build().
	createdCommand, err := model_core.MarshalAndEncode(
		model_core.NewPatchedMessage(
			model_core.NewProtoBinaryMarshaler(&model_command_pb.Command{
				Arguments: []*model_command_pb.ArgumentList_Element{{
					Level: &model_command_pb.ArgumentList_Element_Leaf{
						Leaf: "pwd",
					},
				}},
				EnvironmentVariables:        environmentVariableList.Message,
				DirectoryCreationParameters: directoryCreationParametersValue.Message.DirectoryCreationParameters,
				FileCreationParameters:      fileCreationParametersValue.Message.FileCreationParameters,
				WorkingDirectory:            path.EmptyBuilder.GetUNIXString(),
				StableInputRootPathUuid:     repoRuleStableInputRootPathUUID,
			}),
			environmentVariableList.Patcher,
		),
		referenceFormat,
		actionEncoder,
	)
	if err != nil {
		return PatchedStableInputRootPathValue[TMetadata]{}, fmt.Errorf("failed to create command: %w", err)
	}

	createdInputRoot, err := model_core.MarshalAndEncode(
		model_core.NewSimplePatchedMessage[TMetadata](
			model_core.NewProtoBinaryMarshaler(&model_filesystem_pb.DirectoryContents{
				Leaves: &model_filesystem_pb.DirectoryContents_LeavesInline{
					LeavesInline: &model_filesystem_pb.Leaves{},
				},
			}),
		),
		referenceFormat,
		directoryCreationParameters.GetEncoder(),
	)
	if err != nil {
		return PatchedStableInputRootPathValue[TMetadata]{}, fmt.Errorf("failed to create input root: %w", err)
	}

	action, err := model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (encoding.BinaryMarshaler, error) {
		commandReference, err := patcher.CaptureAndAddDecodableReference(ctx, createdCommand, e)
		if err != nil {
			return nil, err
		}
		inputRootReference, err := patcher.CaptureAndAddDecodableReference(ctx, createdInputRoot, e)
		if err != nil {
			return nil, err
		}
		return model_core.NewProtoBinaryMarshaler(&model_command_pb.Action{
			CommandReference: commandReference,
			// TODO: We shouldn't be handcrafting a
			// DirectoryReference here.
			InputRootReference: &model_filesystem_pb.DirectoryReference{
				Reference:                      inputRootReference,
				MaximumSymlinkEscapementLevels: &wrapperspb.UInt32Value{},
			},
		}), nil
	})
	if err != nil {
		return PatchedStableInputRootPathValue[TMetadata]{}, fmt.Errorf("failed to create action: %w", err)
	}
	createdAction, err := model_core.MarshalAndEncode(action, referenceFormat, actionEncoder)
	if err != nil {
		return PatchedStableInputRootPathValue[TMetadata]{}, fmt.Errorf("failed to encode action: %w", err)
	}

	// Invoke "pwd".
	actionResultKey, err := model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (*model_analysis_pb.SuccessfulActionResult_Key, error) {
		actionReference, err := patcher.CaptureAndAddDecodableReference(ctx, createdAction, e)
		if err != nil {
			return nil, err
		}
		return &model_analysis_pb.SuccessfulActionResult_Key{
			ExecuteRequest: &model_analysis_pb.ExecuteRequest{
				PlatformPkixPublicKey: repoPlatform.Message.ExecPkixPublicKey,
				ActionReference:       actionReference,
				ExecutionTimeout:      &durationpb.Duration{Seconds: 60},
			},
		}, nil
	})
	if err != nil {
		return PatchedStableInputRootPathValue[TMetadata]{}, fmt.Errorf("failed to create action result key: %w", err)
	}
	actionResult := e.GetSuccessfulActionResultValue(actionResultKey)
	if !actionResult.IsSet() {
		return PatchedStableInputRootPathValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	// Capture the standard output of "pwd" and trim the trailing
	// newline character that it adds.
	outputs, err := model_parser.MaybeDereference(ctx, directoryReaders.CommandOutputs, model_core.Nested(actionResult, actionResult.Message.OutputsReference))
	if err != nil {
		return PatchedStableInputRootPathValue[TMetadata]{}, fmt.Errorf("failed to obtain outputs from action result: %w", err)
	}

	stdoutEntry, err := model_filesystem.NewFileContentsEntryFromProto(
		model_core.Nested(outputs, outputs.Message.GetStdout()),
	)
	if err != nil {
		return PatchedStableInputRootPathValue[TMetadata]{}, fmt.Errorf("invalid standard output entry: %w", err)
	}
	stdout, err := fileReader.FileReadAll(ctx, stdoutEntry, 1<<20)
	if err != nil {
		return PatchedStableInputRootPathValue[TMetadata]{}, fmt.Errorf("failed to read standard output: %w", err)
	}

	return model_core.NewSimplePatchedMessage[TMetadata](
		&model_analysis_pb.StableInputRootPath_Value{
			InputRootPath: strings.TrimSuffix(string(stdout), "\n"),
		},
	), nil
}

func (baseComputer[TReference, TMetadata]) ComputeStableInputRootPathObjectValue(ctx context.Context, key *model_analysis_pb.StableInputRootPathObject_Key, e StableInputRootPathObjectEnvironment[TReference, TMetadata]) (*model_starlark.BarePath, error) {
	stableInputRootPath := e.GetStableInputRootPathValue(&model_analysis_pb.StableInputRootPath_Key{})
	if !stableInputRootPath.IsSet() {
		return nil, evaluation.ErrMissingDependency
	}
	// TODO: This currently assumes UNIX-based paths. We should
	// likely add an option on the platform that controls the
	// pathname format.
	var resolver model_starlark.PathResolver
	if err := path.Resolve(path.UNIXFormat.NewParser(stableInputRootPath.Message.InputRootPath), &resolver); err != nil {
		return nil, fmt.Errorf("failed to resolve stable input root path: %w", err)
	}
	return resolver.CurrentPath, nil
}
