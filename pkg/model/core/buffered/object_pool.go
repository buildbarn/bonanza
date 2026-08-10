package buffered

import (
	"container/heap"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"sync"

	model_core "bonanza.build/pkg/model/core"
	model_parser "bonanza.build/pkg/model/parser"
	model_tag "bonanza.build/pkg/model/tag"
	dag_pb "bonanza.build/pkg/proto/storage/dag"
	"bonanza.build/pkg/storage/object"
	"bonanza.build/pkg/storage/tag"
	pg_sync "bonanza.build/pkg/sync"

	"github.com/buildbarn/bb-storage/pkg/clock"
	"github.com/buildbarn/bb-storage/pkg/eviction"
	"github.com/buildbarn/bb-storage/pkg/program"
	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// simplifyDone returns nil if the provided channel is closed, meaning
// that the object it belongs to has been written to storage. This
// allows references and metadata to be fully disconnected from the
// pool.
func simplifyDone(done <-chan struct{}) <-chan struct{} {
	if done != nil {
		select {
		case <-done:
			return nil
		default:
		}
	}
	return done
}

type objectStateType int

const (
	// The object has been captured, but has not been assigned a
	// reference index as part of the UploadDags() stream. The
	// object is stored in ObjectPool.queuedObjects.
	objectStateQueued objectStateType = iota
	// The object has been assigned a reference index, either
	// because it was used as the root of a DAG, or because it is
	// the child of an object whose contents have been provided to
	// the server.
	objectStateInFlight
	// The server has reported that the object and all of its
	// children have been written to storage. The object is stored
	// in the eviction set, meaning it may be evicted from the pool.
	objectStateFinalized
)

// objectState holds the contents of an object that was captured by
// the pool, together with its upload state.
type objectState struct {
	contents *object.Contents
	state    objectStateType

	// Closed when the server reports that the object and all of
	// its children have been written to storage. If writing fails,
	// this channel is never closed. Instead, SendQueuedObjects()
	// returns an error.
	done chan struct{}

	// Index in ObjectPool.queuedObjects if state is
	// objectStateQueued.
	heapIndex int
}

// queuedObjectsHeap is a priority queue of objects that have not been
// assigned a reference index yet. Objects with the largest height are
// returned first. As the height of a parent is always strictly larger
// than that of its children, the object at the top of the heap never
// has a parent that is also queued. Initiating DAGs in this order thus
// causes as many queued objects to be covered by as few DAGs as
// possible.
type queuedObjectsHeap []*objectState

func (h queuedObjectsHeap) Len() int {
	return len(h)
}

func (h queuedObjectsHeap) Less(i, j int) bool {
	return h[i].contents.LocalReference.CompareByHeight(h[j].contents.LocalReference) > 0
}

func (h queuedObjectsHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].heapIndex = i
	h[j].heapIndex = j
}

func (h *queuedObjectsHeap) Push(x any) {
	os := x.(*objectState)
	os.heapIndex = len(*h)
	*h = append(*h, os)
}

func (h *queuedObjectsHeap) Pop() any {
	old := *h
	n := len(old) - 1
	os := old[n]
	old[n] = nil
	os.heapIndex = -1
	*h = old[:n]
	return os
}

// referenceState keeps track of all reference indices of the
// UploadDags() stream that refer to the same object, and for which
// the server has not yet sent a FinalizeObject message. The server
// may collapse these reference indices, acknowledging multiple of
// them using a single FinalizeObject message.
type referenceState struct {
	reference object.LocalReference

	// Reference indices that have not been acknowledged, in
	// increasing order. The first element is the lowest reference
	// index, which the server uses to refer to the object.
	referenceIndices []uint64
}

// tagRequest is a pending call to ObjectPool.updateTag().
type tagRequest struct {
	reference object.LocalReference
	tag       *dag_pb.UploadDagsRequest_InitiateDag_Tag
}

// pendingProvide is a RequestObjectContents message received from the
// server, for which a ProvideObjectContents still needs to be sent.
type pendingProvide struct {
	lowestReferenceIndex uint64
	reference            *referenceState
}

