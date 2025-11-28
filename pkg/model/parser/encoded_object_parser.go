package parser

import (
	"unique"

	model_core "bonanza.build/pkg/model/core"
	model_encoding "bonanza.build/pkg/model/encoding"
)

type encodedObjectParser[TReference any] struct {
	decoder model_encoding.BinaryDecoder
}

// NewEncodedObjectParser creates an ObjectParser that decodes objects.
// Decoding operations may include decompression and decryption.
func NewEncodedObjectParser[
	TReference any,
](decoder model_encoding.BinaryDecoder) ObjectParser[TReference, model_core.Message[[]byte, TReference]] {
	return &encodedObjectParser[TReference]{
		decoder: decoder,
	}
}

func (p *encodedObjectParser[TReference]) ParseObject(in model_core.Message[[]byte, TReference], decodingParameters []byte) (model_core.Message[[]byte, TReference], error) {
	decoded, err := p.decoder.DecodeBinary(in.Message, decodingParameters)
	if err != nil {
		return model_core.Message[[]byte, TReference]{}, err
	}
	return model_core.NewMessage(decoded, in.OutgoingReferences), nil
}

func (p *encodedObjectParser[TReference]) AppendUniqueKeys(keys []unique.Handle[any]) []unique.Handle[any] {
	return p.decoder.AppendUniqueDecodingKeys(keys)
}

func (p *encodedObjectParser[TReference]) GetDecodingParametersSizeBytes() int {
	return p.decoder.GetDecodingParametersSizeBytes()
}
