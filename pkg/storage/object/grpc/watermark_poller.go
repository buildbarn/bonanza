package grpc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"sort"
	"sync"
	"time"

	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/clock"
	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// WatermarkPollerShard contains the properties of a single storage
// server whose watermark is polled by WatermarkPoller.
type WatermarkPollerShard struct {
	// The name and weight of the storage server, as used to
	// partition the key space. These are included in the
	// incarnations hash, so that changes to the layout of the key
	// space cause leases to be invalidated.
	Name   string
	Weight uint32

	Client object_pb.UploaderClient
}

// WatermarkPoller obtains the watermarks and incarnations of all
// storage servers in a replica. Storage frontends attach the resulting
// lease validation context to requests that need to check leases, so
// that storage servers can check leases against the highest watermark
// of the replica, as opposed to just their own.
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
//
// If a storage server reports that a request was intended for another
// incarnation, the watermarks are polled again immediately. Requests
// sent to that storage server wait for this to complete.
type WatermarkPoller struct {
	shards             []WatermarkPollerShard
	shardIndicesByName []int
	clock              clock.Clock
	interval           time.Duration
	errorLogger        util.ErrorLogger

	wakeup chan struct{}

	lock sync.Mutex
	// The latest state obtained from each storage server.
	states []shardState
	// Cached hash of the names, weights and incarnations of all
	// storage servers. Nil if it needs to be recomputed.
	incarnationsHash []byte
	// Whether polling is suspended due to inactivity, meaning
	// that the next request needs to wake up Run().
	suspended bool
	// Whether the watermark was requested since polling was last
	// performed.
	used bool
	// The number of polls that have started and completed.
	pollsStarted   uint64
	pollsCompleted uint64
	// Closed once the next poll completes.
	nextPollCompleted chan struct{}
}

// shardState contains the latest watermark and incarnation that were
// obtained from a single storage server.
type shardState struct {
	// The time at which the watermark was requested. Zero if no
	// watermark has been obtained yet.
	requestedAt time.Time
	// The watermark reported by the storage server, or nil if the
	// storage server has not lost any data yet.
	watermark *timestamppb.Timestamp
	// The time at which the watermark was obtained, according to
	// the clock of the storage server.
	obtainedAt *timestamppb.Timestamp
	// The incarnation of the storage server.
	incarnation []byte
	// If non-zero, the storage server reported that its incarnation
	// differs from the one above. Requests sent to the storage
	// server need to wait for the poll with this number to
	// complete.
	refreshByPoll uint64
}

// NewWatermarkPoller creates a WatermarkPoller that polls the
// watermarks of the provided storage servers. Run() needs to be called
// to perform the actual polling.
func NewWatermarkPoller(shards []WatermarkPollerShard, clock clock.Clock, interval time.Duration, errorLogger util.ErrorLogger) *WatermarkPoller {
	shardIndicesByName := make([]int, len(shards))
	for i := range shardIndicesByName {
		shardIndicesByName[i] = i
	}
	sort.Slice(shardIndicesByName, func(i, j int) bool {
		return shards[shardIndicesByName[i]].Name < shards[shardIndicesByName[j]].Name
	})
	return &WatermarkPoller{
		shards:             shards,
		shardIndicesByName: shardIndicesByName,
		clock:              clock,
		interval:           interval,
		errorLogger:        errorLogger,

		wakeup:            make(chan struct{}, 1),
		states:            make([]shardState, len(shards)),
		suspended:         true,
		nextPollCompleted: make(chan struct{}),
	}
}

// wakeupLocked causes Run() to poll immediately, regardless of whether
// it is suspended or waiting for the polling interval to elapse.
func (p *WatermarkPoller) wakeupLocked() {
	p.suspended = false
	select {
	case p.wakeup <- struct{}{}:
	default:
	}
}

