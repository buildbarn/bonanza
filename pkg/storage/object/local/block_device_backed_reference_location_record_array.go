package local

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
	// blockDeviceBackedReferenceLocationRecordSize is the size of a
	// single serialized ReferenceLocationRecord in bytes. In
	// serialized form, a ReferenceLocationRecord contains the following
	// fields:
	//
	// - Epoch ID                     4 bytes
	// - SHA256_V1 flat reference    35 bytes
	// - Hash table probing attempt   1 bytes
	// - Object location              8 bytes
	// - Record checksum              8 bytes
	//                        Total: 56 bytes
	blockDeviceBackedReferenceLocationRecordSize = 4 + object.SHA256V1FlatReferenceSizeBytes + 1 + 8 + 8
)

type blockDeviceBackedLocationRecordArray struct {
	device blockdevice.BlockDevice
}

// NewBlockDeviceBackedReferenceLocationRecordArray creates a persistent
// ReferenceLocationRecordArray. It works by using a block device as an
// array-like structure, writing serialized ReferenceLocationRecords next to
// each other.
func NewBlockDeviceBackedReferenceLocationRecordArray(device blockdevice.BlockDevice) ReferenceLocationRecordArray {
	return &blockDeviceBackedLocationRecordArray{
		device: device,
	}
}

// computeChecksumForRecord computes an FNV-1a hash of all the fields in a
// serialized ReferenceLocationRecord, using a hash initialization that
// corresponds to that of the epoch ID.
func computeChecksumForRecord(record *[blockDeviceBackedReferenceLocationRecordSize]byte, h uint64) uint64 {
	for i := 4; i < 4+object.SHA256V1FlatReferenceSizeBytes+1+8; i++ {
		h ^= uint64(record[i])
		h *= 1099511628211
	}
	return h
}

func (lra *blockDeviceBackedLocationRecordArray) Get(index uint64, resolver EpochIDResolver) (ReferenceLocationRecord, error) {
	var record [blockDeviceBackedReferenceLocationRecordSize]byte
	if _, err := lra.device.ReadAt(record[:], int64(index)*blockDeviceBackedReferenceLocationRecordSize); err != nil {
		return ReferenceLocationRecord{}, err
	}

	// Reobtain the epoch state that was used by this record. This
	// may fail if the entry refers to an epoch that is so old, that
	// all data belonging to it has already been overwritten.
	epochID := binary.LittleEndian.Uint32(record[:])
	epochState, found := resolver.GetEpochStateForEpochID(epochID)
	if !found {
		return ReferenceLocationRecord{}, lossymap.ErrRecordInvalidOrExpired
	}

	// Discard entries for which the checksum of the record doesn't
	// match up with what's expected. Such records may have either
	// been corrupted or correspond to objects that weren't flushed
	// before some previous shutdown.
	if computeChecksumForRecord(&record, epochState.HashSeed) != binary.LittleEndian.Uint64(record[4+object.SHA256V1FlatReferenceSizeBytes+1+8:]) {
		return ReferenceLocationRecord{}, lossymap.ErrRecordInvalidOrExpired
	}

	// Deserialize the read record into a ReferenceLocationRecord.
	reference, err := object.SHA256V1ReferenceFormat.NewFlatReference(
		record[4 : 4+object.SHA256V1FlatReferenceSizeBytes],
	)
	if err != nil {
		return ReferenceLocationRecord{}, lossymap.ErrRecordInvalidOrExpired
	}

	// Suppress entries for which the location is out of bounds.
	// This may be because data has been overwritten.
	location := binary.LittleEndian.Uint64(record[4+object.SHA256V1FlatReferenceSizeBytes+1:])
	if !epochState.IsValidLocation(location, reference.GetSizeBytes()) {
		return ReferenceLocationRecord{}, lossymap.ErrRecordInvalidOrExpired
	}

	return ReferenceLocationRecord{
		RecordKey: ReferenceLocationRecordKey{
			Key:     reference,
			Attempt: uint8(record[4+object.SHA256V1FlatReferenceSizeBytes]),
		},
		Value: location,
	}, nil
}

func (lra *blockDeviceBackedLocationRecordArray) Put(index uint64, record ReferenceLocationRecord, resolver EpochIDResolver) error {
	reference := record.RecordKey.Key
	if reference.GetReferenceFormat().ToProto() != object_pb.ReferenceFormat_SHA256_V1 {
		return status.Error(codes.Unimplemented, "This implementation only supports reference format SHA256_V1")
	}

	epochState, epochID := resolver.GetCurrentEpochState()
	location := record.Value
	if epochState.IsValidLocation(location, reference.GetSizeBytes()) {
		// Serialize the ReferenceLocationRecord ready to be written to disk.
		var rawRecord [blockDeviceBackedReferenceLocationRecordSize]byte
		binary.LittleEndian.PutUint32(rawRecord[:], epochID)
		copy(rawRecord[4:], record.RecordKey.Key.GetRawFlatReference())
		rawRecord[4+object.SHA256V1FlatReferenceSizeBytes] = record.RecordKey.Attempt
		binary.LittleEndian.PutUint64(rawRecord[4+object.SHA256V1FlatReferenceSizeBytes+1:], location)
		binary.LittleEndian.PutUint64(rawRecord[4+object.SHA256V1FlatReferenceSizeBytes+1+8:], computeChecksumForRecord(&rawRecord, epochState.HashSeed))

		if _, err := lra.device.WriteAt(rawRecord[:], int64(index)*blockDeviceBackedReferenceLocationRecordSize); err != nil {
			return util.StatusWrap(err, "Failed to write location record")
		}
	}
	return nil
}
