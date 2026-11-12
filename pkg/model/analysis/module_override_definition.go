package analysis

import (
	"context"
	"sort"
	"strings"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
)

func (baseComputer[TReference, TMetadata]) ComputeModuleOverrideDefinitionValue(ctx context.Context, key *model_analysis_pb.ModuleOverrideDefinition_Key, e ModuleOverrideDefinitionEnvironment[TReference, TMetadata]) (PatchedModuleOverrideDefinitionValue[TMetadata], error) {
	remoteOverridesValue := e.GetModulesWithRemoteOverridesValue(&model_analysis_pb.ModulesWithRemoteOverrides_Key{})
	if !remoteOverridesValue.IsSet() {
		return PatchedModuleOverrideDefinitionValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	remoteOverrides := remoteOverridesValue.Message.ModuleOverrides
	i, ok := sort.Find(
		len(remoteOverrides),
		func(i int) int { return strings.Compare(key.ModuleName, remoteOverrides[i].Name) },
	)
	if !ok {
		// No override for this module. Return an empty value,
		// so that changes to the overrides of other modules
		// don't cause this value to change.
		return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.ModuleOverrideDefinition_Value{}), nil
	}

	definition := model_core.Patch(e, model_core.Nested(remoteOverridesValue, remoteOverrides[i].Definition))
	return model_core.NewPatchedMessage(
		&model_analysis_pb.ModuleOverrideDefinition_Value{
			Definition: definition.Message,
		},
		definition.Patcher,
	), nil
}