// getIncarnationsHashLocked returns the hash of the names, weights and
// incarnations of all storage servers.
func (p *WatermarkPoller) getIncarnationsHashLocked() []byte {
	if p.incarnationsHash == nil {
		shards := make([]*object_pb.IncarnationsHashInput_Shard, 0, len(p.shards))
		for _, i := range p.shardIndicesByName {
			shards = append(shards, object_pb.IncarnationsHashInput_Shard_builder{
				Name:        p.shards[i].Name,
				Weight:      p.shards[i].Weight,
				Incarnation: p.states[i].incarnation,
			}.Build())
		}
		data, err := proto.MarshalOptions{Deterministic: true}.Marshal(object_pb.IncarnationsHashInput_builder{
			Shards: shards,
		}.Build())
		if err != nil {
			panic(err)
		}
		hash := sha256.Sum256(data)
		p.incarnationsHash = hash[:]
	}
	return p.incarnationsHash
}

// getLeaseValidationContextLocked computes the lease validation context
// for requests sent to a given storage server. It returns false if the
// watermarks or the incarnation of the storage server need to be polled
// before it can be computed.
func (p *WatermarkPoller) getLeaseValidationContextLocked(shardIndex int, now time.Time) (*object_pb.LeaseValidationContext, bool) {
	var oldestRequestedAt time.Time
	var highestWatermark, oldestObtainedAt *timestamppb.Timestamp
	for _, state := range p.states {
		if state.requestedAt.IsZero() {
			return nil, false
		}
		if oldestRequestedAt.IsZero() || state.requestedAt.Before(oldestRequestedAt) {
			oldestRequestedAt = state.requestedAt
		}
		if state.watermark != nil && (highestWatermark == nil || state.watermark.AsTime().After(highestWatermark.AsTime())) {
			highestWatermark = state.watermark
		}
		if oldestObtainedAt == nil || state.obtainedAt.AsTime().Before(oldestObtainedAt.AsTime()) {
			oldestObtainedAt = state.obtainedAt
		}
	}
	return object_pb.LeaseValidationContext_builder{
			Watermark: object_pb.Watermark_builder{
				Watermark:  highestWatermark,
				ObtainedAt: oldestObtainedAt,
			}.Build(),
			IncarnationsHash:    p.getIncarnationsHashLocked(),
			ExpectedIncarnation: p.states[shardIndex].incarnation,
		}.Build(),
		!now.After(oldestRequestedAt.Add(2*p.interval)) && p.states[shardIndex].refreshByPoll == 0
}

// getLeaseValidationContext returns the lease validation context for
// requests sent to a given storage server. It causes polling to resume
// if it was suspended due to inactivity. If the information that is
// known is outdated, it waits for it to be polled again.
func (p *WatermarkPoller) getLeaseValidationContext(ctx context.Context, shardIndex int) (*object_pb.LeaseValidationContext, error) {
	p.lock.Lock()
	p.used = true
	if p.suspended {
		p.wakeupLocked()
	}
	if leaseValidationContext, ok := p.getLeaseValidationContextLocked(shardIndex, p.clock.Now()); ok {
		p.lock.Unlock()
		return leaseValidationContext, nil
	}

	// Wait for the next poll to complete. If the incarnation of
	// the storage server needs to be refreshed, wait for a poll
	// that started after this was reported.
	waitForPoll := p.pollsCompleted + 1
	if refreshByPoll := p.states[shardIndex].refreshByPoll; waitForPoll < refreshByPoll {
		waitForPoll = refreshByPoll
	}
	for p.pollsCompleted < waitForPoll {
		nextPollCompleted := p.nextPollCompleted
		p.lock.Unlock()
		select {
		case <-nextPollCompleted:
		case <-ctx.Done():
			return nil, util.StatusFromContext(ctx)
		}
		p.lock.Lock()
	}

	// If polling failed, provide the outdated information anyway.
	// Storage servers reject it if it's too old.
	defer p.lock.Unlock()
	leaseValidationContext, _ := p.getLeaseValidationContextLocked(shardIndex, p.clock.Now())
	if leaseValidationContext == nil {
		return nil, status.Error(codes.Unavailable, "Watermarks of one or more storage servers have not been obtained yet")
	}
	return leaseValidationContext, nil
}

