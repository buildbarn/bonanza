package local

import (
	"github.com/buildbarn/bb-storage/pkg/lossymap"
)

type inMemoryReferenceLocationRecord struct {
	epochID uint32
	record  ReferenceLocationRecord
}

type inMemoryReferenceLocationRecordArray struct {
	records []inMemoryReferenceLocationRecord
}

// NewInMemoryReferenceLocationRecordArray creates a
// ReferenceLocationRecordArray that stores its data in memory. This is
// sufficient for setups where persistency across restarts is not
// needed. Either because the data store is only used for local caching
// (e.g., on workers), or because storage nodes use mirroring and the
// loss of a single replica is tolerated.
func NewInMemoryReferenceLocationRecordArray(size int) ReferenceLocationRecordArray {
	return &inMemoryReferenceLocationRecordArray{
		records: make([]inMemoryReferenceLocationRecord, size),
	}
}

func (lra *inMemoryReferenceLocationRecordArray) Get(index uint64, resolver EpochIDResolver) (ReferenceLocationRecord, error) {
	record := lra.records[index]
	if epochState, found := resolver.GetEpochStateForEpochID(record.epochID); found && epochState.IsValidLocation(
		record.record.Value,
		record.record.RecordKey.Key.GetSizeBytes(),
	) {
		return record.record, nil
	}
	return ReferenceLocationRecord{}, lossymap.ErrRecordInvalidOrExpired
}

func (lra *inMemoryReferenceLocationRecordArray) Put(index uint64, record ReferenceLocationRecord, resolver EpochIDResolver) error {
	if epochState, epochID := resolver.GetCurrentEpochState(); epochState.IsValidLocation(
		record.Value,
		record.RecordKey.Key.GetSizeBytes(),
	) {
		lra.records[index] = inMemoryReferenceLocationRecord{
			epochID: epochID,
			record:  record,
		}
	}
	return nil
}
