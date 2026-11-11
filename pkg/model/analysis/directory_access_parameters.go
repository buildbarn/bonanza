package analysis

import (
	"context"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_parser "bonanza.build/pkg/model/parser"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_command_pb "bonanza.build/pkg/proto/model/command"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
)

func (baseComputer[TReference, TMetadata]) ComputeDirectoryAccessParametersValue(ctx context.Context, key *model_analysis_pb.DirectoryAccessParameters_Key, e DirectoryAccessParametersEnvironment[TReference, TMetadata]) (PatchedDirectoryAccessParametersValue[TMetadata], error) {
	buildSpecification := e.GetBuildSpecificationValue(&model_analysis_pb.BuildSpecification_Key{})
	if !buildSpecification.IsSet() {
		return PatchedDirectoryAccessParametersValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.DirectoryAccessParameters_Value{
		DirectoryAccessParameters: buildSpecification.Message.DirectoryCreationParameters.GetAccess(),
	}), nil
}

// DirectoryReaders contains ObjectReaders that can be used to follow
// references to objects that are encoded using the directory access
// parameters that are part of the BuildSpecification.
type DirectoryReaders[TReference any] struct {
	DirectoryContents model_parser.MessageObjectReader[TReference, *model_filesystem_pb.DirectoryContents]
	Leaves            model_parser.MessageObjectReader[TReference, *model_filesystem_pb.Leaves]
	CommandOutputs    model_parser.MessageObjectReader[TReference, *model_command_pb.Outputs]
}

func (c *baseComputer[TReference, TMetadata]) ComputeDirectoryAccessParametersObject(directoryAccessParametersValue model_core.Message[*model_analysis_pb.DirectoryAccessParameters_Value, TReference]) (*DirectoryReaders[TReference], error) {
	directoryAccessParameters, err := model_filesystem.NewDirectoryAccessParametersFromProto(
		directoryAccessParametersValue.Message.DirectoryAccessParameters,
		c.referenceFormat,
		c.binaryEncoderFactory,
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
