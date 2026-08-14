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
	statuspb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	statusClientCanceledUpload = status.New(codes.Canceled, "Client canceled upload of object").Proto()
	statusChildUploadFailure   = status.New(codes.Canceled, "One or more child objects were not uploaded successfully").Proto()
	statusRootUploadFailure    = status.New(codes.Canceled, "Root object was not uploaded successfully").Proto()
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
			requestedObjects:                 map[uint64]*objectState[TLease]{},
		}
		r.lastRequestedObject = &r.firstRequestedObject
		r.lastFinalizedObject = &r.firstFinalizedObject
		r.lastFinalizedTag = &r.firstFinalizedTag
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

	// If set, the FinalizeObject message that still needs to be
	// sent to the client to report whether or not an object was
	// successfully written to storage.
	//
	// This field may be set multiple times during the lifetime of
	// objectState. If an object has already been received by the
	// server and is in the process of being written to storage, it
	// may also appear in other DAGs, or other parts of the same
	// DAG. In that case a second FinalizeObject message still needs
	// to be sent to the client to release the reference index.
	finalizeObject *dag.UploadDagsResponse_FinalizeObject

	// If the RequestObjectContents or FinalizeObject message is
	// queued for transmission, the next object in the transmission
	// queue.
	nextRequestedOrFinalizedObject *objectState[TLease]

	// If set, the object is still in the process of its existence
	// being checked, transmitted to the server, or written into
	// object.Uploader.
	unfinalized *unfinalizedObjectState[TLease]

	// The number of times InitiateDag was called without specifying
	// a root tag, for which no FinalizeObject has been returned
	// yet.
	unfinalizedDAGsWithoutTagsCount uint32

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
	tags []*unfinalizedTagState
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
	hasChildFailures         bool
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

// unfinalizedTagState contains all state for a single tag that is in
// the process of being uploaded to storage.
type unfinalizedTagState struct {
	rootReferenceIndex uint64
	key                tag.Key
	signedValue        tag.SignedValue
}

// finalizedTagState contains all state for a single tag that has been
// written to storage, but for which a FinalizeTag message still needs
// to be sent back to the client.
type finalizedTagState struct {
	finalization     dag.UploadDagsResponse_FinalizeTag
	nextFinalizedTag *finalizedTagState
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
	pendingObjects         pendingObjectsHeap[TLease]
	pendingObjectsWakeup   pg_sync.ConditionVariable
	requestableObjectCount int

	// Queues for messages to be sent back to the client.
	firstRequestedObject   *objectState[TLease]
	lastRequestedObject    **objectState[TLease]
	requestedObjectCount   int
	firstFinalizedObject   *objectState[TLease]
	lastFinalizedObject    **objectState[TLease]
	firstFinalizedTag      *finalizedTagState
	lastFinalizedTag       **finalizedTagState
	outgoingMessagesWakeup pg_sync.ConditionVariable

	// Objects for which we have sent RequestObjectContents, but
	// still need to receive ProvideObjectContents.
	requestedObjects map[uint64]*objectState[TLease]
}

