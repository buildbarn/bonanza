package parser

import (
	"unique"

	model_core "bonanza.build/pkg/model/core"
	model_format "bonanza.build/pkg/model/format"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type formatObjectParser[TReference, TValue any] struct {
	unmarshaler model_format.Unmarshaler[TValue]
}

// NewFormatObjectParser creates an ObjectParser that unmarshals the
// payload of objects using a model_format.Unmarshaler.
//
// The unmarshaler is used to uniquely identify the parser. It must
// therefore be comparable, and unmarshalers that compare equal must
// yield the same results.
func NewFormatObjectParser[TReference, TValue any](unmarshaler model_format.Unmarshaler[TValue]) MessageObjectParser[TReference, TValue] {
	return &formatObjectParser[TReference, TValue]{
		unmarshaler: unmarshaler,
	}
}

func (p *formatObjectParser[TReference, TValue]) ParseObject(in model_core.Message[[]byte, TReference], decodingParameters []byte) (model_core.Message[TValue, TReference], error) {
	if len(decodingParameters) > 0 {
		return model_core.Message[TValue, TReference]{}, status.Error(codes.InvalidArgument, "Unexpected decoding parameters")
	}

	value, err := p.unmarshaler.Unmarshal(in.Message)
	if err != nil {
		return model_core.Message[TValue, TReference]{}, err
	}
	return model_core.NewMessage(value, in.OutgoingReferences), nil
}

func (p *formatObjectParser[TReference, TValue]) AppendUniqueKeys(keys []unique.Handle[any]) []unique.Handle[any] {
	return append(keys, unique.Make[any](p.unmarshaler))
}

func (formatObjectParser[TReference, TValue]) GetDecodingParametersSizeBytes() int {
	return 0
}
