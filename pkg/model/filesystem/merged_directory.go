package filesystem

import (
	"context"
	"slices"

	model_core "bonanza.build/pkg/model/core"
	model_parser "bonanza.build/pkg/model/parser"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// MergeConflictPolicy determines how CreateMergedDirectory() resolves
// conflicts between directories that are merged.
type MergeConflictPolicy int

const (
	// MergeConflictPolicyError causes conflicts to be reported as
	// errors. Files and symbolic links having the same name are only
	// considered to be conflicting if they differ. Directories
	// having the same name are merged.
	MergeConflictPolicyError MergeConflictPolicy = iota

	// MergeConflictPolicyKeepFirst causes conflicts to be resolved
	// by retaining the file, symbolic link or directory that was
	// merged first.
	MergeConflictPolicyKeepFirst
)

type mergedDirectorySourceKind int

const (
	// The contents of a directory referenced by a
	// DirectoryReference.
	mergedDirectorySourceReference mergedDirectorySourceKind = iota
	// The contents of a directory, provided in literal form.
	mergedDirectorySourceContents
	// A child directory of a directory, identified by its index.
	mergedDirectorySourceChild
	// A directory that is created explicitly.
	mergedDirectorySourceExplicitDirectory
	// A file or symbolic link that is added explicitly.
	mergedDirectorySourceExplicitLeaf
)

// mergedDirectorySource is one of the inputs of a MergedDirectory.
type mergedDirectorySource[TReference object.BasicReference] struct {
	kind mergedDirectorySourceKind

	// For mergedDirectorySourceReference.
	reference model_core.Message[*model_filesystem_pb.DirectoryReference, TReference]

	// For mergedDirectorySourceContents, the contents of the
	// directory. For mergedDirectorySourceChild, the contents of the
	// parent directory, and the index of the child directory.
	contents   model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference]
	childIndex uint64

	// For mergedDirectorySourceExplicitDirectory and
	// mergedDirectorySourceExplicitLeaf.
	name      path.Component
	directory *MergedDirectory[TReference]
	leaf      model_core.Message[*model_filesystem_pb.DirectoryEntry_Node, TReference]
}

// mergedDirectorySourceIdentity is used to deduplicate sources of a
// MergedDirectory. Directories stored in separate objects are
// identified by their reference. Directories provided in literal form
// are identified by the location of their entries in memory, as
// identical messages obtained from the same object share the same
// entries.
type mergedDirectorySourceIdentity struct {
	reference  object.LocalReference
	entries    *model_filesystem_pb.DirectoryEntry
	length     int
	isChild    bool
	childIndex uint64
}

// MergedDirectory is a directory hierarchy that is formed by merging
// existing directories into it, and by explicitly creating
// directories and files. Merging is performed lazily. Instead of
// loading the contents of directories as they are merged, the sources
// of each directory are recorded. Contents are only loaded when
// CreateMergedDirectory() is called, and only for directories that
// have multiple distinct sources.
//
// Sources are deduplicated. Merging a directory that has already been
// merged is cheap.
type MergedDirectory[TReference object.BasicReference] struct {
	sources             []mergedDirectorySource[TReference]
	identities          map[mergedDirectorySourceIdentity]struct{}
	explicitDirectories map[path.Component]*MergedDirectory[TReference]
}

func (d *MergedDirectory[TReference]) addSource(source mergedDirectorySource[TReference], identity *mergedDirectorySourceIdentity) {
	if identity != nil {
		if _, ok := d.identities[*identity]; ok {
			return
		}
		if d.identities == nil {
			d.identities = map[mergedDirectorySourceIdentity]struct{}{}
		}
		d.identities[*identity] = struct{}{}
	}
	d.sources = append(d.sources, source)
}

// MergeDirectoryReference merges the contents of a directory referenced
// by a DirectoryReference into the directory.
func (d *MergedDirectory[TReference]) MergeDirectoryReference(directoryReference model_core.Message[*model_filesystem_pb.DirectoryReference, TReference]) error {
	reference := directoryReference.Message.GetReference()
	if reference == nil {
		// Empty directory.
		return nil
	}
	flattenedReference, err := model_core.FlattenDecodableReference(model_core.Nested(directoryReference, reference))
	if err != nil {
		return err
	}
	d.addSource(mergedDirectorySource[TReference]{
		kind:      mergedDirectorySourceReference,
		reference: directoryReference,
	}, &mergedDirectorySourceIdentity{
		reference: flattenedReference.Value.GetLocalReference(),
	})
	return nil
}

