package leaserenewing

import (
	"container/heap"
	"context"
	"fmt"
	"sync"
	"time"

	"bonanza.build/pkg/ds"
	"bonanza.build/pkg/storage/object"
	"bonanza.build/pkg/storage/tag"
	pg_sync "bonanza.build/pkg/sync"

	"github.com/buildbarn/bb-storage/pkg/util"

	"golang.org/x/sync/semaphore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Namespace in which references reside. This is used by the resolver to
// create references to objects to which the tag points.
type Namespace[TReference any] interface {
	WithLocalReference(localReference object.LocalReference) TReference
}

// Reference is a constraint for the types of references that are used
// by Resolver.
type Reference[T any] interface {
	comparable
	fmt.Stringer
	object.BasicReference

	WithLocalReference(localReference object.LocalReference) T
}

// Resolver is a decorator for tag.Resolver that attempts to reobtain
// leases for tags that reference objects for which the lease is
// expired or missing. This is done by traversing into the parts of the
// DAG for which leases are expired or about to expire. Upon success,
// the tag is updated to use the new lease.
//
// Traversal is managed centrally, meaning that concurrent calls to
// ResolveTag() for tags referencing overlapping parts of DAGs only
// renew leases of these parts once. The amount of memory used is
// bounded by a limit on the number and total size of parent objects
// that are being processed.
type Resolver[TNamespace Namespace[TReference], TReference Reference[TReference], TLease any] struct {
	tagStore                       tag.Store[TNamespace, TLease]
	objectUploader                 object.Uploader[TReference, TLease]
	objectStoreSemaphore           *semaphore.Weighted
	maximumUnfinalizedParentsLimit object.Limit

	// Fields protected by lock.
	lock                             sync.Mutex
	remainingUnfinalizedParentsLimit object.Limit
	objectsByReference               map[TReference]*objectState[TReference, TLease]
	pendingObjects                   pendingObjectsHeap[TReference, TLease]
	pendingObjectsWakeup             pg_sync.ConditionVariable
}

var _ tag.Resolver[object.Namespace] = (*Resolver[object.Namespace, object.GlobalReference, any])(nil)

// NewResolver creates a Resolver that decorates a provided tag.Store.
// ProcessSingleObject() needs to be called repeatedly to process the
// objects whose leases need to be renewed.
func NewResolver[TNamespace Namespace[TReference], TReference Reference[TReference], TLease any](
	tagStore tag.Store[TNamespace, TLease],
	objectUploader object.Uploader[TReference, TLease],
	objectStoreSemaphore *semaphore.Weighted,
	maximumUnfinalizedParentsLimit object.Limit,
) *Resolver[TNamespace, TReference, TLease] {
	return &Resolver[TNamespace, TReference, TLease]{
		tagStore:                       tagStore,
		objectUploader:                 objectUploader,
		objectStoreSemaphore:           objectStoreSemaphore,
		maximumUnfinalizedParentsLimit: maximumUnfinalizedParentsLimit,

		remainingUnfinalizedParentsLimit: maximumUnfinalizedParentsLimit,
		objectsByReference:               map[TReference]*objectState[TReference, TLease]{},
	}
}

// ResolveTag resolves a tag. If the tag's lease is expired or about to
// expire, the leases of the DAG to which it points are renewed.
func (r *Resolver[TNamespace, TReference, TLease]) ResolveTag(ctx context.Context, namespace TNamespace, key tag.Key, minimumTimestamp *time.Time) (tag.SignedValue, bool, error) {
	// No need to renew leases if the current lease is already
	// valid, or if the caller indicated that it doesn't care about
	// tags above a certain age.
	signedValue, complete, err := r.tagStore.ResolveTag(ctx, namespace, key, minimumTimestamp)
	if err != nil || complete || (minimumTimestamp != nil && signedValue.Value.Timestamp.Before(*minimumTimestamp)) {
		return signedValue, complete, err
	}

	// Tag has an expired lease. Attempt to obtain a new lease on
	// the root object referenced by the tag.
	localReference := signedValue.Value.Reference
	lease, complete, err := r.renewLease(ctx, namespace.WithLocalReference(localReference))
	if err != nil {
		var badSignedValue tag.SignedValue
		return badSignedValue, false, util.StatusWrapf(err, "Failed to obtain lease for object with reference %s", localReference)
	}
	if !complete {
		return signedValue, false, nil
	}

	// Update the tag to use the new lease. This permits lookups for
	// the same tag in the nearby future to go faster.
	if err := r.tagStore.UpdateTag(ctx, namespace, key, signedValue, lease); err != nil {
		var badSignedValue tag.SignedValue
		return badSignedValue, false, util.StatusWrapf(err, "Failed to update tag with lease for object with reference %s", localReference)
	}
	return signedValue, true, nil
}

// renewLease obtains a lease for an object, renewing the leases of
// objects underneath it if needed. If the object or any of the objects
// underneath it are absent, complete is set to false.
func (r *Resolver[TNamespace, TReference, TLease]) renewLease(ctx context.Context, reference TReference) (lease TLease, complete bool, err error) {
	localReference := reference.GetLocalReference()
	if localReference.GetDegree() == 0 {
		// Leaf objects have no leases of children that need
		// to be renewed.
		if err := util.AcquireSemaphore(ctx, r.objectStoreSemaphore, 1); err != nil {
			return lease, false, err
		}
		result, err := r.objectUploader.UploadObject(ctx, reference, nil, nil, false)
		r.objectStoreSemaphore.Release(1)
		if err != nil {
			return lease, false, err
		}
		switch resultType := result.(type) {
		case object.UploadObjectComplete[TLease]:
			return resultType.Lease, true, nil
		case object.UploadObjectMissing[TLease]:
			return lease, false, nil
		default:
			return lease, false, status.Errorf(codes.Internal, "Storage reported leaf object with reference %s as being incomplete", reference)
		}
	}

	// Deny requests for objects that are too big to fit within
	// the limit. getPendingObject() would not be willing to
	// dequeue them.
	if !r.maximumUnfinalizedParentsLimit.CanAcquireParentAndChildren(localReference) {
		return lease, false, status.Error(codes.InvalidArgument, "Height or maximum total parents size of the object exceeds the configured limit")
	}

	// Enqueue the object for lease renewal, or join an existing
	// attempt to renew its lease.
	r.lock.Lock()
	o := r.getOrCreateObjectStateLocked(reference)
	if o.hasCallers == nil {
		o.hasCallers = &hasCallersState[TLease]{
			done: make(chan struct{}),
		}
	}
	hasCallers := o.hasCallers
	r.lock.Unlock()

	select {
	case <-hasCallers.done:
		return hasCallers.lease, hasCallers.complete, hasCallers.err
	case <-ctx.Done():
		// Renewing continues in the background, so that
		// subsequent calls may still benefit from it.
		return lease, false, util.StatusFromContext(ctx)
	}
}

// getOrCreateObjectStateLocked looks up the state that is tracked for a
// single object whose lease is being renewed. If no state exists, it is
// created and the object is queued for processing.
func (r *Resolver[TNamespace, TReference, TLease]) getOrCreateObjectStateLocked(reference TReference) *objectState[TReference, TLease] {
	o, ok := r.objectsByReference[reference]
	if !ok {
		o = &objectState[TReference, TLease]{
			reference: reference,
		}
		r.objectsByReference[reference] = o
		heap.Push(&r.pendingObjects, o)
		r.pendingObjectsWakeup.Broadcast()
	}
	return o
}

// detachObjectStateLocked removes the object state from the map of
// active objects. This function is either called when the object is
// finalized, or if an error has occurred that prevents us from renewing
// its lease successfully. In the latter case, subsequent calls start a
// new attempt.
func (r *Resolver[TNamespace, TReference, TLease]) detachObjectStateLocked(o *objectState[TReference, TLease]) {
	if r.objectsByReference[o.reference] == o {
		delete(r.objectsByReference, o.reference)
	}
}

// getPendingObject returns the next object for which leases need to be
// renewed. Objects are only dequeued if doing so doesn't cause the
// limit on the number and total size of parent objects to be exceeded.
func (r *Resolver[TNamespace, TReference, TLease]) getPendingObject(ctx context.Context) (*objectState[TReference, TLease], error) {
	r.lock.Lock()
	for {
		if len(r.pendingObjects.Slice) > 0 && r.remainingUnfinalizedParentsLimit.AcquireParentAndChildren(r.pendingObjects.Slice[0].reference.GetLocalReference()) {
			o := heap.Pop(&r.pendingObjects).(*objectState[TReference, TLease])
			r.lock.Unlock()
			return o, nil
		}
		// Upon failure, Wait() returns with the lock released.
		if err := r.pendingObjectsWakeup.Wait(ctx, &r.lock); err != nil {
			return nil, err
		}
	}
}

// ProcessSingleObject needs to be invoked repeatedly to ensure that
// Resolver renews leases of objects that are queued. It returns false
// once the provided context is canceled.
func (r *Resolver[TNamespace, TReference, TLease]) ProcessSingleObject(ctx context.Context) bool {
	o, err := r.getPendingObject(ctx)
	if err != nil {
		return false
	}

	// Calls against storage are not associated with the context
	// of any of the callers, as multiple callers may depend on the
	// outcome.
	if err := util.AcquireSemaphore(context.Background(), r.objectStoreSemaphore, 1); err != nil {
		panic(err)
	}
	go func() {
		localReference := o.reference.GetLocalReference()
		result, err := r.objectUploader.UploadObject(
			context.Background(),
			o.reference,
			/* contents = */ nil,
			/* childrenLeases = */ nil,
			/* wantContentsIfIncomplete = */ localReference.GetDegree() > 0,
		)
		r.objectStoreSemaphore.Release(1)

		r.lock.Lock()
		if err == nil {
			switch resultType := result.(type) {
			case object.UploadObjectComplete[TLease]:
				r.finalizeObjectLocked(o, &resultType.Lease, nil)
			case object.UploadObjectIncomplete[TLease]:
				// The leases of one or more children
				// need to be renewed. Queue them. Only
				// retain the outgoing references of
				// the object, so that its contents can
				// be released.
				wantOutgoingReferencesLeases := resultType.WantOutgoingReferencesLeases
				outgoingReferences := resultType.Contents.DetachOutgoingReferences()
				o.hasUnfinalizedChildren = &hasUnfinalizedChildrenState[TLease]{
					leases:                   make([]TLease, localReference.GetDegree()),
					unfinalizedChildrenCount: len(wantOutgoingReferencesLeases),
				}
				for _, i := range wantOutgoingReferencesLeases {
					oChild := r.getOrCreateObjectStateLocked(o.reference.WithLocalReference(outgoingReferences.GetOutgoingReference(i)))
					oChild.unfinalizedParents = append(oChild.unfinalizedParents, unfinalizedParent[TReference, TLease]{
						object:     o,
						leaseIndex: i,
					})
				}
			case object.UploadObjectMissing[TLease]:
				r.finalizeObjectLocked(o, nil, nil)
			default:
				panic("unknown upload object result type")
			}
		} else {
			r.finalizeObjectLocked(o, nil, util.StatusWrapf(err, "Failed to obtain lease for object with reference %s", o.reference))
		}

		// Now that all children have been queued, release the
		// capacity that was reserved for them.
		r.remainingUnfinalizedParentsLimit.ReleaseChildren(localReference)
		r.pendingObjectsWakeup.Broadcast()
		r.lock.Unlock()
	}()
	return true
}

// finalizeObjectLocked is called when the lease of an object has been
// obtained, or when it has been determined that no valid lease can be
// obtained. A nil lease indicates that the object or one or more objects
// underneath it are absent.
func (r *Resolver[TNamespace, TReference, TLease]) finalizeObjectLocked(o *objectState[TReference, TLease], lease *TLease, err error) {
	r.detachObjectStateLocked(o)

	// Unblock callers of ResolveTag() waiting for this object.
	if hasCallers := o.hasCallers; hasCallers != nil {
		if lease != nil {
			hasCallers.lease = *lease
			hasCallers.complete = true
		}
		hasCallers.err = err
		close(hasCallers.done)
	}

	// Propagate the outcome to the parents.
	for _, unfinalizedParent := range o.unfinalizedParents {
		oParent := unfinalizedParent.object
		hasUnfinalizedChildren := oParent.hasUnfinalizedChildren
		if lease == nil {
			hasUnfinalizedChildren.missingChildrenCount++
		} else {
			hasUnfinalizedChildren.leases[unfinalizedParent.leaseIndex] = *lease
		}
		if err != nil && hasUnfinalizedChildren.childErr == nil {
			// Prevent subsequent callers from joining an
			// attempt that is going to fail.
			hasUnfinalizedChildren.childErr = err
			r.detachObjectStateLocked(oParent)
		}

		hasUnfinalizedChildren.unfinalizedChildrenCount--
		if hasUnfinalizedChildren.unfinalizedChildrenCount == 0 {
			oParent.hasUnfinalizedChildren = nil
			r.finalizeParentLocked(oParent, hasUnfinalizedChildren)
		}
	}

	r.remainingUnfinalizedParentsLimit.ReleaseParent(o.reference.GetLocalReference())
	r.pendingObjectsWakeup.Broadcast()
}

// finalizeParentLocked is called when an object is no longer waiting
// for the leases of any of its children to be renewed. Unless an error
// occurred, the object is updated to use the leases that were obtained.
func (r *Resolver[TNamespace, TReference, TLease]) finalizeParentLocked(o *objectState[TReference, TLease], hasUnfinalizedChildren *hasUnfinalizedChildrenState[TLease]) {
	if err := hasUnfinalizedChildren.childErr; err != nil {
		r.finalizeObjectLocked(o, nil, err)
		return
	}

	// Update the leases of the object. Leases of children that
	// were not renewed are left at the zero value, meaning that
	// the ones that are currently stored are retained.
	//
	// This is also done if one or more children are absent, as
	// this reduces the amount of work that needs to be performed
	// when retrying after the absent objects have been uploaded.
	go func() {
		if err := util.AcquireSemaphore(context.Background(), r.objectStoreSemaphore, 1); err != nil {
			panic(err)
		}
		result, err := r.objectUploader.UploadObject(
			context.Background(),
			o.reference,
			/* contents = */ nil,
			hasUnfinalizedChildren.leases,
			/* wantContentsIfIncomplete = */ false,
		)
		r.objectStoreSemaphore.Release(1)

		r.lock.Lock()
		defer r.lock.Unlock()
		if err != nil {
			r.finalizeObjectLocked(o, nil, util.StatusWrapf(err, "Failed to update leases for object with reference %s", o.reference))
			return
		}
		if hasUnfinalizedChildren.missingChildrenCount > 0 {
			// One or more objects underneath are absent.
			r.finalizeObjectLocked(o, nil, nil)
			return
		}
		switch resultType := result.(type) {
		case object.UploadObjectComplete[TLease]:
			r.finalizeObjectLocked(o, &resultType.Lease, nil)
		case object.UploadObjectIncomplete[TLease]:
			// The leases we obtained were not accepted, or
			// objects that were previously complete became
			// incomplete. This shouldn't happen under
			// normal conditions. Return a transient error,
			// so that the tag isn't erroneously reported as
			// being incomplete.
			r.finalizeObjectLocked(o, nil, status.Errorf(codes.Internal, "Leases of children of object with reference %s expired before renewing leases completed", o.reference))
		case object.UploadObjectMissing[TLease]:
			r.finalizeObjectLocked(o, nil, status.Errorf(codes.Internal, "Object with reference %s went missing before renewing of leases completed", o.reference))
		default:
			panic("unknown upload object result type")
		}
	}()
}

// objectState contains all of the state associated with an object
// whose lease is in the process of being renewed.
type objectState[TReference Reference[TReference], TLease any] struct {
	reference TReference

	// If set, one or more calls to ResolveTag() are waiting for
	// the lease of this object to be renewed.
	hasCallers *hasCallersState[TLease]

	// Objects that are waiting for the lease of this object to be
	// renewed.
	unfinalizedParents []unfinalizedParent[TReference, TLease]

	// If set, the object is waiting for the leases of one or more
	// of its children to be renewed.
	hasUnfinalizedChildren *hasUnfinalizedChildrenState[TLease]
}

// hasCallersState is attached to the state of an object that has one or
// more callers waiting for renewal of leases of that object to
// complete.
type hasCallersState[TLease any] struct {
	done     chan struct{}
	lease    TLease
	complete bool
	err      error
}

type unfinalizedParent[TReference Reference[TReference], TLease any] struct {
	object     *objectState[TReference, TLease]
	leaseIndex int
}

// hasUnfinalizedChildrenState contains all state for a single object
// that is waiting for the leases of one or more of its children to be
// renewed.
type hasUnfinalizedChildrenState[TLease any] struct {
	leases                   []TLease
	missingChildrenCount     int
	childErr                 error
	unfinalizedChildrenCount int
}

// pendingObjectsHeap is a binary heap of objects whose leases still
// need to be renewed. Objects are processed in order of increasing
// height, so that objects lower in the graph are given precedence.
// This ensures that the limit on the number and total size of parent
// objects can't be exhausted by objects higher up in the graph.
type pendingObjectsHeap[TReference Reference[TReference], TLease any] struct {
	ds.Slice[*objectState[TReference, TLease]]
}

func (h pendingObjectsHeap[TReference, TLease]) Less(i, j int) bool {
	ri, rj := h.Slice[i].reference.GetLocalReference(), h.Slice[j].reference.GetLocalReference()
	return ri.CompareByHeight(rj) < 0
}
