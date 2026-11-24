package object_test

import (
	"strings"
	"testing"
	"time"

	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

	"go.uber.org/mock/gomock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestWatermark(t *testing.T) {
	ctrl := gomock.NewController(t)

	clock := NewMockClock(ctrl)
	clock.EXPECT().Now().Return(time.Unix(1700001000, 0)).AnyTimes()
	w := object.NewWatermark(clock, time.Minute)

	// Initially no watermark is known.
	require.True(t, w.Get().IsZero())

	// The watermark can be raised, but never lowered.
	w.Raise(time.Unix(1700000100, 0))
	require.Equal(t, time.Unix(1700000100, 0), w.Get())
	w.Raise(time.Unix(1700000050, 0))
	require.Equal(t, time.Unix(1700000100, 0), w.Get())
	require.NoError(t, w.RaiseFromProto(&object_pb.Watermark{
		Watermark:  &timestamppb.Timestamp{Seconds: 1700000200},
		ObtainedAt: &timestamppb.Timestamp{Seconds: 1700000970},
	}))
	require.Equal(t, time.Unix(1700000200, 0), w.Get())

	// Absent values are ignored, as are absent watermarks.
	require.NoError(t, w.RaiseFromProto(nil))
	require.NoError(t, w.RaiseFromProto(&object_pb.Watermark{
		ObtainedAt: &timestamppb.Timestamp{Seconds: 1700000970},
	}))
	require.Equal(t, time.Unix(1700000200, 0), w.Get())

	// Values in the future, caused by clock skew between servers,
	// are capped to the current time. This prevents them from
	// remaining in effect after clock skew has been resolved.
	w.Raise(time.Unix(1700009000, 0))
	require.Equal(t, time.Unix(1700001000, 0), w.Get())

	// Invalid values are rejected.
	// The message of the error returned by the Protobuf library is
	// intentionally unstable, so only check our own prefix.
	err := w.RaiseFromProto(&object_pb.Watermark{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.True(t, strings.HasPrefix(status.Convert(err).Message(), "Invalid time at which the watermark was obtained: "))

	// Watermarks that were obtained too long ago are rejected, as
	// data may have been lost in the meantime.
	testutil.RequireEqualStatus(t, status.Error(codes.Unavailable, "Watermark of the cluster was obtained 1m1s ago, which exceeds the maximum permitted age of 1m0s, meaning data may have been lost in the meantime"), w.RaiseFromProto(&object_pb.Watermark{
		Watermark:  &timestamppb.Timestamp{Seconds: 1700000500},
		ObtainedAt: &timestamppb.Timestamp{Seconds: 1700000939},
	}))
}