// MergeDirectoryContents merges the contents of a directory, provided
// in literal form, into the directory.
func (d *MergedDirectory[TReference]) MergeDirectoryContents(contents model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference]) {
	if len(contents.Message) == 0 {
		// Empty directory.
		return
	}
	d.addSource(mergedDirectorySource[TReference]{
		kind:     mergedDirectorySourceContents,
		contents: contents,
	}, &mergedDirectorySourceIdentity{
		entries: contents.Message[0],
		length:  len(contents.Message),
	})
}

func (d *MergedDirectory[TReference]) mergeChild(parent model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference], childIndex uint64) {
	d.addSource(mergedDirectorySource[TReference]{
		kind:       mergedDirectorySourceChild,
		contents:   parent,
		childIndex: childIndex,
	}, &mergedDirectorySourceIdentity{
		entries:    parent.Message[0],
		length:     len(parent.Message),
		isChild:    true,
		childIndex: childIndex,
	})
}

// GetOrCreateDirectory returns a subdirectory of the directory that is
// created explicitly. Repeated calls for the same name return the same
// subdirectory. If any of the directories merged into this directory
// also contain an entry having this name, it is subject to the
// conflict resolution policy.
func (d *MergedDirectory[TReference]) GetOrCreateDirectory(name path.Component) *MergedDirectory[TReference] {
	if child, ok := d.explicitDirectories[name]; ok {
		return child
	}
	child := &MergedDirectory[TReference]{}
	if d.explicitDirectories == nil {
		d.explicitDirectories = map[path.Component]*MergedDirectory[TReference]{}
	}
	d.explicitDirectories[name] = child
	d.addSource(mergedDirectorySource[TReference]{
		kind:      mergedDirectorySourceExplicitDirectory,
		name:      name,
		directory: child,
	}, nil)
	return child
}

// AddFile adds a file to the directory. If any of the directories
// merged into this directory also contain an entry having this name,
// it is subject to the conflict resolution policy.
func (d *MergedDirectory[TReference]) AddFile(name path.Component, properties model_core.Message[*model_filesystem_pb.FileProperties, TReference]) {
	d.addSource(mergedDirectorySource[TReference]{
		kind: mergedDirectorySourceExplicitLeaf,
		name: name,
		leaf: model_core.Nested(properties, model_filesystem_pb.DirectoryEntry_Node_builder{
			Name: name.String(),
			File: proto.ValueOrDefault(properties.Message),
		}.Build()),
	}, nil)
}

// AddSymlink adds a symbolic link to the directory. The target is
// stored in normalized form, so that symbolic links can be compared
// for equality. If any of the directories merged into this directory
// also contain an entry having this name, it is subject to the
// conflict resolution policy.
func (d *MergedDirectory[TReference]) AddSymlink(name path.Component, target path.Parser) error {
	targetBuilder, scopeWalker := path.EmptyBuilder.Join(path.VoidScopeWalker)
	if err := path.Resolve(target, scopeWalker); err != nil {
		return util.StatusWrap(err, "Failed to resolve target")
	}
	d.addSource(mergedDirectorySource[TReference]{
		kind: mergedDirectorySourceExplicitLeaf,
		name: name,
		leaf: model_core.NewSimpleMessage[TReference](model_filesystem_pb.DirectoryEntry_Node_builder{
			Name: name.String(),
			Symlink: model_filesystem_pb.SymlinkProperties_builder{
				Target: targetBuilder.GetUNIXString(),
			}.Build(),
		}.Build()),
	}, nil)
	return nil
}

// mergedDirectoryOccurrence is an entry having a given name that is
// contained in one of the sources of a MergedDirectory.
type mergedDirectoryOccurrence[TReference object.BasicReference] struct {
	// For files and symbolic links, the node of the entry.
	node model_core.Message[*model_filesystem_pb.DirectoryEntry_Node, TReference]

	// For directories contained in sources that are stored, the
	// contents of the parent directory and the index of the
	// directory.
	parent     model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference]
	childIndex uint64

	// For directories that are created explicitly.
	explicitDirectory *MergedDirectory[TReference]
}

