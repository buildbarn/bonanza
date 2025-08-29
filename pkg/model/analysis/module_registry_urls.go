package analysis

import (
	"context"

	"bonanza.build/pkg/evaluation"
	model_core "bonanza.build/pkg/model/core"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
)

func (c *baseComputer[TReference, TMetadata]) ComputeModuleRegistryUrlsValue(ctx context.Context, key *model_analysis_pb.ModuleRegistryUrls_Key, e ModuleRegistryUrlsEnvironment[TReference, TMetadata]) (PatchedModuleRegistryUrlsValue[TMetadata], error) {
	buildSpecification := e.GetBuildSpecificationValue(&model_analysis_pb.BuildSpecification_Key{})
	if !buildSpecification.IsSet() {
		return PatchedModuleRegistryUrlsValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.ModuleRegistryUrls_Value{
		RegistryUrls: buildSpecification.Message.BuildSpecification.ModuleRegistryUrls,
	}), nil
}