// ObjectPool holds the contents of objects that were created by
// bonanza_builder, writing them to storage asynchronously. Objects
// remain in memory until they have been written to storage. After
// that, they are kept around as a cache, until space is needed to
// store other objects.
//
// Objects are written to storage eagerly using the
// bonanza.storage.dag.Uploader.UploadDags() protocol. All objects in
// the pool that have not been written to storage yet are placed in a
// priority queue, ordered by height. Whenever the server permits
// initiating another DAG, the highest object is used as the root of a
// new DAG. Any queued children of this object are removed from the
// queue as soon as they are assigned a reference index, or when the
// server reports that the root object already exists in storage. This
// means that if uploading is not able to keep up, queued objects are
// automatically collapsed into fewer, larger DAGs.
type ObjectPool struct {
	lock sync.Mutex

	// If set, uploading has terminated. No new objects may be
	// captured, and objects that were not written to storage never
	// will be.
	err error

	maximumLimit   object.Limit
	remainingLimit object.Limit
	objects        map[object.LocalReference]*objectState
	queuedObjects  queuedObjectsHeap
	capacityWakeup pg_sync.ConditionVariable

	// Objects that have been written to storage, and may therefore
	// be evicted from the pool.
	evictionSet           eviction.Set[object.LocalReference]
	evictableObjectsCount int

	pendingTags  []*tagRequest
	senderWakeup pg_sync.ConditionVariable

	// The number of tags that have been requested, for which no
	// FinalizeTag message has been received.
	unfinalizedTags uint64
	tagsWakeup      pg_sync.ConditionVariable

	// State of the UploadDags() stream.
	connected               bool
	nextReferenceIndex      uint64
	maximumUnfinalizedDAGs  uint32
	unfinalizedDAGs         uint32
	referencesByLowestIndex map[uint64]*referenceState
	referencesByReference   map[object.LocalReference]*referenceState
	unfinalizedRootsByIndex map[uint64]*tagRequest
	pendingProvides         []pendingProvide
}

// NewObjectPool creates an ObjectPool that is empty. The provided limit
// controls the maximum number of objects and their combined size that
// may be stored in the pool. Calls to CaptureCreatedObject() block if
// the pool is full and none of the objects have been written to
// storage yet.
//
// The provided eviction set determines the order in which objects that
// have been written to storage are evicted. Objects are touched when
// they are captured or read, as both operations may be satisfied
// without issuing any RPCs if the object is still present in the pool.
func NewObjectPool(limit object.Limit, evictionSet eviction.Set[object.LocalReference]) *ObjectPool {
	return &ObjectPool{
		maximumLimit:   limit,
		remainingLimit: limit,
		objects:        map[object.LocalReference]*objectState{},
		evictionSet:    evictionSet,
	}
}

// markFinalizedLocked marks a single object as being written to
// storage, causing any waiters to be woken up. The object is inserted
// into the eviction set, so that it may be evicted.
func (op *ObjectPool) markFinalizedLocked(os *objectState) {
	if os.state == objectStateQueued {
		heap.Remove(&op.queuedObjects, os.heapIndex)
	}
	os.state = objectStateFinalized
	close(os.done)
	op.evictionSet.Insert(os.contents.LocalReference)
	op.evictableObjectsCount++
	op.capacityWakeup.Broadcast()
}

// touchLocked indicates that an object in the pool was used. If the
// object has been written to storage, this affects the order in which
// it is evicted. Objects that have not been written cannot be evicted,
// and are therefore not tracked by the eviction set.
func (op *ObjectPool) touchLocked(os *objectState) {
	if os.state == objectStateFinalized {
		op.evictionSet.Touch(os.contents.LocalReference)
	}
}

