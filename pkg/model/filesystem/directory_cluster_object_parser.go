package filesystem

import (
	"context"
	"iter"
	"unique"

	model_core "bonanza.build/pkg/model/core"
	model_parser "bonanza.build/pkg/model/parser"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	"bonanza.build/pkg/storage/object"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// DirectoryCluster is a list of all directories that are contained in
// a single object in storage. Directories are stored in topological
// order.
//
// Objects containing directories hold a list of DirectoryEntry
// messages. The directory at index zero corresponds to this list in
// its entirety. If the object is referenced by a DirectoryReference,
// this is the root directory. If the object is referenced by a
// DirectoryEntry.Parent, it is a part of another directory's B-tree.
// Any subsequent directories correspond to directory_contents elements
// stored in the object.
//
// The advantage of a DirectoryCluster over a plain list of messages is
// that it's possible to refer to individual directories using an index.
type DirectoryCluster struct {
	Directories []Directory
}

// Directory contained in a DirectoryCluster.
type Directory struct {
	// The top level of the directory's B-tree.
	Entries []*model_filesystem_pb.DirectoryEntry

	// For each of the entries in the directory, the index at which
	// the contents of the subdirectory are stored in the
	// DirectoryCluster, if the entry is a directory_contents
	// element. For other entries, the value is -1.
	ChildDirectoryIndices []int
}

type directoryClusterObjectParser[TReference any] struct{}

// NewDirectoryClusterObjectParser creates an ObjectParser that is
// capable of parsing directory objects, and exposing them in the form
// of a DirectoryCluster.
func NewDirectoryClusterObjectParser[TReference any]() model_parser.MessageObjectParser[TReference, DirectoryCluster] {
	return &directoryClusterObjectParser[TReference]{}
}

func (*directoryClusterObjectParser[TReference]) ParseObject(in model_core.Message[[]byte, TReference], decodingParameters []byte) (model_core.Message[DirectoryCluster, TReference], error) {
	if len(decodingParameters) > 0 {
		return model_core.Message[DirectoryCluster, TReference]{}, status.Error(codes.InvalidArgument, "Unexpected decoding parameters")
	}
	entries, err := model_parser.NewProtoListObjectParser[TReference, model_filesystem_pb.DirectoryEntry]().ParseObject(in, nil)
	if err != nil {
		return model_core.Message[DirectoryCluster, TReference]{}, err
	}
	var cluster DirectoryCluster
	if err := cluster.addDirectory(entries.Message); err != nil {
		return model_core.Message[DirectoryCluster, TReference]{}, err
	}
	return model_core.Nested(in, cluster), nil
}

// addDirectory recursively visits all directories contained in the
// object and stores them in a list. This allows the caller to address
// each directory separately.
func (c *DirectoryCluster) addDirectory(entries []*model_filesystem_pb.DirectoryEntry) error {
	directoryIndex := len(c.Directories)
	c.Directories = append(c.Directories, Directory{Entries: entries})
	childDirectoryIndices := make([]int, 0, len(entries))
	for _, entry := range entries {
		switch level := entry.Level.(type) {
		case *model_filesystem_pb.DirectoryEntry_Parent_, *model_filesystem_pb.DirectoryEntry_Node_:
			childDirectoryIndices = append(childDirectoryIndices, -1)
		case *model_filesystem_pb.DirectoryEntry_DirectoryContents_:
			childDirectoryIndices = append(childDirectoryIndices, len(c.Directories))
			if err := c.addDirectory(level.DirectoryContents.Entries); err != nil {
				return err
			}
		default:
			return status.Error(codes.InvalidArgument, "Invalid directory entry level type")
		}
	}
	c.Directories[directoryIndex].ChildDirectoryIndices = childDirectoryIndices
	return nil
}

type directoryClusterObjectParserKey struct{}

func (*directoryClusterObjectParser[TReference]) AppendUniqueKeys(keys []unique.Handle[any]) []unique.Handle[any] {
	return append(keys, unique.Make[any](directoryClusterObjectParserKey{}))
}

func (directoryClusterObjectParser[TReference]) GetDecodingParametersSizeBytes() int {
	return 0
}

// DirectoryClusterLocation describes where a directory, or a part of a
// directory's B-tree is stored. It contains the reference of the object
// containing it and the index of the directory within the
// DirectoryCluster.
type DirectoryClusterLocation struct {
	Reference model_core.Decodable[object.LocalReference]
	Cluster   model_core.Message[DirectoryCluster, object.LocalReference]
	Index     int
}

// GetDirectory returns the directory stored at the location.
func (l *DirectoryClusterLocation) GetDirectory() model_core.Message[[]*model_filesystem_pb.DirectoryEntry, object.LocalReference] {
	return model_core.Nested(l.Cluster, l.Cluster.Message.Directories[l.Index].Entries)
}

// DirectoryClusterNode describes where a node of a file, symbolic link
// or subdirectory is stored.
type DirectoryClusterNode struct {
	// The location of the directory (or part of the directory's
	// B-tree) in which the node is stored.
	Location DirectoryClusterLocation
	// The index of the node within the entries of the directory.
	EntryIndex int
	// The position of the node among all nodes in the directory.
	Ordinal uint64
	// The number of subdirectory nodes preceding this node. If this
	// node is a subdirectory, this is the index at which its
	// contents are stored.
	DirectoryIndex uint64
}

// GetNode returns the node that is stored at the location.
func (n *DirectoryClusterNode) GetNode() model_core.Message[*model_filesystem_pb.DirectoryEntry_Node, object.LocalReference] {
	d := n.Location.GetDirectory()
	return model_core.Nested(d, d.Message[n.EntryIndex].Level.(*model_filesystem_pb.DirectoryEntry_Node_).Node)
}

func clusterDirectoryTree(reader model_parser.MessageObjectReader[object.LocalReference, DirectoryCluster]) directoryTree[DirectoryClusterLocation] {
	return directoryTree[DirectoryClusterLocation]{
		getEntries: func(location DirectoryClusterLocation) []*model_filesystem_pb.DirectoryEntry {
			return location.Cluster.Message.Directories[location.Index].Entries
		},
		descend: func(ctx context.Context, location DirectoryClusterLocation, parent *model_filesystem_pb.DirectoryEntry_Parent) (DirectoryClusterLocation, error) {
			reference, err := model_core.FlattenDecodableReference(model_core.Nested(location.Cluster, parent.Reference))
			if err != nil {
				return DirectoryClusterLocation{}, err
			}
			cluster, err := reader.ReadObject(ctx, reference)
			if err != nil {
				return DirectoryClusterLocation{}, err
			}
			return DirectoryClusterLocation{
				Reference: reference,
				Cluster:   cluster,
			}, nil
		},
	}
}

// LookupDirectoryClusterNode searches a directory for a file, symbolic
// link or subdirectory having a given name, and returns its location if
// found. Additional DirectoryClusters containing parts of the
// B-tree are loaded from storage as needed.
func LookupDirectoryClusterNode(
	ctx context.Context,
	reader model_parser.MessageObjectReader[object.LocalReference, DirectoryCluster],
	directory *DirectoryClusterLocation,
	name string,
) (DirectoryClusterNode, bool, error) {
	tree := clusterDirectoryTree(reader)
	location, index, directoryIndex, found, err := tree.lookupNode(ctx, *directory, name)
	if err != nil || !found {
		return DirectoryClusterNode{}, false, err
	}
	return DirectoryClusterNode{
		Location:       location,
		EntryIndex:     index,
		DirectoryIndex: directoryIndex,
	}, true, nil
}

// ResolveDirectoryClusterChild looks up the contents of the
// subdirectory stored at a given index. If the contents of the
// subdirectory are not stored in the same DirectoryCluster, additional
// DirectoryClusters containing parts of the B-tree are loaded from
// storage.
func ResolveDirectoryClusterChild(
	ctx context.Context,
	reader model_parser.MessageObjectReader[object.LocalReference, DirectoryCluster],
	directory *DirectoryClusterLocation,
	directoryIndex uint64,
) (DirectoryClusterLocation, error) {
	tree := clusterDirectoryTree(reader)
	location, index, err := tree.getDirectoryContents(ctx, *directory, directoryIndex)
	if err != nil {
		return DirectoryClusterLocation{}, err
	}
	location.Index = location.Cluster.Message.Directories[location.Index].ChildDirectoryIndices[index]
	return location, nil
}

// AllDirectoryClusterNodes returns the locations of all nodes contained
// in a directory, in alphabetical order. The first skip nodes are
// omitted. Parts of the B-tree that only contain omitted nodes are not
// loaded from storage.
func AllDirectoryClusterNodes(
	ctx context.Context,
	reader model_parser.MessageObjectReader[object.LocalReference, DirectoryCluster],
	directory *DirectoryClusterLocation,
	skip uint64,
	errOut *error,
) iter.Seq[DirectoryClusterNode] {
	return func(yield func(DirectoryClusterNode) bool) {
		tree := clusterDirectoryTree(reader)
		state := directoryTreeWalkState{skip: skip}
		if _, err := tree.walk(
			ctx,
			*directory,
			/* contents = */ false,
			&state,
			func(location DirectoryClusterLocation, index int, state *directoryTreeWalkState) bool {
				return yield(DirectoryClusterNode{
					Location:       location,
					EntryIndex:     index,
					Ordinal:        state.ordinal,
					DirectoryIndex: state.directoryOrdinal,
				})
			},
		); err != nil {
			*errOut = err
		}
	}
}

// AllDirectoryClusterChildren returns the locations of the contents of
// all subdirectories of a given directory, in order. The first skip
// subdirectories are omitted. Additional
// DirectoryClusters containing parts of the B-tree are loaded from
// storage as needed.
func AllDirectoryClusterChildren(
	ctx context.Context,
	reader model_parser.MessageObjectReader[object.LocalReference, DirectoryCluster],
	directory *DirectoryClusterLocation,
	skip uint64,
	errOut *error,
) iter.Seq[DirectoryClusterLocation] {
	return func(yield func(DirectoryClusterLocation) bool) {
		tree := clusterDirectoryTree(reader)
		state := directoryTreeWalkState{skip: skip}
		if _, err := tree.walk(
			ctx,
			*directory,
			/* contents = */ true,
			&state,
			func(location DirectoryClusterLocation, index int, state *directoryTreeWalkState) bool {
				location.Index = location.Cluster.Message.Directories[location.Index].ChildDirectoryIndices[index]
				return yield(location)
			},
		); err != nil {
			*errOut = err
		}
	}
}
