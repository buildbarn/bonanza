package dag

import (
	"container/heap"
	"context"
	"io"
	"sync"

	"bonanza.build/pkg/ds"
	"bonanza.build/pkg/proto/storage/dag"
	tag_pb "bonanza.build/pkg/proto/storage/tag"
	"bonanza.build/pkg/storage/object"
	"bonanza.build/pkg/storage/tag"
	pg_sync "bonanza.build/pkg/sync"

	"github.com/buildbarn/bb-storage/pkg/util"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type uploaderServer[TLease any] struct {
	objectUploader                 object.Uploader[object.GlobalReference, TLease]
	objectStoreSemaphore           *semaphore.Weighted
	tagUpdater                     tag.Updater[object.Namespace, TLease]
	maximumUnfinalizedDAGsCount    uint32
	maximumUnfinalizedParentsLimit object.Limit
}

// NewUploaderServer creates a gRPC server that is capable of forwarding
// storage requests of the dag.Uploader API to instances of
// object.Uploader and tag.Updater.
func NewUploaderServer[TLease any](
	objectUploader object.Uploader[object.GlobalReference, TLease],
	objectStoreSemaphore *semaphore.Weighted,
	tagUpdater tag.Updater[object.Namespace, TLease],
	maximumUnfinalizedDAGsCount uint32,
	maximumUnfinalizedParentsLimit object.Limit,
) dag.UploaderServer {
	return &uploaderServer[TLease]{
		objectUploader:                 objectUploader,
		tagUpdater:                     tagUpdater,
		objectStoreSemaphore:           objectStoreSemaphore,
		maximumUnfinalizedDAGsCount:    maximumUnfinalizedDAGsCount,
		maximumUnfinalizedParentsLimit: maximumUnfinalizedParentsLimit,
	}
}

// UploadDags can be used to upload objects contained in a DAG to
// object.Uploader. Upon success, it can optionally create tags pointing
// to the DAG's root object.
func (s *uploaderServer[TLease]) UploadDags(stream dag.Uploader_UploadDagsServer) error {
	// Perform handshake to negotiate the maximum amount of state
	// the client and server are willing to keep in memory.
	request, err := stream.Recv()
	if err != nil {
		if err == io.EOF {
			return status.Error(codes.InvalidArgument, "Did not receive initial message from client")
		}
		return err
	}
	requestType, ok := request.Type.(*dag.UploadDagsRequest_Handshake_)
	if !ok {
		return status.Error(codes.InvalidArgument, "Initial message from client did not contain a handshake")
	}
	handshakeRequest := requestType.Handshake
	namespace, err := object.NewNamespace(handshakeRequest.Namespace)
	if err != nil {
		return util.StatusWrap(err, "Invalid namespace")
	}
	maximumUnfinalizedParentsLimit := s.maximumUnfinalizedParentsLimit
	if clientLimit := handshakeRequest.MaximumUnfinalizedParentsLimit; clientLimit != nil {
		maximumUnfinalizedParentsLimit = maximumUnfinalizedParentsLimit.Min(object.NewLimit(clientLimit))
	}

	if err := stream.Send(&dag.UploadDagsResponse{
		Type: &dag.UploadDagsResponse_Handshake_{
			Handshake: &dag.UploadDagsResponse_Handshake{
				MaximumUnfinalizedDagsCount: s.maximumUnfinalizedDAGsCount,
			},
		},
	}); err != nil {
		return err
	}

	// Process incoming replication requests.
	group, groupCtx := errgroup.WithContext(stream.Context())
	group.Go(func() error {
		r := dagReceiver[TLease]{
			server:                         s,
			stream:                         stream,
			group:                          group,
			context:                        groupCtx,
			namespace:                      namespace,
			maximumUnfinalizedParentsLimit: maximumUnfinalizedParentsLimit,

			remainingUnfinalizedParentsLimit: maximumUnfinalizedParentsLimit,
			objectsByReference:               map[object.LocalReference]*objectState[TLease]{},
			replicatingObjects:               map[uint64]*objectState[TLease]{},
		}
		r.lastRequestedObject = &r.firstRequestedObject
		r.lastFinalizedDAG = &r.firstFinalizedDAG
		group.Go(r.processIncomingMessages)
		group.Go(r.processPendingObjects)
		group.Go(r.processOutgoingMessages)
		return nil
	})
	return group.Wait()
}

// objectState contains all state that needs to be tracked for a single
// object as part of UploadDags().
type objectState[TLease any] struct {
	reference object.LocalReference

	// If set, the RequestObject message that still needs to be sent
	// to the client to either request the object's contents, or
	// indicate they are not needed.
	//
	// This field may be set multiple times during the lifetime of
	// objectState. If an object has already been received by the
	// server and is in the process of being written to storage, it
	// may also appear in other DAGs, or other parts of the same
	// DAG. In that case a second RequestObject message still needs
	// to be sent to the client to release the reference index.
	requestObject *dag.UploadDagsResponse_RequestObject

	// If the RequestObject message is queued for transmission, the
	// next object in the transmission queue.
	nextRequestedObject *objectState[TLease]

	// If set, the object is still in the process of its existence
	// being checked, transmitted to the server, or written into
	// object.Uploader.
	unfinalized *unfinalizedObjectState[TLease]

	// When the object is finalized, the lease that needs to be
	// provided to PutObject when writing parent objects.
	lease TLease
}

// unfinalizedObjectState contains all state for a single object that is
// in the process of its existence being checked, transmitted to the
// server, or written into object.Uploader.
type unfinalizedObjectState[TLease any] struct {
	// If the object is the root of a DAG, the tag that needs to be
	// written into TagStore after upload has completed.
	dags []*unfinalizedDAGState
	// If the object is a child, the list of parent objects that
	// can't be written to store yet, due to the child not being
	// stored yet.
	unfinalizedParents []unfinalizedParent[TLease]
	// If the object is a parent, the object's contents that should
	// be written to storage after all children have been stored.
	hasUnfinalizedChildren *hasUnfinalizedChildrenState[TLease]
}

type unfinalizedParent[TLease any] struct {
	object *objectState[TLease]
	lease  *TLease
}

// hasUnfinalizedChildrenState contains all state for a single object
// that has been sent by the client to the server, but cannot be written
// to storate yet, due to one or more children not being stored yet.
type hasUnfinalizedChildrenState[TLease any] struct {
	contents                 *object.Contents
	leases                   []TLease
	childErr                 error
	unfinalizedChildrenCount int
}

// pendingObjectsHeap is a binary heap of objects whose existence still
// need to be checked.
type pendingObjectsHeap[TLease any] struct {
	ds.Slice[*objectState[TLease]]
}

func (h pendingObjectsHeap[TLease]) Less(i, j int) bool {
	return h.Slice[i].reference.CompareByHeight(h.Slice[j].reference) < 0
}

type unfinalizedDAGRootTag struct {
	key         tag.Key
	signedValue tag.SignedValue
}

// unfinalizedDAGState contains all state for a single DAG that is in
// the process of being uploaded to storage.
type unfinalizedDAGState struct {
	rootReferenceIndex uint64
	rootTag            *unfinalizedDAGRootTag
}

// finalizedDAGState contains all state for a single DAG that has been
// written to storage, but for which a FinalizeDag message still needs
// to be sent back to the client.
type finalizedDAGState struct {
	finalization     dag.UploadDagsResponse_FinalizeDag
	nextFinalizedDAG *finalizedDAGState
}

// dagReceiver contains all state that needs to be tracked during a call
// to UploadDags().
type dagReceiver[TLease any] struct {
	// Constant fields.
	server                         *uploaderServer[TLease]
	stream                         dag.Uploader_UploadDagsServer
	group                          *errgroup.Group
	context                        context.Context
	namespace                      object.Namespace
	maximumUnfinalizedParentsLimit object.Limit

	lock             sync.Mutex
	gracefulShutdown bool

	// Counters for limiting the maximum amount of parallelism and
	// memory usage.
	unfinalizedDAGsCount             uint32
	remainingUnfinalizedParentsLimit object.Limit

	objectsByReference map[object.LocalReference]*objectState[TLease]

	// Queue of objects that still need to be replicated.
	pendingObjects       pendingObjectsHeap[TLease]
	pendingObjectsWakeup pg_sync.ConditionVariable

	// Queues for messages to be sent back to the client.
	requestObjectCount     int
	firstRequestedObject   *objectState[TLease]
	lastRequestedObject    **objectState[TLease]
	firstFinalizedDAG      *finalizedDAGState
	lastFinalizedDAG       **finalizedDAGState
	outgoingMessagesWakeup pg_sync.ConditionVariable

	// Objects for which we have sent RequestObject, but still need
	// to receive ProvideObjectContents.
	replicatingObjects map[uint64]*objectState[TLease]
}

// newRequestObject creates a new RequestObject message that at some
// point needs to be sent back to the client.
func (r *dagReceiver[TLease]) newRequestObject(referenceIndex uint64) *dag.UploadDagsResponse_RequestObject {
	r.requestObjectCount++
	return &dag.UploadDagsResponse_RequestObject{
		LowestReferenceIndex: referenceIndex,
	}
}

// getOrCreateObjectState looks up the state that is tracked by
// UploadDags() for a single object. If no state exists, it is created.
func (r *dagReceiver[TLease]) getOrCreateObjectState(reference object.LocalReference, referenceIndex uint64) *objectState[TLease] {
	o, ok := r.objectsByReference[reference]
	if ok {
		if o.requestObject == nil {
			// An existing object appeared in another (part
			// of the) DAG, and we have already sent a
			// RequestObject message back to the client.
			// Schedule the transmission of another
			// RequestObject message, indicating that we
			// don't want the client to send the object's
			// contents again.
			o.requestObject = r.newRequestObject(referenceIndex)
			r.queueRequestObjectLocked(o)
		} else {
			// A RequestObject message was already scheduled
			// to be transmitted. Make sure that we send
			// back a single RequestObject message that
			// acknowledges both reference indices at the
			// same time.
			o.requestObject.AdditionalReferenceIndices++
		}
	} else {
		// Object was not seen before, or its state has already
		// been purged in the meantime.
		o = &objectState[TLease]{
			reference:     reference,
			requestObject: r.newRequestObject(referenceIndex),
			unfinalized:   &unfinalizedObjectState[TLease]{},
		}
		r.objectsByReference[reference] = o
		heap.Push(&r.pendingObjects, o)
		r.pendingObjectsWakeup.Broadcast()
	}
	return o
}

// detachObjectState removes the object state from the map of active
// objects. This function is either called when the object is fully
// processed, or if an error has occurred that prevents us from
// deduplicating requests to replicate.
func (r *dagReceiver[TLease]) detachObjectState(o *objectState[TLease]) {
	if r.objectsByReference[o.reference] == o {
		delete(r.objectsByReference, o.reference)
	}
}

// processIncomingMessages processes messages InitiateDag and
// ProvideObjectContents messages sent by the client.
func (r *dagReceiver[TLease]) processIncomingMessages() error {
	var nextReferenceIndex uint64
	for {
		request, err := r.stream.Recv()
		if err == io.EOF {
			r.lock.Lock()
			if r.requestObjectCount != 0 {
				r.lock.Unlock()
				return status.Errorf(codes.InvalidArgument, "Client closed the request, even though the server still needs to request %d or more objects", r.requestObjectCount)
			}
			if len(r.replicatingObjects) != 0 {
				r.lock.Unlock()
				return status.Errorf(codes.InvalidArgument, "Client closed the request, even though the client still needs to provide the contents of %d or more objects", len(r.replicatingObjects))
			}

			// The client has finished sending all objects
			// belonging to the DAGs it wanted to upload.
			// The client can no longer send any
			// ProvideObjectContents messages without
			// sending another InitializeDag. Permit a
			// graceful shutdown.
			r.gracefulShutdown = true
			r.pendingObjectsWakeup.Broadcast()
			r.outgoingMessagesWakeup.Broadcast()
			r.lock.Unlock()
			return nil
		} else if err != nil {
			return err
		}

		switch requestType := request.Type.(type) {
		case *dag.UploadDagsRequest_InitiateDag_:
			initiateDAG := requestType.InitiateDag
			var rootReference object.LocalReference
			var rootTag *unfinalizedDAGRootTag
			err := func() error {
				var err error
				rootReference, err = r.namespace.NewLocalReference(initiateDAG.RootReference)
				if err != nil {
					return util.StatusWrap(err, "Invalid root reference")
				}

				// Deny requests to upload DAGs that have an
				// excessive height or size. Queueing these
				// would be pointless, as getPendingObject()
				// wouldn't be willing to dequeue them.
				if !r.maximumUnfinalizedParentsLimit.CanAcquireObjectAndChildren(rootReference) {
					return status.Error(codes.InvalidArgument, "Height or maximum total parents size of the object exceeds the limit that was established during handshaking")
				}

				if rootTagMessage := initiateDAG.RootTag; rootTagMessage != nil {
					key, err := tag.NewKeyFromProto(rootTagMessage.Key)
					if err != nil {
						return util.StatusWrap(err, "Invalid root tag key")
					}
					signedValue, err := tag.NewSignedValueFromProto(
						&tag_pb.SignedValue{
							Value: &tag_pb.Value{
								Reference: rootReference.GetRawReference(),
								Timestamp: rootTagMessage.Timestamp,
							},
							Signature: rootTagMessage.Signature,
						},
						r.namespace.ReferenceFormat,
						key,
					)
					if err != nil {
						return util.StatusWrap(err, "Invalid root tag signed value")
					}
					rootTag = &unfinalizedDAGRootTag{
						key:         key,
						signedValue: signedValue,
					}
				}
				return nil
			}()

			// The client should respect the maximum number of
			// DAGs that the server is willing to process at once.
			r.lock.Lock()
			if r.unfinalizedDAGsCount == r.server.maximumUnfinalizedDAGsCount {
				r.lock.Unlock()
				return status.Error(codes.InvalidArgument, "Client did not respect the maximum unfinalized DAGs count that was established during handshaking")
			}
			r.unfinalizedDAGsCount++

			if err == nil {
				o := r.getOrCreateObjectState(rootReference, nextReferenceIndex)
				if o.unfinalized == nil {
					// The provided DAG was already
					// uploaded previously. Simply write
					// an additional tag in TagStore.
					r.finalizeDAGLocked(rootReference, o.lease, rootTag, nextReferenceIndex, nil)
				} else {
					// DAG for which we don't know if it
					// exists yet.
					o.unfinalized.dags = append(
						o.unfinalized.dags,
						&unfinalizedDAGState{
							rootReferenceIndex: nextReferenceIndex,
							rootTag:            rootTag,
						},
					)
				}
			} else {
				// Client provided an invalid reference.
				// Instead of making the RPC fail, return
				// the error through fictive RequestObject
				// and FinalizeDag messages. This allows
				r.queueRequestObjectLocked(&objectState[TLease]{
					requestObject: r.newRequestObject(nextReferenceIndex),
				})
				r.queueFinalizeDAGLocked(nextReferenceIndex, err)
			}
			r.lock.Unlock()

			nextReferenceIndex++
		case *dag.UploadDagsRequest_ProvideObjectContents_:
			provideObjectContents := requestType.ProvideObjectContents
			r.lock.Lock()
			o, ok := r.replicatingObjects[provideObjectContents.LowestReferenceIndex]
			if !ok {
				r.lock.Unlock()
				return status.Errorf(codes.InvalidArgument, "Client provided object contents for lowest reference index %d, which was not expected", provideObjectContents.LowestReferenceIndex)
			}
			delete(r.replicatingObjects, provideObjectContents.LowestReferenceIndex)

			if len(provideObjectContents.ObjectContents) == 0 {
				// Client left ObjectContents unset. This can
				// be used to clients to cancel the
				// transmission of DAGs without tearing down
				// the connection entirely.
				var lease TLease
				r.finalizeObjectLocked(o, lease, status.Errorf(codes.Canceled, "Client canceled upload of object with reference %s", o.reference))
				r.lock.Unlock()
			} else {
				r.lock.Unlock()

				// Validate the contents of the provided object.
				contents, err := object.NewContentsFromFullData(o.reference, provideObjectContents.ObjectContents)
				if err != nil {
					return util.StatusWrapf(err, "Invalid contents for object with reference %s", o.reference)
				}

				degree := o.reference.GetDegree()
				leases := make([]TLease, degree)
				unfinalizedChildrenCount := 0
				if degree > 0 {
					// A parent object. Schedule replication
					// of its children.
					r.lock.Lock()
					for i := 0; i < degree; i++ {
						oChild := r.getOrCreateObjectState(contents.GetOutgoingReference(i), nextReferenceIndex)
						if oChild.unfinalized == nil {
							// Child has already finished replicating.
							leases[i] = oChild.lease
						} else {
							oChild.unfinalized.unfinalizedParents = append(
								oChild.unfinalized.unfinalizedParents,
								unfinalizedParent[TLease]{
									object: o,
									lease:  &leases[i],
								},
							)
							unfinalizedChildrenCount++
						}
						nextReferenceIndex++
					}

					if unfinalizedChildrenCount > 0 {
						// The object has one or more children that still
						// need to be checked for existence and/or
						// replicated. This means that the current object's
						// contents can't be written just yet. Preserve
						// them, so that finalizeObjectLocked() against the
						// child object can write them.
						o.unfinalized.hasUnfinalizedChildren = &hasUnfinalizedChildrenState[TLease]{
							contents:                 contents,
							leases:                   leases,
							unfinalizedChildrenCount: unfinalizedChildrenCount,
						}
					}

					// Now that all child objects have been
					// enqueued, release excess capacity
					// reserved by getPendingObject().
					r.remainingUnfinalizedParentsLimit.ReleaseChildren(o.reference)
					r.pendingObjectsWakeup.Broadcast()
					r.lock.Unlock()
				}

				if unfinalizedChildrenCount == 0 {
					r.putObject(o, contents, leases)
				}
			}
		default:
			return status.Error(codes.InvalidArgument, "Client sent a message of an unknown type")
		}
	}
}

func (r *dagReceiver[TLease]) queueRequestObjectLocked(o *objectState[TLease]) {
	if o.requestObject == nil {
		panic("attempted to schedule RequestObject message for object that did not have an outstanding request")
	}
	if o.nextRequestedObject != nil || r.lastRequestedObject == &o.nextRequestedObject {
		panic("RequestObject message is already requested for object")
	}
	*r.lastRequestedObject = o
	r.lastRequestedObject = &o.nextRequestedObject
	r.outgoingMessagesWakeup.Broadcast()
}

func (r *dagReceiver[TLease]) queueFinalizeDAGLocked(rootReferenceIndex uint64, err error) {
	d := &finalizedDAGState{
		finalization: dag.UploadDagsResponse_FinalizeDag{
			RootReferenceIndex: rootReferenceIndex,
			Status:             status.Convert(err).Proto(),
		},
	}
	*r.lastFinalizedDAG = d
	r.lastFinalizedDAG = &d.nextFinalizedDAG
	r.outgoingMessagesWakeup.Broadcast()
}

// getPendingObject returns the next object that needs to be checked for
// existence in object.Uploader.
func (r *dagReceiver[TLease]) getPendingObject() (*objectState[TLease], error) {
	r.lock.Lock()
	for {
		if len(r.pendingObjects.Slice) > 0 {
			// When dequeueing objects, we should respect
			// the memory usage limits that were negotiated
			// during handshaking.
			//
			// We should not only consider the size of the
			// object itself, but also its height ahd the
			// maximum total size of all parents underneath.
			// Otherwise we may dequeue too many objects
			// stored at the top of the graph, preventing us
			// from reading lower ones without exceeding
			// memory limits.
			if r.remainingUnfinalizedParentsLimit.AcquireObjectAndChildren(r.pendingObjects.Slice[0].reference) {
				defer r.lock.Unlock()
				return heap.Pop(&r.pendingObjects).(*objectState[TLease]), nil
			}
		} else if r.gracefulShutdown {
			r.lock.Unlock()
			return nil, nil
		}

		if err := r.pendingObjectsWakeup.Wait(r.context, &r.lock); err != nil {
			return nil, err
		}
	}
}

// processPendingObjects processes objects that have been queued to have
// their existence in object.Uploader checked.
func (r *dagReceiver[TLease]) processPendingObjects() error {
	for {
		o, err := r.getPendingObject()
		if o == nil {
			return err
		}

		if err := util.AcquireSemaphore(r.context, r.server.objectStoreSemaphore, 1); err != nil {
			return err
		}
		r.group.Go(func() error {
			result, err := r.server.objectUploader.UploadObject(
				r.context,
				r.namespace.WithLocalReference(o.reference),
				/* contents = */ nil,
				/* leases = */ nil,
				/* wantContentsIfIncomplete = */ false,
			)
			r.server.objectStoreSemaphore.Release(1)

			r.lock.Lock()
			defer r.lock.Unlock()

			if err == nil {
				switch resultType := result.(type) {
				case object.UploadObjectComplete[TLease]:
					// Object exists. No need to
					// request its contents.
					r.finalizeObjectLocked(o, resultType.Lease, nil)
				case object.UploadObjectIncomplete[TLease], object.UploadObjectMissing[TLease]:
					// Object does not exist. Or it exists,
					// but it has one or more expired
					// leases. Request that the client
					// uploads it again to reobtain valid
					// leases.
					o.requestObject.RequestContents = true
				default:
					panic("unknown upload object result type")
				}
			} else {
				// Internal error.
				var lease TLease
				r.finalizeObjectLocked(o, lease, err)
			}
			r.queueRequestObjectLocked(o)

			// If we're not going to request the object's
			// contents, we're not going to receive a
			// ProvideObjectContents message from the
			// client. This means we're free to request
			// other objects.
			if !o.requestObject.RequestContents {
				r.remainingUnfinalizedParentsLimit.ReleaseChildren(o.reference)
				r.pendingObjectsWakeup.Broadcast()
			}
			return nil
		})
	}
}

// finalizeObjectLocked is called when an object has finished
// replicating, or after an error occurred in the process.
func (r *dagReceiver[TLease]) finalizeObjectLocked(o *objectState[TLease], lease TLease, err error) {
	unfinalized := o.unfinalized
	o.unfinalized = nil

	// Delete the object state if it is completely unused. If not,
	// preserve the lease, so that any future ProvideObjectContents
	// messages that refer to this object can obtain the lease
	// without needing to call GetObjectLease() again.
	if err != nil || o.requestObject == nil {
		r.detachObjectState(o)
	} else {
		o.lease = lease
	}

	for _, unfinalizedParent := range unfinalized.unfinalizedParents {
		// Propagate the lease of the object to the parents, so
		// that they can provide it to object.Uploader when
		// writing.
		*unfinalizedParent.lease = lease
		oParent := unfinalizedParent.object
		hasUnfinalizedChildren := oParent.unfinalized.hasUnfinalizedChildren

		// If an error occurred replicating an object, place any
		// parents in a dead state. This ensures that they don't
		// get written to storage.
		if err != nil && hasUnfinalizedChildren.childErr == nil {
			hasUnfinalizedChildren.contents = nil
			hasUnfinalizedChildren.leases = nil
			hasUnfinalizedChildren.childErr = err
			r.detachObjectState(oParent)
		}

		// If finalizing this object means that the parent
		// object is no longer waiting for any children to be
		// replicated, finalize the parent.
		hasUnfinalizedChildren.unfinalizedChildrenCount--
		if hasUnfinalizedChildren.unfinalizedChildrenCount == 0 {
			oParent.unfinalized.hasUnfinalizedChildren = nil
			if childErr := hasUnfinalizedChildren.childErr; childErr == nil {
				r.lock.Unlock()
				r.putObject(oParent, hasUnfinalizedChildren.contents, hasUnfinalizedChildren.leases)
				r.lock.Lock()
			} else {
				var lease TLease
				r.finalizeObjectLocked(oParent, lease, childErr)
			}
		}
	}

	// If the object is the root of a DAG, write tags into TagStore
	// and send FinalizeDag messages back to the client.
	for _, dag := range unfinalized.dags {
		r.finalizeDAGLocked(o.reference, lease, dag.rootTag, dag.rootReferenceIndex, err)
	}

	r.remainingUnfinalizedParentsLimit.ReleaseObject(o.reference)
	r.pendingObjectsWakeup.Broadcast()
}

// finalizeDAGLocked writes tags into TagStore and sends FinalizeDag
// messages back to the client.
func (r *dagReceiver[TLease]) finalizeDAGLocked(rootReference object.LocalReference, rootLease TLease, rootTag *unfinalizedDAGRootTag, rootReferenceIndex uint64, err error) {
	if err != nil || rootTag == nil {
		// Fast path: Client requested uploading a DAG without
		// storing a tag in TagStore, or uploading failed.
		r.queueFinalizeDAGLocked(rootReferenceIndex, err)
	} else {
		// Slow path: Only send FinalizeDag to the client after
		// we've been able to write a tag. The concurrency of
		// this is bounded by unfinalizedDAGsCount.
		r.group.Go(func() error {
			err := r.server.tagUpdater.UpdateTag(
				r.context,
				r.namespace,
				rootTag.key,
				rootTag.signedValue,
				rootLease,
			)

			r.lock.Lock()
			r.queueFinalizeDAGLocked(rootReferenceIndex, err)
			r.lock.Unlock()
			return nil
		})
	}
}

// putObject writes an object into object.Uploader, after all of its
// children have been written as well.
func (r *dagReceiver[TLease]) putObject(o *objectState[TLease], contents *object.Contents, childrenLeases []TLease) {
	if err := util.AcquireSemaphore(r.context, r.server.objectStoreSemaphore, 1); err != nil {
		return
	}
	r.group.Go(func() error {
		result, err := r.server.objectUploader.UploadObject(
			r.context,
			r.namespace.WithLocalReference(o.reference),
			contents,
			childrenLeases,
			/* wantContentsIfIncomplete = */ false,
		)
		r.server.objectStoreSemaphore.Release(1)

		r.lock.Lock()
		if err == nil {
			switch resultType := result.(type) {
			case object.UploadObjectComplete[TLease]:
				// Successfully uploaded object.
				r.finalizeObjectLocked(o, resultType.Lease, nil)
			case object.UploadObjectIncomplete[TLease]:
				// Client took long to upload the
				// children of the object. This is fine.
				// It just means that the next time the
				// associated tag is resolved or parts
				// of the DAG are reused, its leases
				// must be renewed.
				var lease TLease
				r.finalizeObjectLocked(o, lease, nil)
			default:
				panic("unexpected upload object result type")
			}
		} else {
			// Internal error.
			var lease TLease
			r.finalizeObjectLocked(o, lease, err)
		}
		r.lock.Unlock()
		return nil
	})
}

// processOutgoingMessages sends RequestObject and FinalizeDag messages
// to the client.
func (r *dagReceiver[TLease]) processOutgoingMessages() error {
	for {
		// Wait for one or more UploadDagsResponse messages be sent.
		r.lock.Lock()
		for r.firstRequestedObject == nil && r.firstFinalizedDAG == nil {
			if r.gracefulShutdown && r.unfinalizedDAGsCount == 0 {
				r.lock.Unlock()
				return nil
			}
			if err := r.outgoingMessagesWakeup.Wait(r.context, &r.lock); err != nil {
				return err
			}
		}

		if r.firstRequestedObject != nil {
			// There are one or more objects for which we've
			// checked existence. Send a RequestObject message
			// to the client.
			o := r.firstRequestedObject
			r.firstRequestedObject = o.nextRequestedObject
			if r.firstRequestedObject == nil {
				r.lastRequestedObject = &r.firstRequestedObject
			}
			o.nextRequestedObject = nil

			// Detach the existing RequestObject message.
			// This ensures that if we see this object being
			// referenced later on, we release those
			// reference indices as well by sending
			// additional RequestObject messages.
			requestObject := o.requestObject
			o.requestObject = nil
			r.requestObjectCount--
			if o.unfinalized == nil {
				r.detachObjectState(o)
			}
			if requestObject.RequestContents {
				r.replicatingObjects[requestObject.LowestReferenceIndex] = o
			}
			r.lock.Unlock()

			if err := r.stream.Send(&dag.UploadDagsResponse{
				Type: &dag.UploadDagsResponse_RequestObject_{
					RequestObject: requestObject,
				},
			}); err != nil {
				return err
			}
		} else {
			// There one or more DAGs that have finished
			// uploading. Send a FinalizeDag message to the
			// client.
			//
			// Make sure that this is only done after all
			// RequestObjects are messages are sent, so that
			// the client only receives FinalizeDag after it
			// has purged all object state belonging to that
			// DAG.
			d := r.firstFinalizedDAG
			r.firstFinalizedDAG = d.nextFinalizedDAG
			if r.firstFinalizedDAG == nil {
				r.lastFinalizedDAG = &r.firstFinalizedDAG
			}
			d.nextFinalizedDAG = nil

			if r.unfinalizedDAGsCount == 0 {
				panic("invalid unfinalized DAGs count")
			}
			r.unfinalizedDAGsCount--
			r.lock.Unlock()

			if err := r.stream.Send(&dag.UploadDagsResponse{
				Type: &dag.UploadDagsResponse_FinalizeDag_{
					FinalizeDag: &d.finalization,
				},
			}); err != nil {
				return err
			}
		}
	}
}
