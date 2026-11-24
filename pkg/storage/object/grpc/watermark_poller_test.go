package grpc_test

import (
	"context"
	"sync"
	"testing"
	"time"

	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/object/grpc"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

func TestWatermarkPoller(t *testing.T) {
	ctrl, ctx := gomock.WithContext(context.Background(), t)

	client1 := NewMockUploaderClient(ctrl)
	client2 := NewMockUploaderClient(ctrl)
	clock := NewMockClock(ctrl)
	now := fakeNow{now: time.Unix(1000, 0)}
	clock.EXPECT().Now().DoAndReturn(now.get).AnyTimes()
	poller := grpc.NewWatermarkPoller(
		[]object_pb.UploaderClient{client1, client2},
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

	getWatermarkAsync := func() <-chan *object_pb.Watermark {
		result := make(chan *object_pb.Watermark, 1)
		go func() {
			watermark, err := poller.GetWatermarkForRequest(ctx)
			require.NoError(t, err)
			result <- watermark
		}()
		return result
	}
	// Storage servers report the watermark and the time at which
	// it was obtained using their own clocks, which need not match
	// the clock of the storage frontend.
	expectGetWatermark := func(client *MockUploaderClient, watermark, obtainedAt int64, err error) {
		call := client.EXPECT().GetWatermark(gomock.Any(), testutil.EqProto(t, &emptypb.Empty{}))
		if err != nil {
			call.Return(nil, err)
		} else {
			call.Return(&object_pb.GetWatermarkResponse{
				Watermark: &object_pb.Watermark{
					Watermark:  &timestamppb.Timestamp{Seconds: watermark},
					ObtainedAt: &timestamppb.Timestamp{Seconds: obtainedAt},
				},
			}, nil)
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
	watermark := func(watermark, obtainedAt int64) *object_pb.Watermark {
		return &object_pb.Watermark{
			Watermark:  &timestamppb.Timestamp{Seconds: watermark},
			ObtainedAt: &timestamppb.Timestamp{Seconds: obtainedAt},
		}
	}
	unavailable := status.Error(codes.Unavailable, "Server offline")

	// No polling should take place until a request needs the
	// watermark. As no watermarks are known, the first request must
	// wait for polling to complete. The watermark of the replica
	// should use the highest watermark, and the time at which the
	// oldest of them was obtained.
	expectGetWatermark(client1, 900, 1000, nil)
	expectGetWatermark(client2, 950, 1001, nil)
	_, timerChannel1, timerCreated1 := expectTimer()
	testutil.RequireEqualProto(t, watermark(950, 1000), <-getWatermarkAsync())
	<-timerCreated1

	// While the watermarks are recent, requests should not wait.
	// As this marks the watermark as being used, another round of
	// polling should take place once the timer expires.
	now.set(time.Unix(1005, 0))
	testutil.RequireEqualProto(t, watermark(950, 1000), <-getWatermarkAsync())

	// If the watermark of a storage server cannot be obtained,
	// the one obtained previously continues to be used. The time
	// at which the watermark of the replica was obtained should
	// reflect this.
	expectGetWatermark(client1, 960, 1010, nil)
	expectGetWatermark(client2, 0, 0, unavailable)
	_, timerChannel2, timerCreated2 := expectTimer()
	now.set(time.Unix(1010, 0))
	timerChannel1 <- time.Unix(1010, 0)
	<-timerCreated2

	now.set(time.Unix(1015, 0))
	testutil.RequireEqualProto(t, watermark(960, 1001), <-getWatermarkAsync())

	// Once the watermark becomes outdated, requests wait for the
	// next round of polling. Even if polling fails, they should
	// eventually be released, providing the outdated watermark.
	// Storage servers reject it if it's too old.
	expectGetWatermark(client1, 0, 0, unavailable)
	expectGetWatermark(client2, 0, 0, unavailable)
	_, timerChannel3, timerCreated3 := expectTimer()
	now.set(time.Unix(1020, 0))
	timerChannel2 <- time.Unix(1020, 0)
	<-timerCreated3

	now.set(time.Unix(1025, 0))
	result := getWatermarkAsync()
	expectGetWatermark(client1, 970, 1030, nil)
	expectGetWatermark(client2, 0, 0, unavailable)
	_, timerChannel4, timerCreated4 := expectTimer()
	now.set(time.Unix(1030, 0))
	timerChannel3 <- time.Unix(1030, 0)
	testutil.RequireEqualProto(t, watermark(970, 1001), <-result)
	<-timerCreated4

	// No requests were sent during the last interval. Polling
	// should stop once the timer expires.
	now.set(time.Unix(1040, 0))
	timerChannel4 <- time.Unix(1040, 0)

	// After a long period of inactivity, the next request must
	// wait for polling to resume and complete, as opposed to
	// returning the outdated watermark.
	now.set(time.Unix(2000, 0))
	releasePoll := make(chan struct{})
	client1.EXPECT().GetWatermark(gomock.Any(), gomock.Any()).
		DoAndReturn(func(context.Context, *emptypb.Empty, ...any) (*object_pb.GetWatermarkResponse, error) {
			<-releasePoll
			return &object_pb.GetWatermarkResponse{
				Watermark: &object_pb.Watermark{
					Watermark:  &timestamppb.Timestamp{Seconds: 1500},
					ObtainedAt: &timestamppb.Timestamp{Seconds: 2000},
				},
			}, nil
		})
	expectGetWatermark(client2, 1600, 2001, nil)
	timer5, _, timerCreated5 := expectTimer()
	result = getWatermarkAsync()
	select {
	case <-result:
		t.Fatal("Request should have waited for polling to complete")
	case <-time.After(100 * time.Millisecond):
	}
	close(releasePoll)
	testutil.RequireEqualProto(t, watermark(1600, 2000), <-result)
	<-timerCreated5

	// Requests that wait for polling to complete should respect
	// cancellation of their context.
	now.set(time.Unix(3000, 0))
	canceledCtx, cancelRequest := context.WithCancel(ctx)
	cancelRequest()
	_, err := poller.GetWatermarkForRequest(canceledCtx)
	testutil.RequireEqualStatus(t, status.Error(codes.Canceled, "context canceled"), err)

	// Stopping the poller should not cause any further polling.
	timer5.EXPECT().Stop().Return(true)
	cancel()
	<-runDone
}

func TestWatermarkPollerNeverObtained(t *testing.T) {
	ctrl, ctx := gomock.WithContext(context.Background(), t)

	client := NewMockUploaderClient(ctrl)
	clock := NewMockClock(ctrl)
	clock.EXPECT().Now().Return(time.Unix(1000, 0)).AnyTimes()
	poller := grpc.NewWatermarkPoller(
		[]object_pb.UploaderClient{client},
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
	_, err := poller.GetWatermarkForRequest(ctx)
	testutil.RequireEqualStatus(t, status.Error(codes.Unavailable, "Watermarks of one or more storage servers have not been obtained yet"), err)

	timer.EXPECT().Stop().Return(true)
	cancel()
	<-runDone
}
