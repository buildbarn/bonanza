package analysis

import (
	"context"
	"fmt"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
)

func (baseComputer[TReference, TMetadata]) ComputeModuleFinalBuildListValue(ctx context.Context, key *model_analysis_pb.ModuleFinalBuildList_Key, e ModuleFinalBuildListEnvironment[TReference, TMetadata]) (PatchedModuleFinalBuildListValue[TMetadata], error) {
	roughBuildListValue := e.GetModuleRoughBuildListValue(&model_analysis_pb.ModuleRoughBuildList_Key{})
	if !roughBuildListValue.IsSet() {
		return PatchedModuleFinalBuildListValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	var buildList []*model_analysis_pb.BuildListModule
	var previousVersionStr string
	var previousVersion label.ModuleVersion
	roughBuildList := roughBuildListValue.Message.GetBuildList()
	for i, module := range roughBuildList {
		version, err := label.NewModuleVersion(module.GetDefinition().GetVersion())
		if err != nil {
			return PatchedModuleFinalBuildListValue[TMetadata]{}, fmt.Errorf("module %#v has invalid version %#v: %w", module.GetName(), module.GetDefinition().GetVersion(), err)
		}

		if len(buildList) == 0 || buildList[len(buildList)-1].GetName() != module.GetName() {
			// New module.
			buildList = append(buildList, module)
		} else if cmp := previousVersion.Compare(version); cmp < 0 {
			// Same module, but a higher version.
			buildList[len(buildList)-1] = module
		} else if cmp == 0 && (i+1 >= len(roughBuildList) || module.GetName() != roughBuildList[i+1].GetName()) {
			// Prevent selection process from being
			// non-deterministic.
			return PatchedModuleFinalBuildListValue[TMetadata]{}, fmt.Errorf("module %#v has ambiguous highest versions %#v and %#v", module.GetName(), previousVersionStr, module.GetDefinition().GetVersion())
		}

		previousVersionStr = module.GetDefinition().GetVersion()
		previousVersion = version
	}

	return model_core.NewSimplePatchedMessage[TMetadata](model_analysis_pb.ModuleFinalBuildList_Value_builder{
		BuildList: buildList,
	}.Build()), nil
}
