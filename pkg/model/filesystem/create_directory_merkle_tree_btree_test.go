package filesystem_test

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	model_core "bonanza.build/pkg/model/core"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_format "bonanza.build/pkg/model/format"
	model_parser "bonanza.build/pkg/model/parser"
	model_chunking_pb "bonanza.build/pkg/proto/model/chunking"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	"bonanza.build/pkg/storage/dag"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/filesystem"
	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// inMemoryCapturableDirectory is a simple implementation of
// CapturableDirectory that is backed by maps. It only supports
// directories and symbolic links.
type inMemoryCapturableDirectory struct {
	directories map[string]*inMemoryCapturableDirectory
	symlinks    map[string]string
}

func (inMemoryCapturableDirectory) Close() error {
	return nil
}

func (d *inMemoryCapturableDirectory) ReadDir() ([]filesystem.FileInfo, error) {
	var infos filesystem.FileInfoList
	for name := range d.directories {
		infos = append(infos, filesystem.NewFileInfo(path.MustNewComponent(name), filesystem.FileTypeDirectory, false))
	}
	for name := range d.symlinks {
		infos = append(infos, filesystem.NewFileInfo(path.MustNewComponent(name), filesystem.FileTypeSymlink, false))
	}
	sort.Sort(infos)
	return infos, nil
}

func (d *inMemoryCapturableDirectory) Readlink(name path.Component) (path.Parser, error) {
	return path.UNIXFormat.NewParser(d.symlinks[name.String()]), nil
}

func (d *inMemoryCapturableDirectory) EnterCapturableDirectory(name path.Component) (*model_filesystem.CreatedDirectory[model_core.CreatedObjectTree], model_filesystem.CapturableDirectory[model_core.CreatedObjectTree, model_core.NoopReferenceMetadata], error) {
	return nil, d.directories[name.String()], nil
}

func (inMemoryCapturableDirectory) OpenForFileMerkleTreeCreation(name path.Component) (model_filesystem.CapturableFile[model_core.NoopReferenceMetadata], error) {
	panic("directory does not contain any files")
}

// noFilesCapturedDirectory is an implementation of CapturedDirectory
// for directory hierarchies that don't contain any files. It is used to
// test NewCapturedDirectoryWalker().
type noFilesCapturedDirectory struct{}

func (noFilesCapturedDirectory) Close() error {
	return nil
}

func (noFilesCapturedDirectory) EnterCapturedDirectory(name path.Component) (model_filesystem.CapturedDirectory, error) {
	panic("directory does not contain any files")
}

func (noFilesCapturedDirectory) OpenRead(name path.Component) (filesystem.FileReader, error) {
	panic("directory does not contain any files")
}

// createdObjectReader is an ObjectReader that reads objects directly
// from a CreatedObjectTree.
type createdObjectReader struct{}

func (createdObjectReader) ReadObject(ctx context.Context, reference model_core.CreatedObjectTree) (model_core.Message[[]byte, model_core.CreatedObjectTree], error) {
	return model_core.NewMessage(reference.GetPayload(), object.OutgoingReferencesList[model_core.CreatedObjectTree](reference.Metadata)), nil
}

// localObjectReader is an ObjectReader that reads objects from a map,
// keyed by local reference.
type localObjectReader map[object.LocalReference]*object.Contents

func (r localObjectReader) add(tree model_core.CreatedObjectTree) {
	r[tree.GetLocalReference()] = tree.Contents
	for _, child := range tree.Metadata {
		r.add(child)
	}
}

func (r localObjectReader) ReadObject(ctx context.Context, reference object.LocalReference) (model_core.Message[[]byte, object.LocalReference], error) {
	contents, ok := r[reference]
	if !ok {
		return model_core.Message[[]byte, object.LocalReference]{}, fmt.Errorf("object %s not found", reference)
	}
	return model_core.NewMessage(contents.GetPayload(), object.OutgoingReferences[object.LocalReference](contents)), nil
}

