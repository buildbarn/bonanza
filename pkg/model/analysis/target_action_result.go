package analysis

import (
	"context"
	"encoding"
	"errors"
	"fmt"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_parser "bonanza.build/pkg/model/parser"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_command_pb "bonanza.build/pkg/proto/model/command"

	"google.golang.org/protobuf/types/known/durationpb"
)

func (c *baseComputer[TReference, TMetadata]) ComputeTargetActionResultValue(ctx context.Context, key model_core.Message[*model_analysis_pb.TargetActionResult_Key, TReference], e TargetActionResultEnvironment[TReference, TMetadata]) (PatchedTargetActionResultValue[TMetadata], error) {
	id := model_core.Nested(key, key.Message.Id)
	if id.Message == nil {
		return PatchedTargetActionResultValue[TMetadata]{}, errors.New("no target action identifier specified")
	}
	patchedID1 := model_core.Patch(e, id)
	action := e.GetTargetActionValue(
		model_core.NewPatchedMessage(
			&model_analysis_pb.TargetAction_Key{
				Id: patchedID1.Message,
			},
			patchedID1.Patcher,
		),
	)
	patchedID2 := model_core.Patch(e, id)
	command := e.GetTargetActionCommandValue(
		model_core.NewPatchedMessage(
			&model_analysis_pb.TargetActionCommand_Key{
				Id: patchedID2.Message,
			},
			patchedID2.Patcher,
		),
	)
	actionEncoder, gotActionEncoder := e.GetActionEncoderObjectValue(&model_analysis_pb.ActionEncoderObject_Key{})
	directoryReaders, gotDirectoryReaders := e.GetDirectoryReadersValue(&model_analysis_pb.DirectoryReaders_Key{})
	patchedID3 := model_core.Patch(e, id)
	inputRoot := e.GetTargetActionInputRootValue(
		model_core.NewPatchedMessage(
			&model_analysis_pb.TargetActionInputRoot_Key{
				Id: patchedID3.Message,
			},
			patchedID3.Patcher,
		),
	)
	if !action.IsSet() || !command.IsSet() || !gotActionEncoder || !gotDirectoryReaders || !inputRoot.IsSet() {
		return PatchedTargetActionResultValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	actionDefinition := action.Message.Definition
	if actionDefinition == nil {
		return PatchedTargetActionResultValue[TMetadata]{}, errors.New("action definition missing")
	}

	commandReference := model_core.Patch(e, model_core.Nested(command, command.Message.CommandReference))
	inputRootReference := model_core.Patch(e, model_core.Nested(inputRoot, inputRoot.Message.InputRootReference))
	referenceFormat := c.referenceFormat
	createdAction, err := model_core.MarshalAndEncodeDeterministic(
		model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) encoding.BinaryMarshaler {
			patcher.Merge(commandReference.Patcher)
			patcher.Merge(inputRootReference.Patcher)
			return model_core.NewProtoBinaryMarshaler(&model_command_pb.Action{
				CommandReference:   commandReference.Message,
				InputRootReference: inputRootReference.Message,
			})
		}),
		referenceFormat,
		actionEncoder,
	)
	if err != nil {
		return PatchedTargetActionResultValue[TMetadata]{}, fmt.Errorf("failed to create action: %w", err)
	}

	actionResultKey, err := model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (*model_analysis_pb.SuccessfulActionResult_Key, error) {
		actionReference, err := patcher.CaptureAndAddDecodableReference(ctx, createdAction, e)
		if err != nil {
			return nil, err
		}
		return &model_analysis_pb.SuccessfulActionResult_Key{
			ExecuteRequest: &model_analysis_pb.ExecuteRequest{
				// TODO: Should we make the execution
				// timeout on build actions configurable?
				// Bazel with REv2 does not set this field
				// for build actions, relying on the cluster
				// to pick a default.
				ExecutionTimeout:      &durationpb.Duration{Seconds: 3600},
				ActionReference:       actionReference,
				PlatformPkixPublicKey: actionDefinition.PlatformPkixPublicKey,
			},
		}, nil
	})
	if err != nil {
		return PatchedTargetActionResultValue[TMetadata]{}, fmt.Errorf("failed to create action result key: %w", err)
	}
	actionResult := e.GetSuccessfulActionResultValue(actionResultKey)
	if !actionResult.IsSet() {
		return PatchedTargetActionResultValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	outputs, err := model_parser.MaybeDereference(
		ctx,
		directoryReaders.CommandOutputs,
		model_core.Nested(actionResult, actionResult.Message.OutputsReference),
	)
	if err != nil {
		return PatchedTargetActionResultValue[TMetadata]{}, fmt.Errorf("failed to obtain outputs from action result: %w", err)
	}
	outputRoot := model_core.Patch(e, model_core.Nested(outputs, outputs.Message.GetOutputRoot()))
	return model_core.NewPatchedMessage(
		&model_analysis_pb.TargetActionResult_Value{
			OutputRoot: outputRoot.Message,
		},
		outputRoot.Patcher,
	), nil
}
