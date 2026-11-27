package local

import (
	"testing"
	"time"

	pb "bonanza.build/pkg/proto/storage/object/local"

	"github.com/stretchr/testify/require"

	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestLocationTimeline(t *testing.T) {
	t0 := time.Unix(1700000000, 0).UTC()

	t.Run("SafeSinceAndWatermark", func(t *testing.T) {
		var lt locationTimeline
		lt.record(0, t0)
		lt.record(100, t0.Add(10*time.Second))
		lt.record(200, t0.Add(20*time.Second))

		// Entries that are too close to each other in time,
		// or don't increase the location are ignored.
		lt.record(300, t0.Add(20*time.Second+time.Millisecond))
		lt.record(150, t0.Add(30*time.Second))

		require.Equal(t, t0, lt.getSafeSince(0))
		require.Equal(t, t0, lt.getSafeSince(99))
		require.Equal(t, t0.Add(10*time.Second), lt.getSafeSince(100))
		require.Equal(t, t0.Add(20*time.Second), lt.getSafeSince(1000))

		// Once data before location 50 is gone, objects stored
		// at locations 0 to 99 may no longer be present.
		require.Equal(t, t0.Add(10*time.Second), lt.getWatermark(50, t0.Add(40*time.Second)))
		require.True(t, lt.getSafeSince(99).Before(lt.getWatermark(50, t0.Add(40*time.Second))))
		require.False(t, lt.getSafeSince(100).Before(lt.getWatermark(50, t0.Add(40*time.Second))))

		// If all data is about to be overwritten, the watermark
		// is the current time.
		require.Equal(t, t0.Add(40*time.Second), lt.getWatermark(201, t0.Add(40*time.Second)))

		// The watermark never decreases.
		require.Equal(t, t0.Add(40*time.Second), lt.getWatermark(0, t0.Add(50*time.Second)))
	})

	t.Run("DiscardLocation", func(t *testing.T) {
		var lt locationTimeline
		lt.record(0, t0)
		lt.record(100, t0.Add(10*time.Second))
		lt.record(200, t0.Add(20*time.Second))

		// Discarding the reference-location map entry of an
		// object stored at location 150 invalidates the leases
		// of all objects stored before location 200.
		lt.discardLocation(150)
		watermark := lt.getWatermark(0, t0.Add(30*time.Second))
		require.True(t, lt.getSafeSince(150).Before(watermark))
		require.True(t, lt.getSafeSince(199).Before(watermark))
		require.False(t, lt.getSafeSince(200).Before(watermark))
	})

	t.Run("ThinningPreservesValidity", func(t *testing.T) {
		// Hand out leases while writing lots of data. Leases of
		// objects that are overwritten must always be lower than
		// the watermark, even if entries of the timeline are
		// thinned out in the meantime.
		const span = 10000
		var lt locationTimeline
		type issuedLease struct {
			location  uint64
			safeSince time.Time
		}
		var leases []issuedLease
		now := t0
		for location := uint64(0); location < 50*span; location += 37 {
			now = now.Add(700 * time.Millisecond)
			lt.record(location, now)
			leases = append(leases, issuedLease{
				location:  location,
				safeSince: lt.getSafeSince(location),
			})

			if location > span {
				minimumLocation := location - span
				lt.prune(minimumLocation)
				watermark := lt.getWatermark(minimumLocation, now)
				for _, lease := range leases {
					if lease.location < minimumLocation {
						require.True(t, lease.safeSince.Before(watermark))
					}
				}
				for len(leases) > 0 && leases[0].location < minimumLocation {
					leases = leases[1:]
				}
			}
			require.LessOrEqual(t, len(lt.entries), locationTimelineMaximumEntries)
		}
	})

	t.Run("PersistentState", func(t *testing.T) {
		var lt locationTimeline
		lt.record(0, t0)
		lt.record(100, t0.Add(10*time.Second))
		lt.record(200, t0.Add(20*time.Second))
		lt.raiseFloor(t0.Add(5 * time.Second))

		// Entries that refer to data that has not been
		// synchronized are not persisted.
		entries, watermark := lt.getPersistentState(150)
		require.Equal(t, []*pb.LocationTimelineEntry{
			pb.LocationTimelineEntry_builder{Location: 0, Time: timestamppb.New(t0)}.Build(),
			pb.LocationTimelineEntry_builder{Location: 100, Time: timestamppb.New(t0.Add(10 * time.Second))}.Build(),
		}, entries)
		require.Equal(t, timestamppb.New(t0.Add(5*time.Second)), watermark)

		reloaded := newLocationTimelineFromPersistentState(entries, watermark, 150)
		require.Equal(t, t0.Add(10*time.Second), reloaded.getSafeSince(120))
		require.Equal(t, t0.Add(5*time.Second), reloaded.getWatermark(0, t0.Add(30*time.Second)))
	})
}
