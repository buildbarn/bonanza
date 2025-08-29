package analysis

import (
	"context"

	"bonanza.build/pkg/evaluation"
	model_core "bonanza.build/pkg/model/core"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
)

func (c *baseComputer[TReference, TMetadata]) ComputeFileCreationParametersValue(ctx context.Context, key *model_analysis_pb.FileCreationParameters_Key, e FileCreationParametersEnvironment[TReference, TMetadata]) (PatchedFileCreationParametersValue[TMetadata], error) {
	buildSpecification := e.GetBuildSpecificationValue(&model_analysis_pb.BuildSpecification_Key{})
	if !buildSpecification.IsSet() {
		return PatchedFileCreationParametersValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.FileCreationParameters_Value{
		FileCreationParameters: buildSpecification.Message.BuildSpecification.GetFileCreationParameters(),
	}), nil
}

func (c *baseComputer[TReference, TMetadata]) ComputeFileCreationParametersObjectValue(ctx context.Context, key *model_analysis_pb.FileCreationParametersObject_Key, e FileCreationParametersObjectEnvironment[TReference, TMetadata]) (*model_filesystem.FileCreationParameters, error) {
	fileCreationParameters := e.GetFileCreationParametersValue(&model_analysis_pb.FileCreationParameters_Key{})
	if !fileCreationParameters.IsSet() {
		return nil, evaluation.ErrMissingDependency
	}
	return model_filesystem.NewFileCreationParametersFromProto(
		fileCreationParameters.Message.FileCreationParameters,
		c.getReferenceFormat(),
	)
}