// finalizeObjectAndDescendantsLocked is called when the server reports
// that an object and all of its children have been written to storage
// successfully. Any descendants of the object that are still present
// in the pool are also marked as being written. For descendants that
// are still queued, this prevents us from initiating DAGs for them
// separately.
func (op *ObjectPool) finalizeObjectAndDescendantsLocked(reference object.LocalReference) {
	os, ok := op.objects[reference]
	if !ok || os.state == objectStateFinalized {
		return
	}
	op.markFinalizedLocked(os)
	stack := []*objectState{os}
	for len(stack) > 0 {
		os := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		contents := os.contents
		for i := range contents.GetDegree() {
			if osChild, ok := op.objects[contents.GetOutgoingReference(i)]; ok && osChild.state != objectStateFinalized {
				op.markFinalizedLocked(osChild)
				stack = append(stack, osChild)
			}
		}
	}
}

// evictLocked removes an object that was written to storage from the
// pool, so that space is made available to capture other objects. The
// eviction set determines which object is removed.
func (op *ObjectPool) evictLocked() {
	reference := op.evictionSet.Peek()
	op.evictionSet.Remove()
	op.evictableObjectsCount--
	delete(op.objects, reference)
	op.remainingLimit.ReleaseSingleObject(reference)
}

// failLocked is called when uploading terminates. Objects that have
// not been written to storage never will be, and no new objects may
// be captured.
//
// Objects that have not been written are not marked in any way. Any
// goroutines waiting for them to be written remain blocked until
// their context is cancelled. It is the responsibility of the caller
// of SendQueuedObjects() to do so.
func (op *ObjectPool) failLocked(err error) {
	if op.err != nil {
		return
	}
	op.err = err
	op.pendingTags = nil
	op.unfinalizedRootsByIndex = nil
	op.capacityWakeup.Broadcast()
	op.senderWakeup.Broadcast()
	op.tagsWakeup.Broadcast()
}

// addReferenceIndexLocked registers that a given reference index
// refers to an object. If the object is still queued, it is removed
// from the queue, as it is now part of a DAG that is being uploaded.
func (op *ObjectPool) addReferenceIndexLocked(reference object.LocalReference, referenceIndex uint64) {
	if rs, ok := op.referencesByReference[reference]; ok {
		rs.referenceIndices = append(rs.referenceIndices, referenceIndex)
	} else {
		rs := &referenceState{
			reference:        reference,
			referenceIndices: []uint64{referenceIndex},
		}
		op.referencesByReference[reference] = rs
		op.referencesByLowestIndex[referenceIndex] = rs
	}

	if os, ok := op.objects[reference]; ok && os.state == objectStateQueued {
		heap.Remove(&op.queuedObjects, os.heapIndex)
		os.state = objectStateInFlight
	}
}

// initiateDAGLocked creates an InitiateDag message for a DAG having a
// given root object.
func (op *ObjectPool) initiateDAGLocked(reference object.LocalReference, tr *tagRequest) *dag_pb.UploadDagsRequest {
	referenceIndex := op.nextReferenceIndex
	op.nextReferenceIndex++
	op.addReferenceIndexLocked(reference, referenceIndex)
	op.unfinalizedRootsByIndex[referenceIndex] = tr
	op.unfinalizedDAGs++

	initiateDAG := &dag_pb.UploadDagsRequest_InitiateDag{
		RootReference: reference.GetRawReference(),
	}
	if tr != nil {
		initiateDAG.RootTag = tr.tag
	}
	return &dag_pb.UploadDagsRequest{
		Type: &dag_pb.UploadDagsRequest_InitiateDag_{
			InitiateDag: initiateDAG,
		},
	}
}

// provideObjectContentsLocked creates a ProvideObjectContents message
// in response to a RequestObjectContents message sent by the server.
// Reference indices are assigned to all children of the object.
func (op *ObjectPool) provideObjectContentsLocked(p pendingProvide) *dag_pb.UploadDagsRequest {
	provideObjectContents := &dag_pb.UploadDagsRequest_ProvideObjectContents{
		LowestReferenceIndex: p.lowestReferenceIndex,
	}
	if os, ok := op.objects[p.reference.reference]; ok {
		contents := os.contents
		for i := range contents.GetDegree() {
			referenceIndex := op.nextReferenceIndex
			op.nextReferenceIndex++
			op.addReferenceIndexLocked(contents.GetOutgoingReference(i), referenceIndex)
		}
		provideObjectContents.ObjectContents = contents.GetFullData()
	}
	// If the object is not present in the pool, we leave
	// ObjectContents empty, thereby cancelling the upload. This
	// may happen if the server requests the contents of an object
	// that we did not create, or one that was evicted after being
	// written to storage, but its lease has expired since.
	return &dag_pb.UploadDagsRequest{
		Type: &dag_pb.UploadDagsRequest_ProvideObjectContents_{
			ProvideObjectContents: provideObjectContents,
		},
	}
}

