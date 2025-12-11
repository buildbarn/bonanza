package local

import (
	"context"
	"encoding/binary"
	"sync"

	"bonanza.build/pkg/storage/object"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type store struct {
	currentRegionSizeBytes uint64
	newRegionSizeBytes     uint64

	lock                 *sync.RWMutex
	referenceLocationMap ReferenceLocationMap
	locationBlobMap      LocationBlobMap
	epochList            EpochList

	writeLocks [1 << 8]sync.Mutex
}

// NewStore creates an object store that uses locally connected disks as
// its backing store.
func NewStore(
	lock *sync.RWMutex,
	referenceLocationMap ReferenceLocationMap,
	locationBlobMap LocationBlobMap,
	epochList EpochList,
	currentRegionSizeBytes uint64,
	newRegionSizeBytes uint64,
) object.Store[object.FlatReference, struct{}] {
	return &store{
		lock:                 lock,
		referenceLocationMap: referenceLocationMap,
		locationBlobMap:      locationBlobMap,
		epochList:            epochList,

		currentRegionSizeBytes: currentRegionSizeBytes,
		newRegionSizeBytes:     newRegionSizeBytes,
	}
}

func (s *store) getObjectLocation(reference object.FlatReference) (uint64, bool, error) {
	s.lock.RLock()
	location, err := s.referenceLocationMap.Get(reference, s.epochList)
	s.lock.RUnlock()
	if err != nil {
		return 0, false, err
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
	return location, needsRefresh, nil
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
func (s *store) maybeWriteObject(reference object.FlatReference, contents *object.Contents) error {
	// Acquire a lock throughout the entire process to ensure that
	// no data is written redundantly. Let the first byte of the
	// reference determine which lock to acquire, so that we can
	// still upload objects in parallel.
	writeLock := &s.writeLocks[reference.GetRawFlatReference()[0]]
	writeLock.Lock()
	defer writeLock.Unlock()

	// Check whether the object exists or needs to be refreshed.
	if _, needsRefresh, err := s.getObjectLocation(reference); err != nil {
		if status.Code(err) != codes.NotFound {
			return err
		}
	} else if !needsRefresh {
		return nil
	}

	// Object does not exist. Allocate space for storing the object
	// and write its contents to disk.
	data := contents.GetFullData()
	location, err := s.locationBlobMap.Put(data)
	if err != nil {
		return err
	}

	s.lock.Lock()
	err = s.epochList.FinalizeWriteUpToLocation(location + uint64(len(data)))
	if err == nil {
		err = s.referenceLocationMap.Put(reference, location, s.epochList)
	}
	s.lock.Unlock()
	return err
}

func (s *store) DownloadObject(ctx context.Context, reference object.FlatReference) (*object.Contents, error) {
	location, needsRefresh, err := s.getObjectLocation(reference)
	if err != nil {
		return nil, err
	}
	contents, err := s.readObjectAtLocation(reference, location)
	if err != nil {
		return nil, err
	}
	if needsRefresh {
		if err := s.maybeWriteObject(reference, contents); err != nil {
			return nil, err
		}
	}
	return contents, nil
}

func (s *store) UploadObject(ctx context.Context, reference object.FlatReference, contents *object.Contents, childrenLeases []struct{}, wantContentsIfIncomplete bool) (object.UploadObjectResult[struct{}], error) {
	if location, needsRefresh, err := s.getObjectLocation(reference); err != nil {
		if status.Code(err) != codes.NotFound {
			return nil, err
		}

		if contents == nil {
			return object.UploadObjectMissing[struct{}]{}, nil
		}
		if err := s.maybeWriteObject(reference, contents); err != nil {
			return nil, err
		}
	} else if needsRefresh {
		if contents == nil {
			contents, err = s.readObjectAtLocation(reference, location)
			if err != nil {
				if status.Code(err) != codes.NotFound {
					return nil, err
				}
				return object.UploadObjectMissing[struct{}]{}, nil
			}
		}
		if err := s.maybeWriteObject(reference, contents); err != nil {
			return nil, err
		}
	}
	return object.UploadObjectComplete[struct{}]{}, nil
}
