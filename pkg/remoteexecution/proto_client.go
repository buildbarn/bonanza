package remoteexecution

import (
	"context"
	"crypto/ecdh"
	"iter"

	encryptedaction_pb "bonanza.build/pkg/proto/encryptedaction"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

type protoClient[
	TAction proto.Message,
	TEvent any,
	TResult any,
	TEventPtr interface {
		*TEvent
		proto.Message
	},
	TResultPtr interface {
		*TResult
		proto.Message
	},
] struct {
	Client[[]byte, []byte, []byte]
}

// NewProtoClient creates a decorator for Client that assumes that all
// payloads (action, execution events and result) are Protobuf messages.
func NewProtoClient[
	TAction proto.Message,
	TEvent any,
	TResult any,
	TEventPtr interface {
		*TEvent
		proto.Message
	},
	TResultPtr interface {
		*TResult
		proto.Message
	},
](base Client[[]byte, []byte, []byte]) Client[TAction, TEventPtr, TResultPtr] {
	return &protoClient[TAction, TEvent, TResult, TEventPtr, TResultPtr]{
		Client: base,
	}
}

func (c *protoClient[TAction, TEvent, TResult, TEventPtr, TResultPtr]) RunAction(ctx context.Context, platformECDHPublicKey *ecdh.PublicKey, action TAction, actionAdditionalData *encryptedaction_pb.Action_AdditionalData, resultOut *TResultPtr, errOut *error) iter.Seq[ExecutionUpdate[TEventPtr]] {
	// Marshal the action. We wrap the action in a google.protobuf.Any,
	// so that the worker can reliably reject the action if it's not
	// the right type for that kind of worker.
	actionAny, err := anypb.New(action)
	if err != nil {
		*errOut = util.StatusWrapWithCode(err, codes.InvalidArgument, "Failed to embed action into Any message")
		return func(func(ExecutionUpdate[TEventPtr]) bool) {}
	}
	actionData, err := proto.Marshal(actionAny)
	if err != nil {
		*errOut = util.StatusWrapWithCode(err, codes.InvalidArgument, "Failed to marshal action")
		return func(func(ExecutionUpdate[TEventPtr]) bool) {}
	}

	var resultData []byte
	var baseErr error
	ctxWithCancel, cancel := context.WithCancel(ctx)
	baseEvents := c.Client.RunAction(ctxWithCancel, platformECDHPublicKey, actionData, actionAdditionalData, &resultData, &baseErr)
	return func(yield func(ExecutionUpdate[TEventPtr]) bool) {
		defer cancel()

		// Unmarshal execution events.
		for baseUpdate := range baseEvents {
			update, err := MapExecutionUpdateEvent(baseUpdate, func(eventData []byte) (TEventPtr, error) {
				var event TEvent
				if err := proto.Unmarshal(eventData, TEventPtr(&event)); err != nil {
					return nil, util.StatusWrapWithCode(err, codes.Internal, "Failed to unmarshal event")
				}
				return &event, nil
			})
			if err != nil {
				*errOut = err
				return
			}
			if !yield(update) {
				break
			}
		}
		if baseErr != nil {
			*errOut = baseErr
			return
		}

		// Unmarshal result.
		var result TResult
		if err := proto.Unmarshal(resultData, TResultPtr(&result)); err != nil {
			*errOut = util.StatusWrapWithCode(err, codes.Internal, "Failed to unmarshal result")
			return
		}
		*resultOut = &result
		*errOut = nil
	}
}
