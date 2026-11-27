package analysis

import (
	"context"

	model_core "bonanza.build/pkg/model/core"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_parser "bonanza.build/pkg/model/parser"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	"bonanza.build/pkg/storage/object"
)

// unmodifiedDirectory is a handle to a directory that has been written
// to storage, whose contents are loaded lazily. A directory is either
// referenced through a DirectoryReference, provided in literal form, or
// identified by the index of a child directory in a parent directory.
// The latter makes it possible to refer to child directories without
// loading the B-tree containing them.
type unmodifiedDirectory[TReference object.BasicReference] struct {
	// If set, the directory is referenced by a DirectoryReference.
	reference model_core.Message[*model_filesystem_pb.DirectoryReference, TReference]

	// If set and childIndex is negative, this field contains the
	// contents of the directory. If childIndex is non-negative, it
	// contains the contents of the parent directory.
	directory  model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference]
	childIndex int
}

func newUnmodifiedDirectoryFromReference[TReference object.BasicReference](reference model_core.Message[*model_filesystem_pb.DirectoryReference, TReference]) unmodifiedDirectory[TReference] {
	return unmodifiedDirectory[TReference]{
		reference:  reference,
		childIndex: -1,
	}
}

func newUnmodifiedDirectoryFromContents[TReference object.BasicReference](directory model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference]) unmodifiedDirectory[TReference] {
	return unmodifiedDirectory[TReference]{
		directory:  directory,
		childIndex: -1,
	}
}

func newUnmodifiedDirectoryFromChild[TReference object.BasicReference](parent model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference], childIndex int) unmodifiedDirectory[TReference] {
	return unmodifiedDirectory[TReference]{
		directory:  parent,
		childIndex: childIndex,
	}
}

func (d *unmodifiedDirectory[TReference]) IsSet() bool {
	return d.reference.IsSet() || d.directory.IsSet()
}

func (d *unmodifiedDirectory[TReference]) Clear() {
	*d = unmodifiedDirectory[TReference]{}
}

// isEqualTo returns true if two handles are known to refer to the
// same directory, without loading any contents from storage.
func (d *unmodifiedDirectory[TReference]) isEqualTo(other *unmodifiedDirectory[TReference]) bool {
	if d.reference.IsSet() {
		return other.reference.IsSet() && model_core.MessagesEqual(d.reference, other.reference)
	}
	return !other.reference.IsSet() &&
		d.childIndex == other.childIndex &&
		model_core.MessagesEqualList(d.directory, other.directory)
}

// maximumSymlinkEscapementLevelsAtMost returns true if the symbolic
// links contained in the directory are known to escape it by at most
// a given number of levels, without loading any data from storage.
func (d *unmodifiedDirectory[TReference]) maximumSymlinkEscapementLevelsAtMost(maximumEscapementLevels uint32) bool {
	if d.reference.IsSet() {
		m := d.reference.Message
		return m.HasMaximumSymlinkEscapementLevels() && m.GetMaximumSymlinkEscapementLevels() <= maximumEscapementLevels
	}
	return false
}

// load the contents of the directory.
func (d *unmodifiedDirectory[TReference]) load(ctx context.Context, entriesReader model_parser.MessageObjectReader[TReference, []*model_filesystem_pb.DirectoryEntry]) (model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference], error) {
	if d.reference.IsSet() {
		return model_filesystem.DirectoryDereference(ctx, entriesReader, d.reference)
	}
	if d.childIndex < 0 {
		return d.directory, nil
	}
	return model_filesystem.DirectoryGetChild(ctx, entriesReader, d.directory, uint64(d.childIndex))
}