// getOrCreateObjectState looks up the state that is tracked by
// UploadDags() for a single object. If no state exists, it is created.
func (r *dagReceiver[TLease]) getOrCreateObjectState(reference object.LocalReference, referenceIndex uint64) *objectState[TLease] {
	o, ok := r.objectsByReference[reference]
	if ok {
		if o.finalizeObject == nil {
			// An existing object appeared in another (part
			// of the) DAG, and we have already sent a
			// FinalizeObject message back to the client.
			// Schedule the transmission of another
			// FinalizeObject message, indicating that we
			// don't want the client to send the object's
			// contents again.
			o.finalizeObject = &dag.UploadDagsResponse_FinalizeObject{
				LowestReferenceIndex: referenceIndex,
			}
			r.queueFinalizeObjectLocked(o, nil)
		} else {
			// A FinalizeObject message was already
			// scheduled to be transmitted. Make sure that
			// we send back a single FinalizeObject message
			// that acknowledges both reference indices at
			// the same time.
			o.finalizeObject.AdditionalReferenceIndices++
		}
	} else {
		// Object was not seen before, or its state has already
		// been purged in the meantime.
		o = &objectState[TLease]{
			reference: reference,
			finalizeObject: &dag.UploadDagsResponse_FinalizeObject{
				LowestReferenceIndex: referenceIndex,
			},
			unfinalized: &unfinalizedObjectState[TLease]{},
		}
		r.objectsByReference[reference] = o
		heap.Push(&r.pendingObjects, o)
		r.requestableObjectCount++
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
			if a, b, c := r.requestableObjectCount, r.requestedObjectCount, len(r.requestedObjects); a != 0 || b != 0 || c != 0 {
				r.lock.Unlock()
				return status.Errorf(codes.InvalidArgument, "Client closed the request, even though the server was still checking the existence of %d objects, had %d object request messages queued, and was waiting for the contents of %d objects from the client", a, b, c)
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
			rootReference, err := r.namespace.NewLocalReference(initiateDAG.RootReference)
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

			var rootTag *unfinalizedTagState
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
				rootTag = &unfinalizedTagState{
					rootReferenceIndex: nextReferenceIndex,
					key:                key,
					signedValue:        signedValue,
				}
			}

			// The client should respect the maximum number of
			// DAGs that the server is willing to process at once.
			r.lock.Lock()
			if r.unfinalizedDAGsCount == r.server.maximumUnfinalizedDAGsCount {
				r.lock.Unlock()
				return status.Error(codes.InvalidArgument, "Client did not respect the maximum unfinalized DAGs count that was established during handshaking")
			}
			r.unfinalizedDAGsCount++

			o := r.getOrCreateObjectState(rootReference, nextReferenceIndex)
			if rootTag == nil {
				// Because no tag was provided, we will
				// not send back FinalizeTag. This means
				// that FinalizeObject of the root
				// object concludes the transmission of
				// this DAG.
				o.unfinalizedDAGsWithoutTagsCount++
			} else if o.unfinalized == nil {
				// The provided DAG was already uploaded
				// previously. Simply write an
				// additional tag in TagStore.
				r.updateAndFinalizeTag(rootReference, o.lease, rootTag)
			} else {
				// DAG for which we don't know if it
				// exists yet.
				o.unfinalized.tags = append(o.unfinalized.tags, rootTag)
			}
			r.lock.Unlock()

			nextReferenceIndex++
		case *dag.UploadDagsRequest_ProvideObjectContents_:
			provideObjectContents := requestType.ProvideObjectContents
			r.lock.Lock()
			o, ok := r.requestedObjects[provideObjectContents.LowestReferenceIndex]
			if !ok {
				r.lock.Unlock()
				return status.Errorf(codes.InvalidArgument, "Client provided object contents for lowest reference index %d, which was not expected", provideObjectContents.LowestReferenceIndex)
			}
			delete(r.requestedObjects, provideObjectContents.LowestReferenceIndex)

			if len(provideObjectContents.ObjectContents) == 0 {
				// Client left ObjectContents unset. This can
				// be used to clients to cancel the
				// transmission of DAGs without tearing down
				// the connection entirely.
				var lease TLease
				r.finalizeObjectLocked(o, lease, statusClientCanceledUpload)
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

// queueRequestObjectContentsLocked queues aeRequestObjectContents
// message for transmission back to the client.
func (r *dagReceiver[TLease]) queueRequestObjectContentsLocked(o *objectState[TLease]) {
	if o.nextRequestedOrFinalizedObject != nil || r.lastRequestedObject == &o.nextRequestedOrFinalizedObject || r.lastFinalizedObject == &o.nextRequestedOrFinalizedObject {
		panic("RequestObjectContents or FinalizeObject message is already requested for object")
	}
	*r.lastRequestedObject = o
	r.lastRequestedObject = &o.nextRequestedOrFinalizedObject
	r.requestedObjectCount++
	r.outgoingMessagesWakeup.Broadcast()
}

// queueFinalizeObjectLocked queues a FinalizeObject message for
// transmission back to the client.
func (r *dagReceiver[TLease]) queueFinalizeObjectLocked(o *objectState[TLease], status *statuspb.Status) {
	if o.finalizeObject == nil {
		panic("attempted to schedule FinalizeObject message for object that did not have an outstanding request")
	}
	o.finalizeObject.Status = status

	if o.nextRequestedOrFinalizedObject != nil || r.lastRequestedObject == &o.nextRequestedOrFinalizedObject || r.lastFinalizedObject == &o.nextRequestedOrFinalizedObject {
		panic("RequestObjectContents or FinalizeObject message is already requested for object")
	}
	*r.lastFinalizedObject = o
	r.lastFinalizedObject = &o.nextRequestedOrFinalizedObject
	r.outgoingMessagesWakeup.Broadcast()
}

// queueFinalizeTagLocked queues a FinalizeTag message for transmission
// back to the client.
func (r *dagReceiver[TLease]) queueFinalizeTagLocked(rootReferenceIndex uint64, status *statuspb.Status) {
	t := &finalizedTagState{
		finalization: dag.UploadDagsResponse_FinalizeTag{
			RootReferenceIndex: rootReferenceIndex,
			Status:             status,
		},
	}

	*r.lastFinalizedTag = t
	r.lastFinalizedTag = &t.nextFinalizedTag
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

			r.requestableObjectCount--
			requestContents := false
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
					requestContents = true
					r.queueRequestObjectContentsLocked(o)
				default:
					panic("unknown upload object result type")
				}
			} else {
				// Internal error.
				var lease TLease
				r.finalizeObjectLocked(o, lease, status.Convert(err).Proto())
			}

			// If we're not going to request the object's
			// contents, we're not going to receive a
			// ProvideObjectContents message from the
			// client. This means we're free to request
			// other objects.
			if !requestContents {
				r.remainingUnfinalizedParentsLimit.ReleaseChildren(o.reference)
				r.pendingObjectsWakeup.Broadcast()
			}
			return nil
		})
	}
}

