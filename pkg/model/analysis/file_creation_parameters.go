package analysis

import (
	"context"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
)

func (baseComputer[TReference, TMetadata]) ComputeFileCreationParametersValue(ctx context.Context, key *model_analysis_pb.FileCreationParameters_Key, e FileCreationParametersEnvironment[TReference, TMetadata]) (PatchedFileCreationParametersValue[TMetadata], error) {
	buildSpecification := e.GetBuildSpecificationValue(&model_analysis_pb.BuildSpecification_Key{})
	if !buildSpecification.IsSet() {
		return PatchedFileCreationParametersValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.FileCreationParameters_Value{
		FileCreationParameters: buildSpecification.Message.FileCreationParameters,
	}), nil
}

func (c *baseComputer[TReference, TMetadata]) ComputeFileCreationParametersObject(fileCreationParameters model_core.Message[*model_analysis_pb.FileCreationParameters_Value, TReference]) (*model_filesystem.FileCreationParameters, error) {
	return model_filesystem.NewFileCreationParametersFromProto(
		fileCreationParameters.Message.FileCreationParameters,
		c.referenceFormat,
		c.binaryEncoderFactory,
	)
}
