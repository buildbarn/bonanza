package analysis

import (
	"context"
	"fmt"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
)

func (baseComputer[TReference, TMetadata]) ComputeSuccessfulActionResultValue(ctx context.Context, key model_core.Message[*model_analysis_pb.SuccessfulActionResult_Key, TReference], e SuccessfulActionResultEnvironment[TReference, TMetadata]) (PatchedSuccessfulActionResultValue[TMetadata], error) {
	patchedExecuteRequest := model_core.Patch(e, model_core.Nested(key, key.Message.GetExecuteRequest()))
	actionResult := e.GetActionResultValue(
		model_core.NewPatchedMessage(
			model_analysis_pb.ActionResult_Key_builder{
				ExecuteRequest: patchedExecuteRequest.Message,
			}.Build(),
			patchedExecuteRequest.Patcher,
		),
	)
	if !actionResult.IsSet() {
		return PatchedSuccessfulActionResultValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	if exitCode := actionResult.Message.GetExitCode(); exitCode != 0 {
		return PatchedSuccessfulActionResultValue[TMetadata]{}, fmt.Errorf("action completed with non-zero exit code %d", exitCode)
	}

	patchedOutputsReference := model_core.Patch(e, model_core.Nested(actionResult, actionResult.Message.GetOutputsReference()))
	return model_core.NewPatchedMessage(
		model_analysis_pb.SuccessfulActionResult_Value_builder{
			OutputsReference: patchedOutputsReference.Message,
		}.Build(),
		patchedOutputsReference.Patcher,
	), nil
}
