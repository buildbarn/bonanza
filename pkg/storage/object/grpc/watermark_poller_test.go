package grpc_test

import (
	"context"
	"crypto/sha256"
	"sync"
	"testing"
	"time"

	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/object"
	"bonanza.build/pkg/storage/object/grpc"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"go.uber.org/mock/gomock"
)

// fakeNow provides a clock.Clock.Now() implementation whose value can
// be adjusted by tests.
type fakeNow struct {
	lock sync.Mutex
	now  time.Time
}

func (n *fakeNow) set(t time.Time) {
	n.lock.Lock()
	n.now = t
	n.lock.Unlock()
}

func (n *fakeNow) get() time.Time {
	n.lock.Lock()
	defer n.lock.Unlock()
	return n.now
}

// getIncarnationsHash computes the expected incarnations hash of a
// cluster consisting of shards "a" (weight 1) and "b" (weight 2).
func getIncarnationsHash(t *testing.T, incarnationA, incarnationB string) []byte {
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(object_pb.IncarnationsHashInput_builder{
		Shards: []*object_pb.IncarnationsHashInput_Shard{
			object_pb.IncarnationsHashInput_Shard_builder{Name: "a", Weight: 1, Incarnation: []byte(incarnationA)}.Build(),
			object_pb.IncarnationsHashInput_Shard_builder{Name: "b", Weight: 2, Incarnation: []byte(incarnationB)}.Build(),
		},
	}.Build())
	require.NoError(t, err)
	hash := sha256.Sum256(data)
	return hash[:]
}

