package flatbacked

import (
	"encoding/binary"

	"bonanza.build/pkg/ds/lossymap"
	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/blockdevice"
	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// blockDeviceBackedReferenceLeaseRecordSize is the size of a
	// single serialized record in bytes. In serialized form, a
	// record contains the following fields:
	//
	// - SHA256_V1 local reference   40 bytes
	// - Hash table probing attempt   1 bytes
	// - Lease                        8 bytes
	//                        Total: 49 bytes
	blockDeviceBackedReferenceLeaseRecordSize = object.SHA256V1ReferenceSizeBytes + 1 + 8
)

type blockDeviceBackedLeaseRecordArray struct {
	device blockdevice.BlockDevice
}

// NewBlockDeviceBackedReferenceLeaseRecordArray creates a record array
// for storing leases of that is backed by a block device. By using a
// block device, leases can be persisted across restarts.
//
// Note that for most setups it's recommended to keep leases in memory.
func NewBlockDeviceBackedReferenceLeaseRecordArray(device blockdevice.BlockDevice) lossymap.RecordArray[object.LocalReference, Lease, Lease] {
	return &blockDeviceBackedLeaseRecordArray{
		device: device,
	}
}

func (lra *blockDeviceBackedLeaseRecordArray) Get(index uint64, minimumLease Lease) (lossymap.Record[object.LocalReference, Lease], error) {
	// Read the record from disk.
	var record [blockDeviceBackedReferenceLeaseRecordSize]byte
	if _, err := lra.device.ReadAt(record[:], int64(index)*blockDeviceBackedReferenceLeaseRecordSize); err != nil {
		return lossymap.Record[object.LocalReference, Lease]{}, err
	}

	// Unmarshal the reference and lease stored in the record.
	reference, err := object.SHA256V1ReferenceFormat.NewLocalReference(
		record[:object.SHA256V1ReferenceSizeBytes],
	)
	if err != nil {
		return lossymap.Record[object.LocalReference, Lease]{}, lossymap.ErrRecordInvalidOrExpired
	}
	lease := Lease(binary.LittleEndian.Uint64(record[object.SHA256V1FlatReferenceSizeBytes+1:]))
	if lease < minimumLease {
		return lossymap.Record[object.LocalReference, Lease]{}, lossymap.ErrRecordInvalidOrExpired
	}

	return lossymap.Record[object.LocalReference, Lease]{
		RecordKey: lossymap.RecordKey[object.LocalReference]{
			Key:     reference,
			Attempt: uint8(record[object.SHA256V1ReferenceSizeBytes]),
		},
		Value: lease,
	}, nil
}

func (lra *blockDeviceBackedLeaseRecordArray) Put(index uint64, record lossymap.Record[object.LocalReference, Lease], minimumLease Lease) error {
	reference := record.RecordKey.Key
	if reference.GetReferenceFormat().ToProto() != object_pb.ReferenceFormat_SHA256_V1 {
		return status.Error(codes.Unimplemented, "This implementation only supports reference format SHA256_V1")
	}
	if lease := record.Value; lease >= minimumLease {
		// Marshal the reference and lease.
		var rawRecord [blockDeviceBackedReferenceLeaseRecordSize]byte
		copy(rawRecord[:], record.RecordKey.Key.GetRawReference())
		rawRecord[object.SHA256V1ReferenceSizeBytes] = record.RecordKey.Attempt
		binary.LittleEndian.PutUint64(rawRecord[object.SHA256V1ReferenceSizeBytes+1:], uint64(lease))

		if _, err := lra.device.WriteAt(rawRecord[:], int64(index)*blockDeviceBackedReferenceLeaseRecordSize); err != nil {
			return util.StatusWrap(err, "Failed to write lease record")
		}
	}
	return nil
}
