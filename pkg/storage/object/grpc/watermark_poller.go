package grpc

import (
	"context"
	"sync"
	"time"

	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/clock"
	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// WatermarkPoller obtains the watermarks of all storage servers in a
// replica, and keeps track of the highest one. Storage frontends attach
// it to requests that need to check leases, so that storage servers can
// check leases against the highest watermark of the replica, as opposed
// to just their own.
//
// Watermarks are only polled while requests that need them are being
// sent. This prevents the poller from generating any traffic while the
// cluster is idle. As the watermark may have changed significantly
// while polling was suspended (e.g., due to a storage server restarting
// uncleanly), requests never use a watermark that was obtained by a
// poll that started more than twice the polling interval ago. They
// wait for the watermarks to be polled again instead.
type WatermarkPoller struct {
	clients     []object_pb.UploaderClient
	clock       clock.Clock
	interval    time.Duration
	errorLogger util.ErrorLogger

	watermark *object.Watermark
	wakeup    chan struct{}

	lock sync.Mutex
	// Whether polling is suspended due to inactivity, meaning
	// that the next request needs to wake up Run().
	suspended bool
	// Whether the watermark was requested since polling was last
	// performed.
	used bool
	// The time at which the last completed poll started.
	lastPollStart time.Time
	// Closed once the next poll completes.
	nextPollCompleted chan struct{}
}

var _ object.WatermarkSource = (*WatermarkPoller)(nil)

// NewWatermarkPoller creates a WatermarkPoller that polls the
// watermarks of the provided storage servers. Run() needs to be called
// to perform the actual polling.
func NewWatermarkPoller(clients []object_pb.UploaderClient, clock clock.Clock, interval time.Duration, errorLogger util.ErrorLogger) *WatermarkPoller {
	return &WatermarkPoller{
		clients:     clients,
		clock:       clock,
		interval:    interval,
		errorLogger: errorLogger,

		watermark:         object.NewWatermark(clock),
		wakeup:            make(chan struct{}, 1),
		suspended:         true,
		nextPollCompleted: make(chan struct{}),
	}
}

// GetWatermarkForRequest returns the highest watermark of the replica
// that is known, so that it can be attached to an outgoing request. It
// causes polling to resume if it was suspended due to inactivity. If
// the known watermark is outdated, it waits for the watermarks to be
// polled again.
func (p *WatermarkPoller) GetWatermarkForRequest(ctx context.Context) (*timestamppb.Timestamp, error) {
	p.lock.Lock()
	p.used = true
	if p.suspended {
		p.suspended = false
		p.wakeup <- struct{}{}
	}
	if !p.lastPollStart.IsZero() && !p.clock.Now().After(p.lastPollStart.Add(2*p.interval)) {
		p.lock.Unlock()
		return p.watermark.ToProto(), nil
	}
	nextPollCompleted := p.nextPollCompleted
	p.lock.Unlock()

	select {
	case <-nextPollCompleted:
		return p.watermark.ToProto(), nil
	case <-ctx.Done():
		return nil, util.StatusFromContext(ctx)
	}
}

// pollOnce obtains the watermarks of all storage servers in parallel.
func (p *WatermarkPoller) pollOnce(ctx context.Context) {
	var wg sync.WaitGroup
	for _, client := range p.clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if response, err := client.GetWatermark(ctx, &emptypb.Empty{}); err == nil {
				p.watermark.RaiseFromProto(response.Watermark)
			} else if ctx.Err() == nil {
				p.errorLogger.Log(util.StatusWrap(err, "Failed to obtain watermark"))
			}
		}()
	}
	wg.Wait()
}

// Run polls the watermarks of the storage servers for as long as
// requests are sent that need them. It returns when the context is
// cancelled.
func (p *WatermarkPoller) Run(ctx context.Context) {
	for {
		// Wait for a request to need the watermark.
		select {
		case <-ctx.Done():
			return
		case <-p.wakeup:
		}

		// Poll until an entire interval passes without any
		// requests needing the watermark.
		for {
			p.lock.Lock()
			p.used = false
			p.lock.Unlock()

			pollStart := p.clock.Now()
			p.pollOnce(ctx)

			// Unblock requests that are waiting for the
			// watermarks to be polled. This is also done if
			// polling failed, as requests are likely to
			// fail in that case as well.
			p.lock.Lock()
			p.lastPollStart = pollStart
			close(p.nextPollCompleted)
			p.nextPollCompleted = make(chan struct{})
			p.lock.Unlock()

			timer, t := p.clock.NewTimer(p.interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-t:
			}

			p.lock.Lock()
			if !p.used {
				p.suspended = true
				p.lock.Unlock()
				break
			}
			p.lock.Unlock()
		}
	}
}
