package analysis

import (
	"context"
	"fmt"

	"bonanza.build/pkg/evaluation"
	model_core "bonanza.build/pkg/model/core"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
)

func (c *baseComputer[TReference, TMetadata]) ComputeCompatibleExecutionPlatformsValue(ctx context.Context, key *model_analysis_pb.CompatibleExecutionPlatforms_Key, e CompatibleExecutionPlatformsEnvironment[TReference, TMetadata]) (PatchedCompatibleExecutionPlatformsValue[TMetadata], error) {
	registeredExecutionPlatforms := e.GetRegisteredExecutionPlatformsValue(&model_analysis_pb.RegisteredExecutionPlatforms_Key{})
	if !registeredExecutionPlatforms.IsSet() {
		return PatchedCompatibleExecutionPlatformsValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	allExecutionPlatforms := registeredExecutionPlatforms.Message.ExecutionPlatforms
	var compatibleExecutionPlatforms []*model_analysis_pb.ExecutionPlatform
	for _, executionPlatform := range allExecutionPlatforms {
		if constraintsAreCompatible(executionPlatform.Constraints, key.Constraints) {
			compatibleExecutionPlatforms = append(compatibleExecutionPlatforms, executionPlatform)
		}
	}
	if len(compatibleExecutionPlatforms) == 0 {
		return PatchedCompatibleExecutionPlatformsValue[TMetadata]{}, fmt.Errorf("none of the %d registered execution platforms are compatible with the provided constraints", len(allExecutionPlatforms))
	}

	return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.CompatibleExecutionPlatforms_Value{
		ExecutionPlatforms: compatibleExecutionPlatforms,
	}), nil
}
