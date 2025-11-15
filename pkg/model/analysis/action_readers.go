package analysis

import (
	"context"

	"bonanza.build/pkg/model/evaluation"
	model_parser "bonanza.build/pkg/model/parser"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_command_pb "bonanza.build/pkg/proto/model/command"
	model_fetch_pb "bonanza.build/pkg/proto/model/fetch"
)

// ActionReaders contains ParsedObjectReaders that can be used to follow
// references to objects that are encoded using the action encoders
// that are part of the BuildSpecification.
type ActionReaders[TReference any] struct {
	CommandAction              model_parser.MessageObjectReader[TReference, *model_command_pb.Action]
	CommandPathPatternChildren model_parser.MessageObjectReader[TReference, *model_command_pb.PathPattern_Children]
	CommandResult              model_parser.MessageObjectReader[TReference, *model_command_pb.Result]

	FetchResult model_parser.MessageObjectReader[TReference, *model_fetch_pb.Result]
}

func (c *baseComputer[TReference, TMetadata]) ComputeActionReadersValue(ctx context.Context, key *model_analysis_pb.ActionReaders_Key, e ActionReadersEnvironment[TReference, TMetadata]) (*ActionReaders[TReference], error) {
	actionEncoder, gotActionEncoder := e.GetActionEncoderObjectValue(&model_analysis_pb.ActionEncoderObject_Key{})
	if !gotActionEncoder {
		return nil, evaluation.ErrMissingDependency
	}
	encodedObjectParser := model_parser.NewEncodedObjectParser[TReference](actionEncoder)
	return &ActionReaders[TReference]{
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
	}, nil
}
