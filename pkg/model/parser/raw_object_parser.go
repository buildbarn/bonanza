package parser

import (
	model_core "bonanza.build/pkg/model/core"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type rawObjectParser[TReference any] struct{}

func NewRawObjectParser[TReference any]() ObjectParser[TReference, []byte] {
	return &rawObjectParser[TReference]{}
}

func (rawObjectParser[TReference]) ParseObject(in model_core.Message[[]byte, TReference], decodingParameters []byte) ([]byte, error) {
	if len(decodingParameters) > 0 {
		return nil, status.Error(codes.InvalidArgument, "Unexpected decoding parameters")
	}

	if degree := in.OutgoingReferences.GetDegree(); degree > 0 {
		return nil, status.Errorf(codes.InvalidArgument, "Object has a degree of %d, while zero was expected", degree)
	}
	return in.Message, nil
}

func (rawObjectParser[TReference]) GetDecodingParametersSizeBytes() int {
	return 0
}