// getNextRequestLocked blocks until there is a message that needs to
// be sent to the server. Upon failure, the lock is released.
func (op *ObjectPool) getNextRequestLocked(ctx context.Context) (*dag_pb.UploadDagsRequest, error) {
	for {
		if op.err != nil {
			err := op.err
			op.lock.Unlock()
			return nil, err
		}

		// Sending contents of objects requested by the server
		// takes precedence, as that allows existing DAGs to
		// make progress.
		if len(op.pendingProvides) > 0 {
			p := op.pendingProvides[0]
			op.pendingProvides[0] = pendingProvide{}
			op.pendingProvides = op.pendingProvides[1:]
			return op.provideObjectContentsLocked(p), nil
		}

		if op.unfinalizedDAGs < op.maximumUnfinalizedDAGs {
			// Tag updates are prioritized, as callers of
			// UpdateTag() are blocked waiting for them.
			if len(op.pendingTags) > 0 {
				tr := op.pendingTags[0]
				op.pendingTags[0] = nil
				op.pendingTags = op.pendingTags[1:]
				return op.initiateDAGLocked(tr.reference, tr), nil
			}
			if op.queuedObjects.Len() > 0 {
				os := heap.Pop(&op.queuedObjects).(*objectState)
				os.state = objectStateInFlight
				return op.initiateDAGLocked(os.contents.LocalReference, nil), nil
			}
		}

		if err := op.senderWakeup.Wait(ctx, &op.lock); err != nil {
			return nil, err
		}
	}
}

func (op *ObjectPool) processOutgoingMessages(ctx context.Context, stream dag_pb.Uploader_UploadDagsClient) error {
	// All messages are sent from this goroutine. As reference
	// indices are assigned right before sending, the order in
	// which they are assigned is consistent with the order in
	// which the server receives them.
	for {
		op.lock.Lock()
		request, err := op.getNextRequestLocked(ctx)
		if err != nil {
			return err
		}
		op.lock.Unlock()

		if err := stream.Send(request); err != nil {
			return util.StatusWrap(err, "Failed to send message to server")
		}
	}
}

