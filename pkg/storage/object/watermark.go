package object

import (
	"context"
	"sync/atomic"
	"time"

	object_pb "bonanza.build/pkg/proto/storage/object"

	"github.com/buildbarn/bb-storage/pkg/clock"
	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// WatermarkSource provides the watermark of a cluster of storage
// servers that needs to be attached to requests that are sent to these
// servers.
type WatermarkSource interface {
	// GetWatermarkForRequest returns the watermark to attach to an
	// outgoing request. Implementations may use calls to this
	// method to determine whether the watermark needs to be
	// refreshed, and may block until it has been refreshed.
	GetWatermarkForRequest(ctx context.Context) (*object_pb.Watermark, error)
}

// Watermark keeps track of the highest watermark of a cluster of
// storage servers that has been observed by a storage server. All
// objects written to the cluster at or after the watermark are still
// present.
//
// Watermarks are computed by storage servers based on their own
// clocks. As the watermark never decreases, a single server whose
// clock is ahead could cause the watermark to remain in the future for
// an extended period of time, invalidating all leases. To limit the
// impact of clock skew, watermarks are capped to the current time.
type Watermark struct {
	clock      clock.Clock
	maximumAge time.Duration
	unixNanos  atomic.Int64
}

// NewWatermark creates a Watermark for which no value has been observed
// yet. Watermarks provided by storage frontends that were obtained more
// than maximumAge ago are rejected, as data may have been lost in the
// meantime.
func NewWatermark(clock clock.Clock, maximumAge time.Duration) *Watermark {
	return &Watermark{
		clock:      clock,
		maximumAge: maximumAge,
	}
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

// RaiseFromProto raises the watermark to a value provided by a storage
// frontend. If no value is provided, the watermark is left unchanged.
// An error is returned if the value is invalid, or if it was obtained
// too long ago.
func (w *Watermark) RaiseFromProto(m *object_pb.Watermark) error {
	if m == nil {
		return nil
	}

	if err := m.ObtainedAt.CheckValid(); err != nil {
		return util.StatusWrapWithCode(err, codes.InvalidArgument, "Invalid time at which the watermark was obtained")
	}
	if age := w.clock.Now().Sub(m.ObtainedAt.AsTime()); age > w.maximumAge {
		return status.Errorf(codes.Unavailable, "Watermark of the cluster was obtained %s ago, which exceeds the maximum permitted age of %s, meaning data may have been lost in the meantime", age, w.maximumAge)
	}

	if m.Watermark != nil {
		if err := m.Watermark.CheckValid(); err != nil {
			return util.StatusWrapWithCode(err, codes.InvalidArgument, "Invalid watermark")
		}
		w.Raise(m.Watermark.AsTime())
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
