package local

// EpochState describes for a given state what the range of valid
// locations are. This allows implementations of
// ReferenceLocationRecordArray to suppress and overwrite entries that
// are no longer valid.
//
// EpochState also contains a hash seed that may be used by
// ReferenceLocationRecordArray when computing hashes of entries. This
// is needed to ensure that entries belonging to previous unclean
// shutdowns are suppressed.
type EpochState struct {
	HashSeed        uint64
	MinimumLocation uint64
	MaximumLocation uint64
}

// IsValidLocation returns true if a given location and object size are
// valid within the current epoch. This is used to ensure that entries
// in the ReferenceLocationRecordArray are suppressed if they refer to
// locations on the block device that have in the meantime been
// overwritten.
func (s *EpochState) IsValidLocation(location uint64, sizeBytes int) bool {
	return location >= s.MinimumLocation &&
		location < s.MaximumLocation &&
		s.MaximumLocation-location >= uint64(sizeBytes)
}

// EpochIDResolver is used by implementations of
// ReferenceLocationRecordArray to determine whether any entries it
// stores still point to valid data in the location-blob map.
type EpochIDResolver interface {
	GetEpochStateForEpochID(epochID uint32) (EpochState, bool)
	GetCurrentEpochState() (EpochState, uint32)

	// DiscardLocation is called when a reference-location map
	// entry pointing to a given location is discarded, because the
	// reference-location map is full. As the object can no longer
	// be accessed, leases referring to it need to be invalidated.
	DiscardLocation(location uint64)
}
