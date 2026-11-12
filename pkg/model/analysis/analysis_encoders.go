package analysis

import (
	"context"

	model_core "bonanza.build/pkg/model/core"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_parser "bonanza.build/pkg/model/parser"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
)

func (baseComputer[TReference, TMetadata]) ComputeAnalysisEncodersValue(ctx context.Context, key *model_analysis_pb.AnalysisEncoders_Key, e AnalysisEncodersEnvironment[TReference, TMetadata]) (PatchedAnalysisEncodersValue[TMetadata], error) {
	return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.AnalysisEncoders_Value{}), nil
}

// AnalysisReaders contains ObjectReaders that can be used to follow
// references to objects that are created by analysis functions.
type AnalysisReaders[TReference any] struct {
	Args                          model_parser.MessageObjectReader[TReference, []*model_analysis_pb.Args]
	ArgsAdd                       model_parser.MessageObjectReader[TReference, []*model_analysis_pb.Args_Leaf_Add]
	BuildSettingOverride          model_parser.MessageObjectReader[TReference, []*model_analysis_pb.BuildSettingOverride]
	ConfiguredTargetAction        model_parser.MessageObjectReader[TReference, []*model_analysis_pb.ConfiguredTarget_Value_Action]
	ConfiguredTargetOutput        model_parser.MessageObjectReader[TReference, []*model_analysis_pb.ConfiguredTarget_Value_Output]
	FilesToRunProvider            model_parser.MessageObjectReader[TReference, []*model_analysis_pb.FilesToRunProvider]
	MacroExpansionContextEntry    model_parser.MessageObjectReader[TReference, []*model_starlark_pb.MacroExpansionContext_Entry]
	MacroInstanceEntry            model_parser.MessageObjectReader[TReference, []*model_starlark_pb.MacroInstance_Entry]
	ModuleExtensionReposValueRepo model_parser.MessageObjectReader[TReference, []*model_analysis_pb.ModuleExtensionRepos_Value_Repo]
	PackageValueTarget            model_parser.MessageObjectReader[TReference, []*model_analysis_pb.Package_Value_Target]
	TargetLabel                   model_parser.MessageObjectReader[TReference, []*model_analysis_pb.TargetLabel]
	TestSuiteTest                 model_parser.MessageObjectReader[TReference, []*model_starlark_pb.TestSuite_Test]
}

// AnalysisEncoders contains the encoder that needs to be used to create
// objects that are created by analysis functions, and readers for
// accessing such objects.
type AnalysisEncoders[TReference any] struct {
	Encoder model_encoding.DeterministicBinaryEncoder
	Readers AnalysisReaders[TReference]
}

func (c *baseComputer[TReference, TMetadata]) ComputeAnalysisEncodersObject(value model_core.Message[*model_analysis_pb.AnalysisEncoders_Value, TReference]) (*AnalysisEncoders[TReference], error) {
	// TODO: Set up encoding!
	return &AnalysisEncoders[TReference]{
		Encoder: model_encoding.NewChainedDeterministicBinaryEncoder(nil),
		Readers: AnalysisReaders[TReference]{
			Args: model_parser.LookupParsedObjectReader(
				c.parsedObjectPoolIngester,
				model_parser.NewProtoListObjectParser[TReference, model_analysis_pb.Args](),
			),
			ArgsAdd: model_parser.LookupParsedObjectReader(
				c.parsedObjectPoolIngester,
				model_parser.NewProtoListObjectParser[TReference, model_analysis_pb.Args_Leaf_Add](),
			),
			BuildSettingOverride: model_parser.LookupParsedObjectReader(
				c.parsedObjectPoolIngester,
				model_parser.NewProtoListObjectParser[TReference, model_analysis_pb.BuildSettingOverride](),
			),
			ConfiguredTargetAction: model_parser.LookupParsedObjectReader(
				c.parsedObjectPoolIngester,
				model_parser.NewProtoListObjectParser[TReference, model_analysis_pb.ConfiguredTarget_Value_Action](),
			),
			ConfiguredTargetOutput: model_parser.LookupParsedObjectReader(
				c.parsedObjectPoolIngester,
				model_parser.NewProtoListObjectParser[TReference, model_analysis_pb.ConfiguredTarget_Value_Output](),
			),
			FilesToRunProvider: model_parser.LookupParsedObjectReader(
				c.parsedObjectPoolIngester,
				model_parser.NewProtoListObjectParser[TReference, model_analysis_pb.FilesToRunProvider](),
			),
			MacroExpansionContextEntry: model_parser.LookupParsedObjectReader(
				c.parsedObjectPoolIngester,
				model_parser.NewProtoListObjectParser[TReference, model_starlark_pb.MacroExpansionContext_Entry](),
			),
			MacroInstanceEntry: model_parser.LookupParsedObjectReader(
				c.parsedObjectPoolIngester,
				model_parser.NewProtoListObjectParser[TReference, model_starlark_pb.MacroInstance_Entry](),
			),
			ModuleExtensionReposValueRepo: model_parser.LookupParsedObjectReader(
				c.parsedObjectPoolIngester,
				model_parser.NewProtoListObjectParser[TReference, model_analysis_pb.ModuleExtensionRepos_Value_Repo](),
			),
			PackageValueTarget: model_parser.LookupParsedObjectReader(
				c.parsedObjectPoolIngester,
				model_parser.NewProtoListObjectParser[TReference, model_analysis_pb.Package_Value_Target](),
			),
			TargetLabel: model_parser.LookupParsedObjectReader(
				c.parsedObjectPoolIngester,
				model_parser.NewProtoListObjectParser[TReference, model_analysis_pb.TargetLabel](),
			),
			TestSuiteTest: model_parser.LookupParsedObjectReader(
				c.parsedObjectPoolIngester,
				model_parser.NewProtoListObjectParser[TReference, model_starlark_pb.TestSuite_Test](),
			),
		},
	}, nil
}

// getAnalysisEncoders returns the encoder that needs to be used to
// create objects that are created by analysis functions, and readers
// for accessing such objects. If they are not available yet, ok is set
// to false.
func getAnalysisEncoders[TReference any](e AnalysisEncodersObjectGetter[TReference]) (*AnalysisEncoders[TReference], bool) {
	return e.GetAnalysisEncodersObject(&model_analysis_pb.AnalysisEncoders_Key{})
}

// getAnalysisReaders returns readers that can be used to follow
// references to objects that are created by analysis functions. If they
// are not available yet, ok is set to false.
func getAnalysisReaders[TReference any](e AnalysisEncodersObjectGetter[TReference]) (*AnalysisReaders[TReference], bool) {
	encoders, ok := getAnalysisEncoders(e)
	if !ok {
		return nil, false
	}
	return &encoders.Readers, true
}
