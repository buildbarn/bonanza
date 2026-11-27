package analysis

import (
	"bytes"
	"context"
	"errors"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	"bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_core_pb "bonanza.build/pkg/proto/model/core"
)

func (*baseComputer[TReference, TMetadata]) ComputeTargetActionValue(ctx context.Context, key model_core.Message[*model_analysis_pb.TargetAction_Key, TReference], e TargetActionEnvironment[TReference, TMetadata]) (PatchedTargetActionValue[TMetadata], error) {
	analysisReaders, gotAnalysisReaders := getAnalysisReaders(e)

	id := key.Message.GetId()
	if id == nil {
		return PatchedTargetActionValue[TMetadata]{}, errors.New("no target action identifier specified")
	}
	patchedVariant := model_core.Patch(e, model_core.Nested(key, id.GetVariant()))
	configuredTarget := e.GetConfiguredTargetValue(
		model_core.NewPatchedMessage(
			model_analysis_pb.ConfiguredTarget_Key_builder{
				Label:   id.GetLabel(),
				Variant: patchedVariant.Message,
			}.Build(),
			patchedVariant.Patcher,
		),
	)
	if !configuredTarget.IsSet() || !gotAnalysisReaders {
		return PatchedTargetActionValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	actionID := id.GetActionId()
	action, err := btree.Find(
		ctx,
		analysisReaders.ConfiguredTargetAction,
		model_core.Nested(configuredTarget, configuredTarget.Message.GetActions()),
		func(entry model_core.Message[*model_analysis_pb.ConfiguredTarget_Value_Action, TReference]) (int, *model_core_pb.DecodableReference) {
			switch entry.Message.WhichLevel() {
			case model_analysis_pb.ConfiguredTarget_Value_Action_Leaf_case:
				return bytes.Compare(actionID, entry.Message.GetLeaf().GetId()), nil
			case model_analysis_pb.ConfiguredTarget_Value_Action_Parent_case:
				return bytes.Compare(actionID, entry.Message.GetParent().GetFirstId()), entry.Message.GetParent().GetReference()
			default:
				return 0, nil
			}
		},
	)
	if err != nil {
		return PatchedTargetActionValue[TMetadata]{}, err
	}
	if !action.IsSet() {
		return PatchedTargetActionValue[TMetadata]{}, errors.New("target does not yield an action with the provided identifier")
	}
	actionLevel := action.Message.GetLeaf()
	if actionLevel == nil {
		return PatchedTargetActionValue[TMetadata]{}, errors.New("action is not a leaf")
	}

	patchedDefinition := model_core.Patch(e, model_core.Nested(action, actionLevel.GetDefinition()))
	return model_core.NewPatchedMessage(
		model_analysis_pb.TargetAction_Value_builder{
			Definition: patchedDefinition.Message,
		}.Build(),
		patchedDefinition.Patcher,
	), nil
}
