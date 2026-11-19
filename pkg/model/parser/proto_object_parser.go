package parser

import (
	model_format "bonanza.build/pkg/model/format"

	"google.golang.org/protobuf/proto"
)

// NewProtoObjectParser creates an ObjectParser that is capable of
// unmarshaling objects containing Protobuf messages.
func NewProtoObjectParser[
	TReference any,
	TMessage any,
	TMessagePtr interface {
		*TMessage
		proto.Message
	},
]() MessageObjectParser[TReference, TMessagePtr] {
	return NewFormatObjectParser[TReference](model_format.NewProto[TMessage, TMessagePtr]())
}
