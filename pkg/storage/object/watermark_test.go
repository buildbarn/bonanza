package object_test

import (
	"context"
	"strings"
	"testing"
	"time"

	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"go.uber.org/mock/gomock"
)

func TestWatermark(t *testing.T) {
	ctrl := gomock.NewController(t)

	clock := NewMockClock(ctrl)
	clock.EXPECT().Now().Return(time.Unix(1700001000, 0)).AnyTimes()
	// The callback for invalidating leases may acquire locks that are
	// also held while reading the last incarnations hash to persist
	// it. Calling GetLastIncarnationsHash() from within the callback
	// should therefore not deadlock. It should still return the old
	// value, as invalidation has not completed yet.
	invalidations := 0
	var hashesDuringInvalidation [][]byte
	var w *object.Watermark
	w = object.NewWatermark(clock, time.Minute, []byte("incarnation"), []byte("hash1"), func() {
		invalidations++
		hashesDuringInvalidation = append(hashesDuringInvalidation, w.GetLastIncarnationsHash())
	})
	require.Equal(t, []byte("incarnation"), w.GetIncarnation())
	require.Equal(t, []byte("hash1"), w.GetLastIncarnationsHash())

	leaseValidationContext := func(watermark, obtainedAt int64, incarnationsHash string) *object_pb.LeaseValidationContext {
		m := object_pb.LeaseValidationContext_builder{
			Watermark: object_pb.Watermark_builder{
				ObtainedAt: &timestamppb.Timestamp{Seconds: obtainedAt},
			}.Build(),
			IncarnationsHash:    []byte(incarnationsHash),
			ExpectedIncarnation: []byte("incarnation"),
		}.Build()
		if watermark != 0 {
			m.GetWatermark().SetWatermark(&timestamppb.Timestamp{Seconds: watermark})
		}
		return m
	}

	// Initially no watermark is known.
	require.True(t, w.Get().IsZero())

	// The watermark can be raised, but never lowered.
	w.Raise(time.Unix(1700000100, 0))
	require.Equal(t, time.Unix(1700000100, 0), w.Get())
	w.Raise(time.Unix(1700000050, 0))
	require.Equal(t, time.Unix(1700000100, 0), w.Get())
	require.NoError(t, w.ProcessLeaseValidationContext(leaseValidationContext(1700000200, 1700000970, "hash1")))
	require.Equal(t, time.Unix(1700000200, 0), w.Get())

	// Absent watermarks are ignored.
	require.NoError(t, w.ProcessLeaseValidationContext(leaseValidationContext(0, 1700000970, "hash1")))
	require.Equal(t, time.Unix(1700000200, 0), w.Get())

	// Values in the future, caused by clock skew between servers,
	// are capped to the current time. This prevents them from
	// remaining in effect after clock skew has been resolved.
	invalidations2 := 0
	w2 := object.NewWatermark(clock, time.Minute, []byte("incarnation"), nil, func() { invalidations2++ })
	w2.Raise(time.Unix(1700009000, 0))
	require.Equal(t, time.Unix(1700001000, 0), w2.Get())

	// Requests are required to provide a lease validation context.
	testutil.RequireEqualStatus(t, status.Error(codes.InvalidArgument, "No lease validation context provided"), w.ProcessLeaseValidationContext(nil))

	// Requests intended for another incarnation of the storage
	// server should be rejected.
	m := leaseValidationContext(1700000500, 1700000970, "hash2")
	m.SetExpectedIncarnation([]byte("other"))
	testutil.RequireEqualStatus(t, status.Error(codes.FailedPrecondition, "Request was intended for incarnation 6f74686572 of the storage server, while its current incarnation is 696e6361726e6174696f6e"), w.ProcessLeaseValidationContext(m))

	// Invalid values are rejected. The message of the error
	// returned by the Protobuf library is intentionally unstable,
	// so only check our own prefix.
	m = leaseValidationContext(1700000500, 1700000970, "hash2")
	m.GetWatermark().ClearObtainedAt()
	err := w.ProcessLeaseValidationContext(m)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.True(t, strings.HasPrefix(status.Convert(err).Message(), "Invalid time at which the watermark was obtained: "))

	// Watermarks that were obtained too long ago are rejected, as
	// data may have been lost in the meantime.
	testutil.RequireEqualStatus(t, status.Error(codes.Unavailable, "Watermark of the cluster was obtained 1m1s ago, which exceeds the maximum permitted age of 1m0s, meaning data may have been lost in the meantime"), w.ProcessLeaseValidationContext(leaseValidationContext(1700000500, 1700000939, "hash2")))

	// None of the requests so far should have caused leases to be
	// invalidated, nor should the rejected requests have affected
	// the watermark or the last incarnations hash.
	require.Equal(t, 0, invalidations)
	require.Equal(t, time.Unix(1700000200, 0), w.Get())
	require.Equal(t, []byte("hash1"), w.GetLastIncarnationsHash())

	// If the incarnations hash changes, leases of all objects stored
	// by the storage server should be invalidated, as objects
	// referenced by them may have been lost. This is done by raising
	// the storage server's own watermark, as that is reported
	// through GetWatermark() and propagated to all other storage
	// servers. The watermark of the cluster is unaffected.
	require.NoError(t, w.ProcessLeaseValidationContext(leaseValidationContext(1700000300, 1700000970, "hash2")))
	require.Equal(t, 1, invalidations)
	require.Equal(t, [][]byte{[]byte("hash1")}, hashesDuringInvalidation)
	require.Equal(t, time.Unix(1700000300, 0), w.Get())
	require.Equal(t, []byte("hash2"), w.GetLastIncarnationsHash())

	// Subsequent requests providing the same incarnations hash
	// should not cause leases to be invalidated again.
	require.NoError(t, w.ProcessLeaseValidationContext(leaseValidationContext(1700000300, 1700000970, "hash2")))
	require.Equal(t, 1, invalidations)

	// The same holds for the first request received by a storage
	// server whose last incarnations hash is unknown.
	require.NoError(t, w2.ProcessLeaseValidationContext(leaseValidationContext(0, 1700000970, "hash1")))
	require.Equal(t, 1, invalidations2)

	// Calling GetLastIncarnationsHash() on a nil Watermark is
	// permitted, as it may be called during shutdown before the
	// Watermark has been created.
	var nilWatermark *object.Watermark
	require.Nil(t, nilWatermark.GetLastIncarnationsHash())
}

