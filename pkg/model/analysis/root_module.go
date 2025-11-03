package analysis

import (
	"context"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
)

func (baseComputer[TReference, TMetadata]) ComputeRootModuleValue(ctx context.Context, key *model_analysis_pb.RootModule_Key, e RootModuleEnvironment[TReference, TMetadata]) (PatchedRootModuleValue[TMetadata], error) {
	buildSpecification := e.GetBuildSpecificationValue(&model_analysis_pb.BuildSpecification_Key{})
	if !buildSpecification.IsSet() {
		return PatchedRootModuleValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.RootModule_Value{
		RootModuleName:                  buildSpecification.Message.RootModuleName,
		IgnoreRootModuleDevDependencies: buildSpecification.Message.IgnoreRootModuleDevDependencies,
	}), nil
}
