package parser

import (
	model_format "bonanza.build/pkg/model/format"

	"google.golang.org/protobuf/proto"
)

// NewProtoListObjectParser creates an ObjectParser that is capable of
// unmarshaling objects containing lists of Protobuf messages. Messages
// are prefixed with their size, encoded as a variable length integer.
func NewProtoListObjectParser[
	TReference any,
	TMessage any,
	TMessagePtr interface {
		*TMessage
		proto.Message
	},
]() MessageObjectParser[TReference, []TMessagePtr] {
	return NewFormatObjectParser[TReference](model_format.NewProtoList[TMessage, TMessagePtr]())
}
