package filesystem

import (
	"context"
	"math"
	"sort"
	"sync/atomic"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"

	"github.com/buildbarn/bb-storage/pkg/filesystem"
	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
	"github.com/buildbarn/bb-storage/pkg/util"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// CapturableDirectory is an interface for a directory that can be
// traversed by CreateFileMerkleTree().
type CapturableDirectory[TDirectory, TFile model_core.ReferenceMetadata] interface {
	// Identical to filesystem.Directory.
	Close() error
	ReadDir() ([]filesystem.FileInfo, error)
	Readlink(name path.Component) (path.Parser, error)

	// Enter a directory, so that it may be traversed. The
	// implementation has the possibility to return an existing
	// directory instead. This can be of use when computing a Merkle
	// tree that is based on an existing directory structure that is
	// altered slightly.
	EnterCapturableDirectory(name path.Component) (*CreatedDirectory[TDirectory], CapturableDirectory[TDirectory, TFile], error)

	// Open a file, so that a Merkle tree of its contents can be
	// computed. The actual Merkle tree computation is performed by
	// calling CapturableFile.CreateFileMerkleTree(). That way file
	// Merkle tree computation can happen in parallel.
	OpenForFileMerkleTreeCreation(name path.Component) (CapturableFile[TFile], error)
}

// CapturableFile is called into by CreateFileMerkleTree() to obtain a
// Merkle tree for a given file. Either one of these methods will be
// called exactly once.
type CapturableFile[TFile model_core.ReferenceMetadata] interface {
	CreateFileMerkleTree(ctx context.Context) (model_core.PatchedMessage[*model_filesystem_pb.FileContents, TFile], error)
	Discard()
}

type simpleCapturableFile[TFile model_core.ReferenceMetadata] struct {
	fileContents model_core.PatchedMessage[*model_filesystem_pb.FileContents, TFile]
}

// NewSimpleCapturableFile creates a CapturableFile that returns a
// FileContents message that was computed previously. This can be used
// to place already existing files in directory Merkle trees.
func NewSimpleCapturableFile[TFile model_core.ReferenceMetadata](fileContents model_core.PatchedMessage[*model_filesystem_pb.FileContents, TFile]) CapturableFile[TFile] {
	return &simpleCapturableFile[TFile]{
		fileContents: fileContents,
	}
}

func (cf *simpleCapturableFile[TFile]) CreateFileMerkleTree(ctx context.Context) (model_core.PatchedMessage[*model_filesystem_pb.FileContents, TFile], error) {
	return cf.fileContents, nil
}

func (cf *simpleCapturableFile[TFile]) Discard() {
	cf.fileContents.Discard()
}

type unfinalizedDirectory[TDirectory, TFile model_core.ReferenceMetadata] struct {
	// Nodes of all children, in alphabetical order. The contents
	// of files are filled in asynchronously.
	nodes       []unfinalizedNode[TFile]
	directories []CreatedDirectory[TDirectory]

	unfinalizedCount atomic.Uint32
	parent           *unfinalizedDirectory[TDirectory, TFile]
	out              *CreatedDirectory[TDirectory]
}

type unfinalizedNode[TFile model_core.ReferenceMetadata] struct {
	node         *model_filesystem_pb.DirectoryEntry_Node
	fileContents model_core.PatchedMessage[*model_filesystem_pb.FileContents, TFile]
	// For directories, the index in unfinalizedDirectory.directories.
	directoryIndex int
}

type directoryMerkleTreeBuilder[TDirectory, TFile model_core.ReferenceMetadata] struct {
	context     context.Context
	concurrency *semaphore.Weighted
	group       *errgroup.Group
	parameters  *DirectoryCreationParameters
	capturer    DirectoryMerkleTreeCapturer[TDirectory, TFile]
}

func (b *directoryMerkleTreeBuilder[TDirectory, TFile]) walkDirectory(
	directory CapturableDirectory[TDirectory, TFile],
	directoryPath *path.Trace,
	parent *unfinalizedDirectory[TDirectory, TFile],
	out *CreatedDirectory[TDirectory],
) error {
	if b.context.Err() != nil {
		return util.StatusFromContext(b.context)
	}

	// Obtain the list of children and count them. That way we can
	// simplify the reference counting scheme. It also allows us to
	// create lists that don't need further resizing, meaning any
	// references to elements remain stable.
	entries, err := directory.ReadDir()
	if err != nil {
		return util.StatusWrapf(err, "Failed to read contents of directory %#v", directoryPath.GetUNIXString())
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name().String() < entries[j].Name().String()
	})
	directoriesCount, filesCount, nodesCount := 0, 0, 0
	for _, entry := range entries {
		switch entry.Type() {
		case filesystem.FileTypeDirectory:
			directoriesCount++
			nodesCount++
		case filesystem.FileTypeRegularFile:
			filesCount++
			nodesCount++
		case filesystem.FileTypeSymlink:
			nodesCount++
		}
	}
	if nodesCount >= math.MaxUint32 {
		return status.Errorf(codes.InvalidArgument, "Directory %#v contains too many entries", directoryPath.GetUNIXString())
	}

	ud := unfinalizedDirectory[TDirectory, TFile]{
		nodes:       make([]unfinalizedNode[TFile], 0, nodesCount),
		directories: make([]CreatedDirectory[TDirectory], directoriesCount),
		parent:      parent,
		out:         out,
	}
	ud.unfinalizedCount.Store(uint32(directoriesCount + filesCount + 1))

	finalized := uint32(1)
	nextDirectoryIndex := 0
	for _, entry := range entries {
		name := entry.Name()
		switch entry.Type() {
		case filesystem.FileTypeDirectory:
			childPath := directoryPath.Append(name)
			childExisting, childDirectory, err := directory.EnterCapturableDirectory(name)
			if err != nil {
				return util.StatusWrapf(err, "Failed to open directory %#v", childPath.GetUNIXString())
			}
			directoryIndex := nextDirectoryIndex
			nextDirectoryIndex++
			ud.nodes = append(ud.nodes, unfinalizedNode[TFile]{
				node: &model_filesystem_pb.DirectoryEntry_Node{
					Name: name.String(),
				},
				directoryIndex: directoryIndex,
			})
			if childOut := &ud.directories[directoryIndex]; childExisting == nil {
				err = b.walkDirectory(
					childDirectory,
					childPath,
					&ud,
					childOut,
				)
				childDirectory.Close()
				if err != nil {
					return err
				}
			} else {
				// There is no need to recursively
				// traverse this directory, as the
				// directory's contents were computed
				// previously.
				*childOut = *childExisting
				finalized++
			}
		case filesystem.FileTypeRegularFile:
			f, err := directory.OpenForFileMerkleTreeCreation(name)
			if err != nil {
				return util.StatusWrapf(err, "Failed to open file %#v", directoryPath.Append(name).GetUNIXString())
			}
			ud.nodes = append(ud.nodes, unfinalizedNode[TFile]{
				node: &model_filesystem_pb.DirectoryEntry_Node{
					Name: name.String(),
					Kind: &model_filesystem_pb.DirectoryEntry_Node_File{
						File: &model_filesystem_pb.FileProperties{
							IsExecutable: entry.IsExecutable(),
						},
					},
				},
				directoryIndex: -1,
			})
			fileNode := &ud.nodes[len(ud.nodes)-1]

			if err := util.AcquireSemaphore(b.context, b.concurrency, 1); err != nil {
				f.Discard()
				return err
			}
			b.group.Go(func() error {
				defer b.concurrency.Release(1)

				fileContents, err := f.CreateFileMerkleTree(b.context)
				if err != nil {
					return util.StatusWrapf(err, "Failed to create Merkle tree for file %#v", directoryPath.Append(name).GetUNIXString())
				}
				fileNode.fileContents = fileContents
				return b.maybeFinalizeDirectory(&ud, 1)
			})
		case filesystem.FileTypeSymlink:
			targetParser, err := directory.Readlink(name)
			if err != nil {
				return util.StatusWrapf(err, "Failed to read target of symbolic link %#v", directoryPath.Append(name).GetUNIXString())
			}

			// Convert the symlink target to a string.
			targetBuilder, scopeWalker := path.EmptyBuilder.Join(path.VoidScopeWalker)
			if err := path.Resolve(targetParser, scopeWalker); err != nil {
				return util.StatusWrapf(err, "Failed to resolve target of symbolic link %#v", directoryPath.Append(name).GetUNIXString())
			}
			ud.nodes = append(ud.nodes, unfinalizedNode[TFile]{
				node: &model_filesystem_pb.DirectoryEntry_Node{
					Name: name.String(),
					Kind: &model_filesystem_pb.DirectoryEntry_Node_Symlink{
						Symlink: &model_filesystem_pb.SymlinkProperties{
							Target: targetBuilder.GetUNIXString(),
						},
					},
				},
				directoryIndex: -1,
			})
		}
	}

	return b.maybeFinalizeDirectory(&ud, finalized)
}

