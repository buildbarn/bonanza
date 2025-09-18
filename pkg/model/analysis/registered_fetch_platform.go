package analysis

import (
	"context"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
)

func (c *baseComputer[TReference, TMetadata]) ComputeRegisteredFetchPlatformValue(ctx context.Context, key *model_analysis_pb.RegisteredFetchPlatform_Key, e RegisteredFetchPlatformEnvironment[TReference, TMetadata]) (PatchedRegisteredFetchPlatformValue[TMetadata], error) {
	buildSpecification := e.GetBuildSpecificationValue(&model_analysis_pb.BuildSpecification_Key{})
	if !buildSpecification.IsSet() {
		return PatchedRegisteredFetchPlatformValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.RegisteredFetchPlatform_Value{
		FetchPlatformPkixPublicKey: buildSpecification.Message.FetchPlatformPkixPublicKey,
	}), nil
}
