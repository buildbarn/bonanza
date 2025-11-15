package analysis

import (
	"context"

	"bonanza.build/pkg/model/evaluation"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_parser "bonanza.build/pkg/model/parser"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_command_pb "bonanza.build/pkg/proto/model/command"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
)

// DirectoryReaders contains ParsedObjectReaders that can be used to
// follow references to objects that are encoded using the directory
// access parameters that are part of the BuildSpecification.
type DirectoryReaders[TReference any] struct {
	DirectoryContents model_parser.MessageObjectReader[TReference, *model_filesystem_pb.DirectoryContents]
	Leaves            model_parser.MessageObjectReader[TReference, *model_filesystem_pb.Leaves]
	CommandOutputs    model_parser.MessageObjectReader[TReference, *model_command_pb.Outputs]
}

func (c *baseComputer[TReference, TMetadata]) ComputeDirectoryReadersValue(ctx context.Context, key *model_analysis_pb.DirectoryReaders_Key, e DirectoryReadersEnvironment[TReference, TMetadata]) (*DirectoryReaders[TReference], error) {
	directoryAccessParametersValue := e.GetDirectoryAccessParametersValue(&model_analysis_pb.DirectoryAccessParameters_Key{})
	if !directoryAccessParametersValue.IsSet() {
		return nil, evaluation.ErrMissingDependency
	}
	directoryAccessParameters, err := model_filesystem.NewDirectoryAccessParametersFromProto(
		directoryAccessParametersValue.Message.DirectoryAccessParameters,
		c.referenceFormat,
	)
	if err != nil {
		return nil, err
	}

	encoderObjectParser := model_parser.NewEncodedObjectParser[TReference](directoryAccessParameters.GetEncoder())
	return &DirectoryReaders[TReference]{
		DirectoryContents: model_parser.LookupParsedObjectReader(
			c.parsedObjectPoolIngester,
			model_parser.NewChainedObjectParser(
				encoderObjectParser,
				model_parser.NewProtoObjectParser[TReference, model_filesystem_pb.DirectoryContents](),
			),
		),
		Leaves: model_parser.LookupParsedObjectReader(
			c.parsedObjectPoolIngester,
			model_parser.NewChainedObjectParser(
				encoderObjectParser,
				model_parser.NewProtoObjectParser[TReference, model_filesystem_pb.Leaves](),
			),
		),
		CommandOutputs: model_parser.LookupParsedObjectReader(
			c.parsedObjectPoolIngester,
			model_parser.NewChainedObjectParser(
				encoderObjectParser,
				model_parser.NewProtoObjectParser[TReference, model_command_pb.Outputs](),
			),
		),
	}, nil
}
