package filesystem

import (
	"context"
	"iter"
	"math"

	model_core "bonanza.build/pkg/model/core"
	model_parser "bonanza.build/pkg/model/parser"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"

	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// DirectoryDereference loads the contents of a directory referenced by
// a DirectoryReference. Empty directories are not backed by an object,
// meaning that no data is loaded for those.
func DirectoryDereference[TReference any](
	ctx context.Context,
	reader model_parser.MessageObjectReader[TReference, []*model_filesystem_pb.DirectoryEntry],
	directoryReference model_core.Message[*model_filesystem_pb.DirectoryReference, TReference],
) (model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference], error) {
	if directoryReference.Message.Reference == nil {
		// Empty directory.
		return model_core.Nested(directoryReference, []*model_filesystem_pb.DirectoryEntry(nil)), nil
	}
	entries, err := model_parser.Dereference(ctx, reader, model_core.Nested(directoryReference, directoryReference.Message.Reference))
	if err != nil {
		return model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference]{}, err
	}
	return entries, nil
}

// directoryEntryCounts contains the number of nodes and
// directory_contents elements that are contained in a part of a
// directory's B-tree.
type directoryEntryCounts struct {
	nodes             uint64
	directoryNodes    uint64
	directoryContents uint64
}

func (c *directoryEntryCounts) add(other directoryEntryCounts) {
	c.nodes += other.nodes
	c.directoryNodes += other.directoryNodes
	c.directoryContents += other.directoryContents
}

func getDirectoryEntryCounts(entry *model_filesystem_pb.DirectoryEntry) (directoryEntryCounts, error) {
	switch level := entry.Level.(type) {
	case *model_filesystem_pb.DirectoryEntry_Parent_:
		return directoryEntryCounts{
			nodes:             uint64(level.Parent.LeafNodesCount) + uint64(level.Parent.DirectoryNodesCount),
			directoryNodes:    uint64(level.Parent.DirectoryNodesCount),
			directoryContents: uint64(level.Parent.DirectoryContentsCount),
		}, nil
	case *model_filesystem_pb.DirectoryEntry_Node_:
		if _, ok := level.Node.Kind.(*model_filesystem_pb.DirectoryEntry_Node_Directory); ok {
			return directoryEntryCounts{nodes: 1, directoryNodes: 1}, nil
		}
		return directoryEntryCounts{nodes: 1}, nil
	case *model_filesystem_pb.DirectoryEntry_DirectoryContents_:
		return directoryEntryCounts{directoryContents: 1}, nil
	default:
		return directoryEntryCounts{}, status.Error(codes.InvalidArgument, "Invalid directory entry level type")
	}
}

func getDirectoryEntriesCounts(entries []*model_filesystem_pb.DirectoryEntry) (directoryEntryCounts, error) {
	var counts directoryEntryCounts
	for _, entry := range entries {
		entryCounts, err := getDirectoryEntryCounts(entry)
		if err != nil {
			return directoryEntryCounts{}, err
		}
		counts.add(entryCounts)
	}
	return counts, nil
}

// GetDirectoryDirectoriesCount returns the number of subdirectories
// contained in a directory, without loading any objects from storage.
func GetDirectoryDirectoriesCount(entries []*model_filesystem_pb.DirectoryEntry) (uint32, error) {
	counts, err := getDirectoryEntriesCounts(entries)
	if err != nil {
		return 0, err
	}
	if counts.directoryNodes != counts.directoryContents {
		return 0, status.Errorf(codes.InvalidArgument, "Directory contains %d subdirectory nodes, but %d subdirectory contents", counts.directoryNodes, counts.directoryContents)
	}
	if counts.directoryNodes > math.MaxUint32 {
		return 0, status.Error(codes.InvalidArgument, "Directory contains too many subdirectories")
	}
	return uint32(counts.directoryNodes), nil
}