// reportIncarnationMismatch is called when a storage server reports
// that a request was intended for another incarnation. It causes the
// incarnation of the storage server to be polled again.
func (p *WatermarkPoller) reportIncarnationMismatch(shardIndex int, leaseValidationContext *object_pb.LeaseValidationContext) {
	p.lock.Lock()
	defer p.lock.Unlock()
	state := &p.states[shardIndex]
	if state.refreshByPoll == 0 && bytes.Equal(state.incarnation, leaseValidationContext.GetExpectedIncarnation()) {
		state.refreshByPoll = p.pollsStarted + 1
		p.wakeupLocked()
	}
}

// GetLeaseValidationContextSource returns a LeaseValidationContextSource
// that can be used to obtain lease validation contexts for requests
// sent to a given storage server.
func (p *WatermarkPoller) GetLeaseValidationContextSource(shardIndex int) object.LeaseValidationContextSource {
	return &watermarkPollerShardSource{
		poller:     p,
		shardIndex: shardIndex,
	}
}

// pollOnce obtains the watermarks and incarnations of all storage
// servers in parallel.
func (p *WatermarkPoller) pollOnce(ctx context.Context) {
	p.lock.Lock()
	p.pollsStarted++
	p.lock.Unlock()

	var wg sync.WaitGroup
	for i, shard := range p.shards {
		wg.Add(1)
		go func() {
			defer wg.Done()
			requestedAt := p.clock.Now()
			response, err := shard.Client.GetWatermark(ctx, &emptypb.Empty{})
			var watermark *object_pb.Watermark
			var watermarkTime *timestamppb.Timestamp
			if err == nil {
				watermark = response.GetWatermark()
				err = watermark.GetObtainedAt().CheckValid()
				watermarkTime = watermark.GetWatermark()
				if err == nil && watermarkTime != nil {
					err = watermarkTime.CheckValid()
				}
			}
			if err != nil {
				if ctx.Err() == nil {
					p.errorLogger.Log(util.StatusWrapf(err, "Failed to obtain watermark of shard %#v", shard.Name))
				}
				return
			}
			p.lock.Lock()
			state := &p.states[i]
			if !bytes.Equal(state.incarnation, response.GetIncarnation()) {
				p.incarnationsHash = nil
			}
			state.requestedAt = requestedAt
			state.watermark = watermarkTime
			state.obtainedAt = watermark.GetObtainedAt()
			state.incarnation = response.GetIncarnation()
			p.lock.Unlock()
		}()
	}
	wg.Wait()

	// Unblock requests that are waiting for the watermarks to be
	// polled. This is also done if polling failed, as requests are
	// likely to fail in that case as well.
	p.lock.Lock()
	p.pollsCompleted++
	for i := range p.states {
		if state := &p.states[i]; state.refreshByPoll != 0 && state.refreshByPoll <= p.pollsCompleted {
			state.refreshByPoll = 0
		}
	}
	close(p.nextPollCompleted)
	p.nextPollCompleted = make(chan struct{})
	p.lock.Unlock()
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

			p.pollOnce(ctx)

			// Wait for the interval to elapse. Poll
			// immediately if a storage server reported that
			// its incarnation changed.
			timer, t := p.clock.NewTimer(p.interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-p.wakeup:
				timer.Stop()
				continue
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

// watermarkPollerShardSource is the LeaseValidationContextSource that
// is returned by WatermarkPoller.GetLeaseValidationContextSource().
type watermarkPollerShardSource struct {
	poller     *WatermarkPoller
	shardIndex int
}

func (s *watermarkPollerShardSource) GetLeaseValidationContext(ctx context.Context) (*object_pb.LeaseValidationContext, error) {
	return s.poller.getLeaseValidationContext(ctx, s.shardIndex)
}

func (s *watermarkPollerShardSource) ReportIncarnationMismatch(leaseValidationContext *object_pb.LeaseValidationContext) {
	s.poller.reportIncarnationMismatch(s.shardIndex, leaseValidationContext)
}
