package analysis

import (
	"context"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
)

func (baseComputer[TReference, TMetadata]) ComputeRepoPlatformLabelValue(ctx context.Context, key *model_analysis_pb.RepoPlatformLabel_Key, e RepoPlatformLabelEnvironment[TReference, TMetadata]) (PatchedRepoPlatformLabelValue[TMetadata], error) {
	buildSpecification := e.GetBuildSpecificationValue(&model_analysis_pb.BuildSpecification_Key{})
	if !buildSpecification.IsSet() {
		return PatchedRepoPlatformLabelValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.RepoPlatformLabel_Value{
		ApparentLabel: buildSpecification.Message.RepoPlatformLabel,
	}), nil
}