// finalizeObjectLocked is called when an object has finished
// replicating, or after an error occurred in the process.
func (r *dagReceiver[TLease]) finalizeObjectLocked(o *objectState[TLease], lease TLease, status *statuspb.Status) {
	r.queueFinalizeObjectLocked(o, status)

	unfinalized := o.unfinalized
	o.unfinalized = nil

	// Upon success, don't detach the object state immediately. This
	// alllows us to hold on to the lease a bit longer. That way we
	// need to do fewer lookups against storage, and may send fewer
	// FinalizeObject messages back to the client.
	hasFailure := status.GetCode() != 0
	if hasFailure {
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
		if hasFailure && !hasUnfinalizedChildren.hasChildFailures {
			hasUnfinalizedChildren.contents = nil
			hasUnfinalizedChildren.leases = nil
			hasUnfinalizedChildren.hasChildFailures = true
			r.detachObjectState(oParent)
		}

		// If finalizing this object means that the parent
		// object is no longer waiting for any children to be
		// replicated, finalize the parent.
		hasUnfinalizedChildren.unfinalizedChildrenCount--
		if hasUnfinalizedChildren.unfinalizedChildrenCount == 0 {
			oParent.unfinalized.hasUnfinalizedChildren = nil
			if hasUnfinalizedChildren.hasChildFailures {
				var lease TLease
				r.finalizeObjectLocked(oParent, lease, statusChildUploadFailure)
			} else {
				r.lock.Unlock()
				r.putObject(oParent, hasUnfinalizedChildren.contents, hasUnfinalizedChildren.leases)
				r.lock.Lock()
			}
		}
	}

	// If the object is the root of a DAG, write tags into TagStore
	// and send FinalizeTag messages back to the client.
	for _, tag := range unfinalized.tags {
		if hasFailure {
			r.queueFinalizeTagLocked(tag.rootReferenceIndex, statusRootUploadFailure)
		} else {
			r.updateAndFinalizeTag(o.reference, lease, tag)
		}
	}

	r.remainingUnfinalizedParentsLimit.ReleaseObject(o.reference)
	r.pendingObjectsWakeup.Broadcast()
}

// updateAndFinalizeTag writes tags into TagStore and sends a
// FinalizeTag messages back to the client.
func (r *dagReceiver[TLease]) updateAndFinalizeTag(rootReference object.LocalReference, rootLease TLease, rootTag *unfinalizedTagState) {
	r.group.Go(func() error {
		err := r.server.tagUpdater.UpdateTag(
			r.context,
			r.namespace,
			rootTag.key,
			rootTag.signedValue,
			rootLease,
		)

		r.lock.Lock()
		r.queueFinalizeTagLocked(rootTag.rootReferenceIndex, status.Convert(err).Proto())
		r.lock.Unlock()
		return nil
	})
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
			r.finalizeObjectLocked(o, lease, status.Convert(err).Proto())
		}
		r.lock.Unlock()
		return nil
	})
}

