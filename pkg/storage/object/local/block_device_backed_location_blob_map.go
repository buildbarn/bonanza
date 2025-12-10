package local

import (
	"sync"

	"github.com/buildbarn/bb-storage/pkg/blockdevice"
)

type blockDeviceBackedLocationBlobMap struct {
	blockDevice     blockdevice.BlockDevice
	sectorSizeBytes int
	sectorCount     int64

	lock         sync.RWMutex
	nextLocation uint64
	sharedSector *sharedSector
}

// NewBlockDeviceBackedLocationBlobMap creates a location-blob map that
// is backed by a block device. This implementation is the one that is
// most suitable for production workloads.
func NewBlockDeviceBackedLocationBlobMap(blockDevice blockdevice.BlockDevice, sectorSizeBytes int, sectorCount int64, initialLocation uint64) LocationBlobMap {
	return &blockDeviceBackedLocationBlobMap{
		blockDevice:     blockDevice,
		sectorSizeBytes: sectorSizeBytes,
		sectorCount:     sectorCount,

		// Round the initial location at which we should start
		// writing up to the next sector. If we wanted to resume
		// writing at an exact byte offset, we'd need to reload
		// the sector from disk, which is not worth the hassle.
		nextLocation: (initialLocation + uint64(sectorSizeBytes) - 1) / uint64(sectorSizeBytes) * uint64(sectorSizeBytes),
	}
}

func (lbm *blockDeviceBackedLocationBlobMap) Get(location uint64, sizeBytes int) ([]byte, error) {
	output := make([]byte, sizeBytes)
	outputRemaining := output

	blockDeviceSizeBytes := uint64(lbm.sectorSizeBytes) * uint64(lbm.sectorCount)
	offset := location % blockDeviceSizeBytes
	inputRemaining := blockDeviceSizeBytes - offset
	for inputRemaining < uint64(len(outputRemaining)) {
		// Object wraps around the end of the block device. Read
		// data until the end of the block device.
		if _, err := lbm.blockDevice.ReadAt(outputRemaining[:inputRemaining], int64(offset)); err != nil {
			return nil, err
		}
		outputRemaining = outputRemaining[inputRemaining:]

		offset = 0
		inputRemaining = blockDeviceSizeBytes
	}

	if _, err := lbm.blockDevice.ReadAt(outputRemaining, int64(offset)); err != nil {
		return nil, err
	}
	return output, nil
}

func (lbm *blockDeviceBackedLocationBlobMap) Put(data []byte) (uint64, error) {
	// Determine the location at which to store the object.
	lbm.lock.Lock()
	var location uint64
	location, lbm.nextLocation = allocateLocation(lbm.nextLocation, len(data))

	// We only want to perform sector aligned writes, as writing to
	// sectors partially may cause the kernel to issue reads to
	// merge the sector's contents. However, we do not want to align
	// objects at sector boundaries as that would lead to large
	// losses. Furthermore, objects may wrap around the end of the
	// block device. This means that an object may need to be
	// written in up to four separate parts:
	//
	// 1. If the start of the object is not sector aligned, a
	//    leading part that shares the same sector as the object(s)
	//    before it.
	//
	// 2. Zero or more full sectors of data.
	//
	// 3. If the object wraps around the end of the block device,
	//    zero or more full sectors of data stored at the start of
	//    the block device.
	//
	// 4. If the end of the object is not sector aligned, a trailing
	//    part that shares the same sector as the object(s) after it.
	//
	// Determine which of the four cases above are relevant.
	var firstSector *sharedSector
	firstSectorIndex := location / uint64(lbm.sectorSizeBytes) % uint64(lbm.sectorCount)
	var fullSectorIndex uint64
	if location%uint64(lbm.sectorSizeBytes) == 0 {
		// The start of the object is sector aligned.
		fullSectorIndex = firstSectorIndex
	} else {
		// The start of the object is not sector aligned.
		firstSector = lbm.sharedSector
		fullSectorIndex = (firstSectorIndex + 1) % uint64(lbm.sectorCount)
	}

	var lastSector *sharedSector
	var lastSectorIndex uint64
	if lbm.nextLocation%uint64(lbm.sectorSizeBytes) == 0 {
		// The end of the object is sector aligned.
		lbm.sharedSector = nil
	} else {
		// The end of the object is not sector aligned.
		//
		// If the sector in which it ends is not the same as the
		// one in which it starts, this indicates the start of a
		// new sector that is shared by multiple objects.
		lastSectorIndex = lbm.nextLocation / uint64(lbm.sectorSizeBytes) % uint64(lbm.sectorCount)
		if firstSector == nil || firstSectorIndex != lastSectorIndex {
			lbm.sharedSector = &sharedSector{
				data: make([]byte, lbm.sectorSizeBytes),
			}
		}
		lastSector = lbm.sharedSector
	}
	lbm.lock.Unlock()

	// Case 1: write the leading partial sector.
	if firstSector != nil {
		firstSector.lock.Lock()
		data = data[copy(firstSector.data[location%uint64(lbm.sectorSizeBytes):], data):]
		_, err := lbm.blockDevice.WriteAt(firstSector.data, int64(firstSectorIndex)*int64(lbm.sectorSizeBytes))
		firstSector.lock.Unlock()
		if err != nil {
			return 0, err
		}
	}

	// Cases 2 and 3: write full sectors.
	for len(data) >= lbm.sectorSizeBytes {
		availableBytes := int(min(
			uint64(len(data)/lbm.sectorSizeBytes),
			uint64(lbm.sectorCount)-fullSectorIndex),
		) * lbm.sectorSizeBytes
		if _, err := lbm.blockDevice.WriteAt(data[:availableBytes], int64(fullSectorIndex)*int64(lbm.sectorSizeBytes)); err != nil {
			return 0, err
		}
		data = data[availableBytes:]
		fullSectorIndex = 0
	}

	// Case 4: write the trailing partial sector.
	if len(data) > 0 {
		lastSector.lock.Lock()
		copy(lastSector.data, data)
		_, err := lbm.blockDevice.WriteAt(lastSector.data, int64(lastSectorIndex)*int64(lbm.sectorSizeBytes))
		lastSector.lock.Unlock()
		if err != nil {
			return 0, err
		}
	}
	return location, nil
}

func (lbm *blockDeviceBackedLocationBlobMap) GetNextPutLocation() uint64 {
	lbm.lock.RLock()
	defer lbm.lock.RUnlock()
	return lbm.nextLocation
}

// sharedSector contains the bookkeeping of a single sector of storage
// that stores data belonging to more than one object. Calls to
// WriteAt() against such a sector must be synchronized, so that
// subsequent calls don't erase data that was written previously.
type sharedSector struct {
	lock sync.Mutex
	data []byte
}
