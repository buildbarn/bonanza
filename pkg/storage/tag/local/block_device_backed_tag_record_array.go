package local

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"time"

	"bonanza.build/pkg/ds/lossymap"
	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/object"
	object_flatbacked "bonanza.build/pkg/storage/object/flatbacked"
	"bonanza.build/pkg/storage/tag"

	"github.com/buildbarn/bb-storage/pkg/blockdevice"
	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// blockDeviceBackedReferenceTagRecordSize is the size of a
	// single serialized record in bytes. In serialized form, a
	// record contains the following fields:
	//
	// - Key signature Ed1559 public key  32 bytes
	// - Key hash                         32 bytes
	// - Value SHA256_V1 local reference  40 bytes
	// - Value timestamp                   8 bytes
	// - Signed value Ed25519 signature   64 bytes
	// - Lease                             8 bytes
	// - Hash table probing attempt        1 bytes
	//                            Total: 185 bytes
	blockDeviceBackedReferenceTagRecordSize = ed25519.PublicKeySize + sha256.Size + object.SHA256V1ReferenceSizeBytes + 8 + ed25519.SignatureSize + 8 + 1
)

type blockDeviceBackedTagRecordArray struct {
	device blockdevice.BlockDevice
}

// NewBlockDeviceBackedReferenceTagRecordArray creates a record array
// for storing tags that is backed by a block device. By using a block
// device, tags can be persisted across restarts.
func NewBlockDeviceBackedReferenceTagRecordArray(device blockdevice.BlockDevice) lossymap.RecordArray[tag.Key, ValueWithLease, struct{}] {
	return &blockDeviceBackedTagRecordArray{
		device: device,
	}
}

func (lra *blockDeviceBackedTagRecordArray) Get(index uint64, unused struct{}) (lossymap.Record[tag.Key, ValueWithLease], error) {
	// Read the record from disk.
	var record [blockDeviceBackedReferenceTagRecordSize]byte
	if _, err := lra.device.ReadAt(record[:], int64(index)*blockDeviceBackedReferenceTagRecordSize); err != nil {
		return lossymap.Record[tag.Key, ValueWithLease]{}, err
	}

	// Read the tag.
	key := tag.Key{
		SignaturePublicKey: *(*[ed25519.PublicKeySize]byte)(record[:]),
		Hash:               *(*[sha256.Size]byte)(record[ed25519.PublicKeySize:]),
	}

	// Read the value.
	valueReference, err := object.SHA256V1ReferenceFormat.NewLocalReference(
		record[ed25519.PublicKeySize+sha256.Size : ed25519.PublicKeySize+sha256.Size+object.SHA256V1ReferenceSizeBytes],
	)
	if err != nil {
		return lossymap.Record[tag.Key, ValueWithLease]{}, lossymap.ErrRecordInvalidOrExpired
	}
	valueTimestamp := int64(binary.LittleEndian.Uint64(record[ed25519.PublicKeySize+sha256.Size+object.SHA256V1ReferenceSizeBytes:]))

	// Verify the value's signature.
	signedValue, err := key.VerifySignature(
		tag.Value{
			Reference: valueReference,
			Timestamp: time.Unix(valueTimestamp/1e9, valueTimestamp%1e9),
		},
		(*[ed25519.SignatureSize]byte)(record[ed25519.PublicKeySize+sha256.Size+object.SHA256V1ReferenceSizeBytes+8:]),
	)
	if err != nil {
		return lossymap.Record[tag.Key, ValueWithLease]{}, lossymap.ErrRecordInvalidOrExpired
	}

	return lossymap.Record[tag.Key, ValueWithLease]{
		RecordKey: lossymap.RecordKey[tag.Key]{
			Key:     key,
			Attempt: uint8(record[ed25519.PublicKeySize+sha256.Size+object.SHA256V1ReferenceSizeBytes+8+ed25519.SignatureSize+8]),
		},
		Value: ValueWithLease{
			SignedValue: signedValue,
			Lease:       object_flatbacked.Lease(binary.LittleEndian.Uint64(record[ed25519.PublicKeySize+sha256.Size+object.SHA256V1ReferenceSizeBytes+8+ed25519.SignatureSize:])),
		},
	}, nil
}

func (lra *blockDeviceBackedTagRecordArray) Put(index uint64, record lossymap.Record[tag.Key, ValueWithLease], unused struct{}) error {
	valueReference := record.Value.SignedValue.Value.Reference
	if valueReference.GetReferenceFormat().ToProto() != object_pb.ReferenceFormat_SHA256_V1 {
		return status.Error(codes.Unimplemented, "This implementation only supports reference format SHA256_V1")
	}

	// Marshal the reference and lease.
	var rawRecord [blockDeviceBackedReferenceTagRecordSize]byte
	copy(rawRecord[:], record.RecordKey.Key.SignaturePublicKey[:])
	copy(rawRecord[ed25519.PublicKeySize:], record.RecordKey.Key.Hash[:])
	copy(rawRecord[ed25519.PublicKeySize+sha256.Size:], valueReference.GetRawReference())
	binary.LittleEndian.PutUint64(rawRecord[ed25519.PublicKeySize+sha256.Size+object.SHA256V1ReferenceSizeBytes:], uint64(record.Value.SignedValue.Value.Timestamp.UnixNano()))
	copy(rawRecord[ed25519.PublicKeySize+sha256.Size+object.SHA256V1ReferenceSizeBytes+8:], record.Value.SignedValue.Signature[:])
	binary.LittleEndian.PutUint64(rawRecord[ed25519.PublicKeySize+sha256.Size+object.SHA256V1ReferenceSizeBytes+8+ed25519.SignatureSize:], uint64(record.Value.Lease))
	rawRecord[ed25519.PublicKeySize+sha256.Size+object.SHA256V1ReferenceSizeBytes+8+ed25519.SignatureSize+8] = record.RecordKey.Attempt

	if _, err := lra.device.WriteAt(rawRecord[:], int64(index)*blockDeviceBackedReferenceTagRecordSize); err != nil {
		return util.StatusWrap(err, "Failed to write lease record")
	}
	return nil
}
