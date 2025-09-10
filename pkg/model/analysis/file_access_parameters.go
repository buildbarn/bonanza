package analysis

import (
	"context"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
)

func (c *baseComputer[TReference, TMetadata]) ComputeFileAccessParametersValue(ctx context.Context, key *model_analysis_pb.FileAccessParameters_Key, e FileAccessParametersEnvironment[TReference, TMetadata]) (PatchedFileAccessParametersValue[TMetadata], error) {
	fileCreationParameters := e.GetFileCreationParametersValue(&model_analysis_pb.FileCreationParameters_Key{})
	if !fileCreationParameters.IsSet() {
		return PatchedFileAccessParametersValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.FileAccessParameters_Value{
		FileAccessParameters: fileCreationParameters.Message.FileCreationParameters.GetAccess(),
	}), nil
}
