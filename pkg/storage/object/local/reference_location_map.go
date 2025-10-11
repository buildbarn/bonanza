package local

import (
	"bonanza.build/pkg/ds/lossymap"
	"bonanza.build/pkg/storage/object"
)

// ReferenceLocationMap is used by the local object store to keep track
// of the location on disk where a given object is stored.
type ReferenceLocationMap = lossymap.Map[object.FlatReference, uint64, EpochIDResolver]

// ReferenceLocationRecordArray is an array of ReferenceLocationRecord
// objects. Implementations of this type are used if a
// ReferenceLocationMap is backed by a hash table.
type ReferenceLocationRecordArray = lossymap.RecordArray[object.FlatReference, uint64, EpochIDResolver]

// ReferenceLocationRecord represents a record that is stored in a
// ReferenceLocationRecordArray.
type ReferenceLocationRecord = lossymap.Record[object.FlatReference, uint64]

// ReferenceLocationRecordKey represents the key portion of a record
// that is stored in a ReferenceLocationRecordArray..
type ReferenceLocationRecordKey = lossymap.RecordKey[object.FlatReference]