func TestWatermarkPoller(t *testing.T) {
	ctrl, ctx := gomock.WithContext(context.Background(), t)

	// Shards are provided in an order that differs from their
	// names, to ensure the incarnations hash is computed in the
	// order of the names.
	clientB := NewMockUploaderClient(ctrl)
	clientA := NewMockUploaderClient(ctrl)
	clock := NewMockClock(ctrl)
	now := fakeNow{now: time.Unix(1000, 0)}
	clock.EXPECT().Now().DoAndReturn(now.get).AnyTimes()
	poller := grpc.NewWatermarkPoller(
		[]grpc.WatermarkPollerShard{
			{Name: "b", Weight: 2, Client: clientB},
			{Name: "a", Weight: 1, Client: clientA},
		},
		clock,
		10*time.Second,
		util.DefaultErrorLogger,
	)
	sourceB := poller.GetLeaseValidationContextSource(0)
	sourceA := poller.GetLeaseValidationContextSource(1)

	runCtx, cancel := context.WithCancel(ctx)
	runDone := make(chan struct{})
	go func() {
		poller.Run(runCtx)
		close(runDone)
	}()

	getAsync := func(source object.LeaseValidationContextSource) <-chan *object_pb.LeaseValidationContext {
		result := make(chan *object_pb.LeaseValidationContext, 1)
		go func() {
			leaseValidationContext, err := source.GetLeaseValidationContext(ctx)
			require.NoError(t, err)
			result <- leaseValidationContext
		}()
		return result
	}
	// Storage servers report the watermark and the time at which
	// it was obtained using their own clocks, which need not match
	// the clock of the storage frontend.
	getWatermarkResponse := func(watermark, obtainedAt int64, incarnation string) *object_pb.GetWatermarkResponse {
		return object_pb.GetWatermarkResponse_builder{
			Watermark: object_pb.Watermark_builder{
				Watermark:  &timestamppb.Timestamp{Seconds: watermark},
				ObtainedAt: &timestamppb.Timestamp{Seconds: obtainedAt},
			}.Build(),
			Incarnation: []byte(incarnation),
		}.Build()
	}
	expectGetWatermark := func(client *MockUploaderClient, watermark, obtainedAt int64, incarnation string, err error) {
		call := client.EXPECT().GetWatermark(gomock.Any(), testutil.EqProto(t, &emptypb.Empty{}))
		if err != nil {
			call.Return(nil, err)
		} else {
			call.Return(getWatermarkResponse(watermark, obtainedAt, incarnation), nil)
		}
	}
	expectTimer := func() (*MockTimer, chan time.Time, <-chan struct{}) {
		timer := NewMockTimer(ctrl)
		timerChannel := make(chan time.Time)
		timerCreated := make(chan struct{})
		clock.EXPECT().NewTimer(10 * time.Second).DoAndReturn(func(d time.Duration) (*MockTimer, <-chan time.Time) {
			close(timerCreated)
			return timer, timerChannel
		})
		return timer, timerChannel, timerCreated
	}
	leaseValidationContext := func(watermark, obtainedAt int64, incarnationsHash []byte, expectedIncarnation string) *object_pb.LeaseValidationContext {
		return object_pb.LeaseValidationContext_builder{
			Watermark: object_pb.Watermark_builder{
				Watermark:  &timestamppb.Timestamp{Seconds: watermark},
				ObtainedAt: &timestamppb.Timestamp{Seconds: obtainedAt},
			}.Build(),
			IncarnationsHash:    incarnationsHash,
			ExpectedIncarnation: []byte(expectedIncarnation),
		}.Build()
	}
	unavailable := status.Error(codes.Unavailable, "Server offline")
	hash1 := getIncarnationsHash(t, "a1", "b1")

	// No polling should take place until a request needs the
	// watermark. As no watermarks are known, the first request must
	// wait for polling to complete. The watermark of the replica
	// should use the highest watermark, and the time at which the
	// oldest of them was obtained.
	expectGetWatermark(clientB, 900, 1000, "b1", nil)
	expectGetWatermark(clientA, 950, 1001, "a1", nil)
	_, timerChannel1, timerCreated1 := expectTimer()
	testutil.RequireEqualProto(t, leaseValidationContext(950, 1000, hash1, "b1"), <-getAsync(sourceB))
	<-timerCreated1

	// While the watermarks are recent, requests should not wait.
	// As this marks the watermark as being used, another round of
	// polling should take place once the timer expires. The
	// expected incarnation depends on the storage server to which
	// the request is sent.
	now.set(time.Unix(1005, 0))
	testutil.RequireEqualProto(t, leaseValidationContext(950, 1000, hash1, "a1"), <-getAsync(sourceA))

	// If the watermark of a storage server cannot be obtained,
	// the one obtained previously continues to be used. The time
	// at which the watermark of the replica was obtained should
	// reflect this.
	expectGetWatermark(clientB, 960, 1010, "b1", nil)
	expectGetWatermark(clientA, 0, 0, "", unavailable)
	_, timerChannel2, timerCreated2 := expectTimer()
	now.set(time.Unix(1010, 0))
	timerChannel1 <- time.Unix(1010, 0)
	<-timerCreated2

	now.set(time.Unix(1015, 0))
	testutil.RequireEqualProto(t, leaseValidationContext(960, 1001, hash1, "b1"), <-getAsync(sourceB))

	// Once the watermark becomes outdated, requests wait for the
	// next round of polling. Even if polling fails, they should
	// eventually be released, providing the outdated watermark.
	// Storage servers reject it if it's too old.
	expectGetWatermark(clientB, 0, 0, "", unavailable)
	expectGetWatermark(clientA, 0, 0, "", unavailable)
	_, timerChannel3, timerCreated3 := expectTimer()
	now.set(time.Unix(1020, 0))
	timerChannel2 <- time.Unix(1020, 0)
	<-timerCreated3

	now.set(time.Unix(1025, 0))
	result := getAsync(sourceB)
	expectGetWatermark(clientB, 970, 1030, "b1", nil)
	expectGetWatermark(clientA, 0, 0, "", unavailable)
	_, timerChannel4, timerCreated4 := expectTimer()
	now.set(time.Unix(1030, 0))
	timerChannel3 <- time.Unix(1030, 0)
	testutil.RequireEqualProto(t, leaseValidationContext(970, 1001, hash1, "b1"), <-result)
	<-timerCreated4

	// No requests were sent during the last interval. Polling
	// should stop once the timer expires.
	now.set(time.Unix(1040, 0))
	timerChannel4 <- time.Unix(1040, 0)

	// After a long period of inactivity, the next request must
	// wait for polling to resume and complete, as opposed to
	// returning the outdated watermark. The incarnation of one of
	// the storage servers changed in the meantime, which should
	// cause the incarnations hash to change.
	now.set(time.Unix(2000, 0))
	releasePoll := make(chan struct{})
	clientB.EXPECT().GetWatermark(gomock.Any(), gomock.Any()).
		DoAndReturn(func(context.Context, *emptypb.Empty, ...any) (*object_pb.GetWatermarkResponse, error) {
			<-releasePoll
			return getWatermarkResponse(1500, 2000, "b2"), nil
		})
	expectGetWatermark(clientA, 1600, 2001, "a1", nil)
	timer5, _, timerCreated5 := expectTimer()
	result = getAsync(sourceB)
	select {
	case <-result:
		t.Fatal("Request should have waited for polling to complete")
	case <-time.After(100 * time.Millisecond):
	}
	close(releasePoll)
	hash2 := getIncarnationsHash(t, "a1", "b2")
	testutil.RequireEqualProto(t, leaseValidationContext(1600, 2000, hash2, "b2"), <-result)
	<-timerCreated5

	// If a storage server reports that its incarnation differs
	// from the one that was expected, polling should take place
	// immediately. Requests sent to that storage server should
	// wait for it to complete, while requests sent to other
	// storage servers may continue to use the information that is
	// currently known.
	timer5.EXPECT().Stop().Return(true)
	releasePoll = make(chan struct{})
	clientB.EXPECT().GetWatermark(gomock.Any(), gomock.Any()).
		DoAndReturn(func(context.Context, *emptypb.Empty, ...any) (*object_pb.GetWatermarkResponse, error) {
			<-releasePoll
			return getWatermarkResponse(1500, 2002, "b3"), nil
		})
	expectGetWatermark(clientA, 1600, 2003, "a1", nil)
	timer6, _, timerCreated6 := expectTimer()
	now.set(time.Unix(2002, 0))
	sourceB.ReportIncarnationMismatch(leaseValidationContext(1600, 2000, hash2, "b2"))
	result = getAsync(sourceB)
	testutil.RequireEqualProto(t, leaseValidationContext(1600, 2000, hash2, "a1"), <-getAsync(sourceA))
	select {
	case <-result:
		t.Fatal("Request should have waited for polling to complete")
	case <-time.After(100 * time.Millisecond):
	}

	// Reporting a mismatch for an incarnation that is already
	// being refreshed should not cause any additional polling.
	sourceB.ReportIncarnationMismatch(leaseValidationContext(1600, 2000, hash2, "b2"))

	close(releasePoll)
	hash3 := getIncarnationsHash(t, "a1", "b3")
	testutil.RequireEqualProto(t, leaseValidationContext(1600, 2002, hash3, "b3"), <-result)
	<-timerCreated6

	// Reports of mismatches for incarnations that have already
	// been refreshed should be ignored.
	sourceB.ReportIncarnationMismatch(leaseValidationContext(1600, 2000, hash2, "b2"))
	testutil.RequireEqualProto(t, leaseValidationContext(1600, 2002, hash3, "b3"), <-getAsync(sourceB))

	// Requests that wait for polling to complete should respect
	// cancellation of their context.
	now.set(time.Unix(3000, 0))
	canceledCtx, cancelRequest := context.WithCancel(ctx)
	cancelRequest()
	_, err := sourceA.GetLeaseValidationContext(canceledCtx)
	testutil.RequireEqualStatus(t, status.Error(codes.Canceled, "context canceled"), err)

	// Stopping the poller should not cause any further polling.
	timer6.EXPECT().Stop().Return(true)
	cancel()
	<-runDone
}

