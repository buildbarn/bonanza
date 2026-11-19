package remoteexecution

import (
	"context"
	"crypto/ecdh"
	"iter"

	encryptedaction_pb "bonanza.build/pkg/proto/encryptedaction"
)

// ExecutionStage of an action that is being run through Client.
type ExecutionStage int

const (
	// ExecutionStageQueued indicates that the action is queued
	// inside the scheduler, waiting for a worker to pick it up.
	ExecutionStageQueued ExecutionStage = iota
	// ExecutionStageExecuting indicates that the action is
	// currently executing on a worker.
	ExecutionStageExecuting
)

// ExecutionUpdate is yielded by Client.RunAction() whenever the
// scheduler reports on the state of the action.
type ExecutionUpdate[TEvent any] struct {
	Stage ExecutionStage

	// If HasEvent is set, Event contains an execution event that
	// was posted by the worker since the previous update. Execution
	// events are only provided if the action is executing.
	Event    TEvent
	HasEvent bool
}

// MapExecutionUpdateEvent converts the event contained in an
// ExecutionUpdate, if any.
func MapExecutionUpdateEvent[TFrom, TTo any](update ExecutionUpdate[TFrom], mapper func(TFrom) (TTo, error)) (ExecutionUpdate[TTo], error) {
	newUpdate := ExecutionUpdate[TTo]{Stage: update.Stage}
	if update.HasEvent {
		event, err := mapper(update.Event)
		if err != nil {
			return ExecutionUpdate[TTo]{}, err
		}
		newUpdate.Event = event
		newUpdate.HasEvent = true
	}
	return newUpdate, nil
}

// Client for running actions via the remote execution protocol.
//
// For every action that needs to be invoked, an action payload needs to
// be provided. While the action is queued or executing, updates on its
// stage and any execution events are provided through an iterator.
// Upon completion, a single result is returned.
type Client[TAction, TEvent, TResult any] interface {
	RunAction(ctx context.Context, platformECDHPublicKey *ecdh.PublicKey, action TAction, actionAdditionalData *encryptedaction_pb.Action_AdditionalData, result *TResult, errOut *error) iter.Seq[ExecutionUpdate[TEvent]]
}
