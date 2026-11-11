package analysis

import (
	"context"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
)

func (baseComputer[TReference, TMetadata]) ComputeBuiltinsModuleNamesValue(ctx context.Context, key *model_analysis_pb.BuiltinsModuleNames_Key, e BuiltinsModuleNamesEnvironment[TReference, TMetadata]) (PatchedBuiltinsModuleNamesValue[TMetadata], error) {
	buildSpecification := e.GetBuildSpecificationValue(&model_analysis_pb.BuildSpecification_Key{})
	if !buildSpecification.IsSet() {
		return PatchedBuiltinsModuleNamesValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.BuiltinsModuleNames_Value{
		BuiltinsModuleNames: buildSpecification.Message.BuiltinsModuleNames,
	}), nil
}
