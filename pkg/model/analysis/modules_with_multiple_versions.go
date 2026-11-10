package analysis

import (
	"context"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
)

func (baseComputer[TReference, TMetadata]) ComputeModulesWithMultipleVersionsValue(ctx context.Context, key *model_analysis_pb.ModulesWithMultipleVersions_Key, e ModulesWithMultipleVersionsEnvironment[TReference, TMetadata]) (PatchedModulesWithMultipleVersionsValue[TMetadata], error) {
	modulesWithOverridesValue := e.GetModulesWithOverridesValue(&model_analysis_pb.ModulesWithOverrides_Key{})
	if !modulesWithOverridesValue.IsSet() {
		return PatchedModulesWithMultipleVersionsValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	var modulesWithMultipleVersions []*model_analysis_pb.OverridesListModule
	for _, module := range modulesWithOverridesValue.Message.OverridesList {
		if len(module.Versions) > 0 {
			modulesWithMultipleVersions = append(modulesWithMultipleVersions, module)
		}
	}
	return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.ModulesWithMultipleVersions_Value{
		OverridesList: modulesWithMultipleVersions,
	}), nil
}

func (baseComputer[TReference, TMetadata]) ComputeModulesWithMultipleVersionsObject(modulesWithMultipleVersionsValue model_core.Message[*model_analysis_pb.ModulesWithMultipleVersions_Value, TReference]) (map[label.Module]OverrideVersions, error) {
	return parseOverridesList(modulesWithMultipleVersionsValue.Message.OverridesList)
}