// directoryTree provides generic algorithms for traversing the B-tree
// of a directory. The B-tree consists of pages of entries, where pages
// are represented by TPage. This allows the same algorithms to be used
// both on plain messages and on DirectoryClusters.
type directoryTree[TPage any] struct {
	getEntries func(TPage) []*model_filesystem_pb.DirectoryEntry
	descend    func(ctx context.Context, page TPage, parent *model_filesystem_pb.DirectoryEntry_Parent) (TPage, error)
}

// lookupNode searches the second section of the B-tree for a node with
// a given name. Upon success, it returns the page and index at which
// the node is stored, and the number of subdirectory nodes preceding
// it. The latter can be used to locate the contents of the
// subdirectory in the first section of the B-tree.
func (t *directoryTree[TPage]) lookupNode(ctx context.Context, page TPage, name string) (TPage, int, uint64, bool, error) {
	var directoryRank uint64
	for {
		// Find the last entry containing nodes whose key
		// precedes or is equal to the name. Entries that don't
		// contain any nodes (i.e., ones that are part of the
		// first section) are skipped.
		candidateIndex, candidateRank := -1, directoryRank
		rank := directoryRank
		for i, entry := range t.getEntries(page) {
			var key string
			switch level := entry.Level.(type) {
			case *model_filesystem_pb.DirectoryEntry_Parent_:
				if level.Parent.LeafNodesCount == 0 && level.Parent.DirectoryNodesCount == 0 {
					continue
				}
				key = level.Parent.FirstName
			case *model_filesystem_pb.DirectoryEntry_Node_:
				key = level.Node.Name
			case *model_filesystem_pb.DirectoryEntry_DirectoryContents_:
				continue
			default:
				var badPage TPage
				return badPage, 0, 0, false, status.Error(codes.InvalidArgument, "Invalid directory entry level type")
			}
			if key > name {
				break
			}
			candidateIndex, candidateRank = i, rank
			counts, err := getDirectoryEntryCounts(entry)
			if err != nil {
				var badPage TPage
				return badPage, 0, 0, false, err
			}
			rank += counts.directoryNodes
		}
		if candidateIndex < 0 {
			var badPage TPage
			return badPage, 0, 0, false, nil
		}

		switch level := t.getEntries(page)[candidateIndex].Level.(type) {
		case *model_filesystem_pb.DirectoryEntry_Parent_:
			var err error
			page, err = t.descend(ctx, page, level.Parent)
			if err != nil {
				var badPage TPage
				return badPage, 0, 0, false, err
			}
			directoryRank = candidateRank
		case *model_filesystem_pb.DirectoryEntry_Node_:
			if level.Node.Name != name {
				var badPage TPage
				return badPage, 0, 0, false, nil
			}
			return page, candidateIndex, candidateRank, true, nil
		default:
			panic("unreachable")
		}
	}
}

// getDirectoryContents returns the page and index at which the
// contents of the subdirectory with a given index are stored.
func (t *directoryTree[TPage]) getDirectoryContents(ctx context.Context, page TPage, directoryIndex uint64) (TPage, int, error) {
	remaining := directoryIndex
	for {
		var parent *model_filesystem_pb.DirectoryEntry_Parent
	FindEntry:
		for i, entry := range t.getEntries(page) {
			counts, err := getDirectoryEntryCounts(entry)
			if err != nil {
				var badPage TPage
				return badPage, 0, err
			}
			if remaining < counts.directoryContents {
				switch level := entry.Level.(type) {
				case *model_filesystem_pb.DirectoryEntry_Parent_:
					parent = level.Parent
					break FindEntry
				case *model_filesystem_pb.DirectoryEntry_DirectoryContents_:
					return page, i, nil
				}
			}
			remaining -= counts.directoryContents
		}
		if parent == nil {
			var badPage TPage
			return badPage, 0, status.Errorf(codes.InvalidArgument, "Subdirectory index %d is out of range", directoryIndex)
		}
		var err error
		page, err = t.descend(ctx, page, parent)
		if err != nil {
			var badPage TPage
			return badPage, 0, err
		}
	}
}

