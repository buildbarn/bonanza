package analysis

import (
	"context"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_encoding "bonanza.build/pkg/model/encoding"
	"bonanza.build/pkg/model/evaluation"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_parser "bonanza.build/pkg/model/parser"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/storage/object"
)

func (baseComputer[TReference, TMetadata]) ComputeStarlarkValueEncodersValue(ctx context.Context, key *model_analysis_pb.StarlarkValueEncoders_Key, e StarlarkValueEncodersEnvironment[TReference, TMetadata]) (PatchedStarlarkValueEncodersValue[TMetadata], error) {
	buildSpecification := e.GetBuildSpecificationValue(&model_analysis_pb.BuildSpecification_Key{})
	if !buildSpecification.IsSet() {
		return PatchedStarlarkValueEncodersValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.StarlarkValueEncoders_Value{
		FileAccessParameters:  buildSpecification.Message.FileCreationParameters.GetAccess(),
		StarlarkValueEncoders: buildSpecification.Message.StarlarkValueEncoders,
	}), nil
}

// StarlarkValueEncoders contains the encoder that needs to be used to
// create objects containing Starlark values, and readers that can be
// used to follow references to such objects.
type StarlarkValueEncoders[TReference any] struct {
	Encoder model_encoding.DeterministicBinaryEncoder
	Readers model_starlark.ValueReaders[TReference]

	bzlFileBuiltins map[string]eval.Value
}

// newValueDecodingOptions creates ValueDecodingOptions that can be used
// to decode Starlark values.
func (sve *StarlarkValueEncoders[TReference]) newValueDecodingOptions(labelCreator func(label.ResolvedLabel) (eval.Value, error)) *model_starlark.ValueDecodingOptions[TReference] {
	return &model_starlark.ValueDecodingOptions[TReference]{
		Readers:         &sve.Readers,
		LabelCreator:    labelCreator,
		BzlFileBuiltins: sve.bzlFileBuiltins,
	}
}

func (c *baseComputer[TReference, TMetadata]) ComputeStarlarkValueEncodersObject(value model_core.Message[*model_analysis_pb.StarlarkValueEncoders_Value, TReference]) (*StarlarkValueEncoders[TReference], error) {
	fileReader, err := model_filesystem.NewFileReaderFromProto(
		value.Message.FileAccessParameters,
		c.referenceFormat,
		c.binaryEncoderFactory,
		c.parsedObjectPoolIngester,
		c.objectStoreSemaphore,
	)
	if err != nil {
		return nil, err
	}

	encoder, err := c.binaryEncoderFactory.NewDeterministicBinaryEncoderFromProto(
		value.Message.StarlarkValueEncoders,
		uint32(c.referenceFormat.GetMaximumObjectSizeBytes()),
	)
	if err != nil {
		return nil, err
	}
	encodedObjectParser := model_parser.NewEncodedObjectParser[TReference](encoder)
	return &StarlarkValueEncoders[TReference]{
		Encoder: encoder,
		Readers: model_starlark.ValueReaders[TReference]{
			CompiledProgramFunctions: model_parser.LookupParsedObjectReader(
				c.parsedObjectPoolIngester,
				model_parser.NewChainedObjectParser(
					encodedObjectParser,
					model_parser.NewProtoListObjectParser[TReference, model_starlark_pb.CompiledProgram_FunctionEntry](),
				),
			),
			Dict: model_parser.LookupParsedObjectReader(
				c.parsedObjectPoolIngester,
				model_parser.NewChainedObjectParser(
					encodedObjectParser,
					model_parser.NewProtoListObjectParser[TReference, model_starlark_pb.Dict_Entry](),
				),
			),
			List: model_parser.LookupParsedObjectReader(
				c.parsedObjectPoolIngester,
				model_parser.NewChainedObjectParser(
					encodedObjectParser,
					model_parser.NewProtoListObjectParser[TReference, model_starlark_pb.List_Element](),
				),
			),
			PackageGroupOverrides: model_parser.LookupParsedObjectReader(
				c.parsedObjectPoolIngester,
				model_parser.NewChainedObjectParser(
					encodedObjectParser,
					model_parser.NewProtoObjectParser[TReference, model_starlark_pb.PackageGroup_Subpackages_Overrides](),
				),
			),
			FileReader: fileReader,
		},
		bzlFileBuiltins: c.bzlFileBuiltins,
	}, nil
}

// getStarlarkValueEncoders returns the encoders and readers that need
// to be used to access objects containing Starlark values. If they are
// not available yet, ok is set to false.
func getStarlarkValueEncoders[TReference object.BasicReference](e StarlarkValueEncodersObjectGetter[TReference]) (*StarlarkValueEncoders[TReference], bool) {
	return e.GetStarlarkValueEncodersObject(&model_analysis_pb.StarlarkValueEncoders_Key{})
}

// getStarlarkValueReaders returns readers that can be used to follow
// references to objects containing Starlark values. If they are not
// available yet, ok is set to false.
func getStarlarkValueReaders[TReference object.BasicReference](e StarlarkValueEncodersObjectGetter[TReference]) (*model_starlark.ValueReaders[TReference], bool) {
	encoders, ok := getStarlarkValueEncoders(e)
	if !ok {
		return nil, false
	}
	return &encoders.Readers, true
}
