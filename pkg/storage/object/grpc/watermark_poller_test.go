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

func TestWatermarkPoller(t *testing.T) {
	ctrl, ctx := gomock.WithContext(context.Background(), t)

	client1 := NewMockUploaderClient(ctrl)
	client2 := NewMockUploaderClient(ctrl)
	clock := NewMockClock(ctrl)
	var nowLock sync.Mutex
	now := time.Unix(1000, 0)
	setNow := func(t time.Time) {
		nowLock.Lock()
		now = t
		nowLock.Unlock()
	}
	clock.EXPECT().Now().DoAndReturn(func() time.Time {
		nowLock.Lock()
		defer nowLock.Unlock()
		return now
	}).AnyTimes()
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

	getWatermarkAsync := func(ctx context.Context) <-chan *timestamppb.Timestamp {
		result := make(chan *timestamppb.Timestamp, 1)
		go func() {
			watermark, err := poller.GetWatermarkForRequest(ctx)
			require.NoError(t, err)
			result <- watermark
		}()
		return result
	}

	// No polling should take place until a request needs the
	// watermark. As no watermark is known, the first request must
	// wait for polling to complete.
	timer1 := NewMockTimer(ctrl)
	timerChannel1 := make(chan time.Time)
	timerCreated1 := make(chan struct{})
	client1.EXPECT().GetWatermark(gomock.Any(), testutil.EqProto(t, &emptypb.Empty{})).
		Return(&object_pb.GetWatermarkResponse{Watermark: &timestamppb.Timestamp{Seconds: 900}}, nil)
	client2.EXPECT().GetWatermark(gomock.Any(), testutil.EqProto(t, &emptypb.Empty{})).
		Return(&object_pb.GetWatermarkResponse{Watermark: &timestamppb.Timestamp{Seconds: 950}}, nil)
	clock.EXPECT().NewTimer(10 * time.Second).DoAndReturn(func(d time.Duration) (*MockTimer, <-chan time.Time) {
		close(timerCreated1)
		return timer1, timerChannel1
	})
	require.Equal(t, &timestamppb.Timestamp{Seconds: 950}, <-getWatermarkAsync(ctx))
	<-timerCreated1

	// While the watermark is recent, requests should not wait. As
	// this marks the watermark as being used, another round of
	// polling should take place once the timer expires.
	setNow(time.Unix(1005, 0))
	require.Equal(t, &timestamppb.Timestamp{Seconds: 950}, <-getWatermarkAsync(ctx))

	timer2 := NewMockTimer(ctrl)
	timerChannel2 := make(chan time.Time)
	timerCreated2 := make(chan struct{})
	client1.EXPECT().GetWatermark(gomock.Any(), gomock.Any()).
		Return(&object_pb.GetWatermarkResponse{Watermark: &timestamppb.Timestamp{Seconds: 960}}, nil)
	client2.EXPECT().GetWatermark(gomock.Any(), gomock.Any()).
		Return(&object_pb.GetWatermarkResponse{Watermark: &timestamppb.Timestamp{Seconds: 955}}, nil)
	clock.EXPECT().NewTimer(10 * time.Second).DoAndReturn(func(d time.Duration) (*MockTimer, <-chan time.Time) {
		close(timerCreated2)
		return timer2, timerChannel2
	})
	setNow(time.Unix(1010, 0))
	timerChannel1 <- time.Unix(1010, 0)
	<-timerCreated2

	// No requests were sent during the last interval. Polling
	// should stop once the timer expires.
	setNow(time.Unix(1020, 0))
	timerChannel2 <- time.Unix(1020, 0)

	// After a long period of inactivity, the watermark known by
	// the poller may be outdated. The next request must wait for
	// polling to resume and complete, as opposed to returning the
	// outdated watermark.
	setNow(time.Unix(1100, 0))
	timer3 := NewMockTimer(ctrl)
	timerCreated3 := make(chan struct{})
	releasePoll := make(chan struct{})
	client1.EXPECT().GetWatermark(gomock.Any(), gomock.Any()).
		DoAndReturn(func(context.Context, *emptypb.Empty, ...any) (*object_pb.GetWatermarkResponse, error) {
			<-releasePoll
			return &object_pb.GetWatermarkResponse{Watermark: &timestamppb.Timestamp{Seconds: 1090}}, nil
		})
	client2.EXPECT().GetWatermark(gomock.Any(), gomock.Any()).
		Return(&object_pb.GetWatermarkResponse{Watermark: &timestamppb.Timestamp{Seconds: 1080}}, nil)
	clock.EXPECT().NewTimer(10 * time.Second).DoAndReturn(func(d time.Duration) (*MockTimer, <-chan time.Time) {
		close(timerCreated3)
		return timer3, make(chan time.Time)
	})
	result := getWatermarkAsync(ctx)
	select {
	case <-result:
		t.Fatal("Request should have waited for polling to complete")
	case <-time.After(100 * time.Millisecond):
	}
	close(releasePoll)
	require.Equal(t, &timestamppb.Timestamp{Seconds: 1090}, <-result)
	<-timerCreated3

	// Requests that wait for polling to complete should respect
	// cancellation of their context.
	setNow(time.Unix(1200, 0))
	canceledCtx, cancelRequest := context.WithCancel(ctx)
	cancelRequest()
	_, err := poller.GetWatermarkForRequest(canceledCtx)
	testutil.RequireEqualStatus(t, status.Error(codes.Canceled, "context canceled"), err)

	// Stopping the poller should not cause any further polling.
	timer3.EXPECT().Stop().Return(true)
	cancel()
	<-runDone
}