func (b *directoryMerkleTreeBuilder[TDirectory, TFile]) maybeFinalizeDirectory(ud *unfinalizedDirectory[TDirectory, TFile], count uint32) error {
	for ; ud.unfinalizedCount.Add(-count) == 0; ud = ud.parent {
		count = 1
		createdDirectory, err := b.finalizeDirectory(ud)
		if err != nil {
			return err
		}
		*ud.out = createdDirectory
	}
	return nil
}

func (b *directoryMerkleTreeBuilder[TDirectory, TFile]) finalizeDirectory(ud *unfinalizedDirectory[TDirectory, TFile]) (CreatedDirectory[TDirectory], error) {
	parentNodeComputer := NewDirectoryEntryParentNodeComputer(
		b.context,
		model_core.CreatedObjectCapturerFunc[TDirectory](b.capturer.CaptureDirectory),
	)
	treeBuilder := newDirectoryEntriesBTreeBuilder(b.parameters, parentNodeComputer)
	defer treeBuilder.Discard()

	// First section: the contents of all subdirectories, in the
	// same order as the subdirectory nodes.
	var escapementLevels maximumSymlinkEscapementLevels
	for _, unfinalizedNode := range ud.nodes {
		if unfinalizedNode.directoryIndex < 0 {
			if symlink, ok := unfinalizedNode.node.Kind.(*model_filesystem_pb.DirectoryEntry_Node_Symlink); ok {
				escapementCounter := NewEscapementCountingScopeWalker()
				if err := path.Resolve(path.UNIXFormat.NewParser(symlink.Symlink.Target), escapementCounter); err != nil {
					return CreatedDirectory[TDirectory]{}, util.StatusWrapf(err, "Failed to resolve target of symbolic link %#v", unfinalizedNode.node.Name)
				}
				escapementLevels.raise(escapementCounter.GetLevels())
			}
			continue
		}

		createdDirectory := &ud.directories[unfinalizedNode.directoryIndex]
		var child maximumSymlinkEscapementLevels
		child.raise(createdDirectory.MaximumSymlinkEscapementLevels)
		escapementLevels.raiseChild(&child)

		// The top level of the subdirectory's B-tree is bounded
		// in size, meaning it can be embedded as is.
		directoryContents := createdDirectory.Message
		if err := treeBuilder.PushChild(
			model_core.NewPatchedMessage(
				&model_filesystem_pb.DirectoryEntry{
					Level: &model_filesystem_pb.DirectoryEntry_DirectoryContents_{
						DirectoryContents: &model_filesystem_pb.DirectoryEntry_DirectoryContents{
							Entries: directoryContents.Message,
						},
					},
				},
				directoryContents.Patcher,
			),
		); err != nil {
			return CreatedDirectory[TDirectory]{}, err
		}
	}

	// Second section: nodes for all files, symbolic links and
	// subdirectories, in alphabetical order.
	for _, unfinalizedNode := range ud.nodes {
		node := unfinalizedNode.node
		patcher := model_core.NewReferenceMessagePatcher[TDirectory]()
		if unfinalizedNode.directoryIndex >= 0 {
			node.Kind = &model_filesystem_pb.DirectoryEntry_Node_Directory{
				Directory: &model_filesystem_pb.DirectoryProperties{
					DirectoriesCount: ud.directories[unfinalizedNode.directoryIndex].DirectoriesCount,
				},
			}
		} else if file, ok := node.Kind.(*model_filesystem_pb.DirectoryEntry_Node_File); ok && unfinalizedNode.fileContents.IsSet() {
			file.File.Contents = unfinalizedNode.fileContents.Message
			patcher = model_core.MapReferenceMessagePatcherMetadata(
				unfinalizedNode.fileContents.Patcher,
				func(capturedObject model_core.MetadataEntry[TFile]) TDirectory {
					return b.capturer.CaptureFileNode(capturedObject.Metadata)
				},
			)
		}
		if err := treeBuilder.PushChild(
			model_core.NewPatchedMessage(
				&model_filesystem_pb.DirectoryEntry{
					Level: &model_filesystem_pb.DirectoryEntry_Node_{Node: node},
				},
				patcher,
			),
		); err != nil {
			return CreatedDirectory[TDirectory]{}, err
		}
	}

	// FinalizeList() ensures that the top level of the B-tree is
	// bounded in size, so that it can be embedded into the B-tree
	// of the parent directory. As this is also done for root
	// directories, the top level of a directory's B-tree is
	// identical, regardless of whether it's a root directory or a
	// subdirectory.
	entries, err := treeBuilder.FinalizeList()
	if err != nil {
		return CreatedDirectory[TDirectory]{}, util.StatusWrap(err, "Failed to finalize directory B-tree")
	}
	return CreatedDirectory[TDirectory]{
		Message:                        entries,
		DirectoriesCount:               uint32(len(ud.directories)),
		MaximumSymlinkEscapementLevels: escapementLevels.toProto(),
	}, nil
}

