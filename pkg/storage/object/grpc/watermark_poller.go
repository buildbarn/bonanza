package grpc

import (
	"context"
	"sync"
	"time"

	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/clock"
	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// WatermarkPoller obtains the watermarks of all storage servers in a
// replica, and keeps track of the highest one. Storage frontends attach
// it to requests that need to check leases, so that storage servers can
// check leases against the highest watermark of the replica, as opposed
// to just their own.
//
// The watermark of the replica also carries the time at which the
// oldest of the watermarks of the storage servers was obtained, so that
// storage servers can reject requests if the watermark of one of the
// storage servers could not be obtained for an extended period of time.
// All of these timestamps are computed by storage servers. The clock of
// the storage frontend is only used to determine when to poll.
//
// Watermarks are only polled while requests that need them are being
// sent. This prevents the poller from generating any traffic while the
// cluster is idle. As the watermark may have changed significantly
// while polling was suspended (e.g., due to a storage server restarting
// uncleanly), requests never use watermarks that were requested more
// than twice the polling interval ago. They wait for the watermarks to
// be polled again instead.
type WatermarkPoller struct {
	clients     []object_pb.UploaderClient
	clock       clock.Clock
	interval    time.Duration
	errorLogger util.ErrorLogger

	wakeup chan struct{}

	lock sync.Mutex
	// The latest watermarks obtained from each storage server.
	shards []shardWatermark
	// Whether polling is suspended due to inactivity, meaning
	// that the next request needs to wake up Run().
	suspended bool
	// Whether the watermark was requested since polling was last
	// performed.
	used bool
	// Closed once the next poll completes.
	nextPollCompleted chan struct{}
}

// shardWatermark contains the latest watermark that was obtained from a
// single storage server.
type shardWatermark struct {
	// The time at which the watermark was requested. Zero if no
	// watermark has been obtained yet.
	requestedAt time.Time
	// The watermark reported by the storage server, or nil if the
	// storage server has not lost any data yet.
	watermark *timestamppb.Timestamp
	// The time at which the watermark was obtained, according to
	// the clock of the storage server.
	obtainedAt *timestamppb.Timestamp
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

		wakeup:            make(chan struct{}, 1),
		shards:            make([]shardWatermark, len(clients)),
		suspended:         true,
		nextPollCompleted: make(chan struct{}),
	}
}

// getWatermarkLocked computes the watermark of the replica. It returns
// false if the watermarks of the storage servers need to be polled
// before it can be computed.
func (p *WatermarkPoller) getWatermarkLocked(now time.Time) (*object_pb.Watermark, bool) {
	var oldestRequestedAt time.Time
	var highestWatermark, oldestObtainedAt *timestamppb.Timestamp
	for _, shard := range p.shards {
		if shard.requestedAt.IsZero() {
			return nil, false
		}
		if oldestRequestedAt.IsZero() || shard.requestedAt.Before(oldestRequestedAt) {
			oldestRequestedAt = shard.requestedAt
		}
		if shard.watermark != nil && (highestWatermark == nil || shard.watermark.AsTime().After(highestWatermark.AsTime())) {
			highestWatermark = shard.watermark
		}
		if oldestObtainedAt == nil || shard.obtainedAt.AsTime().Before(oldestObtainedAt.AsTime()) {
			oldestObtainedAt = shard.obtainedAt
		}
	}
	return &object_pb.Watermark{
		Watermark:  highestWatermark,
		ObtainedAt: oldestObtainedAt,
	}, !now.After(oldestRequestedAt.Add(2 * p.interval))
}

// GetWatermarkForRequest returns the watermark of the replica, so that
// it can be attached to an outgoing request. It causes polling to
// resume if it was suspended due to inactivity. If the watermarks that
// are known are outdated, it waits for them to be polled again.
func (p *WatermarkPoller) GetWatermarkForRequest(ctx context.Context) (*object_pb.Watermark, error) {
	p.lock.Lock()
	p.used = true
	if p.suspended {
		p.suspended = false
		p.wakeup <- struct{}{}
	}
	if watermark, ok := p.getWatermarkLocked(p.clock.Now()); ok {
		p.lock.Unlock()
		return watermark, nil
	}
	nextPollCompleted := p.nextPollCompleted
	p.lock.Unlock()

	select {
	case <-nextPollCompleted:
	case <-ctx.Done():
		return nil, util.StatusFromContext(ctx)
	}

	// If polling failed, provide the outdated watermarks anyway.
	// Storage servers reject them if they are too old.
	p.lock.Lock()
	defer p.lock.Unlock()
	watermark, _ := p.getWatermarkLocked(p.clock.Now())
	if watermark == nil {
		return nil, status.Error(codes.Unavailable, "Watermarks of one or more storage servers have not been obtained yet")
	}
	return watermark, nil
}

// pollOnce obtains the watermarks of all storage servers in parallel.
func (p *WatermarkPoller) pollOnce(ctx context.Context) {
	var wg sync.WaitGroup
	for i, client := range p.clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			requestedAt := p.clock.Now()
			response, err := client.GetWatermark(ctx, &emptypb.Empty{})
			var watermark *object_pb.Watermark
			if err == nil {
				watermark = response.Watermark
				err = watermark.GetObtainedAt().CheckValid()
				if err == nil && watermark.Watermark != nil {
					err = watermark.Watermark.CheckValid()
				}
			}
			if err != nil {
				if ctx.Err() == nil {
					p.errorLogger.Log(util.StatusWrap(err, "Failed to obtain watermark"))
				}
				return
			}
			p.lock.Lock()
			p.shards[i] = shardWatermark{
				requestedAt: requestedAt,
				watermark:   watermark.Watermark,
				obtainedAt:  watermark.ObtainedAt,
			}
			p.lock.Unlock()
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

			// Unblock requests that are waiting for the
			// watermarks to be polled. This is also done if
			// polling failed, as requests are likely to
			// fail in that case as well.
			p.pollOnce(ctx)
			p.lock.Lock()
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
