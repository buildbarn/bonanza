package analysis

import (
	"context"
	"fmt"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
)

func (c *baseComputer[TReference, TMetadata]) ComputeSuccessfulActionResultValue(ctx context.Context, key model_core.Message[*model_analysis_pb.SuccessfulActionResult_Key, TReference], e SuccessfulActionResultEnvironment[TReference, TMetadata]) (PatchedSuccessfulActionResultValue[TMetadata], error) {
	patchedExecuteRequest := model_core.Patch(e, model_core.Nested(key, key.Message.ExecuteRequest))
	actionResult := e.GetActionResultValue(
		model_core.NewPatchedMessage(
			&model_analysis_pb.ActionResult_Key{
				ExecuteRequest: patchedExecuteRequest.Message,
			},
			patchedExecuteRequest.Patcher,
		),
	)
	if !actionResult.IsSet() {
		return PatchedSuccessfulActionResultValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	if exitCode := actionResult.Message.ExitCode; exitCode != 0 {
		return PatchedSuccessfulActionResultValue[TMetadata]{}, fmt.Errorf("action completed with non-zero exit code %d", exitCode)
	}

	patchedOutputsReference := model_core.Patch(e, model_core.Nested(actionResult, actionResult.Message.OutputsReference))
	return model_core.NewPatchedMessage(
		&model_analysis_pb.SuccessfulActionResult_Value{
			OutputsReference: patchedOutputsReference.Message,
		},
		patchedOutputsReference.Patcher,
	), nil
}
