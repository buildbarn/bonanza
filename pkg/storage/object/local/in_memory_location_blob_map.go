package local

import (
	"sync/atomic"
)

type inMemoryLocationBlobMap struct {
	data         []byte
	nextLocation atomic.Uint64
}

// NewInMemoryLocationBlobMap creates a location-blob map that is backed
// by a simple fixed size byte slice that resides in memory. This may
// not be useful for most production worthy setups, as they need to
// store more data than fits in memory.
func NewInMemoryLocationBlobMap(sizeBytes int) LocationBlobMap {
	return &inMemoryLocationBlobMap{
		data: make([]byte, sizeBytes),
	}
}

func (lbm *inMemoryLocationBlobMap) Get(location uint64, sizeBytes int) ([]byte, error) {
	output := make([]byte, 0, sizeBytes)
	outputRemaining := sizeBytes - len(output)

	offset := location % uint64(len(lbm.data))
	inputRemaining := uint64(len(lbm.data)) - offset
	for inputRemaining < uint64(outputRemaining) {
		// The blob wraps around the end of the byte array. Copy
		// everything up to the end of the byte array and retry
		// at the start.
		output = append(output, lbm.data[offset:][:inputRemaining]...)
		outputRemaining -= int(inputRemaining)

		offset = 0
		inputRemaining = uint64(len(lbm.data))
	}

	output = append(output, lbm.data[offset:][:outputRemaining]...)
	return output, nil
}

func (lbm *inMemoryLocationBlobMap) Put(data []byte) (uint64, error) {
	// Determine the location at which to store the new blob.
	var location uint64
	for {
		oldNextLocation := lbm.nextLocation.Load()
		var newNextLocation uint64
		location, newNextLocation = allocateLocation(lbm.nextLocation.Load(), len(data))
		if lbm.nextLocation.CompareAndSwap(oldNextLocation, newNextLocation) {
			break
		}
	}

	// The blob may wrap around the end of the byte array. Perform
	// repeated copies to the byte array until the input is
	// exhausted.
	offset := location % uint64(len(lbm.data))
	for len(data) > 0 {
		data = data[copy(lbm.data[offset:], data):]
		offset = 0
	}
	return location, nil
}

func (lbm *inMemoryLocationBlobMap) GetNextPutLocation() uint64 {
	return lbm.nextLocation.Load()
}