// processOutgoingMessages sends RequestObjectContents, FinalizeObject,
// and FinalizeTag messages to the client.
func (r *dagReceiver[TLease]) processOutgoingMessages() error {
	for {
		// Wait for one or more UploadDagsResponse messages be sent.
		r.lock.Lock()
		for r.firstFinalizedObject == nil && r.firstFinalizedTag == nil && r.firstRequestedObject == nil {
			if r.gracefulShutdown && r.unfinalizedDAGsCount == 0 {
				r.lock.Unlock()
				return nil
			}
			if err := r.outgoingMessagesWakeup.Wait(r.context, &r.lock); err != nil {
				return err
			}
		}

		// The order of preference in which we send messages is
		// chosen intentionally, so that resources are released
		// prior to acquiring them.
		if r.firstFinalizedObject != nil {
			// There are one or more objects that were
			// written to storage, or were already
			// determined to exist. Send a FinalizeObject
			// message to the client.
			o := r.firstFinalizedObject
			r.firstFinalizedObject = o.nextRequestedOrFinalizedObject
			if r.firstFinalizedObject == nil {
				r.lastFinalizedObject = &r.firstFinalizedObject
			}
			o.nextRequestedOrFinalizedObject = nil

			// Detach the existing FinalizeObject message.
			// This ensures that if we see this object being
			// referenced later on, we release those
			// reference indices as well by sending
			// additional FinalizeObject messages.
			finalizeObject := o.finalizeObject
			o.finalizeObject = nil
			if o.unfinalized == nil {
				r.detachObjectState(o)
			}

			// If this object is the root of a DAG for which
			// no tag was provided, this concludes the
			// transmission. Allow the transmission of
			// additional DAGs to be initiated.
			if r.unfinalizedDAGsCount < o.unfinalizedDAGsWithoutTagsCount {
				panic("invalid unfinalized DAGs count")
			}
			r.unfinalizedDAGsCount -= o.unfinalizedDAGsWithoutTagsCount
			r.lock.Unlock()

			if err := r.stream.Send(&dag.UploadDagsResponse{
				Type: &dag.UploadDagsResponse_FinalizeObject_{
					FinalizeObject: finalizeObject,
				},
			}); err != nil {
				return err
			}
		} else if r.firstFinalizedTag != nil {
			// There is a tag that has just been written to
			// storage. Send a FinalizeTag message to the
			// client.
			//
			// Make sure that this is only done after all
			// FinalizeObject are messages are sent, so that
			// the client only receives FinalizeTag after it
			// has purged all object state belonging to that
			// DAG.
			d := r.firstFinalizedTag
			r.firstFinalizedTag = d.nextFinalizedTag
			if r.firstFinalizedTag == nil {
				r.lastFinalizedTag = &r.firstFinalizedTag
			}
			d.nextFinalizedTag = nil

			if r.unfinalizedDAGsCount == 0 {
				panic("invalid unfinalized DAGs count")
			}
			r.unfinalizedDAGsCount--
			r.lock.Unlock()

			if err := r.stream.Send(&dag.UploadDagsResponse{
				Type: &dag.UploadDagsResponse_FinalizeTag_{
					FinalizeTag: &d.finalization,
				},
			}); err != nil {
				return err
			}
		} else {
			// There are one or more objects for which we've
			// determined they are absent. Send a
			// RequestObjectContents message to the client.
			o := r.firstRequestedObject
			r.firstRequestedObject = o.nextRequestedOrFinalizedObject
			if r.firstRequestedObject == nil {
				r.lastRequestedObject = &r.firstRequestedObject
			}
			if r.requestedObjectCount == 0 {
				panic("invalid requested object count")
			}
			r.requestedObjectCount--
			o.nextRequestedOrFinalizedObject = nil

			lowestReferenceIndex := o.finalizeObject.LowestReferenceIndex
			r.requestedObjects[lowestReferenceIndex] = o
			r.lock.Unlock()

			if err := r.stream.Send(&dag.UploadDagsResponse{
				Type: &dag.UploadDagsResponse_RequestObjectContents_{
					RequestObjectContents: &dag.UploadDagsResponse_RequestObjectContents{
						LowestReferenceIndex: lowestReferenceIndex,
					},
				},
			}); err != nil {
				return err
			}
		}
	}
}
