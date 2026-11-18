package virtual

import (
	"context"
	"iter"

	model_core "bonanza.build/pkg/model/core"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_parser "bonanza.build/pkg/model/parser"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-remote-execution/pkg/filesystem/virtual"
	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type objectBackedInitialContentsFetcherOptions struct {
	context                context.Context
	directoryClusterReader model_parser.MessageObjectReader[object.LocalReference, model_filesystem.DirectoryCluster]
	fileFactory            FileFactory
	symlinkFactory         virtual.SymlinkFactory
}

type objectBackedInitialContentsFetcher struct {
	options          *objectBackedInitialContentsFetcherOptions
	clusterReference model_core.Decodable[object.LocalReference]
	directoryIndex   int
}

// NewObjectBackedInitialContentsFetcher creates a
// InitialContentsFetcher that can be used to populate an
// InMemoryPrepopulatedDirectory with the contents of a directory
// structure that has been written to the Object Store.
func NewObjectBackedInitialContentsFetcher(
	ctx context.Context,
	directoryClusterReader model_parser.MessageObjectReader[object.LocalReference, model_filesystem.DirectoryCluster],
	fileFactory FileFactory,
	symlinkFactory virtual.SymlinkFactory,
	rootClusterReference model_core.Decodable[object.LocalReference],
) virtual.InitialContentsFetcher {
	return &objectBackedInitialContentsFetcher{
		options: &objectBackedInitialContentsFetcherOptions{
			context:                ctx,
			directoryClusterReader: directoryClusterReader,
			fileFactory:            fileFactory,
			symlinkFactory:         symlinkFactory,
		},
		clusterReference: rootClusterReference,
	}
}

func (icf *objectBackedInitialContentsFetcher) getDirectory() (model_filesystem.DirectoryClusterLocation, error) {
	options := icf.options
	cluster, err := options.directoryClusterReader.ReadObject(options.context, icf.clusterReference)
	if err != nil {
		return model_filesystem.DirectoryClusterLocation{}, util.StatusWrapf(err, "Failed to fetch directory cluster with reference %s", model_core.DecodableLocalReferenceToString(icf.clusterReference))
	}
	if icf.directoryIndex >= len(cluster.Message.Directories) {
		return model_filesystem.DirectoryClusterLocation{}, status.Errorf(codes.InvalidArgument, "Directory index %d is out of range, as directory cluster with reference %s only contains %d directories", icf.directoryIndex, model_core.DecodableLocalReferenceToString(icf.clusterReference), len(cluster.Message.Directories))
	}
	return model_filesystem.DirectoryClusterLocation{
		Reference: icf.clusterReference,
		Cluster:   cluster,
		Index:     icf.directoryIndex,
	}, nil
}

func (icf *objectBackedInitialContentsFetcher) FetchContents(fileReadMonitorFactory virtual.FileReadMonitorFactory) (map[path.Component]virtual.InitialChild, error) {
	location, err := icf.getDirectory()
	if err != nil {
		return nil, err
	}
	options := icf.options

	// Ensure that leaves are properly unlinked if this method fails.
	children := map[path.Component]virtual.InitialChild{}
	var leavesToUnlink []virtual.LinkableLeaf
	defer func() {
		for _, leaf := range leavesToUnlink {
			leaf.Unlink()
		}
	}()

	// Iterate over the contents of subdirectories in lockstep with
	// the nodes.
	var errChildren error
	nextChild, stopChildren := iter.Pull(model_filesystem.AllDirectoryClusterChildren(options.context, options.directoryClusterReader, &location, 0, &errChildren))
	defer stopChildren()

	var errNodes error
	for node := range model_filesystem.AllDirectoryClusterNodes(options.context, options.directoryClusterReader, &location, 0, &errNodes) {
		nodeMessage := node.GetNode()
		name := nodeMessage.Message.Name
		component, ok := path.NewComponent(name)
		if !ok {
			return nil, status.Errorf(codes.InvalidArgument, "Node %#v has an invalid name", name)
		}
		if _, ok := children[component]; ok {
			return nil, status.Errorf(codes.InvalidArgument, "Directory contains multiple children named %#v", name)
		}

		switch kind := nodeMessage.Message.Kind.(type) {
		case *model_filesystem_pb.DirectoryEntry_Node_Directory:
			// Create InitialContentsFetchers for all child
			// directories. These can yield even more
			// InitialContentsFetchers for grandchildren.
			childLocation, ok := nextChild()
			if !ok {
				if errChildren != nil {
					return nil, errChildren
				}
				return nil, status.Error(codes.InvalidArgument, "Directory contains fewer subdirectory contents than subdirectory nodes")
			}
			children[component] = virtual.InitialChild{}.FromDirectory(
				&objectBackedInitialContentsFetcher{
					options:          options,
					clusterReference: childLocation.Reference,
					directoryIndex:   childLocation.Index,
				},
			)
		case *model_filesystem_pb.DirectoryEntry_Node_File:
			// Create storage backed read-only files.
			properties := kind.File
			if properties == nil {
				return nil, status.Errorf(codes.InvalidArgument, "File %#v does not have any properties", name)
			}
			fileContents, err := model_filesystem.NewFileContentsEntryFromProto(
				model_core.Nested(nodeMessage, properties.Contents),
			)
			if err != nil {
				return nil, util.StatusWrapf(err, "Invalid contents for file %#v", name)
			}
			leaf, err := options.fileFactory.LookupFile(
				fileContents,
				properties.IsExecutable,
			)
			if err != nil {
				return nil, util.StatusWrapf(err, "Failed to look up file %#v", name)
			}
			children[component] = virtual.InitialChild{}.FromLeaf(leaf)
			leavesToUnlink = append(leavesToUnlink, leaf)
		case *model_filesystem_pb.DirectoryEntry_Node_Symlink:
			// Create symbolic links.
			leaf, err := options.symlinkFactory.LookupSymlink(path.UNIXFormat.NewParser(kind.Symlink.Target))
			if err != nil {
				return nil, status.Errorf(codes.InvalidArgument, "Failed to look up symlink named %#v", name)
			}
			children[component] = virtual.InitialChild{}.FromLeaf(leaf)
			leavesToUnlink = append(leavesToUnlink, leaf)
		default:
			return nil, status.Errorf(codes.InvalidArgument, "Node %#v has an unknown type", name)
		}
	}
	if errNodes != nil {
		return nil, errNodes
	}
	if _, ok := nextChild(); ok {
		return nil, status.Error(codes.InvalidArgument, "Directory contains more subdirectory contents than subdirectory nodes")
	}
	if errChildren != nil {
		return nil, errChildren
	}

	leavesToUnlink = nil
	return children, nil
}

// ApplyGetRawDirectory can be used to obtain the top level of the
// B-tree that backs a directory, assuming the contents of the directory
// still match it.
//
// This can be used to efficiently compute a Merkle tree of a directory
// hierarchy, skipping parts of the hierarchy that have not been
// modified.
type ApplyGetRawDirectory struct {
	RawDirectory model_core.Message[[]*model_filesystem_pb.DirectoryEntry, object.LocalReference]
	Err          error
}

func (icf *objectBackedInitialContentsFetcher) VirtualApply(data any) bool {
	switch typedData := data.(type) {
	case *ApplyGetRawDirectory:
		if location, err := icf.getDirectory(); err == nil {
			directory := location.GetDirectory()
			typedData.RawDirectory = directory
		} else {
			typedData.Err = err
		}
		return true
	default:
		return false
	}
}
