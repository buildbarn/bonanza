package parser

import (
	model_core "bonanza.build/pkg/model/core"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type protoObjectParser[
	TReference any,
	TMessage any,
	TMessagePtr interface {
		*TMessage
		proto.Message
	},
] struct{}

func NewProtoObjectParser[
	TReference any,
	TMessage any,
	TMessagePtr interface {
		*TMessage
		proto.Message
	},
]() ObjectParser[TReference, model_core.Message[TMessagePtr, TReference]] {
	return &protoObjectParser[TReference, TMessage, TMessagePtr]{}
}

func (protoObjectParser[TReference, TMessage, TMessagePtr]) ParseObject(in model_core.Message[[]byte, TReference], decodingParameters []byte) (model_core.Message[TMessagePtr, TReference], int, error) {
	if len(decodingParameters) > 0 {
		return model_core.Message[TMessagePtr, TReference]{}, 0, status.Error(codes.InvalidArgument, "Unexpected decoding parameters")
	}

	var message TMessage
	if err := proto.Unmarshal(in.Message, TMessagePtr(&message)); err != nil {
		return model_core.Message[TMessagePtr, TReference]{}, 0, util.StatusWrapWithCode(err, codes.InvalidArgument, "Failed to unmarshal message")
	}
	return model_core.NewMessage(TMessagePtr(&message), in.OutgoingReferences), len(in.Message), nil
}

func (protoObjectParser[TReference, TMessage, TMessagePtr]) GetDecodingParametersSizeBytes() int {
	return 0
}
