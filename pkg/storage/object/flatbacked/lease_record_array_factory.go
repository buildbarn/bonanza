package flatbacked

import (
	"cmp"

	"bonanza.build/pkg/ds/lossymap"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/blockdevice"
)

type leaseRecordArrayFactory struct{}

// LeaseRecordArrayFactory can be used to create backing stores for the
// leases map that the flat backed storage backend uses. The leases map
// can either be stored in memory or on disk.
var LeaseRecordArrayFactory lossymap.RecordArrayFactory[object.LocalReference, Lease, Lease] = leaseRecordArrayFactory{}

func (leaseRecordArrayFactory) GetBlockDeviceBackedRecordSize() int {
	return blockDeviceBackedReferenceLeaseRecordSize
}

func (leaseRecordArrayFactory) NewInMemoryRecordArray(entries int) lossymap.RecordArray[object.LocalReference, Lease, Lease] {
	return lossymap.NewLowerBoundComparingRecordArray(
		lossymap.NewSimpleRecordArray[object.LocalReference, Lease](entries),
		/* leaseComparator = */ func(a, b *Lease) int {
			return cmp.Compare(*a, *b)
		},
	)
}

func (leaseRecordArrayFactory) NewBlockDeviceBackedRecordArray(blockDevice blockdevice.BlockDevice) lossymap.RecordArray[object.LocalReference, Lease, Lease] {
	return NewBlockDeviceBackedReferenceLeaseRecordArray(blockDevice)
}
