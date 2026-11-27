package analysis

import (
	"context"
	"fmt"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_parser "bonanza.build/pkg/model/parser"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"

	"google.golang.org/grpc/status"
)

func (baseComputer[TReference, TMetadata]) ComputeActionResultValue(ctx context.Context, key model_core.Message[*model_analysis_pb.ActionResult_Key, TReference], e ActionResultEnvironment[TReference, TMetadata]) (PatchedActionResultValue[TMetadata], error) {
	actionEncoders, gotActionEncoders := e.GetActionEncodersObject(&model_analysis_pb.ActionEncoders_Key{})
	rawActionResultValue := e.GetRawActionResultValue(
		model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.RawActionResult_Key {
			return &model_analysis_pb.RawActionResult_Key{
				ExecuteRequest: model_core.Patch(e, model_core.Nested(key, key.Message.GetExecuteRequest())).Merge(patcher),
			}
		}),
	)
	if !gotActionEncoders || !rawActionResultValue.IsSet() {
		return PatchedActionResultValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	result, err := model_parser.Dereference(
		ctx,
		actionEncoders.Readers.CommandResult,
		model_core.Nested(rawActionResultValue, rawActionResultValue.Message.GetResultReference()),
	)
	if err != nil {
		return PatchedActionResultValue[TMetadata]{}, fmt.Errorf("failed to read completion event: %w", err)
	}
	if err := status.ErrorProto(result.Message.GetStatus()); err != nil {
		return PatchedActionResultValue[TMetadata]{}, err
	}
	outputsReference := model_core.Nested(result, result.Message.GetOutputsReference())
	return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.ActionResult_Value {
		return &model_analysis_pb.ActionResult_Value{
			ExitCode:         result.Message.GetExitCode(),
			OutputsReference: model_core.Patch(e, outputsReference).Merge(patcher),
		}
	}), nil
}
