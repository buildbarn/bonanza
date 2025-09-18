package analysis

import (
	"context"
	"fmt"

	"bonanza.build/pkg/model/evaluation"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_parser "bonanza.build/pkg/model/parser"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
)

func (c *baseComputer[TReference, TMetadata]) ComputeFileReaderValue(ctx context.Context, key *model_analysis_pb.FileReader_Key, e FileReaderEnvironment[TReference, TMetadata]) (*model_filesystem.FileReader[TReference], error) {
	fileAccessParametersValue := e.GetFileAccessParametersValue(&model_analysis_pb.FileAccessParameters_Key{})
	if !fileAccessParametersValue.IsSet() {
		return nil, evaluation.ErrMissingDependency
	}
	fileAccessParameters, err := model_filesystem.NewFileAccessParametersFromProto(
		fileAccessParametersValue.Message.FileAccessParameters,
		c.referenceFormat,
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
	return model_filesystem.NewFileReader(fileContentsListReader, fileChunkReader), nil
}
