package object

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/buildbarn/bb-storage/pkg/clock"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// WatermarkSource provides the watermark of a cluster of storage
// servers that needs to be attached to requests that are sent to these
// servers.
type WatermarkSource interface {
	// GetWatermarkForRequest returns the watermark to attach to an
	// outgoing request. Nil is returned if no watermark is known.
	// Implementations may use calls to this method to determine
	// whether the watermark needs to be refreshed, and may block
	// until it has been refreshed.
	GetWatermarkForRequest(ctx context.Context) (*timestamppb.Timestamp, error)
}

var _ WatermarkSource = (*Watermark)(nil)

// Watermark keeps track of the highest watermark of a cluster of
// storage servers that has been observed. All objects written to the
// cluster at or after the watermark are still present.
//
// Watermarks are computed by storage servers based on their own
// clocks. As the watermark never decreases, a single server whose
// clock is ahead could cause the watermark to remain in the future for
// an extended period of time, invalidating all leases. To limit the
// impact of clock skew, watermarks are capped to the current time.
// This remains safe, as all leases handed out are capped in the same
// way.
type Watermark struct {
	clock     clock.Clock
	unixNanos atomic.Int64
}

// NewWatermark creates a Watermark for which no value has been observed
// yet.
func NewWatermark(clock clock.Clock) *Watermark {
	return &Watermark{
		clock: clock,
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

// RaiseFromProto raises the watermark to a value contained in a
// Protobuf message. Absent or invalid values are ignored.
func (w *Watermark) RaiseFromProto(t *timestamppb.Timestamp) {
	if t != nil && t.CheckValid() == nil {
		w.Raise(t.AsTime())
	}
}

// Get the current value of the watermark. The zero time is returned if
// no watermark has been observed.
func (w *Watermark) Get() time.Time {
	if n := w.unixNanos.Load(); n != 0 {
		return time.Unix(0, n)
	}
	return time.Time{}
}

// ToProto converts the current value of the watermark to a Protobuf
// message, so that it can be sent to other servers. Nil is returned if
// no watermark has been observed.
func (w *Watermark) ToProto() *timestamppb.Timestamp {
	if n := w.unixNanos.Load(); n != 0 {
		return timestamppb.New(time.Unix(0, n))
	}
	return nil
}

// GetWatermarkForRequest returns the current value of the watermark,
// so that it can be attached to outgoing requests.
func (w *Watermark) GetWatermarkForRequest(ctx context.Context) (*timestamppb.Timestamp, error) {
	return w.ToProto(), nil
}