func TestWatermarkPollerNeverObtained(t *testing.T) {
	ctrl, ctx := gomock.WithContext(context.Background(), t)

	client := NewMockUploaderClient(ctrl)
	clock := NewMockClock(ctrl)
	clock.EXPECT().Now().Return(time.Unix(1000, 0)).AnyTimes()
	poller := grpc.NewWatermarkPoller(
		[]grpc.WatermarkPollerShard{
			{Name: "a", Weight: 1, Client: client},
		},
		clock,
		10*time.Second,
		util.DefaultErrorLogger,
	)

	runCtx, cancel := context.WithCancel(ctx)
	runDone := make(chan struct{})
	go func() {
		poller.Run(runCtx)
		close(runDone)
	}()

	// If the watermark of a storage server has never been
	// obtained, no watermark of the replica can be provided.
	timer := NewMockTimer(ctrl)
	client.EXPECT().GetWatermark(gomock.Any(), gomock.Any()).
		Return(nil, status.Error(codes.Unavailable, "Server offline"))
	clock.EXPECT().NewTimer(10*time.Second).Return(timer, make(chan time.Time))
	_, err := poller.GetLeaseValidationContextSource(0).GetLeaseValidationContext(ctx)
	testutil.RequireEqualStatus(t, status.Error(codes.Unavailable, "Watermarks of one or more storage servers have not been obtained yet"), err)

	timer.EXPECT().Stop().Return(true)
	cancel()
	<-runDone
}