func (o *mergedDirectoryOccurrence[TReference]) isDirectory() bool {
	return o.explicitDirectory != nil || o.parent.IsSet()
}

type mergedDirectoryCreator[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	context    context.Context
	reader     model_parser.MessageObjectReader[TReference, []*model_filesystem_pb.DirectoryEntry]
	parameters *DirectoryCreationParameters
	capturer   model_core.ObjectCapturer[TReference, TMetadata]
	policy     MergeConflictPolicy
}

// CreateMergedDirectory creates the B-trees of the directories
// contained in a MergedDirectory, and returns the top level of the
// B-tree of its root directory.
//
// Directories that only have a single source are not loaded, and are
// embedded into the resulting directory hierarchy as is. Directories
// having multiple sources are merged by loading the nodes of each
// source, recursing into directories that are present in multiple
// sources.
func CreateMergedDirectory[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	ctx context.Context,
	reader model_parser.MessageObjectReader[TReference, []*model_filesystem_pb.DirectoryEntry],
	parameters *DirectoryCreationParameters,
	capturer model_core.ObjectCapturer[TReference, TMetadata],
	policy MergeConflictPolicy,
	d *MergedDirectory[TReference],
) (CreatedDirectory[TMetadata], error) {
	c := mergedDirectoryCreator[TReference, TMetadata]{
		context:    ctx,
		reader:     reader,
		parameters: parameters,
		capturer:   capturer,
		policy:     policy,
	}
	return c.create(d, nil)
}

// loadSource loads the contents of a source that is stored.
func (c *mergedDirectoryCreator[TReference, TMetadata]) loadSource(source *mergedDirectorySource[TReference]) (model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference], error) {
	switch source.kind {
	case mergedDirectorySourceReference:
		return DirectoryDereference(c.context, c.reader, source.reference)
	case mergedDirectorySourceContents:
		return source.contents, nil
	case mergedDirectorySourceChild:
		return DirectoryGetChild(c.context, c.reader, source.contents, source.childIndex)
	default:
		panic("source is not stored")
	}
}

