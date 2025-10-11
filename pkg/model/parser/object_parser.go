package parser

import (
	model_core "bonanza.build/pkg/model/core"
)

// ObjectParser is used by ParsedObjectReader to parse objects after
// they have been read from storage. Parsing steps may include decoding
// (decompression/decryption), but also unmarshaling.
//
// The same object may be parseable by multiple ObjectParsers. For
// example, for some objects it may be preferential to first scan them
// and create an index, so that subsequent access is faster. This might
// result in multiple ObjectParser implementations, each of which is
// used in different circumstances.
type ObjectParser[TReference, TParsedObject any] interface {
	ParseObject(in model_core.Message[[]byte, TReference], decodingParameters []byte) (TParsedObject, int, error)
	GetDecodingParametersSizeBytes() int
}
