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
)

// DirectoryDereference loads the contents of a directory referenced by
// a DirectoryReference. Empty directories are not backed by an object,
// meaning that no data is loaded for those.
func DirectoryDereference[TReference any](
	ctx context.Context,
	reader model_parser.MessageObjectReader[TReference, []*model_filesystem_pb.DirectoryEntry],
	directoryReference model_core.Message[*model_filesystem_pb.DirectoryReference, TReference],
) (model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference], error) {
	reference := directoryReference.Message.GetReference()
	if reference == nil {
		// Empty directory.
		return model_core.Nested(directoryReference, []*model_filesystem_pb.DirectoryEntry(nil)), nil
	}
	entries, err := model_parser.Dereference(ctx, reader, model_core.Nested(directoryReference, reference))
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
	switch entry.WhichLevel() {
	case model_filesystem_pb.DirectoryEntry_Parent_case:
		return directoryEntryCounts{
			nodes:             uint64(entry.GetParent().GetLeafNodesCount()) + uint64(entry.GetParent().GetDirectoryNodesCount()),
			directoryNodes:    uint64(entry.GetParent().GetDirectoryNodesCount()),
			directoryContents: uint64(entry.GetParent().GetDirectoryContentsCount()),
		}, nil
	case model_filesystem_pb.DirectoryEntry_Node_case:
		if entry.GetNode().GetDirectory() != nil {
			return directoryEntryCounts{nodes: 1, directoryNodes: 1}, nil
		}
		return directoryEntryCounts{nodes: 1}, nil
	case model_filesystem_pb.DirectoryEntry_DirectoryContents_case:
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
			switch entry.WhichLevel() {
			case model_filesystem_pb.DirectoryEntry_Parent_case:
				if entry.GetParent().GetLeafNodesCount() == 0 && entry.GetParent().GetDirectoryNodesCount() == 0 {
					continue
				}
				key = entry.GetParent().GetFirstName()
			case model_filesystem_pb.DirectoryEntry_Node_case:
				key = entry.GetNode().GetName()
			case model_filesystem_pb.DirectoryEntry_DirectoryContents_case:
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

		switch xmsg := t.getEntries(page)[candidateIndex]; xmsg.WhichLevel() {
		case model_filesystem_pb.DirectoryEntry_Parent_case:
			var err error
			page, err = t.descend(ctx, page, xmsg.GetParent())
			if err != nil {
				var badPage TPage
				return badPage, 0, 0, false, err
			}
			directoryRank = candidateRank
		case model_filesystem_pb.DirectoryEntry_Node_case:
			if xmsg.GetNode().GetName() != name {
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
				switch entry.WhichLevel() {
				case model_filesystem_pb.DirectoryEntry_Parent_case:
					parent = entry.GetParent()
					break FindEntry
				case model_filesystem_pb.DirectoryEntry_DirectoryContents_case:
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
		switch entry.WhichLevel() {
		case model_filesystem_pb.DirectoryEntry_Parent_case:
			if n <= state.skip {
				state.skip -= n
				state.ordinal += n
				state.directoryOrdinal += counts.directoryNodes
			} else {
				child, err := t.descend(ctx, page, entry.GetParent())
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
			return model_parser.Dereference(ctx, reader, model_core.Nested(page, parent.GetReference()))
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
	return model_core.Nested(page, page.Message[index].GetNode()), directoryIndex, nil
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
	return model_core.Nested(page, page.Message[index].GetDirectoryContents().GetEntries()), nil
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
				return yield(model_core.Nested(page, page.Message[index].GetNode()))
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
				return yield(node, model_core.Nested(page, page.Message[index].GetDirectoryContents().GetEntries()))
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

func (l *maximumSymlinkEscapementLevels) raise(levels *uint32) {
	if levels == nil {
		l.unbounded = true
	} else if l.levels < *levels {
		l.levels = *levels
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

func (l *maximumSymlinkEscapementLevels) toProto() *uint32 {
	if l.unbounded {
		return nil
	}
	return &l.levels
}

// maximumSymlinkEscapementLevelsMessage is implemented by all messages
// that have a maximum_symlink_escapement_levels field.
type maximumSymlinkEscapementLevelsMessage interface {
	HasMaximumSymlinkEscapementLevels() bool
	GetMaximumSymlinkEscapementLevels() uint32
}

// getMaximumSymlinkEscapementLevels returns the value of the
// maximum_symlink_escapement_levels field of a message, or nil if the
// field is not set (i.e., the escapement is not bounded).
func getMaximumSymlinkEscapementLevels(m maximumSymlinkEscapementLevelsMessage) *uint32 {
	if !m.HasMaximumSymlinkEscapementLevels() {
		return nil
	}
	levels := m.GetMaximumSymlinkEscapementLevels()
	return &levels
}

func (l *maximumSymlinkEscapementLevels) raiseEntries(entries []*model_filesystem_pb.DirectoryEntry) error {
	for _, entry := range entries {
		switch entry.WhichLevel() {
		case model_filesystem_pb.DirectoryEntry_Parent_case:
			l.raise(getMaximumSymlinkEscapementLevels(entry.GetParent()))
		case model_filesystem_pb.DirectoryEntry_Node_case:
			if symlink := entry.GetNode().GetSymlink(); symlink != nil {
				escapementCounter := NewEscapementCountingScopeWalker()
				if err := path.Resolve(path.UNIXFormat.NewParser(symlink.GetTarget()), escapementCounter); err != nil {
					return util.StatusWrapf(err, "Failed to resolve target of symbolic link %#v", entry.GetNode().GetName())
				}
				l.raise(escapementCounter.GetLevels())
			}
		case model_filesystem_pb.DirectoryEntry_DirectoryContents_case:
			var child maximumSymlinkEscapementLevels
			if err := child.raiseEntries(entry.GetDirectoryContents().GetEntries()); err != nil {
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
func GetDirectoryMaximumSymlinkEscapementLevels(entries []*model_filesystem_pb.DirectoryEntry) (*uint32, error) {
	return getDirectoryEntriesMaximumSymlinkEscapementLevels(entries)
}

// getDirectoryEntriesMaximumSymlinkEscapementLevels computes the
// maximum symlink escapement levels of a part of a directory's B-tree,
// relative to the directory to which the B-tree belongs.
func getDirectoryEntriesMaximumSymlinkEscapementLevels(entries []*model_filesystem_pb.DirectoryEntry) (*uint32, error) {
	var l maximumSymlinkEscapementLevels
	if err := l.raiseEntries(entries); err != nil {
		return nil, err
	}
	return l.toProto(), nil
}
