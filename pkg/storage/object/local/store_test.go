package local_test

import (
	"cmp"
	"context"
	"sync"
	"testing"

	"bonanza.build/pkg/ds/lossymap"
	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/object"
	object_local "bonanza.build/pkg/storage/object/local"

	"github.com/buildbarn/bb-storage/pkg/random"
	"github.com/stretchr/testify/require"
)

// newTestStore creates a store with real in-memory implementations for testing.
func newTestStore(t *testing.T, bufferSize, currentRegionSizeBytes, newRegionSizeBytes uint64) (object.Store[object.FlatReference, struct{}], object_local.EpochList) {
	var lock sync.RWMutex
	randomNumberGenerator := random.NewFastSingleThreadedGenerator()
	epochList := object_local.NewVolatileEpochList(bufferSize, randomNumberGenerator)

	recordArray := object_local.NewInMemoryReferenceLocationRecordArray(1024)
	referenceLocationMap := lossymap.NewHashMap(
		recordArray,
		func(k *lossymap.RecordKey[object.FlatReference]) uint64 {
			// FNV-1a hash.
			h := uint64(0)
			for _, c := range k.Key.GetRawFlatReference() {
				h ^= uint64(c)
				h *= 1099511628211
			}
			return h
		},
		1024,
		func(a, b *uint64) int { return cmp.Compare(*a, *b) },
		16,
		64,
		"test",
	)

	locationBlobMap := object_local.NewInMemoryLocationBlobMap(int(bufferSize))

	store := object_local.NewStore(
		&lock,
		referenceLocationMap,
		locationBlobMap,
		epochList,
		currentRegionSizeBytes,
		newRegionSizeBytes,
	)

	return store, epochList
}

func TestStoreRefreshObjectInOldRegion(t *testing.T) {
	// This test verifies that objects in the "old" region get refreshed
	// (copied to the end of the buffer) when accessed, preventing them
	// from being overwritten by the write cursor.
	store, epochList := newTestStore(
		t,
		/* bufferSizeBytes = */ 1000,
		/* currentRegionSize = */ 300,
		/* newRegionSizeBytes = */ 200,
	)
	ctx := context.Background()

	// SHA256("Hello") = 185f8db32271fe25f561a6fc938b2e264306ec304eda518007d1764826381969
	exampleData := []byte("Hello")
	ref := object.MustNewSHA256V1FlatReference("185f8db32271fe25f561a6fc938b2e264306ec304eda518007d1764826381969", uint32(len(exampleData)))
	contents := object.MustNewContents(object_pb.ReferenceFormat_SHA256_V1, nil, exampleData)

	// Upload the object. It starts at location 0.
	_, err := store.UploadObject(ctx, ref, contents, nil, false)
	require.NoError(t, err)

	state, _ := epochList.GetCurrentEpochState()
	require.Equal(t, uint64(0), state.MinimumLocation)

	// Fill the buffer to push MinimumLocation forward, moving our
	// object into the "old" region.
	filler := make([]byte, 800)
	for i := range filler {
		filler[i] = byte(i)
	}
	fillerRef := object.MustNewSHA256V1FlatReference("cd2eb0837c9b4c962c22d2ff8b5441b7b45805887f051d39bf133b583baf6860", uint32(len(filler)))
	fillerContents := object.MustNewContents(object_pb.ReferenceFormat_SHA256_V1, nil, filler)

	_, err = store.UploadObject(ctx, fillerRef, fillerContents, nil, false)
	require.NoError(t, err)

	// Download triggers refresh for objects in the "old" region.
	downloadedContents, err := store.DownloadObject(ctx, ref)
	require.NoError(t, err)
	require.Equal(t, exampleData, downloadedContents.GetFullData())

	// After more writes, the object should still be accessible because
	// it was refreshed to a new location.
	_, err = store.UploadObject(ctx, fillerRef, fillerContents, nil, false)
	require.NoError(t, err)

	downloadedContents, err = store.DownloadObject(ctx, ref)
	require.NoError(t, err)
	require.Equal(t, exampleData, downloadedContents.GetFullData())
}
