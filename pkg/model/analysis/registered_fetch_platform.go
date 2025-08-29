package analysis

import (
	"context"

	"bonanza.build/pkg/evaluation"
	model_core "bonanza.build/pkg/model/core"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
)

func (c *baseComputer[TReference, TMetadata]) ComputeRegisteredFetchPlatformValue(ctx context.Context, key *model_analysis_pb.RegisteredFetchPlatform_Key, e RegisteredFetchPlatformEnvironment[TReference, TMetadata]) (PatchedRegisteredFetchPlatformValue[TMetadata], error) {
	buildSpecification := e.GetBuildSpecificationValue(&model_analysis_pb.BuildSpecification_Key{})
	if !buildSpecification.IsSet() {
		return PatchedRegisteredFetchPlatformValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.RegisteredFetchPlatform_Value{
		FetchPlatformPkixPublicKey: buildSpecification.Message.BuildSpecification.GetFetchPlatformPkixPublicKey(),
	}), nil
}