func (op *ObjectPool) processResponseLocked(response *dag_pb.UploadDagsResponse) error {
	switch responseType := response.Type.(type) {
	case *dag_pb.UploadDagsResponse_RequestObjectContents_:
		lowestReferenceIndex := responseType.RequestObjectContents.LowestReferenceIndex
		rs, ok := op.referencesByLowestIndex[lowestReferenceIndex]
		if !ok {
			return status.Errorf(codes.Internal, "Server requested object with lowest reference index %d, which was not expected", lowestReferenceIndex)
		}
		op.pendingProvides = append(op.pendingProvides, pendingProvide{
			lowestReferenceIndex: lowestReferenceIndex,
			reference:            rs,
		})
		op.senderWakeup.Broadcast()
		return nil

	case *dag_pb.UploadDagsResponse_FinalizeObject_:
		finalizeObject := responseType.FinalizeObject
		rs, ok := op.referencesByLowestIndex[finalizeObject.LowestReferenceIndex]
		if !ok {
			return status.Errorf(codes.Internal, "Server finalized object with lowest reference index %d, which was not expected", finalizeObject.LowestReferenceIndex)
		}
		if err := status.ErrorProto(finalizeObject.Status); err != nil {
			return util.StatusWrapf(err, "Server failed to write object with reference %s", rs.reference)
		}

		// If the DAGs contain multiple outgoing references
		// pointing to the same object, the server may collapse
		// these references and acknowledge them using a single
		// FinalizeObject message.
		acknowledgedCount := uint64(finalizeObject.AdditionalReferenceIndices) + 1
		if acknowledgedCount > uint64(len(rs.referenceIndices)) {
			return status.Errorf(codes.Internal, "Server finalized object with lowest reference index %d and %d additional reference indices, which was not expected", finalizeObject.LowestReferenceIndex, finalizeObject.AdditionalReferenceIndices)
		}
		delete(op.referencesByLowestIndex, finalizeObject.LowestReferenceIndex)
		for _, referenceIndex := range rs.referenceIndices[:acknowledgedCount] {
			// Roots of DAGs without tags are finalized as
			// soon as the root object is finalized. Roots of
			// DAGs with tags are finalized by FinalizeTag.
			if tr, ok := op.unfinalizedRootsByIndex[referenceIndex]; ok && tr == nil {
				delete(op.unfinalizedRootsByIndex, referenceIndex)
				op.unfinalizedDAGs--
				op.senderWakeup.Broadcast()
			}
		}
		if remaining := rs.referenceIndices[acknowledgedCount:]; len(remaining) > 0 {
			rs.referenceIndices = remaining
			op.referencesByLowestIndex[remaining[0]] = rs
		} else {
			delete(op.referencesByReference, rs.reference)
		}

		op.finalizeObjectAndDescendantsLocked(rs.reference)
		return nil

	case *dag_pb.UploadDagsResponse_FinalizeTag_:
		finalizeTag := responseType.FinalizeTag
		tr, ok := op.unfinalizedRootsByIndex[finalizeTag.RootReferenceIndex]
		if !ok || tr == nil {
			return status.Errorf(codes.Internal, "Server finalized tag with root reference index %d, which was not expected", finalizeTag.RootReferenceIndex)
		}
		if err := status.ErrorProto(finalizeTag.Status); err != nil {
			return util.StatusWrapf(err, "Server failed to write tag for object with reference %s", tr.reference)
		}
		delete(op.unfinalizedRootsByIndex, finalizeTag.RootReferenceIndex)
		op.unfinalizedDAGs--
		op.senderWakeup.Broadcast()
		op.unfinalizedTags--
		if op.unfinalizedTags == 0 {
			op.tagsWakeup.Broadcast()
		}
		return nil

	default:
		return status.Error(codes.Internal, "Message from server did not contain a supported message type")
	}
}

func (op *ObjectPool) processIncomingMessages(stream dag_pb.Uploader_UploadDagsClient) error {
	for {
		response, err := stream.Recv()
		if err != nil {
			return util.StatusWrap(err, "Failed to receive message from server")
		}
		op.lock.Lock()
		err = op.processResponseLocked(response)
		op.lock.Unlock()
		if err != nil {
			return err
		}
	}
}

