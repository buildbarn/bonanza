package object_test

import (
	"testing"
	"time"

	"bonanza.build/pkg/storage/object"

	"github.com/stretchr/testify/require"

	"go.uber.org/mock/gomock"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestWatermark(t *testing.T) {
	ctrl := gomock.NewController(t)

	clock := NewMockClock(ctrl)
	clock.EXPECT().Now().Return(time.Unix(1700001000, 0)).AnyTimes()
	w := object.NewWatermark(clock)

	// Initially no watermark is known.
	require.True(t, w.Get().IsZero())
	require.Nil(t, w.ToProto())

	// The watermark can be raised, but never lowered.
	w.Raise(time.Unix(1700000100, 0))
	require.Equal(t, time.Unix(1700000100, 0), w.Get())
	w.Raise(time.Unix(1700000050, 0))
	require.Equal(t, time.Unix(1700000100, 0), w.Get())
	w.RaiseFromProto(&timestamppb.Timestamp{Seconds: 1700000200})
	require.Equal(t, time.Unix(1700000200, 0), w.Get())

	// Absent or invalid values are ignored.
	w.RaiseFromProto(nil)
	w.RaiseFromProto(&timestamppb.Timestamp{Nanos: -1})
	w.Raise(time.Time{})
	require.Equal(t, &timestamppb.Timestamp{Seconds: 1700000200}, w.ToProto())

	// Values in the future, caused by clock skew between servers,
	// are capped to the current time. This prevents them from
	// remaining in effect after clock skew has been resolved.
	w.Raise(time.Unix(1700009000, 0))
	require.Equal(t, time.Unix(1700001000, 0), w.Get())
}
