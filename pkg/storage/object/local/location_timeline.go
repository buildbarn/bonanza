package local

import (
	"sort"
	"time"

	pb "bonanza.build/pkg/proto/storage/object/local"

	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// locationTimelineGranularity is the minimum amount of time
	// between successive entries in a locationTimeline.
	locationTimelineGranularity = time.Second

	// locationTimelineMaximumEntries is the maximum number of
	// entries a locationTimeline retains. When exceeded, the
	// resolution of the oldest half of the timeline is halved.
	locationTimelineMaximumEntries = 1024
)

type locationTimelineEntry struct {
	location uint64
	time     time.Time
}

// locationTimeline keeps track of the points in time at which data was
// written to locations in the location-blob map. It is used to compute
// leases ("safe since" timestamps) for objects and the watermark of the
// store.
//
// Entries are strictly increasing in both location and time. Leases
// and the watermark are derived from the same entries, which ensures
// that the lease of an object that is no longer present is always
// lower than the watermark. Removing entries only causes leases to
// become lower and the watermark to become higher, meaning that the
// timeline may be thinned out at any point without compromising
// correctness.
type locationTimeline struct {
	entries []locationTimelineEntry

	// floor is a lower bound on the watermark. It is raised when
	// data is lost in ways that cannot be expressed by raising the
	// minimum location of the epoch list (e.g., records being
	// discarded from the reference-location map).
	floor time.Time

	// lastWatermark ensures that the watermark never decreases.
	lastWatermark time.Time
}

func newLocationTimelineFromPersistentState(entries []*pb.LocationTimelineEntry, watermark *timestamppb.Timestamp, maximumLocation uint64) locationTimeline {
	var lt locationTimeline
	for _, entry := range entries {
		e := locationTimelineEntry{
			location: entry.Location,
			time:     entry.Time.AsTime(),
		}
		// Discard entries that refer to data that was not
		// synchronized, or are not properly ordered.
		if e.location > maximumLocation {
			break
		}
		if len(lt.entries) > 0 {
			last := &lt.entries[len(lt.entries)-1]
			if e.location <= last.location || !e.time.After(last.time) {
				continue
			}
		}
		lt.entries = append(lt.entries, e)
	}
	if watermark != nil {
		lt.floor = watermark.AsTime()
		lt.lastWatermark = lt.floor
	}
	return lt
}

// reset discards all entries in the timeline. This is called when all
// data stored in the location-blob map is lost.
func (lt *locationTimeline) reset(now time.Time) {
	lt.entries = lt.entries[:0]
	lt.raiseFloor(now)
}

// raiseFloor raises the lower bound of the watermark.
func (lt *locationTimeline) raiseFloor(t time.Time) {
	if lt.floor.Before(t) {
		lt.floor = t
	}
}

// record that writing data at a given location starts at a given point
// in time.
func (lt *locationTimeline) record(location uint64, now time.Time) {
	if len(lt.entries) > 0 {
		last := &lt.entries[len(lt.entries)-1]
		if location <= last.location || now.Before(last.time.Add(locationTimelineGranularity)) {
			return
		}
	}
	if len(lt.entries) >= locationTimelineMaximumEntries {
		// Halve the resolution of the oldest half of the
		// timeline.
		half := len(lt.entries) / 2
		kept := 0
		for i := 0; i < half; i += 2 {
			lt.entries[kept] = lt.entries[i]
			kept++
		}
		kept += copy(lt.entries[kept:], lt.entries[half:])
		lt.entries = lt.entries[:kept]
	}
	lt.entries = append(lt.entries, locationTimelineEntry{
		location: location,
		time:     now,
	})
}

// prune entries that are no longer needed, because the data they
// describe has been overwritten. The last entry at or before the
// minimum location is retained, as it's needed to compute leases of
// objects stored right after it.
func (lt *locationTimeline) prune(minimumLocation uint64) {
	i := 0
	for i+1 < len(lt.entries) && lt.entries[i+1].location <= minimumLocation {
		i++
	}
	if i > 0 {
		lt.entries = lt.entries[:copy(lt.entries, lt.entries[i:])]
	}
}

// getSafeSince returns the lease of an object stored at a given
// location. The zero time is returned if the location precedes all
// entries in the timeline, meaning the object needs to be rewritten
// before a valid lease can be handed out.
func (lt *locationTimeline) getSafeSince(location uint64) time.Time {
	i := sort.Search(len(lt.entries), func(i int) bool {
		return lt.entries[i].location > location
	})
	if i == 0 {
		return time.Time{}
	}
	return lt.entries[i-1].time
}

// getLookaheadBytes estimates the number of bytes of data that will be
// written within a given amount of time, based on the amount of data
// written during the same amount of time in the past. The estimate is
// doubled to account for fluctuations in the write rate.
func (lt *locationTimeline) getLookaheadBytes(maximumLocation uint64, lookahead time.Duration, now time.Time) uint64 {
	if lookahead <= 0 || len(lt.entries) == 0 {
		return 0
	}
	cutoff := now.Add(-lookahead)
	i := sort.Search(len(lt.entries), func(i int) bool {
		return lt.entries[i].time.After(cutoff)
	})
	if i > 0 {
		i--
	}
	if start := lt.entries[i].location; maximumLocation > start {
		return 2 * (maximumLocation - start)
	}
	return 0
}

// getWatermark returns the point in time at or after which all data
// written to the location-blob map will still be present once the
// minimum location has been raised to a given value.
func (lt *locationTimeline) getWatermark(minimumLocation uint64, now time.Time) time.Time {
	i := sort.Search(len(lt.entries), func(i int) bool {
		return lt.entries[i].location >= minimumLocation
	})
	var watermark time.Time
	if i < len(lt.entries) {
		watermark = lt.entries[i].time
	} else {
		// All data that is currently stored is about to be
		// overwritten. Only data written in the future is
		// considered to be safe.
		watermark = now
		if n := len(lt.entries); n > 0 && !watermark.After(lt.entries[n-1].time) {
			watermark = lt.entries[n-1].time.Add(time.Nanosecond)
		}
	}
	if watermark.Before(lt.floor) {
		watermark = lt.floor
	}
	if watermark.Before(lt.lastWatermark) {
		watermark = lt.lastWatermark
	}
	lt.lastWatermark = watermark
	return watermark
}

// discardLocation is called when the reference-location map entry for
// an object stored at a given location is discarded. Even though the
// data of the object is still present, it can no longer be accessed.
// Raise the floor of the watermark above the lease of the object.
func (lt *locationTimeline) discardLocation(location uint64) {
	if safeSince := lt.getSafeSince(location); !safeSince.IsZero() {
		lt.raiseFloor(safeSince.Add(time.Nanosecond))
	}
}

func (lt *locationTimeline) getPersistentState(maximumLocation uint64) ([]*pb.LocationTimelineEntry, *timestamppb.Timestamp) {
	entries := make([]*pb.LocationTimelineEntry, 0, len(lt.entries))
	for _, entry := range lt.entries {
		if entry.location > maximumLocation {
			break
		}
		entries = append(entries, &pb.LocationTimelineEntry{
			Location: entry.location,
			Time:     timestamppb.New(entry.time),
		})
	}
	watermark := lt.floor
	if watermark.Before(lt.lastWatermark) {
		watermark = lt.lastWatermark
	}
	var watermarkMessage *timestamppb.Timestamp
	if !watermark.IsZero() {
		watermarkMessage = timestamppb.New(watermark)
	}
	return entries, watermarkMessage
}
