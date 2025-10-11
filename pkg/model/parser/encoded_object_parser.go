package parser

import (
	model_core "bonanza.build/pkg/model/core"
	model_encoding "bonanza.build/pkg/model/encoding"
)

type encodedObjectParser[TReference any] struct {
	encoder model_encoding.BinaryEncoder
}

// NewEncodedObjectParser creates an ObjectParser that decodes objects.
// Decoding operations may include decompression and decryption.
func NewEncodedObjectParser[
	TReference any,
](encoder model_encoding.BinaryEncoder) ObjectParser[TReference, model_core.Message[[]byte, TReference]] {
	return &encodedObjectParser[TReference]{
		encoder: encoder,
	}
}

func (p *encodedObjectParser[TReference]) ParseObject(in model_core.Message[[]byte, TReference], decodingParameters []byte) (model_core.Message[[]byte, TReference], int, error) {
	decoded, err := p.encoder.DecodeBinary(in.Message, decodingParameters)
	if err != nil {
		return model_core.Message[[]byte, TReference]{}, 0, err
	}
	return model_core.NewMessage(decoded, in.OutgoingReferences), len(decoded), nil
}

func (p *encodedObjectParser[TReference]) GetDecodingParametersSizeBytes() int {
	return p.encoder.GetDecodingParametersSizeBytes()
}
