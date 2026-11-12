package analysis

import (
	"context"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	"bonanza.build/pkg/storage/object"
)

func (baseComputer[TReference, TMetadata]) ComputeFileAccessParametersValue(ctx context.Context, key *model_analysis_pb.FileAccessParameters_Key, e FileAccessParametersEnvironment[TReference, TMetadata]) (PatchedFileAccessParametersValue[TMetadata], error) {
	fileCreationParameters := e.GetFileCreationParametersValue(&model_analysis_pb.FileCreationParameters_Key{})
	if !fileCreationParameters.IsSet() {
		return PatchedFileAccessParametersValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.FileAccessParameters_Value{
		FileAccessParameters: fileCreationParameters.Message.FileCreationParameters.GetAccess(),
	}), nil
}

func (c *baseComputer[TReference, TMetadata]) ComputeFileAccessParametersObject(fileAccessParametersValue model_core.Message[*model_analysis_pb.FileAccessParameters_Value, TReference]) (*model_filesystem.FileReader[TReference], error) {
	return model_filesystem.NewFileReaderFromProto(
		fileAccessParametersValue.Message.FileAccessParameters,
		c.referenceFormat,
		c.binaryEncoderFactory,
		c.parsedObjectPoolIngester,
		c.objectStoreSemaphore,
	)
}

// getFileReader returns the FileReader that can be used to read the
// contents of files, and strings that are too large to be stored
// inline. If it is not available yet, ok is set to false.
func getFileReader[TReference object.BasicReference](e FileAccessParametersObjectGetter[TReference]) (*model_filesystem.FileReader[TReference], bool) {
	return e.GetFileAccessParametersObject(&model_analysis_pb.FileAccessParameters_Key{})
}
