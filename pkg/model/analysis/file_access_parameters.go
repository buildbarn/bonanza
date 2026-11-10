package analysis

import (
	"context"
	"fmt"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_parser "bonanza.build/pkg/model/parser"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
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
	fileAccessParameters, err := model_filesystem.NewFileAccessParametersFromProto(
		fileAccessParametersValue.Message.FileAccessParameters,
		c.referenceFormat,
		c.binaryEncoderFactory,
	)
	if err != nil {
		return nil, fmt.Errorf("invalid directory access parameters: %w", err)
	}
	fileContentsListReader := model_parser.LookupParsedObjectReader(
		c.parsedObjectPoolIngester,
		model_parser.NewChainedObjectParser(
			model_parser.NewEncodedObjectParser[TReference](fileAccessParameters.GetFileContentsListEncoder()),
			model_filesystem.NewFileContentsListObjectParser[TReference](),
		),
	)
	fileChunkReader := model_parser.LookupParsedObjectReader(
		c.parsedObjectPoolIngester,
		model_parser.NewChainedObjectParser(
			model_parser.NewEncodedObjectParser[TReference](fileAccessParameters.GetChunkEncoder()),
			model_parser.NewRawObjectParser[TReference](),
		),
	)
	return model_filesystem.NewFileReader(
		fileContentsListReader,
		fileChunkReader,
		c.objectStoreSemaphore,
	), nil
}