// fakeLeaseValidationContextSource is a LeaseValidationContextSource
// that hands out lease validation contexts with increasing expected
// incarnations, and records reports of mismatches.
type fakeLeaseValidationContextSource struct {
	incarnation byte
	reported    [][]byte
}

func (s *fakeLeaseValidationContextSource) GetLeaseValidationContext(ctx context.Context) (*object_pb.LeaseValidationContext, error) {
	return object_pb.LeaseValidationContext_builder{
		ExpectedIncarnation: []byte{s.incarnation},
	}.Build(), nil
}

func (s *fakeLeaseValidationContextSource) ReportIncarnationMismatch(leaseValidationContext *object_pb.LeaseValidationContext) {
	s.reported = append(s.reported, leaseValidationContext.GetExpectedIncarnation())
	s.incarnation++
}

func TestCallWithLeaseValidationContext(t *testing.T) {
	ctx := context.Background()

	t.Run("NoSource", func(t *testing.T) {
		// Without a source, the call should be performed once
		// without a lease validation context.
		result, err := object.CallWithLeaseValidationContext(ctx, nil, func(m *object_pb.LeaseValidationContext) (int, error) {
			require.Nil(t, m)
			return 42, nil
		})
		require.NoError(t, err)
		require.Equal(t, 42, result)
	})

	t.Run("RetryOnIncarnationMismatch", func(t *testing.T) {
		// Incarnation mismatches should be reported, and the
		// call should be retried with a new lease validation
		// context.
		source := &fakeLeaseValidationContextSource{}
		result, err := object.CallWithLeaseValidationContext(ctx, source, func(m *object_pb.LeaseValidationContext) (int, error) {
			if m.GetExpectedIncarnation()[0] < 2 {
				return 0, status.Error(codes.FailedPrecondition, "Incarnation mismatch")
			}
			return int(m.GetExpectedIncarnation()[0]), nil
		})
		require.NoError(t, err)
		require.Equal(t, 2, result)
		require.Equal(t, [][]byte{{0}, {1}}, source.reported)
	})

	t.Run("RetriesExhausted", func(t *testing.T) {
		// The number of retries should be bounded.
		source := &fakeLeaseValidationContextSource{}
		calls := 0
		_, err := object.CallWithLeaseValidationContext(ctx, source, func(m *object_pb.LeaseValidationContext) (int, error) {
			calls++
			return 0, status.Error(codes.FailedPrecondition, "Incarnation mismatch")
		})
		testutil.RequireEqualStatus(t, status.Error(codes.FailedPrecondition, "Incarnation mismatch"), err)
		require.Equal(t, 4, calls)
	})

	t.Run("OtherError", func(t *testing.T) {
		// Other errors should not cause retries.
		source := &fakeLeaseValidationContextSource{}
		_, err := object.CallWithLeaseValidationContext(ctx, source, func(m *object_pb.LeaseValidationContext) (int, error) {
			return 0, status.Error(codes.Unavailable, "Server offline")
		})
		testutil.RequireEqualStatus(t, status.Error(codes.Unavailable, "Server offline"), err)
		require.Empty(t, source.reported)
	})
}