// directoryTreeWalkState is used by directoryTree.walk() to keep track
// of its progress.
type directoryTreeWalkState struct {
	skip     uint64
	ordinal  uint64
	finished bool

	// The number of subdirectory nodes that precede the current
	// position. Only maintained when walking over nodes.
	directoryOrdinal uint64
}

// walk invokes a callback for all nodes (if contents is false) or all
// directory_contents elements (if contents is true) contained in the
// B-tree, skipping the first state.skip elements. Parts of the B-tree
// that don't contain any elements of interest are not loaded.
func (t *directoryTree[TPage]) walk(ctx context.Context, page TPage, contents bool, state *directoryTreeWalkState, yield func(page TPage, index int, state *directoryTreeWalkState) bool) (bool, error) {
	for i, entry := range t.getEntries(page) {
		counts, err := getDirectoryEntryCounts(entry)
		if err != nil {
			return false, err
		}
		n, remainder := counts.nodes, uint64(0)
		if contents {
			n, remainder = counts.directoryContents, counts.nodes
		}
		switch level := entry.Level.(type) {
		case *model_filesystem_pb.DirectoryEntry_Parent_:
			if n <= state.skip {
				state.skip -= n
				state.ordinal += n
				state.directoryOrdinal += counts.directoryNodes
			} else {
				child, err := t.descend(ctx, page, level.Parent)
				if err != nil {
					return false, err
				}
				if cont, err := t.walk(ctx, child, contents, state, yield); !cont || err != nil {
					return cont, err
				}
			}
		default:
			if n > 0 {
				if state.skip > 0 {
					state.skip--
				} else if !yield(page, i, state) {
					return false, nil
				}
				state.ordinal++
				if !contents {
					state.directoryOrdinal += counts.directoryNodes
				}
			}
		}
		if remainder > 0 {
			// We've reached the second section of the
			// B-tree. There are no more directory_contents
			// elements.
			state.finished = true
		}
		if state.finished {
			return true, nil
		}
	}
	return true, nil
}

// messageDirectoryTree is an instance of directoryTree that operates
// on plain messages.
func messageDirectoryTree[TReference any](reader model_parser.MessageObjectReader[TReference, []*model_filesystem_pb.DirectoryEntry]) directoryTree[model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference]] {
	return directoryTree[model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference]]{
		getEntries: func(page model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference]) []*model_filesystem_pb.DirectoryEntry {
			return page.Message
		},
		descend: func(ctx context.Context, page model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference], parent *model_filesystem_pb.DirectoryEntry_Parent) (model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference], error) {
			return model_parser.Dereference(ctx, reader, model_core.Nested(page, parent.Reference))
		},
	}
}

// DirectoryLookupNode searches a directory for a file, symbolic link
// or subdirectory having a given name. If found, the node is returned,
// together with the index of the subdirectory among all subdirectories
// (only meaningful if the node is a directory). If not found, a message
// is returned that is not set.
func DirectoryLookupNode[TReference any](
	ctx context.Context,
	reader model_parser.MessageObjectReader[TReference, []*model_filesystem_pb.DirectoryEntry],
	directory model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference],
	name path.Component,
) (model_core.Message[*model_filesystem_pb.DirectoryEntry_Node, TReference], uint64, error) {
	tree := messageDirectoryTree(reader)
	page, index, directoryIndex, found, err := tree.lookupNode(ctx, directory, name.String())
	if err != nil || !found {
		return model_core.Message[*model_filesystem_pb.DirectoryEntry_Node, TReference]{}, 0, err
	}
	return model_core.Nested(page, page.Message[index].Level.(*model_filesystem_pb.DirectoryEntry_Node_).Node), directoryIndex, nil
}

