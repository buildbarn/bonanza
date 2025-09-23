package remoteworker

import (
	"context"
	"time"

	remoteworker_pb "bonanza.build/pkg/proto/remoteworker"

	"github.com/buildbarn/bb-storage/pkg/util"

	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

type protoExecutor[
	TAction any,
	TEvent proto.Message,
	TResult proto.Message,
	TActionPtr interface {
		*TAction
		proto.Message
	},
] struct {
	Executor[TActionPtr, TEvent, TResult]
}

// NewProtoExecutor creates a decorator for Executor that assumes that
// all payloads of an action (i.e., its action, execution event and
// completion event message) are Protobuf messages. It marshals and
// unmarshals all these messages, so that they can be sent and
// transmitted in binary form.
func NewProtoExecutor[
	TAction any,
	TEvent proto.Message,
	TResult proto.Message,
	TActionPtr interface {
		*TAction
		proto.Message
	},
](
	base Executor[TActionPtr, TEvent, TResult],
) Executor[[]byte, []byte, []byte] {
	return &protoExecutor[TAction, TEvent, TResult, TActionPtr]{
		Executor: base,
	}
}

func (e *protoExecutor[TAction, TEvent, TResult, TActionPtr]) Execute(ctx context.Context, action []byte, executionTimeout time.Duration, executionEvents chan<- []byte) ([]byte, time.Duration, remoteworker_pb.CurrentState_Completed_Result, error) {
	// Unmarshal the action message. Assume it's stored in a
	// google.protobuf.Any message, so that we can more reliably
	// deny incorrectly routed requests.
	var actionAny anypb.Any
	if err := proto.Unmarshal(action, &actionAny); err != nil {
		return nil, 0, 0, util.StatusWrapWithCode(err, codes.InvalidArgument, "Failed to unmarshal action")
	}
	var actionMessage TAction
	if err := actionAny.UnmarshalTo(TActionPtr(&actionMessage)); err != nil {
		return nil, 0, 0, util.StatusWrapWithCode(err, codes.InvalidArgument, "Failed to unmarshal action")
	}

	// Invoke the base executor. Marshal any execution events that
	// are generated during execution.
	var completedEvent TResult
	var virtualExecutionDuration time.Duration
	var result remoteworker_pb.CurrentState_Completed_Result
	executionEventMessages := make(chan TEvent, 1)
	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error {
		gotError := false
		for executionEventMessage := range executionEventMessages {
			if !gotError {
				if executionEvent, err := proto.Marshal(executionEventMessage); err == nil {
					executionEvents <- executionEvent
				} else {
					// Stop processing any further
					// execution events and propagate
					// the error to the caller.
					gotError = true
					group.Go(func() error {
						return util.StatusWrapWithCode(err, codes.Internal, "Failed to marshal execution event")
					})
				}
			}
		}
		return nil
	})
	group.Go(func() (err error) {
		completedEvent, virtualExecutionDuration, result, err = e.Executor.Execute(groupCtx, &actionMessage, executionTimeout, executionEventMessages)
		close(executionEventMessages)
		return err
	})
	if err := group.Wait(); err != nil {
		return nil, 0, 0, err
	}

	// Marshal the completion event.
	marshaledCompletedEvent, err := proto.Marshal(completedEvent)
	if err != nil {
		return nil, 0, 0, util.StatusWrapWithCode(err, codes.Internal, "Failed to marshal result")
	}
	return marshaledCompletedEvent, virtualExecutionDuration, result, nil
}
