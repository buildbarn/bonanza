package main

import (
	"cmp"
	"context"
	"os"
	"time"

	"bonanza.build/pkg/proto/configuration/bonanza_storage_shard"
	object_pb "bonanza.build/pkg/proto/storage/object"
	tag_pb "bonanza.build/pkg/proto/storage/tag"
	"bonanza.build/pkg/storage/object"
	object_flatbacked "bonanza.build/pkg/storage/object/flatbacked"
	object_leasemarshaling "bonanza.build/pkg/storage/object/leasemarshaling"
	object_local "bonanza.build/pkg/storage/object/local"
	object_namespacemapping "bonanza.build/pkg/storage/object/namespacemapping"
	"bonanza.build/pkg/storage/tag"
	tag_leasemarshaling "bonanza.build/pkg/storage/tag/leasemarshaling"
	tag_local "bonanza.build/pkg/storage/tag/local"

	"github.com/buildbarn/bb-storage/pkg/clock"
	"github.com/buildbarn/bb-storage/pkg/global"
	bb_grpc "github.com/buildbarn/bb-storage/pkg/grpc"
	"github.com/buildbarn/bb-storage/pkg/lossymap"
	"github.com/buildbarn/bb-storage/pkg/program"
	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func main() {
	program.RunMain(func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
		if len(os.Args) != 2 {
			return status.Error(codes.InvalidArgument, "Usage: bonanza_storage_shard bonanza_storage_shard.jsonnet")
		}
		var configuration bonanza_storage_shard.ApplicationConfiguration
		if err := util.UnmarshalConfigurationFromFile(os.Args[1], &configuration); err != nil {
			return util.StatusWrapf(err, "Failed to read configuration from %s", os.Args[1])
		}
		lifecycleState, grpcClientFactory, err := global.ApplyConfiguration(configuration.Global, dependenciesGroup)
		if err != nil {
			return util.StatusWrap(err, "Failed to apply global configuration options")
		}

		// Leases of objects are checked against the watermark
		// of the cluster. Leases close to the watermark are
		// renewed proactively.
		leaseRenewalFraction := configuration.LeaseRenewalFraction
		if leaseRenewalFraction < 0 || leaseRenewalFraction >= 1 {
			return status.Error(codes.InvalidArgument, "Lease renewal fraction must be at least 0 and less than 1")
		}

		// Objects may reference objects stored on other shards.
		// Leases are therefore checked against the highest
		// watermark of the cluster, which storage frontends
		// provide as part of their requests.
		//
		// The watermark of the local object store accounts for
		// data that is overwritten within the lookahead.
		// Watermarks provided by storage frontends that are
		// older than that are rejected, as other shards may
		// have lost data in the meantime.
		watermarkLookahead := configuration.WatermarkLookahead
		if err := watermarkLookahead.CheckValid(); err != nil {
			return util.StatusWrap(err, "Invalid watermark lookahead")
		}
		var localObjectStore object_local.Store
		var clusterWatermark *object.Watermark
		getWatermark := func() time.Time {
			watermark := clusterWatermark.Get()
			if localWatermark := localObjectStore.GetWatermark(); watermark.Before(localWatermark) {
				watermark = localWatermark
			}
			return watermark
		}

		// Create a map for leases of objects, so that
		// UploadDag() can reliably enforce that clients upload
		// complete DAGs.
		leasesMap, syncLeasesMap, err := lossymap.NewHashMapFromConfiguration(
			configuration.LeasesMap,
			"LeasesMap",
			object_flatbacked.LeaseRecordArrayFactory,
			/* recordKeyHasher = */ func(k *lossymap.RecordKey[object.LocalReference]) uint64 {
				h := uint64(14695981039346656037)
				for _, c := range k.Key.GetRawReference() {
					h = (h ^ uint64(c)) * 1099511628211
				}
				return (h ^ uint64(k.Attempt)) * 1099511628211
			},
			/* valueComparator = */ func(a, b *object_flatbacked.Lease) int {
				return cmp.Compare(*a, *b)
			},
			/* persistent = */ true,
		)
		if err != nil {
			return util.StatusWrap(err, "Failed to create leases map")
		}

		// Create storage for tags. Tags can be used to assign
		// arbitrary identifiers to objects.
		tagsMap, syncTagsMap, err := lossymap.NewHashMapFromConfiguration(
			configuration.TagsMap,
			"TagsMap",
			tag_local.TagRecordArrayFactory,
			/* recordKeyHasher = */ func(k *lossymap.RecordKey[tag.Key]) uint64 {
				h := uint64(14695981039346656037)
				for _, c := range k.Key.SignaturePublicKey {
					h = (h ^ uint64(c)) * 1099511628211
				}
				for _, c := range k.Key.Hash {
					h = (h ^ uint64(c)) * 1099511628211
				}
				return (h ^ uint64(k.Attempt)) * 1099511628211
			},
			/* valueComparator = */ func(a, b *tag_local.ValueWithLease) int {
				return a.SignedValue.Value.Timestamp.Compare(b.SignedValue.Value.Timestamp)
			},
			/* persistent = */ true,
		)
		if err != nil {
			return util.StatusWrap(err, "Failed to create tags map")
		}

		// Construct a flat storage backend for objects. Objects
		// whose leases need to be renewed are rewritten, so
		// that they get a new lease.
		localObjectStore, err = object_local.NewStoreFromConfiguration(
			dependenciesGroup,
			configuration.LocalObjectStore,
			&object_local.LeaseTrackingConfiguration{
				Clock:              clock.SystemClock,
				WatermarkLookahead: watermarkLookahead.AsDuration(),
				MinimumSafeSince: func() time.Time {
					return object_flatbacked.GetLeaseRenewalCutoff(getWatermark(), clock.SystemClock.Now(), leaseRenewalFraction)
				},
				GetLastIncarnationsHash: func() []byte {
					return clusterWatermark.GetLastIncarnationsHash()
				},
				// The leases map and tags map contain
				// leases, which are trusted after a
				// clean restart. Ensure they are
				// persisted before the lease tracking
				// state is written.
				SyncOnCleanShutdown: func() error {
					if syncLeasesMap != nil {
						if err := syncLeasesMap(); err != nil {
							return util.StatusWrap(err, "Failed to synchronize leases map")
						}
					}
					if syncTagsMap != nil {
						if err := syncTagsMap(); err != nil {
							return util.StatusWrap(err, "Failed to synchronize tags map")
						}
					}
					return nil
				},
			},
		)
		if err != nil {
			return util.StatusWrap(err, "Failed to create local object store")
		}

		// Storage frontends provide the incarnations of all
		// shards in the cluster as part of their requests. If
		// these change, leases are invalidated by raising the
		// watermark of the local object store. This causes the
		// raise to be reported to storage frontends, which
		// propagate it to all other shards. Preserve the last
		// value received across clean restarts.
		incarnation, lastIncarnationsHash := localObjectStore.GetIncarnation()
		clusterWatermark = object.NewWatermark(
			clock.SystemClock,
			watermarkLookahead.AsDuration(),
			incarnation,
			lastIncarnationsHash,
			localObjectStore.InvalidateLeases,
		)

		flatBackedObjectStore := object_flatbacked.NewStore(
			localObjectStore,
			clock.SystemClock,
			getWatermark,
			leaseRenewalFraction,
			leasesMap,
		)

		tagStore := tag_local.NewStore(
			tagsMap,
			clock.SystemClock,
			getWatermark,
			leaseRenewalFraction,
		)

		// Expose all storage backends via gRPC.
		leaseMarshaler := object_flatbacked.LeaseMarshaler
		if err := bb_grpc.NewServersFromConfigurationAndServe(
			configuration.GrpcServers,
			func(s grpc.ServiceRegistrar) {
				object_pb.RegisterDownloaderServer(
					s,
					object.NewDownloaderServer(
						object_namespacemapping.NewNamespaceRemovingDownloader[object.GlobalReference](
							flatBackedObjectStore,
						),
					),
				)
				object_pb.RegisterUploaderServer(
					s,
					object.NewUploaderServer(
						object_leasemarshaling.NewUploader(
							object_namespacemapping.NewNamespaceRemovingUploader[object.GlobalReference](
								flatBackedObjectStore,
							),
							leaseMarshaler,
						),
						clusterWatermark,
						/* getWatermark = */ func() (time.Time, time.Time) {
							return localObjectStore.GetWatermark(), clock.SystemClock.Now()
						},
					),
				)
				tag_pb.RegisterResolverServer(
					s,
					tag.NewResolverServer(
						tagStore,
						clusterWatermark,
					),
				)
				tag_pb.RegisterUpdaterServer(
					s,
					tag.NewUpdaterServer(
						tag_leasemarshaling.NewUpdater(
							tagStore,
							leaseMarshaler,
						),
						clusterWatermark,
					),
				)
			},
			siblingsGroup,
			grpcClientFactory,
		); err != nil {
			return util.StatusWrap(err, "gRPC server failure")
		}

		lifecycleState.MarkReadyAndWait(siblingsGroup)
		return nil
	})
}
