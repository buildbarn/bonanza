package parser

import (
	"context"

	model_core "bonanza.build/pkg/model/core"
)

type parsedObjectReader[TReference, TParsedObject any] struct {
	rawReader ObjectReader[TReference, model_core.Message[[]byte, TReference]]
	parser    ObjectParser[TReference, TParsedObject]
}

// NewParsedObjectReader creates a decorator for ObjectReader that
// parses objects after they have been downloaded.
func NewParsedObjectReader[TReference, TParsedObject any](rawReader ObjectReader[TReference, model_core.Message[[]byte, TReference]], parser ObjectParser[TReference, TParsedObject]) DecodingObjectReader[TReference, TParsedObject] {
	return &parsedObjectReader[TReference, TParsedObject]{
		rawReader: rawReader,
		parser:    parser,
	}
}

func (r *parsedObjectReader[TReference, TParsedObject]) ReadObject(ctx context.Context, reference model_core.Decodable[TReference]) (TParsedObject, error) {
	raw, err := r.rawReader.ReadObject(ctx, reference.Value)
	if err != nil {
		var bad TParsedObject
		return bad, err
	}
	return r.parser.ParseObject(raw, reference.GetDecodingParameters())
}

func (r *parsedObjectReader[TReference, TParsedObject]) GetDecodingParametersSizeBytes() int {
	return r.parser.GetDecodingParametersSizeBytes()
}