// SendQueuedObjects writes all objects that are captured by the pool
// to storage. This method blocks until the provided context is
// cancelled, in which case nil is returned, or until uploading fails.
//
// Upon failure, the pool stops accepting new objects. Calls that wait
// for objects to be written (e.g., ExportReference()) are NOT
// interrupted. The caller of this method MUST therefore cancel the
// contexts of any such calls when an error is returned.
//
// This method may only be called once.
func (op *ObjectPool) SendQueuedObjects(ctx context.Context, client dag_pb.UploaderClient, namespace object.Namespace) error {
	err := program.RunLocal(ctx, func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
		stream, err := client.UploadDags(ctx)
		if err != nil {
			return util.StatusWrap(err, "Failed to create stream")
		}

		// Always call Recv() after we're done to ensure
		// resources associated with the stream are cleaned up.
		dependenciesGroup.Go(func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
			<-ctx.Done()
			for {
				if _, err := stream.Recv(); err != nil {
					return nil
				}
			}
		})

		// As all objects reside in memory, there is no need to
		// limit the breadth at which the server traverses DAGs.
		if err := stream.Send(&dag_pb.UploadDagsRequest{
			Type: &dag_pb.UploadDagsRequest_Handshake_{
				Handshake: &dag_pb.UploadDagsRequest_Handshake{
					Namespace: namespace.ToProto(),
				},
			},
		}); err != nil {
			return util.StatusWrap(err, "Failed to send handshake message to server")
		}

		response, err := stream.Recv()
		if err != nil {
			return util.StatusWrap(err, "Failed to receive handshake message from server")
		}
		handshake, ok := response.Type.(*dag_pb.UploadDagsResponse_Handshake_)
		if !ok {
			return status.Error(codes.Internal, "Initial message from server did not contain a handshake")
		}
		maximumUnfinalizedDAGs := handshake.Handshake.MaximumUnfinalizedDagsCount
		if maximumUnfinalizedDAGs == 0 {
			return status.Error(codes.Internal, "Server does not permit initiating any DAGs")
		}

		op.lock.Lock()
		if op.connected {
			op.lock.Unlock()
			return status.Error(codes.FailedPrecondition, "Objects in the pool are already being sent")
		}
		op.connected = true
		op.maximumUnfinalizedDAGs = maximumUnfinalizedDAGs
		op.referencesByLowestIndex = map[uint64]*referenceState{}
		op.referencesByReference = map[object.LocalReference]*referenceState{}
		op.unfinalizedRootsByIndex = map[uint64]*tagRequest{}
		op.senderWakeup.Broadcast()
		op.lock.Unlock()

		siblingsGroup.Go(func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
			return op.processOutgoingMessages(ctx, stream)
		})
		siblingsGroup.Go(func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
			return op.processIncomingMessages(stream)
		})
		return nil
	})

	op.lock.Lock()
	if err == nil {
		err = status.Error(codes.Unavailable, "Object pool is no longer sending objects")
	}
	op.failLocked(err)
	op.lock.Unlock()

	if ctx.Err() != nil {
		// Graceful shutdown.
		return nil
	}
	return err
}

// updateTag schedules the upload of a DAG with a tag that refers to
// its root object. This method does not wait for the tag to be
// created. FlushTags() can be used to wait for all tags to be created.
// Failures to create tags are reported by SendQueuedObjects().
func (op *ObjectPool) updateTag(reference object.LocalReference, rootTag *dag_pb.UploadDagsRequest_InitiateDag_Tag) error {
	op.lock.Lock()
	defer op.lock.Unlock()
	if op.err != nil {
		return util.StatusWrap(op.err, "Object pool is no longer accepting tags")
	}
	op.pendingTags = append(op.pendingTags, &tagRequest{
		reference: reference,
		tag:       rootTag,
	})
	op.unfinalizedTags++
	op.senderWakeup.Broadcast()
	return nil
}

// FlushTags waits for all tags that were created through
// NewTagBoundUpdater() to be written to storage, including the objects
// they reference. This should be called before cancelling the context
// passed to SendQueuedObjects(), as tags that have not been written by
// then are lost.
func (op *ObjectPool) FlushTags(ctx context.Context) error {
	op.lock.Lock()
	for {
		if op.err != nil {
			err := op.err
			op.lock.Unlock()
			return util.StatusWrap(err, "Object pool is no longer sending objects")
		}
		if op.unfinalizedTags == 0 {
			op.lock.Unlock()
			return nil
		}
		if err := op.tagsWakeup.Wait(ctx, &op.lock); err != nil {
			return err
		}
	}
}

// Reference of an object whose contents may be buffered in memory.
type Reference struct {
	object.LocalReference

	// If not nil, the reference was obtained by converting a
	// ReferenceMetadata using ReferenceObject(), and refers to an
	// object that was at some point being written to storage.
	done <-chan struct{}
}

// ReferenceMetadata of an object whose contents may or may not have
// been written to storage yet.
type ReferenceMetadata struct {
	// If not nil, the reference metadata points to an object for
	// which CaptureCreatedObject() was called, and the object may
	// still be in the process of being written to storage.
	done <-chan struct{}
}

// Discard the metadata of an object. As the contents of objects are
// tracked by the pool, this is a no-op.
func (ReferenceMetadata) Discard() {}

type objectManager struct {
	pool *ObjectPool
}

