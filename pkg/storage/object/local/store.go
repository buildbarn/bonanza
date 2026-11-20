package local

import (
	"context"
	"encoding/binary"
	"sync"
	"time"

	"bonanza.build/pkg/storage/object"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Store of objects that is backed by locally connected disks. In
// addition to providing the regular object store operations, it is
// capable of reporting its watermark: the point in time at or after
// which all data written to the store is still present.
//
// Leases returned by UploadObject() are expressed as points in time.
// The object is guaranteed to be present as long as the watermark does
// not exceed the lease.
type Store interface {
	object.Store[object.FlatReference, time.Time]

	GetWatermark() time.Time
}

type store struct {
	currentRegionSizeBytes uint64
	newRegionSizeBytes     uint64
	minimumSafeSince       func() time.Time

	lock                 *sync.RWMutex
	referenceLocationMap ReferenceLocationMap
	locationBlobMap      LocationBlobMap
	epochList            EpochList

	writeLocks [1 << 8]sync.Mutex
}

// NewStore creates an object store that uses locally connected disks as
// its backing store.
//
// If minimumSafeSince is provided, objects whose lease is lower than
// the value it returns are rewritten when UploadObject() is called.
// This ensures that the leases returned by UploadObject() remain valid
// for a sufficient amount of time.
func NewStore(
	lock *sync.RWMutex,
	referenceLocationMap ReferenceLocationMap,
	locationBlobMap LocationBlobMap,
	epochList EpochList,
	currentRegionSizeBytes uint64,
	newRegionSizeBytes uint64,
	minimumSafeSince func() time.Time,
) Store {
	return &store{
		lock:                 lock,
		referenceLocationMap: referenceLocationMap,
		locationBlobMap:      locationBlobMap,
		epochList:            epochList,

		currentRegionSizeBytes: currentRegionSizeBytes,
		newRegionSizeBytes:     newRegionSizeBytes,
		minimumSafeSince:       minimumSafeSince,
	}
}

func (s *store) getObjectLocation(reference object.FlatReference) (uint64, bool, time.Time, error) {
	s.lock.RLock()
	location, err := s.referenceLocationMap.Get(reference, s.epochList)
	var safeSince time.Time
	if err == nil {
		safeSince = s.epochList.GetSafeSince(location)
	}
	s.lock.RUnlock()
	if err != nil {
		return 0, false, time.Time{}, err
	}

	// Determine whether the object needs to be refreshed based on
	// its position within the ring buffer. Each object has a
	// deterministic refresh threshold within the current region,
	// derived from the object's reference. This spreads refresh
	// operations evenly across all objects.
	distanceFromMaximum := int64(s.locationBlobMap.GetNextPutLocation() - location)

	// Compute a deterministic threshold for this object within the
	// current region. Objects whose distance from maximum goes
	// above (newRegionSizeBytes + threshold) need to be refreshed.
	// XOR with location ensures the threshold changes each time the
	// object is relocated, preventing the same objects from always
	// being refreshed earlier than others.
	threshold := (location ^ binary.LittleEndian.Uint64(reference.GetRawFlatReference())) % s.currentRegionSizeBytes
	needsRefresh := distanceFromMaximum > int64(s.newRegionSizeBytes+threshold)

	// Also refresh the object if its lease is too low. Without
	// this, leases of objects could not be renewed.
	if !needsRefresh && s.minimumSafeSince != nil && safeSince.Before(s.minimumSafeSince()) {
		needsRefresh = true
	}
	return location, needsRefresh, safeSince, nil
}

func (s *store) readObjectAtLocation(reference object.FlatReference, location uint64) (*object.Contents, error) {
	sizeBytes := reference.GetSizeBytes()
	data, err := s.locationBlobMap.Get(location, sizeBytes)
	if err != nil {
		return nil, err
	}

	contents, err := object.NewContentsFromFullData(reference.GetLocalReference(), data)
	if err != nil {
		// The data we read from disk was corrupted. This can
		// happen due to misconfiguration (e.g., pointing
		// multiple storage backends to the same block device)
		// or actual data loss. It can also happen under normal
		// operation if the object was positioned right after
		// the write cursor, and additional objects were
		// uploaded while the object was being read. This causes
		// the object to be overwritten.
		//
		// Report the object as being absent. Also discard all
		// data up to and including this object, as we can no
		// longer trust that data.
		s.lock.Lock()
		s.epochList.DiscardUpToLocation(location + uint64(sizeBytes))
		s.lock.Unlock()
		return nil, status.Error(codes.NotFound, "Object was assumed to exist, but its contents were invalid")
	}
	return contents, nil
}

// maybeWriteObject writes an object to storage, if and only if it does
// not exist or needs to be refreshed. This filters out redundant object
// uploads or refreshes, which may occur if multiple clients interact
// with the same object.
func (s *store) maybeWriteObject(reference object.FlatReference, contents *object.Contents) (time.Time, error) {
	// Acquire a lock throughout the entire process to ensure that
	// no data is written redundantly. Let the first byte of the
	// reference determine which lock to acquire, so that we can
	// still upload objects in parallel.
	writeLock := &s.writeLocks[reference.GetRawFlatReference()[0]]
	writeLock.Lock()
	defer writeLock.Unlock()

	// Check whether the object exists or needs to be refreshed.
	if _, needsRefresh, safeSince, err := s.getObjectLocation(reference); err != nil {
		if status.Code(err) != codes.NotFound {
			return time.Time{}, err
		}
	} else if !needsRefresh {
		return safeSince, nil
	}

	// Object does not exist. Allocate space for storing the object
	// and write its contents to disk.
	data := contents.GetFullData()
	location, err := s.locationBlobMap.Put(data)
	if err != nil {
		return time.Time{}, err
	}

	s.lock.Lock()
	var safeSince time.Time
	err = s.epochList.FinalizeWriteUpToLocation(location + uint64(len(data)))
	if err == nil {
		err = s.referenceLocationMap.Put(reference, location, s.epochList)
		if err == nil {
			safeSince = s.epochList.GetSafeSince(location)
		} else {
			// The reference-location map may have
			// displaced an existing entry before failing,
			// meaning an object of which we don't know the
			// location may no longer be accessible. Its
			// lease may have been handed out, so invalidate
			// all leases.
			s.epochList.InvalidateLeases()
		}
	}
	s.lock.Unlock()
	return safeSince, err
}

func (s *store) DownloadObject(ctx context.Context, reference object.FlatReference) (*object.Contents, error) {
	location, needsRefresh, _, err := s.getObjectLocation(reference)
	if err != nil {
		return nil, err
	}
	contents, err := s.readObjectAtLocation(reference, location)
	if err != nil {
		return nil, err
	}
	if needsRefresh {
		if _, err := s.maybeWriteObject(reference, contents); err != nil {
			return nil, err
		}
	}
	return contents, nil
}

func (s *store) UploadObject(ctx context.Context, reference object.FlatReference, contents *object.Contents, childrenLeases []time.Time, wantContentsIfIncomplete bool) (object.UploadObjectResult[time.Time], error) {
	location, needsRefresh, safeSince, err := s.getObjectLocation(reference)
	if err != nil {
		if status.Code(err) != codes.NotFound {
			return nil, err
		}

		if contents == nil {
			return object.UploadObjectMissing[time.Time]{}, nil
		}
		safeSince, err = s.maybeWriteObject(reference, contents)
		if err != nil {
			return nil, err
		}
	} else if needsRefresh {
		if contents == nil {
			contents, err = s.readObjectAtLocation(reference, location)
			if err != nil {
				if status.Code(err) != codes.NotFound {
					return nil, err
				}
				return object.UploadObjectMissing[time.Time]{}, nil
			}
		}
		safeSince, err = s.maybeWriteObject(reference, contents)
		if err != nil {
			return nil, err
		}
	}
	return object.UploadObjectComplete[time.Time]{
		Lease: safeSince,
	}, nil
}

func (s *store) GetWatermark() time.Time {
	s.lock.RLock()
	defer s.lock.RUnlock()
	return s.epochList.GetWatermark()
}
