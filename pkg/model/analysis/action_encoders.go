package analysis

import (
	"context"

	"bonanza.build/pkg/evaluation"
	model_core "bonanza.build/pkg/model/core"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
)

func (c *baseComputer[TReference, TMetadata]) ComputeActionEncodersValue(ctx context.Context, key *model_analysis_pb.ActionEncoders_Key, e ActionEncodersEnvironment[TReference, TMetadata]) (PatchedActionEncodersValue[TMetadata], error) {
	buildSpecification := e.GetBuildSpecificationValue(&model_analysis_pb.BuildSpecification_Key{})
	if !buildSpecification.IsSet() {
		return PatchedActionEncodersValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.ActionEncoders_Value{
		ActionEncoders: buildSpecification.Message.BuildSpecification.GetActionEncoders(),
	}), nil
}

func (c *baseComputer[TReference, TMetadata]) ComputeActionEncoderObjectValue(ctx context.Context, key *model_analysis_pb.ActionEncoderObject_Key, e ActionEncoderObjectEnvironment[TReference, TMetadata]) (model_encoding.BinaryEncoder, error) {
	encoders := e.GetActionEncodersValue(&model_analysis_pb.ActionEncoders_Key{})
	if !encoders.IsSet() {
		return nil, evaluation.ErrMissingDependency
	}
	return model_encoding.NewBinaryEncoderFromProto(
		encoders.Message.ActionEncoders,
		uint32(c.getReferenceFormat().GetMaximumObjectSizeBytes()),
	)
}
