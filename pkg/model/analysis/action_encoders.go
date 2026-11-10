package analysis

import (
	"context"

	model_core "bonanza.build/pkg/model/core"
	model_encoding "bonanza.build/pkg/model/encoding"
	"bonanza.build/pkg/model/evaluation"
	model_parser "bonanza.build/pkg/model/parser"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_command_pb "bonanza.build/pkg/proto/model/command"
	model_fetch_pb "bonanza.build/pkg/proto/model/fetch"
)

func (baseComputer[TReference, TMetadata]) ComputeActionEncodersValue(ctx context.Context, key *model_analysis_pb.ActionEncoders_Key, e ActionEncodersEnvironment[TReference, TMetadata]) (PatchedActionEncodersValue[TMetadata], error) {
	buildSpecification := e.GetBuildSpecificationValue(&model_analysis_pb.BuildSpecification_Key{})
	if !buildSpecification.IsSet() {
		return PatchedActionEncodersValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.ActionEncoders_Value{
		ActionEncoders: buildSpecification.Message.ActionEncoders,
	}), nil
}

// ActionReaders contains ObjectReaders that can be used to follow
// references to objects that are encoded using the action encoders that
// are part of the BuildSpecification.
type ActionReaders[TReference any] struct {
	CommandAction              model_parser.MessageObjectReader[TReference, *model_command_pb.Action]
	CommandPathPatternChildren model_parser.MessageObjectReader[TReference, *model_command_pb.PathPattern_Children]
	CommandResult              model_parser.MessageObjectReader[TReference, *model_command_pb.Result]

	FetchResult model_parser.MessageObjectReader[TReference, *model_fetch_pb.Result]
}

// ActionEncoders contains the encoder that needs to be used to create
// objects belonging to actions, and readers for accessing such objects.
// Both are derived from the action encoders that are part of the
// BuildSpecification.
type ActionEncoders[TReference any] struct {
	Encoder model_encoding.DeterministicBinaryEncoder
	Readers ActionReaders[TReference]
}

func (c *baseComputer[TReference, TMetadata]) ComputeActionEncodersObject(value model_core.Message[*model_analysis_pb.ActionEncoders_Value, TReference]) (*ActionEncoders[TReference], error) {
	actionEncoder, err := c.binaryEncoderFactory.NewDeterministicBinaryEncoderFromProto(
		value.Message.ActionEncoders,
		uint32(c.referenceFormat.GetMaximumObjectSizeBytes()),
	)
	if err != nil {
		return nil, err
	}
	encodedObjectParser := model_parser.NewEncodedObjectParser[TReference](actionEncoder)
	return &ActionEncoders[TReference]{
		Encoder: actionEncoder,
		Readers: ActionReaders[TReference]{
			CommandAction: model_parser.LookupParsedObjectReader(
				c.parsedObjectPoolIngester,
				model_parser.NewChainedObjectParser(
					encodedObjectParser,
					model_parser.NewProtoObjectParser[TReference, model_command_pb.Action](),
				),
			),
			CommandPathPatternChildren: model_parser.LookupParsedObjectReader(
				c.parsedObjectPoolIngester,
				model_parser.NewChainedObjectParser(
					encodedObjectParser,
					model_parser.NewProtoObjectParser[TReference, model_command_pb.PathPattern_Children](),
				),
			),
			CommandResult: model_parser.LookupParsedObjectReader(
				c.parsedObjectPoolIngester,
				model_parser.NewChainedObjectParser(
					encodedObjectParser,
					model_parser.NewProtoObjectParser[TReference, model_command_pb.Result](),
				),
			),

			FetchResult: model_parser.LookupParsedObjectReader(
				c.parsedObjectPoolIngester,
				model_parser.NewChainedObjectParser(
					encodedObjectParser,
					model_parser.NewProtoObjectParser[TReference, model_fetch_pb.Result](),
				),
			),
		},
	}, nil
}
