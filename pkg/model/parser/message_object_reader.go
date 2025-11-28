package parser

import (
	model_core "bonanza.build/pkg/model/core"
)

// MessageObjectReader can be used to read the contents of an object
// from storage and gain access to its parsed contents. This is a high
// level convenience type on top of ObjectReader, as a couple of
// assumptions have been made:
//
//   - The reference of the object to be read has decoding parameters,
//     which are necessary to successfully decode the object. Decoding
//     parameters may include a tag/nonce that is needed to decrypt the
//     object's contents.
//
//   - The object's contents are returned using a Message, meaning
//     references to any child objects are returned as well.
//
//   - The reference type of the object and that of its children are
//     identical. For high level code, this tends to be a common
//     assumption, as it's necessary for easy graph traversal.
type MessageObjectReader[TReference, TMessage any] = DecodingObjectReader[
	TReference,
	model_core.Message[TMessage, TReference],
]
