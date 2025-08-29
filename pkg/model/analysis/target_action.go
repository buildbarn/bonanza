package analysis

import (
	"bytes"
	"context"
	"errors"

	"bonanza.build/pkg/evaluation"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_core_pb "bonanza.build/pkg/proto/model/core"
)

func (c *baseComputer[TReference, TMetadata]) ComputeTargetActionValue(ctx context.Context, key model_core.Message[*model_analysis_pb.TargetAction_Key, TReference], e TargetActionEnvironment[TReference, TMetadata]) (PatchedTargetActionValue[TMetadata], error) {
	id := key.Message.Id
	if id == nil {
		return PatchedTargetActionValue[TMetadata]{}, errors.New("no target action identifier specified")
	}
	patchedConfigurationReference := model_core.Patch(e, model_core.Nested(key, id.ConfigurationReference))
	configuredTarget := e.GetConfiguredTargetValue(
		model_core.NewPatchedMessage(
			&model_analysis_pb.ConfiguredTarget_Key{
				Label:                  id.Label,
				ConfigurationReference: patchedConfigurationReference.Message,
			},
			patchedConfigurationReference.Patcher,
		),
	)
	if !configuredTarget.IsSet() {
		return PatchedTargetActionValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	actionID := id.ActionId
	action, err := btree.Find(
		ctx,
		c.configuredTargetActionReader,
		model_core.Nested(configuredTarget, configuredTarget.Message.Actions),
		func(entry model_core.Message[*model_analysis_pb.ConfiguredTarget_Value_Action, TReference]) (int, *model_core_pb.DecodableReference) {
			switch level := entry.Message.Level.(type) {
			case *model_analysis_pb.ConfiguredTarget_Value_Action_Leaf_:
				return bytes.Compare(actionID, level.Leaf.Id), nil
			case *model_analysis_pb.ConfiguredTarget_Value_Action_Parent_:
				return bytes.Compare(actionID, level.Parent.FirstId), level.Parent.Reference
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
	actionLevel, ok := action.Message.Level.(*model_analysis_pb.ConfiguredTarget_Value_Action_Leaf_)
	if !ok {
		return PatchedTargetActionValue[TMetadata]{}, errors.New("action is not a leaf")
	}

	patchedDefinition := model_core.Patch(e, model_core.Nested(action, actionLevel.Leaf.Definition))
	return model_core.NewPatchedMessage(
		&model_analysis_pb.TargetAction_Value{
			Definition: patchedDefinition.Message,
		},
		patchedDefinition.Patcher,
	), nil
}
