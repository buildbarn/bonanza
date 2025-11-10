package local

import (
	"bonanza.build/pkg/ds/lossymap"
	"bonanza.build/pkg/storage/tag"

	"github.com/buildbarn/bb-storage/pkg/blockdevice"
)

type tagRecordArrayFactory struct{}

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