// NewDirectoryEntryParentNodeComputer creates a ParentNodeComputer for
// the B-trees of directories. Parent nodes contain the number of nodes
// and subdirectories contained in the part of the B-tree, the name of
// its first node, and the maximum symlink escapement levels.
func NewDirectoryEntryParentNodeComputer[TMetadata model_core.ReferenceMetadata](
	ctx context.Context,
	capturer model_core.CreatedObjectCapturer[TMetadata],
) btree.ParentNodeComputer[*model_filesystem_pb.DirectoryEntry, TMetadata] {
	return func(
		createdObject model_core.Decodable[model_core.CreatedObject[TMetadata]],
		childNodes []*model_filesystem_pb.DirectoryEntry,
	) (model_core.PatchedMessage[*model_filesystem_pb.DirectoryEntry, TMetadata], error) {
		counts, err := getDirectoryEntriesCounts(childNodes)
		if err != nil {
			return model_core.PatchedMessage[*model_filesystem_pb.DirectoryEntry, TMetadata]{}, err
		}
		if counts.nodes > math.MaxUint32 || counts.directoryContents > math.MaxUint32 {
			return model_core.PatchedMessage[*model_filesystem_pb.DirectoryEntry, TMetadata]{}, status.Error(codes.InvalidArgument, "Directory B-tree contains too many entries")
		}
		var firstName string
		if len(childNodes) > 0 {
			switch level := childNodes[0].Level.(type) {
			case *model_filesystem_pb.DirectoryEntry_Parent_:
				firstName = level.Parent.FirstName
			case *model_filesystem_pb.DirectoryEntry_Node_:
				firstName = level.Node.Name
			}
		}
		maximumSymlinkEscapementLevels, err := getDirectoryEntriesMaximumSymlinkEscapementLevels(childNodes)
		if err != nil {
			return model_core.PatchedMessage[*model_filesystem_pb.DirectoryEntry, TMetadata]{}, err
		}

		metadataEntry, err := createdObject.Value.Capture(ctx, capturer)
		if err != nil {
			return model_core.PatchedMessage[*model_filesystem_pb.DirectoryEntry, TMetadata]{}, err
		}
		capturedObject := model_core.CopyDecodable(createdObject, metadataEntry)
		return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_filesystem_pb.DirectoryEntry {
			return &model_filesystem_pb.DirectoryEntry{
				Level: &model_filesystem_pb.DirectoryEntry_Parent_{
					Parent: &model_filesystem_pb.DirectoryEntry_Parent{
						Reference:                      patcher.AddDecodableReference(capturedObject),
						FirstName:                      firstName,
						LeafNodesCount:                 uint32(counts.nodes - counts.directoryNodes),
						DirectoryNodesCount:            uint32(counts.directoryNodes),
						DirectoryContentsCount:         uint32(counts.directoryContents),
						MaximumSymlinkEscapementLevels: maximumSymlinkEscapementLevels,
					},
				},
			}
		}), nil
	}
}

