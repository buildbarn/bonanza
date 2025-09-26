package evaluation

import (
	"context"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/program"

	"google.golang.org/protobuf/proto"
)

// RecursiveComputerQueuesFactory is invoked by Executor to create the
// queues that are necessary to schedule the evaluation of keys.
type RecursiveComputerQueuesFactory[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] interface {
	NewQueues() RecursiveComputerQueues[TReference, TMetadata]
}

// RecursiveComputerQueues represents a set of queues that
// RecursiveComputer may use to schedule the evaluation of keys.
//
// Simple implementations may place all keys in a single queue, but this
// has the disadvantage that all work is limited by the same concurrency
// limit. This may be sufficient if all work is CPU intensive, but may
// lead to low utilization if some work calls into remote services and
// may block for large amounts of time.
type RecursiveComputerQueues[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] interface {
	RecursiveComputerQueuePicker[TReference, TMetadata]

	ProcessAllQueuedKeys(group program.Group, computer *RecursiveComputer[TReference, TMetadata])
}

type simpleRecursiveComputerQueuesFactory[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	concurrency uint32
}

// RecursiveComputerQueues creates a RecursiveComputerQueuesFactory that
// always returns RecursiveComputerQueues instances backed by a single
// queue.
//
// This implementation may be sufficient for testing, or can be used as
// a base type for more advanced implementations that create multiple
// queues.
func NewSimpleRecursiveComputerQueuesFactory[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](concurrency uint32) RecursiveComputerQueuesFactory[TReference, TMetadata] {
	return simpleRecursiveComputerQueuesFactory[TReference, TMetadata]{
		concurrency: concurrency,
	}
}

func (f simpleRecursiveComputerQueuesFactory[TReference, TMetadata]) NewQueues() RecursiveComputerQueues[TReference, TMetadata] {
	return &simpleRecursiveComputerQueues[TReference, TMetadata]{
		queue:       NewRecursiveComputerQueue[TReference, TMetadata](),
		concurrency: f.concurrency,
	}
}

type simpleRecursiveComputerQueues[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	queue       *RecursiveComputerQueue[TReference, TMetadata]
	concurrency uint32
}

func (q *simpleRecursiveComputerQueues[TReference, TMetadata]) PickQueue(key model_core.Message[proto.Message, TReference]) *RecursiveComputerQueue[TReference, TMetadata] {
	return q.queue
}

func (q *simpleRecursiveComputerQueues[TReference, TMetadata]) ProcessAllQueuedKeys(group program.Group, computer *RecursiveComputer[TReference, TMetadata]) {
	for i := uint32(0); i < q.concurrency; i++ {
		group.Go(func(ctx context.Context, siblingsGroup, group program.Group) error {
			for computer.ProcessNextQueuedKey(ctx, q.queue) {
			}
			return nil
		})
	}
}