// DirectoryGetChild returns the contents of the subdirectory stored at
// a given index.
func DirectoryGetChild[TReference any](
	ctx context.Context,
	reader model_parser.MessageObjectReader[TReference, []*model_filesystem_pb.DirectoryEntry],
	directory model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference],
	directoryIndex uint64,
) (model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference], error) {
	tree := messageDirectoryTree(reader)
	page, index, err := tree.getDirectoryContents(ctx, directory, directoryIndex)
	if err != nil {
		return model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference]{}, err
	}
	return model_core.Nested(page, page.Message[index].Level.(*model_filesystem_pb.DirectoryEntry_DirectoryContents_).DirectoryContents.Entries), nil
}

// DirectoryLookupChild returns the contents of the subdirectory having
// a given name. If no subdirectory with the provided name exists, a
// message is returned that is not set.
func DirectoryLookupChild[TReference any](
	ctx context.Context,
	reader model_parser.MessageObjectReader[TReference, []*model_filesystem_pb.DirectoryEntry],
	directory model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference],
	name path.Component,
) (model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference], error) {
	node, directoryIndex, err := DirectoryLookupNode(ctx, reader, directory, name)
	if err != nil || !node.IsSet() || node.Message.GetDirectory() == nil {
		return model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference]{}, err
	}
	return DirectoryGetChild(ctx, reader, directory, directoryIndex)
}

// DirectoryAllNodes returns an iterator over all files, symbolic links
// and subdirectories contained in a directory, in alphabetical order.
// The contents of subdirectories are not loaded.
func DirectoryAllNodes[TReference any](
	ctx context.Context,
	reader model_parser.MessageObjectReader[TReference, []*model_filesystem_pb.DirectoryEntry],
	directory model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference],
	errOut *error,
) iter.Seq[model_core.Message[*model_filesystem_pb.DirectoryEntry_Node, TReference]] {
	return func(yield func(model_core.Message[*model_filesystem_pb.DirectoryEntry_Node, TReference]) bool) {
		tree := messageDirectoryTree(reader)
		var state directoryTreeWalkState
		if _, err := tree.walk(
			ctx,
			directory,
			/* contents = */ false,
			&state,
			func(page model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference], index int, state *directoryTreeWalkState) bool {
				return yield(model_core.Nested(page, page.Message[index].Level.(*model_filesystem_pb.DirectoryEntry_Node_).Node))
			},
		); err != nil {
			*errOut = err
		}
	}
}

// DirectoryAllChildren returns an iterator over all subdirectories of
// a directory. For each subdirectory, both its node and its contents
// are returned.
func DirectoryAllChildren[TReference any](
	ctx context.Context,
	reader model_parser.MessageObjectReader[TReference, []*model_filesystem_pb.DirectoryEntry],
	directory model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference],
	errOut *error,
) iter.Seq2[model_core.Message[*model_filesystem_pb.DirectoryEntry_Node, TReference], model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference]] {
	return func(yield func(model_core.Message[*model_filesystem_pb.DirectoryEntry_Node, TReference], model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference]) bool) {
		// Iterate over the subdirectory nodes and their contents
		// in lockstep.
		var errNodes error
		nextNode, stopNodes := iter.Pull(DirectoryAllNodes(ctx, reader, directory, &errNodes))
		defer stopNodes()
		getNextDirectoryNode := func() (model_core.Message[*model_filesystem_pb.DirectoryEntry_Node, TReference], bool) {
			for {
				node, ok := nextNode()
				if !ok || node.Message.GetDirectory() != nil {
					return node, ok
				}
			}
		}

		tree := messageDirectoryTree(reader)
		var state directoryTreeWalkState
		var errYield error
		if _, err := tree.walk(
			ctx,
			directory,
			/* contents = */ true,
			&state,
			func(page model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference], index int, state *directoryTreeWalkState) bool {
				node, ok := getNextDirectoryNode()
				if !ok {
					errYield = status.Error(codes.InvalidArgument, "Directory contains more subdirectory contents than subdirectory nodes")
					return false
				}
				return yield(node, model_core.Nested(page, page.Message[index].Level.(*model_filesystem_pb.DirectoryEntry_DirectoryContents_).DirectoryContents.Entries))
			},
		); err != nil {
			*errOut = err
			return
		}
		if errYield != nil {
			*errOut = errYield
			return
		}
		if _, ok := getNextDirectoryNode(); ok {
			*errOut = status.Error(codes.InvalidArgument, "Directory contains fewer subdirectory contents than subdirectory nodes")
			return
		}
		if errNodes != nil {
			*errOut = errNodes
		}
	}
}

