package leaserenewing

import (
	"container/heap"
	"context"
	"math/bits"
	"sync"

	"bonanza.build/pkg/ds"
	"bonanza.build/pkg/storage/object"
	pg_sync "bonanza.build/pkg/sync"

	"github.com/buildbarn/bb-storage/pkg/util"

	"golang.org/x/sync/semaphore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Reference is a constraint for the types of references that are used
// by Uploader.
type Reference[T any] interface {
	comparable
	object.BasicReference

	WithLocalReference(localReference object.LocalReference) T
}

// Uploader is a decorator for object.Uploader that intercepts results
// that indicate that an object is incomplete due to expired leases. For
// these objects, it recursively traverses all children until leases are
// complete.
type Uploader[TReference Reference[TReference], TLease any] struct {
	base                           object.Uploader[TReference, TLease]
	objectStoreSemaphore           *semaphore.Weighted
	maximumUnfinalizedParentsLimit object.Limit

	// Fields protected by lock.
	lock                             sync.Mutex
	remainingUnfinalizedParentsLimit object.Limit
	objectsByReference               map[TReference]*objectState[TReference, TLease]
	pendingObjects                   pendingObjectsHeap[TReference, TLease]
	pendingObjectsWakeup             pg_sync.ConditionVariable
}

// NewUploader creates a Uploader that dectorates a provided
// object.Uploader.
func NewUploader[TReference Reference[TReference], TLease any](base object.Uploader[TReference, TLease], objectStoreSemaphore *semaphore.Weighted, maximumUnfinalizedParentsLimit object.Limit) *Uploader[TReference, TLease] {
	u := &Uploader[TReference, TLease]{
		base:                           base,
		objectStoreSemaphore:           objectStoreSemaphore,
		maximumUnfinalizedParentsLimit: maximumUnfinalizedParentsLimit,

		remainingUnfinalizedParentsLimit: maximumUnfinalizedParentsLimit,
		objectsByReference:               map[TReference]*objectState[TReference, TLease]{},
	}
	return u
}

// getOrCreateObjectState looks up the state that is tracked by the
// lease renewing process for a single object. If no state exists, it is
// created.
func (u *Uploader[TReference, TLease]) getOrCreateObjectState(reference TReference) *objectState[TReference, TLease] {
	o, ok := u.objectsByReference[reference]
	if !ok {
		o = &objectState[TReference, TLease]{
			reference: reference,
		}
		u.objectsByReference[reference] = o
		heap.Push(&u.pendingObjects, o)
		u.pendingObjectsWakeup.Broadcast()
	}
	return o
}

// detachObjectState removes the object state from the map of active
// objects. This function is either called when the object is fully
// renewed, or if an error has occurred that prevents us from renewing
// it successfully.
func (u *Uploader[TReference, TLease]) detachObjectState(o *objectState[TReference, TLease]) {
	if u.objectsByReference[o.reference] == o {
		delete(u.objectsByReference, o.reference)
	}
}

// UploadObject uploads an object to storage. When called without any
// object contents or leases and the backend indicates that one or more
// leases are missing, it will automatically attempt to check existence
// of the object's children, so that the object's lease may be renewed.
func (u *Uploader[TReference, TLease]) UploadObject(ctx context.Context, reference TReference, contents *object.Contents, childrenLeases []TLease, wantContentsIfIncomplete bool) (object.UploadObjectResult[TLease], error) {
	// Only pick up requests that do nothing more than obtain leases
	// of an existing parent object. This makes it safe to use this
	// backend recursively.
	localReference := reference.GetLocalReference()
	if contents != nil || len(childrenLeases) > 0 || localReference.GetDegree() == 0 {
		return u.base.UploadObject(
			ctx,
			reference,
			contents,
			childrenLeases,
			wantContentsIfIncomplete,
		)
	}

	// Enqueue the object for lease renewal.
	if !u.maximumUnfinalizedParentsLimit.CanAcquireObjectAndChildren(localReference) {
		return nil, status.Error(codes.InvalidArgument, "Height or maximum total parents size of the object exceeds the configured limit")
	}

	u.lock.Lock()
	o := u.getOrCreateObjectState(reference)
	if o.hasCallers == nil {
		o.hasCallers = &hasCallersState[TLease]{
			done: make(chan struct{}),
		}
	}
	hasCallers := o.hasCallers
	u.lock.Unlock()

	// Wait for lease renewal of the object and all of its children
	// to complete.
	<-hasCallers.done
	if hasCallers.err == nil && !wantContentsIfIncomplete {
		if resultType, ok := hasCallers.result.(object.UploadObjectIncomplete[TLease]); ok {
			resultType.Contents = nil
			return resultType, nil
		}
	}
	return hasCallers.result, hasCallers.err
}

// getPendingObject returns the next object for which leases need to be
// renewed.
func (u *Uploader[TReference, TLease]) getPendingObject(ctx context.Context) (*objectState[TReference, TLease], error) {
	u.lock.Lock()
	for {
		if len(u.pendingObjects.Slice) > 0 {
			if u.remainingUnfinalizedParentsLimit.AcquireObjectAndChildren(u.pendingObjects.Slice[0].reference.GetLocalReference()) {
				defer u.lock.Unlock()
				return heap.Pop(&u.pendingObjects).(*objectState[TReference, TLease]), nil
			}
		}
		if err := u.pendingObjectsWakeup.Wait(ctx, &u.lock); err != nil {
			return nil, err
		}
	}
}

// finalizeObjectLocked is called when leases of an object have finished
// rewing, or after an error occurred in the process.
func (u *Uploader[TReference, TLease]) finalizeObjectLocked(o *objectState[TReference, TLease], lease *TLease, result object.UploadObjectResult[TLease], err error) {
	u.detachObjectState(o)

	// If this object has one or more callers of UploadObject(),
	// unblock them.
	if w := o.hasCallers; w != nil {
		w.result = result
		w.err = err
		close(w.done)
	}

	for _, unfinalizedParent := range o.unfinalizedParents {
		// Propagate the lease of the object to the parents, so
		// that they can provide it to UploadObject().
		oParent := unfinalizedParent.object
		hasUnfinalizedChildren := oParent.hasUnfinalizedChildren
		if leaseIndex := unfinalizedParent.leaseIndex; lease == nil {
			hasUnfinalizedChildren.gotNoLease[leaseIndex/bits.UintSize] |= 1 << (leaseIndex % bits.UintSize)
		} else {
			hasUnfinalizedChildren.leases[leaseIndex] = *lease
			hasUnfinalizedChildren.originalIncompleteResult = nil
		}

		// Propagate any errors to the parent.
		if err != nil && hasUnfinalizedChildren.childErr == nil {
			hasUnfinalizedChildren.childErr = err
			u.detachObjectState(oParent)
		}

		// If finalizing this object means that the parent
		// object is no longer waiting for any children to have
		// their leases renewed, update the parent.
		hasUnfinalizedChildren.unfinalizedChildrenCount--
		if hasUnfinalizedChildren.unfinalizedChildrenCount == 0 {
			oParent.hasUnfinalizedChildren = nil

			// Parent is no longer waiting for any leases.
			if originalResult := hasUnfinalizedChildren.originalIncompleteResult; originalResult != nil {
				// We did not obtain any new leases for
				// the parent object's children. Simply
				// return the original UploadObject()
				// response.
				u.finalizeObjectLocked(oParent, nil, *originalResult, hasUnfinalizedChildren.childErr)
			} else {
				// We've successfully obtained one or more
				// leases of the children. Call UploadObject()
				// against the parent to update its leases.
				//
				// We do this even if an error occurred, as
				// this reduces the amount of work that needs
				// to be performed during retries.
				u.lock.Unlock()

				if err := util.AcquireSemaphore(context.Background(), u.objectStoreSemaphore, 1); err != nil {
					panic(err)
				}
				go func() {
					result, err := u.base.UploadObject(
						context.Background(),
						oParent.reference,
						/* contents = */ nil,
						hasUnfinalizedChildren.leases,
						/* wantContentsIfIncomplete = */ false,
					)
					u.objectStoreSemaphore.Release(1)
					if hasUnfinalizedChildren.childErr != nil {
						err = hasUnfinalizedChildren.childErr
					} else if err != nil {
						err = util.StatusWrapf(err, "Failed to update leases for object with reference %s", oParent.reference)
					}

					u.lock.Lock()
					if err == nil {
						switch resultType := result.(type) {
						case object.UploadObjectComplete[TLease]:
							u.finalizeObjectLocked(oParent, &resultType.Lease, result, nil)
						case object.UploadObjectIncomplete[TLease]:
							u.finalizeObjectIncompleteLocked(oParent, resultType.WantOutgoingReferencesLeases, hasUnfinalizedChildren)
						case object.UploadObjectMissing[TLease]:
							u.finalizeObjectLocked(
								oParent,
								nil,
								nil,
								status.Errorf(
									codes.Internal,
									"Object with reference %s went missing before renewing of leases completed",
									oParent.reference,
								),
							)
						default:
							panic("unknown upload object result type")
						}
					} else {
						u.finalizeObjectLocked(oParent, nil, nil, err)
					}
					u.lock.Unlock()
				}()

				u.lock.Lock()
			}
		}
	}

	u.remainingUnfinalizedParentsLimit.ReleaseObject(o.reference.GetLocalReference())
	u.pendingObjectsWakeup.Broadcast()
}

// finalizeObjectIncompleteLocked is called when a second call to
// UploadObject() against an object is made, and just like the first one
// it reports that the object is incomplete. This can mean one of two
// things:
//
//   - We didn't manage to obtain leases for all of its children, due to
//     objects simply being missing. This is fine, and should cause us
//     to report the object as being incomplete.
//
//   - The storage backend didn't accept our leases, or objects that
//     were previously complete became incomplete. This shouldn't happen
//     under normal conditions. Return a transient error, so that a
//     client calling UploadDag() doesn't erroneously see this object as
//     being missing.
func (u *Uploader[TReference, TLease]) finalizeObjectIncompleteLocked(
	o *objectState[TReference, TLease],
	wantOutgoingReferencesLeases []int,
	hasUnfinalizedChildren *hasUnfinalizedChildrenState[TReference, TLease],
) {
	contents := hasUnfinalizedChildren.contents
	for _, i := range wantOutgoingReferencesLeases {
		if hasUnfinalizedChildren.gotNoLease[i/bits.UintSize]&(1<<(i%bits.UintSize)) == 0 {
			u.finalizeObjectLocked(
				o,
				nil,
				nil,
				status.Errorf(
					codes.Internal,
					"Lease of object with reference %s expired before renewing leases of object with reference %s completed",
					contents.GetOutgoingReference(i),
					o.reference,
				),
			)
			return
		}
	}

	u.finalizeObjectLocked(
		o,
		nil,
		object.UploadObjectIncomplete[TLease]{
			Contents:                     hasUnfinalizedChildren.contents,
			WantOutgoingReferencesLeases: wantOutgoingReferencesLeases,
		},
		nil,
	)
}

// ProcessSingleObject needs to be invoked repeatedly to ensure that
// Uploader renews leases for objects that are queued.
func (u *Uploader[TReference, TLease]) ProcessSingleObject(ctx context.Context) bool {
	o, err := u.getPendingObject(ctx)
	if err != nil {
		return false
	}

	if err := util.AcquireSemaphore(context.Background(), u.objectStoreSemaphore, 1); err != nil {
		panic(err)
	}
	go func() {
		result, err := u.base.UploadObject(
			context.Background(),
			o.reference,
			/* contents = */ nil,
			/* childrenLeases = */ nil,
			/* wantContentsIfIncomplete = */ o.reference.GetDegree() > 0,
		)
		u.objectStoreSemaphore.Release(1)

		localReference := o.reference.GetLocalReference()
		u.lock.Lock()
		if err == nil {
			switch resultType := result.(type) {
			case object.UploadObjectComplete[TLease]:
				u.finalizeObjectLocked(o, &resultType.Lease, result, nil)
			case object.UploadObjectIncomplete[TLease]:
				// The object has one or more outgoing
				// references for which leases need to
				// be renewed. Queue the children having
				// the expired leases.
				contents := resultType.Contents
				for _, i := range resultType.WantOutgoingReferencesLeases {
					oChild := u.getOrCreateObjectState(o.reference.WithLocalReference(contents.GetOutgoingReference(i)))
					oChild.unfinalizedParents = append(oChild.unfinalizedParents, unfinalizedParent[TReference, TLease]{
						object:     o,
						leaseIndex: i,
					})
				}
				degree := localReference.GetDegree()
				o.hasUnfinalizedChildren = &hasUnfinalizedChildrenState[TReference, TLease]{
					contents:                 contents,
					originalIncompleteResult: &resultType,
					leases:                   make([]TLease, degree),
					gotNoLease:               make([]uint, (degree+bits.UintSize-1)/bits.UintSize),
					unfinalizedChildrenCount: len(resultType.WantOutgoingReferencesLeases),
				}
			case object.UploadObjectMissing[TLease]:
				u.finalizeObjectLocked(o, nil, result, nil)
			default:
				panic("unknown upload object result type")
			}
		} else {
			u.finalizeObjectLocked(o, nil, nil, util.StatusWrapf(err, "Failed to obtain lease for object with reference %s", o.reference))
		}

		u.remainingUnfinalizedParentsLimit.ReleaseChildren(localReference)
		u.pendingObjectsWakeup.Broadcast()
		u.lock.Unlock()
	}()
	return true
}

// objectState contains all of the state associated with an object that
// is in the process of having its leases requested and/or renewed.
type objectState[TReference Reference[TReference], TLease any] struct {
	reference TReference

	// If set, one or more UploadObject() calls exist that match the
	// provided reference.
	hasCallers *hasCallersState[TLease]

	// If set, the object has one or more objects that are in the
	// process of having its leases renewed.
	unfinalizedParents []unfinalizedParent[TReference, TLease]

	// If set, the object is incomplete and is waiting until the
	// leases of its children have been obtained.
	hasUnfinalizedChildren *hasUnfinalizedChildrenState[TReference, TLease]
}

// hasCallersState is attached to the state of an object that has one or
// more callers waiting for renewal of leases of that object to
// complete.
type hasCallersState[TLease any] struct {
	done   chan struct{}
	result object.UploadObjectResult[TLease]
	err    error
}

type unfinalizedParent[TReference Reference[TReference], TLease any] struct {
	object     *objectState[TReference, TLease]
	leaseIndex int
}

// hasUnfinalizedChildrenState contains all state for a single object
// that is incomplete, in the process of having its leases renewed, and
// is waiting to obtain leases for all of its children.
type hasUnfinalizedChildrenState[TReference Reference[TReference], TLease any] struct {
	contents                 *object.Contents
	originalIncompleteResult *object.UploadObjectIncomplete[TLease]
	leases                   []TLease
	gotNoLease               []uint
	childErr                 error
	unfinalizedChildrenCount int
}

// pendingObjectsHeap is a binary heap of objects whose leases still
// need to be obtained.
type pendingObjectsHeap[TReference Reference[TReference], TLease any] struct {
	ds.Slice[*objectState[TReference, TLease]]
}

func (h pendingObjectsHeap[TReference, TLease]) Less(i, j int) bool {
	ri, rj := h.Slice[i].reference.GetLocalReference(), h.Slice[j].reference.GetLocalReference()
	return ri.CompareByHeight(rj) < 0
}
