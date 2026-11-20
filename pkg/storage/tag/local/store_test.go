package local_test

import (
	"context"
	"testing"
	"time"

	"bonanza.build/pkg/storage/object"
	object_flatbacked "bonanza.build/pkg/storage/object/flatbacked"
	"bonanza.build/pkg/storage/tag"
	tag_local "bonanza.build/pkg/storage/tag/local"

	"github.com/buildbarn/bb-storage/pkg/clock"
	"github.com/buildbarn/bb-storage/pkg/lossymap"
	"github.com/stretchr/testify/require"
)

func TestStoreLeaseClockSkew(t *testing.T) {
	// Leases of tags are computed by the storage shard holding
	// the object the tag references. If that shard's clock is
	// ahead, leases may lie in the future. These must be capped
	// to the current time, as received watermarks are capped in
	// the same way. Otherwise the tag could remain complete after
	// the watermark has been raised to the current time.
	tagsMap := lossymap.NewHashMap(
		tag_local.TagRecordArrayFactory.NewInMemoryRecordArray(16),
		func(k *lossymap.RecordKey[tag.Key]) uint64 { return uint64(k.Key.Hash[0]) + uint64(k.Attempt) },
		16,
		func(a, b *tag_local.ValueWithLease) int {
			return a.SignedValue.Value.Timestamp.Compare(b.SignedValue.Value.Timestamp)
		},
		4,
		4,
		"test",
	)
	var watermark time.Time
	store := tag_local.NewStore(tagsMap, clock.SystemClock, func() time.Time { return watermark }, 0.5)

	ctx := context.Background()
	var namespace object.Namespace
	key := tag.Key{Hash: [...]byte{1, 31: 0}}
	signedValue := tag.SignedValue{
		Value: tag.Value{Timestamp: time.Unix(1000, 0)},
	}

	futureLease := object_flatbacked.NewLeaseFromTime(time.Now().Add(time.Hour))
	require.NoError(t, store.UpdateTag(ctx, namespace, key, signedValue, futureLease))

	// With a watermark well in the past, the tag is complete.
	watermark = time.Now().Add(-time.Hour)
	_, complete, err := store.ResolveTag(ctx, namespace, key, nil)
	require.NoError(t, err)
	require.True(t, complete)

	// Once the watermark is raised to the current time, the tag
	// must no longer be complete, even though the lease that was
	// provided lies in the future.
	watermark = time.Now().Add(time.Second)
	_, complete, err = store.ResolveTag(ctx, namespace, key, nil)
	require.NoError(t, err)
	require.False(t, complete)
}
