package analysis

import (
	"context"

	model_core "bonanza.build/pkg/model/core"
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
	CommandAction              model_parser.ParsedObjectReader[model_core.Decodable[TReference], model_core.Message[*model_command_pb.Action, TReference]]
	CommandPathPatternChildren model_parser.ParsedObjectReader[model_core.Decodable[TReference], model_core.Message[*model_command_pb.PathPattern_Children, TReference]]
	CommandResult              model_parser.ParsedObjectReader[model_core.Decodable[TReference], model_core.Message[*model_command_pb.Result, TReference]]

	FetchResult model_parser.ParsedObjectReader[model_core.Decodable[TReference], model_core.Message[*model_fetch_pb.Result, TReference]]
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