// newDirectoryEntriesBTreeBuilder creates a B-tree builder that is
// capable of constructing the B-trees of directories.
func newDirectoryEntriesBTreeBuilder[TMetadata model_core.ReferenceMetadata](
	parameters *DirectoryCreationParameters,
	parentNodeComputer btree.ParentNodeComputer[*model_filesystem_pb.DirectoryEntry, TMetadata],
) btree.Builder[*model_filesystem_pb.DirectoryEntry, TMetadata] {
	return btree.NewHeightAwareBuilder(
		btree.NewProllyChunkerFactory[TMetadata](
			parameters.directoriesParameters,
			/* isParent = */ func(entry *model_filesystem_pb.DirectoryEntry) bool {
				return entry.GetParent() != nil
			},
		),
		btree.NewObjectCreatingNodeMerger(
			parameters.encoder,
			parameters.referenceFormat,
			parentNodeComputer,
		),
	)
}

// CreatedDirectory contains the message of the root directory of the
// directory hierarchy for which a Merkle tree was created. It also
// contains all of the metadata that needs to be available when creating
// a DirectoryReference message for referring to the root directory.
type CreatedDirectory[TDirectory model_core.ReferenceMetadata] struct {
	// The top level of the directory's B-tree.
	Message model_core.PatchedMessage[[]*model_filesystem_pb.DirectoryEntry, TDirectory]
	// The number of subdirectories contained in the directory.
	DirectoriesCount               uint32
	MaximumSymlinkEscapementLevels *wrapperspb.UInt32Value
}