func (c *mergedDirectoryCreator[TReference, TMetadata]) create(d *MergedDirectory[TReference], dPath *path.Trace) (CreatedDirectory[TMetadata], error) {
	// If the directory only has a single source that is stored,
	// it can be embedded as is.
	if len(d.sources) == 1 {
		switch source := &d.sources[0]; source.kind {
		case mergedDirectorySourceReference:
			contents, err := c.loadSource(source)
			if err != nil {
				return CreatedDirectory[TMetadata]{}, util.StatusWrapf(err, "Failed to load directory %#v", dPath.GetUNIXString())
			}
			return CreatedDirectory[TMetadata]{
				Message:                        model_core.PatchList(c.capturer, contents),
				DirectoriesCount:               source.reference.Message.GetDirectoriesCount(),
				MaximumSymlinkEscapementLevels: source.reference.Message.GetMaximumSymlinkEscapementLevels(),
			}, nil
		case mergedDirectorySourceContents, mergedDirectorySourceChild:
			contents, err := c.loadSource(source)
			if err != nil {
				return CreatedDirectory[TMetadata]{}, util.StatusWrapf(err, "Failed to load directory %#v", dPath.GetUNIXString())
			}
			createdDirectory, err := NewCreatedDirectoryBare(model_core.PatchList(c.capturer, contents))
			if err != nil {
				return CreatedDirectory[TMetadata]{}, util.StatusWrapf(err, "Invalid directory %#v", dPath.GetUNIXString())
			}
			return *createdDirectory, nil
		}
	}

	// Gather the entries of all sources, grouped by name and in
	// the order in which the sources were provided.
	occurrences := map[string][]mergedDirectoryOccurrence[TReference]{}
	for i := range d.sources {
		switch source := &d.sources[i]; source.kind {
		case mergedDirectorySourceReference, mergedDirectorySourceContents, mergedDirectorySourceChild:
			contents, err := c.loadSource(source)
			if err != nil {
				return CreatedDirectory[TMetadata]{}, util.StatusWrapf(err, "Failed to load directory %#v", dPath.GetUNIXString())
			}
			var errIter error
			childIndex := uint64(0)
			for node := range DirectoryAllNodes(c.context, c.reader, contents, &errIter) {
				occurrence := mergedDirectoryOccurrence[TReference]{node: node}
				if node.Message.GetDirectory() != nil {
					occurrence.parent = contents
					occurrence.childIndex = childIndex
					childIndex++
				}
				occurrences[node.Message.GetName()] = append(occurrences[node.Message.GetName()], occurrence)
			}
			if errIter != nil {
				return CreatedDirectory[TMetadata]{}, util.StatusWrapf(errIter, "Failed to load directory %#v", dPath.GetUNIXString())
			}
		case mergedDirectorySourceExplicitDirectory:
			name := source.name.String()
			occurrences[name] = append(occurrences[name], mergedDirectoryOccurrence[TReference]{
				explicitDirectory: source.directory,
			})
		case mergedDirectorySourceExplicitLeaf:
			name := source.name.String()
			occurrences[name] = append(occurrences[name], mergedDirectoryOccurrence[TReference]{
				node: source.leaf,
			})
		}
	}

	names := make([]string, 0, len(occurrences))
	for name := range occurrences {
		names = append(names, name)
	}
	slices.Sort(names)

	nodes := make([]DirectoryNode[TMetadata], 0, len(names))
	createdDirectories := make([]CreatedDirectory[TMetadata], 0, len(names))
	for _, name := range names {
		nameOccurrences := occurrences[name]
		childPath := dPath.Append(path.MustNewComponent(name))
		if nameOccurrences[0].isDirectory() {
			// Merge all directories having this name.
			var child MergedDirectory[TReference]
			for _, occurrence := range nameOccurrences {
				if occurrence.explicitDirectory != nil {
					for _, source := range occurrence.explicitDirectory.sources {
						child.addExistingSource(source)
					}
				} else if occurrence.parent.IsSet() {
					child.mergeChild(occurrence.parent, occurrence.childIndex)
				} else if c.policy == MergeConflictPolicyError {
					return CreatedDirectory[TMetadata]{}, status.Errorf(codes.InvalidArgument, "Directory %#v conflicts with a file or symbolic link", childPath.GetUNIXString())
				}
			}
			createdDirectory, err := c.create(&child, childPath)
			if err != nil {
				return CreatedDirectory[TMetadata]{}, err
			}
			createdDirectories = append(createdDirectories, createdDirectory)
			nodes = append(nodes, DirectoryNode[TMetadata]{
				Name:      name,
				Directory: &createdDirectories[len(createdDirectories)-1],
			})
		} else {
			// Use the first file or symbolic link having
			// this name. If conflicts are not permitted,
			// all other entries must be identical to it.
			first := nameOccurrences[0].node
			if c.policy == MergeConflictPolicyError {
				for _, occurrence := range nameOccurrences[1:] {
					if occurrence.isDirectory() {
						return CreatedDirectory[TMetadata]{}, status.Errorf(codes.InvalidArgument, "Directory %#v conflicts with a file or symbolic link", childPath.GetUNIXString())
					}
					if !model_core.MessagesEqual(first, occurrence.node) {
						return CreatedDirectory[TMetadata]{}, status.Errorf(codes.InvalidArgument, "Conflicting files or symbolic links at %#v", childPath.GetUNIXString())
					}
				}
			}
			nodes = append(nodes, DirectoryNode[TMetadata]{
				Name: name,
				Leaf: model_core.Patch(c.capturer, first),
			})
		}
	}

	createdDirectory, err := CreateDirectory(c.context, c.parameters, c.capturer, nodes)
	if err != nil {
		return CreatedDirectory[TMetadata]{}, util.StatusWrapf(err, "Failed to create directory %#v", dPath.GetUNIXString())
	}
	return createdDirectory, nil
}

// addExistingSource adds a source that was previously added to another
// MergedDirectory, recomputing its identity.
func (d *MergedDirectory[TReference]) addExistingSource(source mergedDirectorySource[TReference]) {
	switch source.kind {
	case mergedDirectorySourceReference:
		// Errors were already reported when the source was
		// added originally.
		_ = d.MergeDirectoryReference(source.reference)
	case mergedDirectorySourceContents:
		d.MergeDirectoryContents(source.contents)
	case mergedDirectorySourceChild:
		d.mergeChild(source.contents, source.childIndex)
	case mergedDirectorySourceExplicitDirectory:
		// Explicit directories having the same name are merged
		// when the directory is created.
		d.addSource(source, nil)
	case mergedDirectorySourceExplicitLeaf:
		d.addSource(source, nil)
	}
}