// maximumSymlinkEscapementLevels is a helper type for computing the
// maximum symlink escapement levels of directory hierarchies.
type maximumSymlinkEscapementLevels struct {
	levels    uint32
	unbounded bool
}

func (l *maximumSymlinkEscapementLevels) raise(levels *wrapperspb.UInt32Value) {
	if levels == nil {
		l.unbounded = true
	} else if l.levels < levels.Value {
		l.levels = levels.Value
	}
}

// raiseChild raises the maximum symlink escapement levels to that of a
// subdirectory. As the subdirectory is one level deeper, its value
// needs to be decremented by one.
func (l *maximumSymlinkEscapementLevels) raiseChild(child *maximumSymlinkEscapementLevels) {
	if child.unbounded {
		l.unbounded = true
	} else if child.levels > 0 && l.levels < child.levels-1 {
		l.levels = child.levels - 1
	}
}

func (l *maximumSymlinkEscapementLevels) toProto() *wrapperspb.UInt32Value {
	if l.unbounded {
		return nil
	}
	return &wrapperspb.UInt32Value{Value: l.levels}
}

func (l *maximumSymlinkEscapementLevels) raiseEntries(entries []*model_filesystem_pb.DirectoryEntry) error {
	for _, entry := range entries {
		switch level := entry.Level.(type) {
		case *model_filesystem_pb.DirectoryEntry_Parent_:
			l.raise(level.Parent.MaximumSymlinkEscapementLevels)
		case *model_filesystem_pb.DirectoryEntry_Node_:
			if symlink, ok := level.Node.Kind.(*model_filesystem_pb.DirectoryEntry_Node_Symlink); ok {
				escapementCounter := NewEscapementCountingScopeWalker()
				if err := path.Resolve(path.UNIXFormat.NewParser(symlink.Symlink.Target), escapementCounter); err != nil {
					return util.StatusWrapf(err, "Failed to resolve target of symbolic link %#v", level.Node.Name)
				}
				l.raise(escapementCounter.GetLevels())
			}
		case *model_filesystem_pb.DirectoryEntry_DirectoryContents_:
			var child maximumSymlinkEscapementLevels
			if err := child.raiseEntries(level.DirectoryContents.Entries); err != nil {
				return err
			}
			l.raiseChild(&child)
		default:
			return status.Error(codes.InvalidArgument, "Invalid directory entry level type")
		}
		if l.unbounded {
			return nil
		}
	}
	return nil
}

// GetDirectoryMaximumSymlinkEscapementLevels computes the maximum
// number of levels by which symbolic links contained in a directory
// hierarchy escape the root of the directory hierarchy. This function
// does not load any objects from storage. Instead, it relies on the
// values stored in DirectoryEntry.Parent messages.
//
// The value that is returned has the same semantics as
// DirectoryReference.maximum_symlink_escapement_levels.
func GetDirectoryMaximumSymlinkEscapementLevels(entries []*model_filesystem_pb.DirectoryEntry) (*wrapperspb.UInt32Value, error) {
	return getDirectoryEntriesMaximumSymlinkEscapementLevels(entries)
}

// getDirectoryEntriesMaximumSymlinkEscapementLevels computes the
// maximum symlink escapement levels of a part of a directory's B-tree,
// relative to the directory to which the B-tree belongs.
func getDirectoryEntriesMaximumSymlinkEscapementLevels(entries []*model_filesystem_pb.DirectoryEntry) (*wrapperspb.UInt32Value, error) {
	var l maximumSymlinkEscapementLevels
	if err := l.raiseEntries(entries); err != nil {
		return nil, err
	}
	return l.toProto(), nil
}