// NewObjectManager creates an object manager that is capable of
// capturing and referencing objects, buffering their contents in
// memory while they are being flushed to storage. This permits code
// that creates objects to continue running, flushing objects
// asynchronously.
func NewObjectManager(pool *ObjectPool) model_core.ObjectManager[Reference, ReferenceMetadata] {
	return &objectManager{
		pool: pool,
	}
}

func (om *objectManager) CaptureCreatedObject(ctx context.Context, createdObject model_core.CreatedObject[ReferenceMetadata]) (ReferenceMetadata, error) {
	op := om.pool
	contents := createdObject.Contents
	reference := contents.LocalReference
	if !op.maximumLimit.CanAcquireSingleObject(reference) {
		return ReferenceMetadata{}, status.Errorf(codes.InvalidArgument, "Object with reference %s does not fit in the object pool", reference)
	}

	op.lock.Lock()
	for {
		if op.err != nil {
			err := op.err
			op.lock.Unlock()
			return ReferenceMetadata{}, util.StatusWrap(err, "Object pool is no longer accepting objects")
		}

		// Check if the same object already exists in the pool.
		// If so, we can simply return a reference to the
		// existing object.
		if os, ok := op.objects[reference]; ok {
			op.touchLocked(os)
			done := simplifyDone(os.done)
			op.lock.Unlock()
			return ReferenceMetadata{done: done}, nil
		}

		if op.remainingLimit.AcquireSingleObject(reference) {
			break
		}

		// The pool is full. Evict objects that have already
		// been written to storage. If there are none, wait for
		// objects to be written.
		if op.evictableObjectsCount > 0 {
			op.evictLocked()
		} else if err := op.capacityWakeup.Wait(ctx, &op.lock); err != nil {
			return ReferenceMetadata{}, err
		}
	}

	os := &objectState{
		contents:  contents,
		done:      make(chan struct{}),
		heapIndex: -1,
	}
	op.objects[reference] = os
	if _, ok := op.referencesByReference[reference]; ok {
		// The object is already part of a DAG that is being
		// uploaded. There is no need to queue it.
		os.state = objectStateInFlight
	} else {
		os.state = objectStateQueued
		heap.Push(&op.queuedObjects, os)
		op.senderWakeup.Broadcast()
	}
	op.lock.Unlock()
	return ReferenceMetadata{done: os.done}, nil
}

func (objectManager) CaptureExistingObject(reference Reference) ReferenceMetadata {
	return ReferenceMetadata{
		done: simplifyDone(reference.done),
	}
}

func (objectManager) ReferenceObject(capturedObject model_core.MetadataEntry[ReferenceMetadata]) Reference {
	return Reference{
		LocalReference: capturedObject.LocalReference,
		done:           simplifyDone(capturedObject.Metadata.done),
	}
}

type objectExporter struct{}

// ObjectExporter accepts references of created objects that may or may
// not have been flushed to storage yet. As objects are flushed to
// storage asynchronously regardless of ExportReference() being called,
// this implementation merely waits for the flushing to complete.
//
// If writing objects to storage fails, ExportReference() blocks until
// its context is cancelled. See ObjectPool.SendQueuedObjects().
var ObjectExporter model_core.ObjectExporter[Reference, object.LocalReference] = objectExporter{}

func (objectExporter) ExportReference(ctx context.Context, internalReference Reference) (object.LocalReference, error) {
	if done := internalReference.done; done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			var badReference object.LocalReference
			return badReference, util.StatusFromContext(ctx)
		}
	}
	return internalReference.LocalReference, nil
}

func (objectExporter) ImportReference(externalReference object.LocalReference) Reference {
	return Reference{
		LocalReference: externalReference,
	}
}

type objectReader struct {
	base model_parser.ObjectReader[object.LocalReference, model_core.Message[[]byte, object.LocalReference]]
	pool *ObjectPool
}

