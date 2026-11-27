package object

import (
	"bytes"
	"context"
	"sync"
	"sync/atomic"
	"time"

	object_pb "bonanza.build/pkg/proto/storage/object"

	"github.com/buildbarn/bb-storage/pkg/clock"
	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// LeaseValidationContextSource provides the information that storage
// servers need to validate leases, which needs to be attached to
// requests that are sent to a single storage server.
type LeaseValidationContextSource interface {
	// GetLeaseValidationContext returns the lease validation
	// context to attach to an outgoing request. Implementations may
	// use calls to this method to determine whether the information
	// contained in it needs to be refreshed, and may block until it
	// has been refreshed.
	GetLeaseValidationContext(ctx context.Context) (*object_pb.LeaseValidationContext, error)

	// ReportIncarnationMismatch is called when a storage server
	// rejected a request with FAILED_PRECONDITION, because the
	// incarnation of the storage server did not match the one
	// contained in the lease validation context. Implementations
	// should refresh the incarnation of the storage server before
	// returning another lease validation context.
	ReportIncarnationMismatch(leaseValidationContext *object_pb.LeaseValidationContext)
}

// maximumIncarnationMismatchRetries is the number of times requests
// are retried after storage servers report that their incarnation does
// not match the one that was expected.
const maximumIncarnationMismatchRetries = 3

// CallWithLeaseValidationContext calls a function that sends a request
// to a storage server, providing it a lease validation context obtained
// from a LeaseValidationContextSource. If the storage server rejects
// the request due to its incarnation not matching the one that was
// expected, the incarnation is refreshed and the request is retried.
//
// If no LeaseValidationContextSource is provided, the function is
// called once without a lease validation context.
func CallWithLeaseValidationContext[T any](ctx context.Context, source LeaseValidationContextSource, call func(*object_pb.LeaseValidationContext) (T, error)) (T, error) {
	if source == nil {
		return call(nil)
	}
	for attempt := 0; ; attempt++ {
		leaseValidationContext, err := source.GetLeaseValidationContext(ctx)
		if err != nil {
			var bad T
			return bad, util.StatusWrap(err, "Failed to obtain lease validation context")
		}
		result, err := call(leaseValidationContext)
		if status.Code(err) != codes.FailedPrecondition || attempt >= maximumIncarnationMismatchRetries {
			return result, err
		}
		source.ReportIncarnationMismatch(leaseValidationContext)
	}
}

// Watermark keeps track of the highest watermark of a cluster of
// storage servers that has been observed by a storage server. All
// objects written to the cluster at or after the watermark are still
// present.
//
// In addition to that, it validates that requests are intended for the
// current incarnation of the storage server, and invalidates the leases
// of all objects stored by the storage server whenever the incarnations
// hash provided by storage frontends changes. This accounts for objects
// that may have been lost due to storage servers in the cluster being
// replaced, or the layout of the cluster's key space being changed.
//
// Leases are invalidated by raising the storage server's own watermark,
// as opposed to the watermark of the cluster. Storage servers observe
// changes to the incarnations hash at slightly different points in
// time, causing them to raise the watermark to different values. As
// raising the watermark to the current time leaves no margin for
// renewing leases, storage servers would disagree on whether recently
// renewed leases are valid. The storage server's own watermark is
// reported through GetWatermark(), causing storage frontends to
// propagate the highest value to all storage servers.
//
// Watermarks are computed by storage servers based on their own
// clocks. As the watermark never decreases, a single server whose
// clock is ahead could cause the watermark to remain in the future for
// an extended period of time, invalidating all leases. To limit the
// impact of clock skew, watermarks are capped to the current time.
type Watermark struct {
	clock       clock.Clock
	maximumAge  time.Duration
	incarnation []byte
	unixNanos   atomic.Int64

	invalidateLeases func()

	// The last incarnations hash received. It is only updated after
	// leases have been invalidated, so that observing the new value
	// implies that invalidation has completed. It can be read
	// without acquiring invalidationLock, as invalidateLeases may
	// acquire locks that are also held while the value is read for
	// persisting it.
	lastIncarnationsHash atomic.Pointer[[]byte]

	// Serializes invalidation of leases when the incarnations hash
	// changes.
	invalidationLock sync.Mutex
}

// NewWatermark creates a Watermark for which no value has been observed
// yet. Watermarks provided by storage frontends that were obtained more
// than maximumAge ago are rejected, as data may have been lost in the
// meantime.
//
// The incarnation is the one of the storage server itself.
// lastIncarnationsHash is the last incarnations hash that was received
// by the storage server prior to restarting, if any. invalidateLeases
// is called whenever the incarnations hash changes. It must raise the
// watermark of the storage server itself to the current time.
func NewWatermark(clock clock.Clock, maximumAge time.Duration, incarnation, lastIncarnationsHash []byte, invalidateLeases func()) *Watermark {
	w := &Watermark{
		clock:            clock,
		maximumAge:       maximumAge,
		incarnation:      incarnation,
		invalidateLeases: invalidateLeases,
	}
	w.lastIncarnationsHash.Store(&lastIncarnationsHash)
	return w
}

// GetIncarnation returns the incarnation of the storage server.
func (w *Watermark) GetIncarnation() []byte {
	return w.incarnation
}

// GetLastIncarnationsHash returns the last incarnations hash that was
// received by the storage server, so that it can be persisted. It is
// permitted to call this method on a nil Watermark, in which case nil
// is returned.
func (w *Watermark) GetLastIncarnationsHash() []byte {
	if w == nil {
		return nil
	}
	return *w.lastIncarnationsHash.Load()
}

// Raise the watermark to a given value. Values lower than the current
// watermark are ignored, meaning that the watermark never decreases.
// Values in the future are capped to the current time.
func (w *Watermark) Raise(t time.Time) {
	if t.IsZero() {
		return
	}
	if now := w.clock.Now(); t.After(now) {
		t = now
	}
	n := t.UnixNano()
	for {
		current := w.unixNanos.Load()
		if n <= current || w.unixNanos.CompareAndSwap(current, n) {
			return
		}
	}
}

// ProcessLeaseValidationContext processes the lease validation context
// that a storage frontend provided as part of a request. It must be
// called before any leases are handed out, stored or checked as part of
// processing the request.
//
// An error is returned if the request is intended for another
// incarnation of the storage server, if the watermark is invalid, or if
// it was obtained too long ago. Otherwise, the watermark is raised to
// the value provided. If the incarnations hash differs from the one
// received previously, the leases of all objects stored by the storage
// server are invalidated.
func (w *Watermark) ProcessLeaseValidationContext(m *object_pb.LeaseValidationContext) error {
	if m == nil {
		return status.Error(codes.InvalidArgument, "No lease validation context provided")
	}
	if !bytes.Equal(m.GetExpectedIncarnation(), w.incarnation) {
		return status.Errorf(codes.FailedPrecondition, "Request was intended for incarnation %x of the storage server, while its current incarnation is %x", m.GetExpectedIncarnation(), w.incarnation)
	}

	watermark := m.GetWatermark()
	if err := watermark.GetObtainedAt().CheckValid(); err != nil {
		return util.StatusWrapWithCode(err, codes.InvalidArgument, "Invalid time at which the watermark was obtained")
	}
	now := w.clock.Now()
	if age := now.Sub(watermark.GetObtainedAt().AsTime()); age > w.maximumAge {
		return status.Errorf(codes.Unavailable, "Watermark of the cluster was obtained %s ago, which exceeds the maximum permitted age of %s, meaning data may have been lost in the meantime", age, w.maximumAge)
	}
	if watermarkWatermark := watermark.GetWatermark(); watermarkWatermark != nil {
		if err := watermarkWatermark.CheckValid(); err != nil {
			return util.StatusWrapWithCode(err, codes.InvalidArgument, "Invalid watermark")
		}
	}

	// If the incarnation of one of the storage servers in the
	// cluster or the layout of the key space changed, any of the
	// existing leases may be invalid. Invalidate them while holding
	// a lock, so that concurrent requests providing the same
	// incarnations hash don't proceed before this has completed.
	// Only update the last incarnations hash afterwards, so that
	// requests observing the new value without acquiring the lock
	// can proceed safely.
	if !bytes.Equal(m.GetIncarnationsHash(), *w.lastIncarnationsHash.Load()) {
		w.invalidationLock.Lock()
		if !bytes.Equal(m.GetIncarnationsHash(), *w.lastIncarnationsHash.Load()) {
			w.invalidateLeases()
			newIncarnationsHash := bytes.Clone(m.GetIncarnationsHash())
			w.lastIncarnationsHash.Store(&newIncarnationsHash)
		}
		w.invalidationLock.Unlock()
	}

	if watermark2 := watermark.GetWatermark(); watermark2 != nil {
		w.Raise(watermark2.AsTime())
	}
	return nil
}

// Get the current value of the watermark. The zero time is returned if
// no watermark has been observed.
func (w *Watermark) Get() time.Time {
	if n := w.unixNanos.Load(); n != 0 {
		return time.Unix(0, n)
	}
	return time.Time{}
}
