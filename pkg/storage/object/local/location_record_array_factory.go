package local

import (
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/blockdevice"
	"github.com/buildbarn/bb-storage/pkg/lossymap"
)

type locationRecordArrayFactory struct{}

// LocationRecordArrayFactory is a factory for creating backing stores
// of the location-record map.
var LocationRecordArrayFactory lossymap.RecordArrayFactory[object.FlatReference, uint64, EpochIDResolver] = locationRecordArrayFactory{}

func (locationRecordArrayFactory) GetBlockDeviceBackedRecordSize() int {
	return blockDeviceBackedReferenceLocationRecordSize
}

func (locationRecordArrayFactory) NewInMemoryRecordArray(entries int) ReferenceLocationRecordArray {
	return NewInMemoryReferenceLocationRecordArray(entries)
}

func (locationRecordArrayFactory) NewBlockDeviceBackedRecordArray(blockDevice blockdevice.BlockDevice) ReferenceLocationRecordArray {
	return NewBlockDeviceBackedReferenceLocationRecordArray(blockDevice)
}