// NewObjectReader creates a decorator for ObjectReader that makes it
// accept buffered references. If the object to be read is present in
// the pool, the contents are returned directly, as opposed to actually
// issuing a read from storage.
func NewObjectReader(
	base model_parser.ObjectReader[object.LocalReference, model_core.Message[[]byte, object.LocalReference]],
	pool *ObjectPool,
) model_parser.ObjectReader[Reference, model_core.Message[[]byte, Reference]] {
	return &objectReader{
		base: base,
		pool: pool,
	}
}

func (r *objectReader) ReadObject(ctx context.Context, reference Reference) (model_core.Message[[]byte, Reference], error) {
	op := r.pool
	op.lock.Lock()
	if os, ok := op.objects[reference.LocalReference]; ok {
		op.touchLocked(os)
		contents := os.contents
		degree := contents.GetDegree()
		outgoingReferences := make(object.OutgoingReferencesList[Reference], 0, degree)
		for i := range degree {
			// Children that are not present in the pool
			// have either been evicted after being written
			// to storage, or existed in storage before
			// the parent was created.
			childReference := contents.GetOutgoingReference(i)
			var done <-chan struct{}
			if osChild, ok := op.objects[childReference]; ok && osChild.state != objectStateFinalized {
				done = osChild.done
			}
			outgoingReferences = append(outgoingReferences, Reference{
				LocalReference: childReference,
				done:           done,
			})
		}
		op.lock.Unlock()
		return model_core.NewMessage(contents.GetPayload(), outgoingReferences), nil
	}
	op.lock.Unlock()

	// Reference refers to an object that was never created locally,
	// or has since been evicted from the pool. As objects are only
	// evicted after being written, we can read it from storage.
	m, err := r.base.ReadObject(ctx, reference.LocalReference)
	if err != nil {
		return model_core.Message[[]byte, Reference]{}, err
	}

	degree := m.OutgoingReferences.GetDegree()
	outgoingReferences := make(object.OutgoingReferencesList[Reference], 0, degree)
	for i := range degree {
		outgoingReferences = append(outgoingReferences, Reference{
			LocalReference: m.OutgoingReferences.GetOutgoingReference(i),
		})
	}
	return model_core.NewMessage(m.Message, outgoingReferences), nil
}

type tagBoundUpdater struct {
	pool       *ObjectPool
	privateKey ed25519.PrivateKey
	publicKey  [ed25519.PublicKeySize]byte
	clock      clock.Clock
}

// NewTagBoundUpdater creates an updater for tags in storage that
// accepts buffered references. Tags are created asynchronously,
// through the same stream that is used to write objects in the pool
// to storage. Calls to UpdateTag() return immediately. Use
// ObjectPool.FlushTags() to wait for tags to be created. Failures to
// create tags are reported by ObjectPool.SendQueuedObjects().
func NewTagBoundUpdater(pool *ObjectPool, privateKey ed25519.PrivateKey, clock clock.Clock) model_tag.BoundUpdater[Reference] {
	return &tagBoundUpdater{
		pool:       pool,
		privateKey: privateKey,
		publicKey:  *(*[ed25519.PublicKeySize]byte)(privateKey.Public().(ed25519.PublicKey)),
		clock:      clock,
	}
}

func (u *tagBoundUpdater) UpdateTag(ctx context.Context, keyHash [sha256.Size]byte, reference Reference) error {
	key := tag.Key{
		SignaturePublicKey: u.publicKey,
		Hash:               keyHash,
	}
	keyMessage, err := key.ToProto()
	if err != nil {
		return util.StatusWrap(err, "Invalid tag key")
	}
	value := tag.Value{
		Reference: reference.LocalReference,
		Timestamp: u.clock.Now(),
	}
	signedValue, err := value.Sign(u.privateKey, keyHash)
	if err != nil {
		return err
	}
	return u.pool.updateTag(
		reference.LocalReference,
		&dag_pb.UploadDagsRequest_InitiateDag_Tag{
			Key:       keyMessage,
			Timestamp: timestamppb.New(signedValue.Value.Timestamp),
			Signature: signedValue.Signature[:],
		},
	)
}

// ObjectReaderForTesting is used to generate mocks for testing.
type ObjectReaderForTesting = model_parser.ObjectReader[object.LocalReference, model_core.Message[[]byte, object.LocalReference]]
