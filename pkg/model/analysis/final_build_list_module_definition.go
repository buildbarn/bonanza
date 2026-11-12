package analysis

import (
	"context"
	"sort"
	"strings"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
)

func (baseComputer[TReference, TMetadata]) ComputeFinalBuildListModuleDefinitionValue(ctx context.Context, key *model_analysis_pb.FinalBuildListModuleDefinition_Key, e FinalBuildListModuleDefinitionEnvironment[TReference, TMetadata]) (PatchedFinalBuildListModuleDefinitionValue[TMetadata], error) {
	buildListValue := e.GetModuleFinalBuildListValue(&model_analysis_pb.ModuleFinalBuildList_Key{})
	if !buildListValue.IsSet() {
		return PatchedFinalBuildListModuleDefinitionValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	// The final build list is sorted by name, and contains at
	// most one version of each module.
	buildList := buildListValue.Message.BuildList
	i, ok := sort.Find(
		len(buildList),
		func(i int) int { return strings.Compare(key.ModuleName, buildList[i].Name) },
	)
	if !ok {
		// The module is not part of the final build list.
		// Return an empty value, so that changes to the versions
		// of other modules don't cause this value to change.
		return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.FinalBuildListModuleDefinition_Value{}), nil
	}
	return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.FinalBuildListModuleDefinition_Value{
		Definition: buildList[i].Definition,
	}), nil
}
