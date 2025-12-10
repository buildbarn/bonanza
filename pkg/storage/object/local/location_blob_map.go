package local

import (
	"math/bits"
)

// LocationBlobMap is a map of object location to its contents. Every
// time an object is stored, it is assigned a location. Locations are
// expected to increase monotonically. As storage space is only finite,
// implementations of LocationBlobMap are expected to behave like ring
// buffers. Once space is exhausted, objects with the lowest location
// values are expected to be overwritten.
type LocationBlobMap interface {
	Get(location uint64, sizeBytes int) ([]byte, error)
	Put([]byte) (uint64, error)
	GetNextPutLocation() uint64
}

// allocateLocation is a helper function for allocating a location for
// storing a blob, given the existing cursor value and the size of the
// blob to be stored.
func allocateLocation(nextLocation uint64, sizeBytes int) (uint64, uint64) {
	if newNextLocation, carryOut := bits.Add64(nextLocation, uint64(sizeBytes), 0); carryOut == 0 {
		return nextLocation, newNextLocation
	}

	// An overflow occurred while attempting to allocate space. This
	// is an extremely unlikely event, as it requires us to have
	// stored 2^64 of data on a single block device, which is the
	// equivalent of having cycled through a 16 TB disk a million
	// times.
	//
	// However, if this situation does occur, we start allocating
	// from location zero again. This will cause the EpochList to
	// reinitialize, meaning we effectively lose all data. This is
	// suboptimal, but so unlikely that it's not worth addressing.
	return 0, uint64(sizeBytes)
}
