package analysis

import (
	"context"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
)

func (baseComputer[TReference, TMetadata]) ComputeModulesWithLocalOverridesValue(ctx context.Context, key *model_analysis_pb.ModulesWithLocalOverrides_Key, e ModulesWithLocalOverridesEnvironment[TReference, TMetadata]) (PatchedModulesWithLocalOverridesValue[TMetadata], error) {
	buildSpecification := e.GetBuildSpecificationValue(&model_analysis_pb.BuildSpecification_Key{})
	if !buildSpecification.IsSet() {
		return PatchedModulesWithLocalOverridesValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	// Only extract the names of the modules, so that this value
	// remains stable if the contents of these modules change. The
	// build specification already lists the modules in sorted order.
	modules := buildSpecification.Message.Modules
	moduleNames := make([]string, 0, len(modules))
	for _, module := range modules {
		moduleNames = append(moduleNames, module.Name)
	}
	return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.ModulesWithLocalOverrides_Value{
		ModuleNames: moduleNames,
	}), nil
}
