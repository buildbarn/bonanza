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

func (c *baseComputer[TReference, TMetadata]) ComputeActionResultValue(ctx context.Context, key model_core.Message[*model_analysis_pb.ActionResult_Key, TReference], e ActionResultEnvironment[TReference, TMetadata]) (PatchedActionResultValue[TMetadata], error) {
	actionReaders, gotActionReaders := e.GetActionReadersValue(&model_analysis_pb.ActionReaders_Key{})
	rawActionResultValue := e.GetRawActionResultValue(
		model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.RawActionResult_Key {
			return &model_analysis_pb.RawActionResult_Key{
				ExecuteRequest: model_core.Patch(e, model_core.Nested(key, key.Message.ExecuteRequest)).Merge(patcher),
			}
		}),
	)
	if !gotActionReaders || !rawActionResultValue.IsSet() {
		return PatchedActionResultValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	result, err := model_parser.Dereference(
		ctx,
		actionReaders.CommandResult,
		model_core.Nested(rawActionResultValue, rawActionResultValue.Message.ResultReference),
	)
	if err != nil {
		return PatchedActionResultValue[TMetadata]{}, fmt.Errorf("failed to read completion event: %w", err)
	}
	if err := status.ErrorProto(result.Message.Status); err != nil {
		return PatchedActionResultValue[TMetadata]{}, err
	}
	outputsReference := model_core.Nested(result, result.Message.OutputsReference)
	return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.ActionResult_Value {
		return &model_analysis_pb.ActionResult_Value{
			ExitCode:         result.Message.ExitCode,
			OutputsReference: model_core.Patch(e, outputsReference).Merge(patcher),
		}
	}), nil
}
