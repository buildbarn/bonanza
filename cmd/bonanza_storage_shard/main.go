package main

import (
	"cmp"
	"context"
	"os"

	"bonanza.build/pkg/ds/lossymap"
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

		// Construct a flat storage backend for objects.
		localObjectStore, err := object_local.NewStoreFromConfiguration(
			dependenciesGroup,
			configuration.LocalObjectStore,
		)
		if err != nil {
			return util.StatusWrap(err, "Failed to create local object store")
		}

		// Put a store for leases in front of the flat storage
		// backend, so that UploadDag() can reliably enforce
		// that clients upload complete DAGs.
		leaseCompletenessDurationMessage := configuration.LeasesMapLeaseCompletenessDuration
		if err := leaseCompletenessDurationMessage.CheckValid(); err != nil {
			return util.StatusWrap(err, "Invalid leases map lease completeness duration")
		}
		leaseCompletenessDuration := leaseCompletenessDurationMessage.AsDuration()
		leasesMap, err := lossymap.NewHashMapFromConfiguration(
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
		flatBackedObjectStore := object_flatbacked.NewStore(
			localObjectStore,
			clock.SystemClock,
			leaseCompletenessDuration,
			leasesMap,
		)

		// Create storage for tags. Tags can be used to assign
		// arbitrary identifiers to objects.
		tagsMap, err := lossymap.NewHashMapFromConfiguration(
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
		tagStore := tag_local.NewStore(
			tagsMap,
			clock.SystemClock,
			leaseCompletenessDuration,
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
					),
				)
				tag_pb.RegisterResolverServer(
					s,
					tag.NewResolverServer(
						tagStore,
					),
				)
				tag_pb.RegisterUpdaterServer(
					s,
					tag.NewUpdaterServer(
						tag_leasemarshaling.NewUpdater(
							tagStore,
							leaseMarshaler,
						),
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
