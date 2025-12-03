package parser

import (
	model_core "bonanza.build/pkg/model/core"
)

// MessageObjectParser is an ObjectParser that returns the parsed object
// in the form of a model_core.Message. This applies most of the basic
// ObjectParser implementations, like the ones that only parse Protobuf
// messages. However, more complex ones like the one that parses file
// contents lists may completely convert the message to a native type.
type MessageObjectParser[TReference, TMessage any] = ObjectParser[
	TReference,
	model_core.Message[TMessage, TReference],
]
