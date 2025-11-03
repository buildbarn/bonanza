package analysis

import (
	"context"
	"sort"
	"strings"

	model_core "bonanza.build/pkg/model/core"
	model_evaluation "bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
)

func (baseComputer[TReference, TMetadata]) ComputeTargetProviderValue(ctx context.Context, key model_core.Message[*model_analysis_pb.TargetProvider_Key, TReference], e TargetProviderEnvironment[TReference, TMetadata]) (PatchedTargetProviderValue[TMetadata], error) {
	targetProviders := e.GetTargetProvidersValue(
		model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.TargetProviders_Key {
			return &model_analysis_pb.TargetProviders_Key{
				Label:                  key.Message.Label,
				ConfigurationReference: model_core.Patch(e, model_core.Nested(key, key.Message.ConfigurationReference)).Merge(patcher),
			}
		}),
	)
	if !targetProviders.IsSet() {
		return PatchedTargetProviderValue[TMetadata]{}, model_evaluation.ErrMissingDependency
	}

	providerIdentifier := key.Message.ProviderIdentifier
	providerInstances := targetProviders.Message.ProviderInstances
	if providerIndex, ok := sort.Find(
		len(providerInstances),
		func(i int) int {
			return strings.Compare(providerIdentifier, providerInstances[i].ProviderInstanceProperties.GetProviderIdentifier())
		},
	); ok {
		return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.TargetProvider_Value {
			return &model_analysis_pb.TargetProvider_Value{
				Fields: model_core.Patch(
					e,
					model_core.Nested(targetProviders, providerInstances[providerIndex].Fields),
				).Merge(patcher),
			}
		}), nil
	}
	return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.TargetProvider_Value{}), nil
}