func TestCreateDirectoryMerkleTreeLargeDirectory(t *testing.T) {
	ctx := context.Background()

	// Create a directory containing many subdirectories, so that
	// its B-tree needs to be split across multiple objects. Some of
	// the subdirectories are interleaved with symbolic links. One
	// of the subdirectories contains a nested directory and a
	// symbolic link that escapes the root directory.
	const childrenCount = 1000
	const specialIndex = 567
	root := &inMemoryCapturableDirectory{
		directories: map[string]*inMemoryCapturableDirectory{},
		symlinks:    map[string]string{},
	}
	var expectedNodeNames []string
	for i := 0; i < childrenCount; i++ {
		name := fmt.Sprintf("dir%04d", i)
		root.directories[name] = &inMemoryCapturableDirectory{}
		expectedNodeNames = append(expectedNodeNames, name)
		if i%50 == 0 {
			linkName := fmt.Sprintf("dir%04d.lnk", i)
			root.symlinks[linkName] = name
			expectedNodeNames = append(expectedNodeNames, linkName)
		}
	}
	specialName := fmt.Sprintf("dir%04d", specialIndex)
	root.directories[specialName] = &inMemoryCapturableDirectory{
		directories: map[string]*inMemoryCapturableDirectory{
			"nested": {},
		},
		symlinks: map[string]string{
			"link": "../../../target",
		},
	}

	directoryParameters, err := model_filesystem.NewDirectoryCreationParametersFromProto(
		model_filesystem_pb.DirectoryCreationParameters_builder{
			Access: &model_filesystem_pb.DirectoryAccessParameters{},
			DirectoriesParameters: model_chunking_pb.Parameters_builder{
				MinimumSizeBytes: 1024,
				HorizonSizeBytes: 8 * 1024,
			}.Build(),
		}.Build(),
		object.SHA256V1ReferenceFormat,
		model_encoding.NewBinaryEncoderFactory(true),
	)
	require.NoError(t, err)

	group, groupCtx := errgroup.WithContext(ctx)
	var createdRootDirectory model_filesystem.CreatedDirectory[model_core.CreatedObjectTree]
	group.Go(func() error {
		return model_filesystem.CreateDirectoryMerkleTree(
			groupCtx,
			semaphore.NewWeighted(1),
			group,
			directoryParameters,
			model_filesystem.CapturableDirectory[model_core.CreatedObjectTree, model_core.NoopReferenceMetadata](root),
			model_filesystem.FileDiscardingDirectoryMerkleTreeCapturer,
			&createdRootDirectory,
		)
	})
	require.NoError(t, group.Wait())

	// The symbolic link escapes its parent directory by three
	// levels, meaning it escapes the root directory by two.
	testutil.RequireEqualProto(t, &wrapperspb.UInt32Value{Value: 2}, createdRootDirectory.MaximumSymlinkEscapementLevels)

	// Store the top level of the B-tree in an object, so that the
	// directory can be accessed through a DirectoryReference.
	rootObject, err := model_core.MarshalAndEncodeDeterministic(
		createdRootDirectory.Message.ToMarshaler(model_format.NewProtoList[model_filesystem_pb.DirectoryEntry]()),
		object.SHA256V1ReferenceFormat,
		directoryParameters.GetEncoder(),
	)
	require.NoError(t, err)
	rootTree := model_core.CreatedObjectTree(rootObject.Value)
	rootMessage, metadata := createdRootDirectory.Message.SortAndSetReferences()
	rootDirectory := model_core.NewMessage(
		rootMessage.Message,
		object.OutgoingReferencesList[model_core.CreatedObjectTree](metadata),
	)

	// The directory does not fit in a single object, so at least
	// part of it must be stored in separate objects. The counts
	// stored in the parent nodes must add up.
	sawParent := false
	for _, entry := range rootDirectory.Message {
		if entry.GetParent() != nil {
			sawParent = true
		}
	}
	require.True(t, sawParent)
	directoriesCount, err := model_filesystem.GetDirectoryDirectoriesCount(rootDirectory.Message)
	require.NoError(t, err)
	require.Equal(t, uint32(childrenCount), directoriesCount)

	// The maximum symlink escapement levels should be derivable
	// from the root directory, without loading any objects.
	maximumSymlinkEscapementLevels, err := model_filesystem.GetDirectoryMaximumSymlinkEscapementLevels(rootDirectory.Message)
	require.NoError(t, err)
	testutil.RequireEqualProto(t, &wrapperspb.UInt32Value{Value: 2}, maximumSymlinkEscapementLevels)

	entriesReader := model_parser.LookupParsedObjectReader(
		model_parser.NewParsedObjectPoolIngester[model_core.CreatedObjectTree](
			/* pool = */ nil,
			createdObjectReader{},
		),
		model_parser.NewProtoListObjectParser[model_core.CreatedObjectTree, model_filesystem_pb.DirectoryEntry](),
	)

	t.Run("AllNodes", func(t *testing.T) {
		var errIter error
		var names []string
		for node := range model_filesystem.DirectoryAllNodes(ctx, entriesReader, rootDirectory, &errIter) {
			names = append(names, node.Message.GetName())
		}
		require.NoError(t, errIter)
		require.Equal(t, expectedNodeNames, names)
	})

	t.Run("AllChildren", func(t *testing.T) {
		var errIter error
		i := 0
		for node, child := range model_filesystem.DirectoryAllChildren(ctx, entriesReader, rootDirectory, &errIter) {
			require.Equal(t, fmt.Sprintf("dir%04d", i), node.Message.GetName())
			childDirectoriesCount, err := model_filesystem.GetDirectoryDirectoriesCount(child.Message)
			require.NoError(t, err)
			require.Equal(t, node.Message.GetDirectory().GetDirectoriesCount(), childDirectoriesCount)
			i++
		}
		require.NoError(t, errIter)
		require.Equal(t, childrenCount, i)
	})

	t.Run("LookupChild", func(t *testing.T) {
		for _, index := range []int{0, 1, specialIndex, childrenCount - 1} {
			child, err := model_filesystem.DirectoryLookupChild(
				ctx,
				entriesReader,
				rootDirectory,
				path.MustNewComponent(fmt.Sprintf("dir%04d", index)),
			)
			require.NoError(t, err)
			require.True(t, child.IsSet())

			var errIter error
			var nodes []*model_filesystem_pb.DirectoryEntry_Node
			for node := range model_filesystem.DirectoryAllNodes(ctx, entriesReader, child, &errIter) {
				nodes = append(nodes, node.Message)
			}
			require.NoError(t, errIter)
			if index == specialIndex {
				require.Len(t, nodes, 2)
				testutil.RequireEqualProto(t, model_filesystem_pb.DirectoryEntry_Node_builder{
					Name: "link",
					Symlink: model_filesystem_pb.SymlinkProperties_builder{
						Target: "../../../target",
					}.Build(),
				}.Build(), nodes[0])
				require.Equal(t, "nested", nodes[1].GetName())
			} else {
				require.Empty(t, nodes)
			}
		}
	})

	t.Run("LookupNode", func(t *testing.T) {
		node, _, err := model_filesystem.DirectoryLookupNode(ctx, entriesReader, rootDirectory, path.MustNewComponent("dir0550.lnk"))
		require.NoError(t, err)
		testutil.RequireEqualProto(t, model_filesystem_pb.DirectoryEntry_Node_builder{
			Name: "dir0550.lnk",
			Symlink: model_filesystem_pb.SymlinkProperties_builder{
				Target: "dir0550",
			}.Build(),
		}.Build(), node.Message)

		node, directoryIndex, err := model_filesystem.DirectoryLookupNode(ctx, entriesReader, rootDirectory, path.MustNewComponent("dir0789"))
		require.NoError(t, err)
		require.Equal(t, "dir0789", node.Message.GetName())
		require.Equal(t, uint64(789), directoryIndex)

		// Names that sort before, between and after the
		// existing entries.
		for _, name := range []string{"a", "dir0550.a", "dir0550.lnk2", "zzz"} {
			node, _, err := model_filesystem.DirectoryLookupNode(ctx, entriesReader, rootDirectory, path.MustNewComponent(name))
			require.NoError(t, err)
			require.False(t, node.IsSet(), name)
		}
	})

	t.Run("OutOfRange", func(t *testing.T) {
		_, err := model_filesystem.DirectoryGetChild(ctx, entriesReader, rootDirectory, childrenCount)
		require.Error(t, err)
	})

	t.Run("Cluster", func(t *testing.T) {
		// Access the directory in the same way as the virtual
		// file system, namely through DirectoryClusters.
		objects := localObjectReader{}
		objects.add(rootTree)
		clusterReader := model_parser.LookupParsedObjectReader(
			model_parser.NewParsedObjectPoolIngester[object.LocalReference](
				/* pool = */ nil,
				objects,
			),
			model_filesystem.NewDirectoryClusterObjectParser[object.LocalReference](),
		)
		rootReference := util.Must(model_core.NewDecodable(rootObject.Value.GetLocalReference(), rootObject.GetDecodingParameters()))
		rootCluster, err := clusterReader.ReadObject(ctx, rootReference)
		require.NoError(t, err)
		rootLocation := model_filesystem.DirectoryClusterLocation{
			Reference: rootReference,
			Cluster:   rootCluster,
		}

		// Iterate over all nodes, starting at an offset.
		const skip = 500
		var errIter error
		var names []string
		var firstDirectoryIndex uint64
		for node := range model_filesystem.AllDirectoryClusterNodes(ctx, clusterReader, &rootLocation, skip, &errIter) {
			if names == nil {
				require.Equal(t, uint64(skip), node.Ordinal)
				firstDirectoryIndex = node.DirectoryIndex
			}
			names = append(names, node.GetNode().Message.GetName())
		}
		require.NoError(t, errIter)
		require.Equal(t, expectedNodeNames[skip:], names)
		expectedFirstDirectoryIndex := uint64(0)
		for _, name := range expectedNodeNames[:skip] {
			if !strings.HasSuffix(name, ".lnk") {
				expectedFirstDirectoryIndex++
			}
		}
		require.Equal(t, expectedFirstDirectoryIndex, firstDirectoryIndex)

		// Iterate over the contents of the subdirectories,
		// starting at an offset.
		childrenSeen := 0
		for childLocation := range model_filesystem.AllDirectoryClusterChildren(ctx, clusterReader, &rootLocation, specialIndex, &errIter) {
			if childrenSeen == 0 {
				childDirectoriesCount, err := model_filesystem.GetDirectoryDirectoriesCount(childLocation.GetDirectory().Message)
				require.NoError(t, err)
				require.Equal(t, uint32(1), childDirectoriesCount)
			}
			childrenSeen++
		}
		require.NoError(t, errIter)
		require.Equal(t, childrenCount-specialIndex, childrenSeen)

		// Look up a subdirectory by name, and resolve its
		// contents.
		node, found, err := model_filesystem.LookupDirectoryClusterNode(ctx, clusterReader, &rootLocation, specialName)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, uint64(specialIndex), node.DirectoryIndex)
		childLocation, err := model_filesystem.ResolveDirectoryClusterChild(ctx, clusterReader, &rootLocation, node.DirectoryIndex)
		require.NoError(t, err)
		linkNode, found, err := model_filesystem.LookupDirectoryClusterNode(ctx, clusterReader, &childLocation, "link")
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, "../../../target", linkNode.GetNode().Message.GetSymlink().GetTarget())
	})

	t.Run("CapturedDirectoryWalker", func(t *testing.T) {
		// Walk over all objects using the walker that is used
		// to upload directory hierarchies. This requires that
		// the names of all subdirectories can be reobtained.
		objects := localObjectReader{}
		objects.add(rootTree)
		seen := map[object.LocalReference]bool{}
		var walk func(dag.ObjectContentsWalker)
		walk = func(w dag.ObjectContentsWalker) {
			contents, children, err := w.GetContents(ctx)
			require.NoError(t, err)
			seen[contents.GetLocalReference()] = true
			for _, child := range children {
				if child != nil {
					walk(child)
				}
			}
		}
		walk(model_filesystem.NewCapturedDirectoryWalker(
			directoryParameters.DirectoryAccessParameters,
			/* fileParameters = */ nil,
			noFilesCapturedDirectory{},
			&rootTree,
			rootObject.GetDecodingParameters(),
		))
		require.Len(t, seen, len(objects))
	})
}
