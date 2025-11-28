package parser

import (
	"unique"

	model_core "bonanza.build/pkg/model/core"
)

// ObjectParser is used by ParsingObjectReader to parse objects after
// they have been read from storage. Parsing steps may include decoding
// (decompression/decryption), but also unmarshaling.
//
// The same object may be parseable by multiple ObjectParsers. For
// example, for some objects it may be preferential to first scan them
// and create an index, so that subsequent access is faster. This might
// result in multiple ObjectParser implementations, each of which is
// used in different circumstances.
//
// Types like ParsedObjectPool require being able to uniquely identify
// instances of ObjectParser, so that the key used to identify objects
// in storage does not collide. The AppendUniqueKeys() method returns a
// sequence of unique handles.
type ObjectParser[TReference, TParsedObject any] interface {
	ParseObject(in model_core.Message[[]byte, TReference], decodingParameters []byte) (TParsedObject, error)
	AppendUniqueKeys(keys []unique.Handle[any]) []unique.Handle[any]
	GetDecodingParametersSizeBytes() int
}
