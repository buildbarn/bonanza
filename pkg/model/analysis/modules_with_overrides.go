package analysis

import (
	"context"
	"strings"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
)

func (baseComputer[TReference, TMetadata]) ComputeModulesWithOverridesValue(ctx context.Context, key *model_analysis_pb.ModulesWithOverrides_Key, e ModulesWithOverridesEnvironment[TReference, TMetadata]) (PatchedModulesWithOverridesValue[TMetadata], error) {
	remoteOverrides := e.GetModulesWithRemoteOverridesValue(&model_analysis_pb.ModulesWithRemoteOverrides_Key{})
	localOverrides := e.GetModulesWithLocalOverridesValue(&model_analysis_pb.ModulesWithLocalOverrides_Key{})
	if !remoteOverrides.IsSet() || !localOverrides.IsSet() {
		return PatchedModulesWithOverridesValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	moduleOverrides := remoteOverrides.Message.GetModuleOverrides()
	clientOverrides := localOverrides.Message.GetModuleNames()
	overrideList := make([]*model_analysis_pb.OverridesListModule, 0, len(moduleOverrides)+len(clientOverrides))

	// Merge sort - both lists are already sorted for caching.
	for len(moduleOverrides) > 0 && len(clientOverrides) > 0 {
		cmp := strings.Compare(moduleOverrides[0].GetName(), clientOverrides[0])
		switch {
		case cmp < 0:
			overrideList = append(overrideList, moduleOverrideToOverrideModule(moduleOverrides[0])...)
			moduleOverrides = moduleOverrides[1:]
		case cmp > 0:
			overrideList = append(overrideList, model_analysis_pb.OverridesListModule_builder{
				Name: clientOverrides[0],
			}.Build())
			clientOverrides = clientOverrides[1:]
		default: // Same values - use client override instead of module as its explicate to this run.
			overrideList = append(overrideList, model_analysis_pb.OverridesListModule_builder{
				Name: clientOverrides[0],
			}.Build())
			clientOverrides = clientOverrides[1:]
			moduleOverrides = moduleOverrides[1:]
		}
	}

	// Add the remaining entries, only one of the two could have leftovers.
	for _, o := range moduleOverrides {
		overrideList = append(overrideList, moduleOverrideToOverrideModule(o)...)
	}
	for _, o := range clientOverrides {
		overrideList = append(overrideList, model_analysis_pb.OverridesListModule_builder{
			Name: o,
		}.Build())
	}

	return model_core.NewSimplePatchedMessage[TMetadata](model_analysis_pb.ModulesWithOverrides_Value_builder{
		OverridesList: overrideList,
	}.Build()), nil
}

func moduleOverrideToOverrideModule(mo *model_analysis_pb.ModuleOverride) []*model_analysis_pb.OverridesListModule {
	olm := model_analysis_pb.OverridesListModule_builder{
		Name: mo.GetName(),
	}.Build()
	switch mo.GetDefinition().WhichKind() {
	case model_analysis_pb.ModuleOverride_Definition_MultipleVersions_case:
		olm.SetVersions(mo.GetDefinition().GetMultipleVersions().GetVersions())
	case model_analysis_pb.ModuleOverride_Definition_SingleVersion_case:
		if mo.GetDefinition().GetSingleVersion().GetVersion() == "" {
			// If no version number is specified, we must
			// still go through minimal version selection to
			// determine the version to use. We should
			// therefore not treat it as a module that is
			// directly accessible.
			return nil
		}
	}
	return []*model_analysis_pb.OverridesListModule{olm}
}