// NewCreatedDirectoryBare creates a CreatedDirectory, recomputing
// DirectoriesCount and MaximumSymlinkEscapementLevels from the top
// level of the directory's B-tree.
//
// This function can be used to embed an existing directory in a Merkle
// tree for which no DirectoryReference exists (e.g., because it was not
// the root directory of a directory cluster). In cases where a
// DirectoryReference is available, this function should not be used.
func NewCreatedDirectoryBare[TDirectory model_core.ReferenceMetadata](message model_core.PatchedMessage[[]*model_filesystem_pb.DirectoryEntry, TDirectory]) (*CreatedDirectory[TDirectory], error) {
	directoriesCount, err := GetDirectoryDirectoriesCount(message.Message)
	if err != nil {
		return nil, err
	}
	maximumSymlinkEscapementLevels, err := GetDirectoryMaximumSymlinkEscapementLevels(message.Message)
	if err != nil {
		return nil, err
	}
	return &CreatedDirectory[TDirectory]{
		Message:                        message,
		DirectoriesCount:               directoriesCount,
		MaximumSymlinkEscapementLevels: maximumSymlinkEscapementLevels,
	}, nil
}

// IsEmpty returns true if the directory does not contain any entries.
// Such directories are referenced by a DirectoryReference whose
// reference is not set.
func (cd *CreatedDirectory[TDirectory]) IsEmpty() bool {
	return len(cd.Message.Message) == 0
}

// ToDirectoryReference creates a DirectoryReference message that
// corresponds to the current directory. The reference should be nil if
// the directory is empty.
func (cd *CreatedDirectory[TDirectory]) ToDirectoryReference(reference *model_core_pb.DecodableReference) *model_filesystem_pb.DirectoryReference {
	return &model_filesystem_pb.DirectoryReference{
		Reference:                      reference,
		DirectoriesCount:               cd.DirectoriesCount,
		MaximumSymlinkEscapementLevels: cd.MaximumSymlinkEscapementLevels,
	}
}

// CaptureDirectoryReference stores the top level of the directory's
// B-tree in a separate object, and returns a DirectoryReference that
// refers to it. If the directory is empty, no object is created.
func (cd *CreatedDirectory[TDirectory]) CaptureDirectoryReference(
	ctx context.Context,
	parameters *DirectoryCreationParameters,
	capturer model_core.CreatedObjectCapturer[TDirectory],
) (model_core.PatchedMessage[*model_filesystem_pb.DirectoryReference, TDirectory], error) {
	if cd.IsEmpty() {
		return model_core.NewSimplePatchedMessage[TDirectory](cd.ToDirectoryReference(nil)), nil
	}
	createdObject, err := model_core.MarshalAndEncodeDeterministic(
		model_core.ProtoListToBinaryMarshaler(cd.Message),
		parameters.referenceFormat,
		parameters.GetEncoder(),
	)
	if err != nil {
		return model_core.PatchedMessage[*model_filesystem_pb.DirectoryReference, TDirectory]{}, err
	}
	return model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TDirectory]) (*model_filesystem_pb.DirectoryReference, error) {
		reference, err := patcher.CaptureAndAddDecodableReference(ctx, createdObject, capturer)
		if err != nil {
			return nil, err
		}
		return cd.ToDirectoryReference(reference), nil
	})
}

// CreateDirectoryMerkleTree creates a Merkle tree that corresponds to
// the contents of a given directory. Upon success, the top level of the
// B-tree of the root directory is returned.
//
// Computation of the Merkle trees of the individual files can be done
// in parallel. These processes may terminate asynchronously, meaning
// that group.Wait() needs to be called to ensure that the capturer is
// invoked for all objects, and the output message is set.
func CreateDirectoryMerkleTree[TDirectory, TFile model_core.ReferenceMetadata](
	ctx context.Context,
	concurrency *semaphore.Weighted,
	group *errgroup.Group,
	directoryParameters *DirectoryCreationParameters,
	directory CapturableDirectory[TDirectory, TFile],
	capturer DirectoryMerkleTreeCapturer[TDirectory, TFile],
	out *CreatedDirectory[TDirectory],
) error {
	b := directoryMerkleTreeBuilder[TDirectory, TFile]{
		context:     ctx,
		concurrency: concurrency,
		group:       group,
		parameters:  directoryParameters,
		capturer:    capturer,
	}

	var parent unfinalizedDirectory[TDirectory, TFile]
	parent.unfinalizedCount.Store(2)
	return b.walkDirectory(directory, nil, &parent, out)
}
