package local

import (
	"bonanza.build/pkg/storage/tag"

	"github.com/buildbarn/bb-storage/pkg/blockdevice"
	"github.com/buildbarn/bb-storage/pkg/lossymap"
)

type tagRecordArrayFactory struct{}

// TagRecordArrayFactory is capable of creating backing stores for Tag
// Store. These backing stores can either reside in memory or on on
// disk.
var TagRecordArrayFactory lossymap.RecordArrayFactory[tag.Key, ValueWithLease, struct{}] = tagRecordArrayFactory{}

func (tagRecordArrayFactory) GetBlockDeviceBackedRecordSize() int {
	return blockDeviceBackedReferenceTagRecordSize
}

func (tagRecordArrayFactory) NewInMemoryRecordArray(entries int) lossymap.RecordArray[tag.Key, ValueWithLease, struct{}] {
	return lossymap.NewSimpleRecordArray[tag.Key, ValueWithLease](entries)
}

func (tagRecordArrayFactory) NewBlockDeviceBackedRecordArray(blockDevice blockdevice.BlockDevice) lossymap.RecordArray[tag.Key, ValueWithLease, struct{}] {
	return NewBlockDeviceBackedReferenceTagRecordArray(blockDevice)
}
