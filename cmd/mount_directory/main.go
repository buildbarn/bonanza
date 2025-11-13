package main

import (
	"context"
	"os"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/encoding"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_filesystem_virtual "bonanza.build/pkg/model/filesystem/virtual"
	model_parser "bonanza.build/pkg/model/parser"
	"bonanza.build/pkg/proto/configuration/mount_directory"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/object"
	object_grpc "bonanza.build/pkg/storage/object/grpc"
	object_namespacemapping "bonanza.build/pkg/storage/object/namespacemapping"

	virtual_configuration "github.com/buildbarn/bb-remote-execution/pkg/filesystem/virtual/configuration"
	"github.com/buildbarn/bb-storage/pkg/global"
	"github.com/buildbarn/bb-storage/pkg/program"
	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func main() {
	program.RunMain(func(ctx context.Context, siblingsGroup, dependenciesGroup program.Group) error {
		if len(os.Args) != 2 {
			return status.Error(codes.InvalidArgument, "Usage: mount_directory mount_directory.jsonnet")
		}
		var configuration mount_directory.ApplicationConfiguration
		if err := util.UnmarshalConfigurationFromFile(os.Args[1], &configuration); err != nil {
			return util.StatusWrapf(err, "Failed to read configuration from %s", os.Args[1])
		}
		lifecycleState, grpcClientFactory, err := global.ApplyConfiguration(configuration.Global, dependenciesGroup)
		if err != nil {
			return util.StatusWrap(err, "Failed to apply global configuration options")
		}

		mount, rootHandleAllocator, err := virtual_configuration.NewMountFromConfiguration(
			configuration.Mount,
			"mount_directory",
			/* rootDirectory = */ virtual_configuration.LongAttributeCaching,
			/* childDirectories = */ virtual_configuration.LongAttributeCaching,
			/* leaves = */ virtual_configuration.LongAttributeCaching,
			/* caseSensitive = */ true,
		)
		if err != nil {
			return util.StatusWrap(err, "Failed to create virtual file system mount")
		}

		namespace, err := object.NewNamespace(configuration.Namespace)
		if err != nil {
			return util.StatusWrap(err, "Invalid namespace")
		}

		grpcClient, err := grpcClientFactory.NewClientFromConfiguration(configuration.GrpcClient, dependenciesGroup)
		if err != nil {
			return util.StatusWrap(err, "Failed to create gRPC client")
		}

		maximumObjectSizeBytes := uint32(namespace.GetMaximumObjectSizeBytes())
		directoryEncoder, err := encoding.NewDeterministicBinaryEncoderFromProto(configuration.DirectoryEncoders, maximumObjectSizeBytes)
		if err != nil {
			return util.StatusWrap(err, "Failed to create directory encoder")
		}
		smallFileEncoder, err := encoding.NewDeterministicBinaryEncoderFromProto(configuration.SmallFileEncoders, maximumObjectSizeBytes)
		if err != nil {
			return util.StatusWrap(err, "Failed to create small file encoder")
		}
		concatenatedFileEncoder, err := encoding.NewDeterministicBinaryEncoderFromProto(configuration.ConcatenatedFileEncoders, maximumObjectSizeBytes)
		if err != nil {
			return util.StatusWrap(err, "Failed to create concatenated file encoder")
		}

		parsedObjectPool, err := model_parser.NewParsedObjectPoolFromConfiguration(configuration.ParsedObjectPool)
		if err != nil {
			return util.StatusWrap(err, "Failed to create parsed object pool")
		}
		parsedObjectPoolIngester := model_parser.NewParsedObjectPoolIngester(
			parsedObjectPool,
			model_parser.NewDownloadingParsedObjectReader(
				object_namespacemapping.NewNamespaceAddingDownloader(
					object_grpc.NewDownloader(object_pb.NewDownloaderClient(grpcClient)),
					namespace,
				),
			),
		)
		leavesReader := model_parser.LookupParsedObjectReader(
			parsedObjectPoolIngester,
			model_parser.NewChainedObjectParser(
				model_parser.NewEncodedObjectParser[object.LocalReference](directoryEncoder),
				model_parser.NewProtoObjectParser[object.LocalReference, model_filesystem_pb.Leaves](),
			),
		)
		directoryClusterReader := model_parser.LookupParsedObjectReader(
			parsedObjectPoolIngester,
			model_parser.NewChainedObjectParser(
				model_parser.NewEncodedObjectParser[object.LocalReference](directoryEncoder),
				model_filesystem.NewDirectoryClusterObjectParser[object.LocalReference](),
			),
		)
		fileContentsListReader := model_parser.LookupParsedObjectReader(
			parsedObjectPoolIngester,
			model_parser.NewChainedObjectParser(
				model_parser.NewEncodedObjectParser[object.LocalReference](concatenatedFileEncoder),
				model_filesystem.NewFileContentsListObjectParser[object.LocalReference](),
			),
		)
		fileChunkReader := model_parser.LookupParsedObjectReader(
			parsedObjectPoolIngester,
			model_parser.NewChainedObjectParser(
				model_parser.NewEncodedObjectParser[object.LocalReference](smallFileEncoder),
				model_parser.NewRawObjectParser[object.LocalReference](),
			),
		)

		rootDirectoryReference, err := model_core.NewDecodableLocalReferenceFromWeakProto(
			namespace.ReferenceFormat,
			configuration.RootDirectoryReference,
		)
		if err != nil {
			return util.StatusWrap(err, "Invalid root directory reference")
		}

		rootDirectoryIndex := uint(0)
		rootDirectoryCluster, err := directoryClusterReader.ReadParsedObject(ctx, rootDirectoryReference)
		if err != nil {
			return util.StatusWrap(err, "Failed to fetch contents of root directory")
		}

		fileFactory := model_filesystem_virtual.NewResolvableHandleAllocatingFileFactory(
			model_filesystem_virtual.NewObjectBackedFileFactory(
				context.Background(),
				model_filesystem.NewFileReader(
					fileContentsListReader,
					fileChunkReader,
				),
				util.DefaultErrorLogger,
			),
			rootHandleAllocator.New(),
			util.DefaultErrorLogger,
		)
		directoryFactory := model_filesystem_virtual.NewObjectBackedDirectoryFactory(
			rootHandleAllocator.New(),
			directoryClusterReader,
			leavesReader,
			fileFactory,
			util.DefaultErrorLogger,
		)
		rootDirectory := directoryFactory.LookupDirectory(
			rootDirectoryReference,
			rootDirectoryIndex,
			uint32(len(rootDirectoryCluster.Message[0].Directory.Directories)),
		)

		if err := mount.Expose(siblingsGroup, rootDirectory); err != nil {
			return util.StatusWrap(err, "Failed to expose virtual file system mount")
		}

		lifecycleState.MarkReadyAndWait(siblingsGroup)
		return nil
	})
}
