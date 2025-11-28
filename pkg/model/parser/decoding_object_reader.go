package parser

import (
	model_core "bonanza.build/pkg/model/core"
)

// DecodingObjectReader is an ObjectReader that assumes that the
// references that are provided contain decoding parameters (e.g.,
// parameters needed to decrypt the object). Because of this, the
// implementation is going to be backed by a BinaryDecoder, which
// permits querying the size of the decoding parameters that are
// accepted.
type DecodingObjectReader[TReference, TParsedObject any] interface {
	ObjectReader[model_core.Decodable[TReference], TParsedObject]
	GetDecodingParametersSizeBytes() int
}
