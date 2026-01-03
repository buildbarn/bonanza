package evaluation

import (
	"context"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/program"
)

// RecursiveComputerEvaluationQueuesFactory is invoked by Executor to
// create the queues that are necessary to schedule the evaluation of
// keys.
type RecursiveComputerEvaluationQueuesFactory[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] interface {
	NewQueues() RecursiveComputerEvaluationQueues[TReference, TMetadata]
}

// RecursiveComputerEvaluationQueues represents a set of queues that
// RecursiveComputer may use to schedule the evaluation of keys.
//
// Simple implementations may place all keys in a single queue, but this
// has the disadvantage that all work is limited by the same concurrency
// limit. This may be sufficient if all work is CPU intensive, but may
// lead to low utilization if some work calls into remote services and
// may block for large amounts of time.
type RecursiveComputerEvaluationQueues[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] interface {
	RecursiveComputerEvaluationQueuePicker[TReference, TMetadata]

	ProcessAllEvaluatableKeys(group program.Group, computer *RecursiveComputer[TReference, TMetadata])
}

type simpleRecursiveComputerEvaluationQueuesFactory[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	concurrency uint32
}

// NewSimpleRecursiveComputerEvaluationQueuesFactory creates a
// RecursiveComputerEvaluationQueuesFactory that always returns
// RecursiveComputerEvaluationQueues instances backed by a single queue.
//
// This implementation may be sufficient for testing, or can be used as
// a base type for more advanced implementations that create multiple
// queues.
func NewSimpleRecursiveComputerEvaluationQueuesFactory[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](concurrency uint32) RecursiveComputerEvaluationQueuesFactory[TReference, TMetadata] {
	return simpleRecursiveComputerEvaluationQueuesFactory[TReference, TMetadata]{
		concurrency: concurrency,
	}
}

func (f simpleRecursiveComputerEvaluationQueuesFactory[TReference, TMetadata]) NewQueues() RecursiveComputerEvaluationQueues[TReference, TMetadata] {
	return &simpleRecursiveComputerEvaluationQueues[TReference, TMetadata]{
		queue:       NewRecursiveComputerEvaluationQueue[TReference, TMetadata](),
		concurrency: f.concurrency,
	}
}

type simpleRecursiveComputerEvaluationQueues[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	queue       *RecursiveComputerEvaluationQueue[TReference, TMetadata]
	concurrency uint32
}

func (q *simpleRecursiveComputerEvaluationQueues[TReference, TMetadata]) PickQueue(typeURL string) *RecursiveComputerEvaluationQueue[TReference, TMetadata] {
	return q.queue
}

func (q *simpleRecursiveComputerEvaluationQueues[TReference, TMetadata]) ProcessAllEvaluatableKeys(group program.Group, computer *RecursiveComputer[TReference, TMetadata]) {
	for i := uint32(0); i < q.concurrency; i++ {
		group.Go(func(ctx context.Context, siblingsGroup, group program.Group) error {
			for computer.ProcessNextEvaluatableKey(ctx, q.queue) {
			}
			return nil
		})
	}
}
