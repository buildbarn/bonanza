package local

import (
	pb "bonanza.build/pkg/proto/storage/object/local"
)

// PersistentStateSource is used by PeriodicSyncer to determine whether
// the persistent state file needs to update, and if so which contents
// it needs to hold.
type PersistentStateSource interface {
	// GetLocationsChangedWakeup returns a channel that triggers if
	// allocations for storing object contents have been made
	// against an EpochList, or if data is being discarded. This can
	// be used by PeriodicSyncer to synchronize data to storage.
	// PeriodicSyncer may apply a short delay before actually
	// synchronize data to perform some batching.
	//
	// This function must be called while holding a read lock on the
	// EpochList.
	GetLocationsChangedWakeup() <-chan struct{}

	// NotifySyncStarting instructs the EpochList that
	// PeriodicSyncer is about to synchronize data to storage.
	// Successive writes to the EpochList should use a new epoch ID,
	// as there is no guarantee their data is synchronized as part
	// of the current epoch.
	//
	// This function must be called while holding a write lock on
	// the EpochList.
	NotifySyncStarting(isFinalSync bool)

	// NotifySyncCompleted instructs the EpochList that the
	// synchronization performed after the last call to
	// NotifySyncStarting was successful.
	//
	// Future calls to GetPersistentState may now return information
	// about epochs that were created before the previous
	// NotifySyncStarting call.
	//
	// Calling this function may cause the next channel returned by
	// GetLocationsChangedWakeup to block once again.
	//
	// This function must be called while holding a write lock on
	// the EpochList.
	NotifySyncCompleted()

	// GetPersistentState returns information about all epochs that
	// are managed by the EpochList and have been synchronized to
	// storage successfully.
	//
	// This function must be called while holding a read lock on the
	// EpochList.
	//
	// The ReferenceLocationMapHashInitialization field of the
	// returned message is left unset.
	GetPersistentState() *pb.PersistentState
}
