package analysis

import (
	"context"

	model_core "bonanza.build/pkg/model/core"
	model_evaluation "bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
)

func (c *baseComputer[TReference, TMetadata]) ComputeTargetProvidersValue(ctx context.Context, key model_core.Message[*model_analysis_pb.TargetProviders_Key, TReference], e TargetProvidersEnvironment[TReference, TMetadata]) (PatchedTargetProvidersValue[TMetadata], error) {
	configuredTarget := e.GetConfiguredTargetValue(
		model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.ConfiguredTarget_Key {
			return &model_analysis_pb.ConfiguredTarget_Key{
				Label:                  key.Message.Label,
				ConfigurationReference: model_core.Patch(e, model_core.Nested(key, key.Message.ConfigurationReference)).Merge(patcher),
			}
		}),
	)
	if !configuredTarget.IsSet() {
		return PatchedTargetProvidersValue[TMetadata]{}, model_evaluation.ErrMissingDependency
	}

	return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.TargetProviders_Value {
		return &model_analysis_pb.TargetProviders_Value{
			ProviderInstances: model_core.PatchList(e, model_core.Nested(configuredTarget, configuredTarget.Message.ProviderInstances)).Merge(patcher),
		}
	}), nil
}
