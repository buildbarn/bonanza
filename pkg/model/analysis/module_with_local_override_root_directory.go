package analysis

import (
	"context"
	"sort"
	"strings"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
)

func (baseComputer[TReference, TMetadata]) ComputeModuleWithLocalOverrideRootDirectoryValue(ctx context.Context, key *model_analysis_pb.ModuleWithLocalOverrideRootDirectory_Key, e ModuleWithLocalOverrideRootDirectoryEnvironment[TReference, TMetadata]) (PatchedModuleWithLocalOverrideRootDirectoryValue[TMetadata], error) {
	buildSpecification := e.GetBuildSpecificationValue(&model_analysis_pb.BuildSpecification_Key{})
	if !buildSpecification.IsSet() {
		return PatchedModuleWithLocalOverrideRootDirectoryValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	// The build specification lists modules in sorted order.
	modules := buildSpecification.Message.Modules
	i, ok := sort.Find(
		len(modules),
		func(i int) int { return strings.Compare(key.ModuleName, modules[i].Name) },
	)
	if !ok {
		// No local override for this module. Return an empty
		// value, so that changes to the overrides of other
		// modules don't cause this value to change.
		return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.ModuleWithLocalOverrideRootDirectory_Value{}), nil
	}

	rootDirectoryReference := model_core.Patch(e, model_core.Nested(buildSpecification, modules[i].RootDirectoryReference))
	return model_core.NewPatchedMessage(
		&model_analysis_pb.ModuleWithLocalOverrideRootDirectory_Value{
			RootDirectoryReference: rootDirectoryReference.Message,
		},
		rootDirectoryReference.Patcher,
	), nil
}
