package filesystem

import (
	"context"

	model_core "bonanza.build/pkg/model/core"
	model_parser "bonanza.build/pkg/model/parser"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/filesystem/path"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// DirectoryComponentWalker can be used to resolve the properties of a
// file contained in a directory that has been written to storage.
type DirectoryComponentWalker[TReference object.BasicReference] struct {
	// Constant fields.
	context       context.Context
	entriesReader model_parser.MessageObjectReader[TReference, []*model_filesystem_pb.DirectoryEntry]
	onUpHandler   func() (path.ComponentWalker, error)

	// Variable fields.
	rootDirectoryReference model_core.Message[*model_filesystem_pb.DirectoryReference, TReference]
	directoriesStack       []model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference]
	fileProperties         model_core.Message[*model_filesystem_pb.FileProperties, TReference]
}

var _ path.ComponentWalker = (*DirectoryComponentWalker[object.BasicReference])(nil)

// NewDirectoryComponentWalker creates a new DirectoryComponentWalker
// that starts resolution within the provided directories.
//
// If rootDirectoryReference is set, resolution starts in the directory
// that it references, which is only loaded from storage when needed.
// Otherwise, resolution starts in the last directory contained in
// directoriesStack.
func NewDirectoryComponentWalker[TReference object.BasicReference](
	ctx context.Context,
	entriesReader model_parser.MessageObjectReader[TReference, []*model_filesystem_pb.DirectoryEntry],
	onUpHandler func() (path.ComponentWalker, error),
	rootDirectoryReference model_core.Message[*model_filesystem_pb.DirectoryReference, TReference],
	directoriesStack []model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference],
) *DirectoryComponentWalker[TReference] {
	return &DirectoryComponentWalker[TReference]{
		context:       ctx,
		entriesReader: entriesReader,
		onUpHandler:   onUpHandler,

		rootDirectoryReference: rootDirectoryReference,
		directoriesStack:       directoriesStack,
	}
}

func (cw *DirectoryComponentWalker[TReference]) dereferenceRootDirectory() error {
	if cw.rootDirectoryReference.IsSet() {
		d, err := DirectoryDereference(cw.context, cw.entriesReader, cw.rootDirectoryReference)
		if err != nil {
			return err
		}
		cw.directoriesStack = append(cw.directoriesStack, d)
		cw.rootDirectoryReference.Clear()
	}
	return nil
}

// lookupNode looks up a file, symbolic link or subdirectory with a
// given name in the current directory. If the node is a subdirectory,
// its contents are pushed onto the directories stack.
func (cw *DirectoryComponentWalker[TReference]) lookupNode(name path.Component) (model_core.Message[*model_filesystem_pb.DirectoryEntry_Node, TReference], error) {
	if err := cw.dereferenceRootDirectory(); err != nil {
		return model_core.Message[*model_filesystem_pb.DirectoryEntry_Node, TReference]{}, err
	}

	d := cw.directoriesStack[len(cw.directoriesStack)-1]
	node, directoryIndex, err := DirectoryLookupNode(cw.context, cw.entriesReader, d, name)
	if err != nil {
		return model_core.Message[*model_filesystem_pb.DirectoryEntry_Node, TReference]{}, err
	}
	if !node.IsSet() {
		return model_core.Message[*model_filesystem_pb.DirectoryEntry_Node, TReference]{}, status.Error(codes.NotFound, "Path does not exist")
	}
	if node.Message.GetDirectory() != nil {
		child, err := DirectoryGetChild(cw.context, cw.entriesReader, d, directoryIndex)
		if err != nil {
			return model_core.Message[*model_filesystem_pb.DirectoryEntry_Node, TReference]{}, err
		}
		cw.directoriesStack = append(cw.directoriesStack, child)
	}
	return node, nil
}

// OnDirectory is called when the path to be resolved has a leading
// directory pathname components.
func (cw *DirectoryComponentWalker[TReference]) OnDirectory(name path.Component) (path.GotDirectoryOrSymlink, error) {
	node, err := cw.lookupNode(name)
	if err != nil {
		return nil, err
	}
	switch node.Message.WhichKind() {
	case model_filesystem_pb.DirectoryEntry_Node_Directory_case:
		return path.GotDirectory{
			Child:        cw,
			IsReversible: true,
		}, nil
	case model_filesystem_pb.DirectoryEntry_Node_File_case:
		return nil, status.Error(codes.InvalidArgument, "Path resolves to a regular file, while a directory was expected")
	case model_filesystem_pb.DirectoryEntry_Node_Symlink_case:
		return path.GotSymlink{
			Parent: path.NewRelativeScopeWalker(cw),
			Target: path.UNIXFormat.NewParser(node.Message.GetSymlink().GetTarget()),
		}, nil
	default:
		return nil, status.Error(codes.InvalidArgument, "Path resolves to a node of an unknown type")
	}
}

// OnTerminal is called when the path to be resolved has a trailing
// pathname component.
func (cw *DirectoryComponentWalker[TReference]) OnTerminal(name path.Component) (*path.GotSymlink, error) {
	node, err := cw.lookupNode(name)
	if err != nil {
		return nil, err
	}
	switch node.Message.WhichKind() {
	case model_filesystem_pb.DirectoryEntry_Node_Directory_case:
		return nil, nil
	case model_filesystem_pb.DirectoryEntry_Node_File_case:
		if node.Message.GetFile() == nil {
			return nil, status.Error(codes.InvalidArgument, "Path resolves to file that does not have any properties")
		}
		cw.fileProperties = model_core.Nested(node, node.Message.GetFile())
		return nil, nil
	case model_filesystem_pb.DirectoryEntry_Node_Symlink_case:
		return &path.GotSymlink{
			Parent: path.NewRelativeScopeWalker(cw),
			Target: path.UNIXFormat.NewParser(node.Message.GetSymlink().GetTarget()),
		}, nil
	default:
		return nil, status.Error(codes.InvalidArgument, "Path resolves to a node of an unknown type")
	}
}

// OnUp is called when the path to be resolved contains a ".."
// component.
func (cw *DirectoryComponentWalker[TReference]) OnUp() (path.ComponentWalker, error) {
	if err := cw.dereferenceRootDirectory(); err != nil {
		return nil, err
	}
	if len(cw.directoriesStack) == 1 {
		return cw.onUpHandler()
	}
	cw.directoriesStack = cw.directoriesStack[:len(cw.directoriesStack)-1]
	return cw, nil
}

// GetCurrentDirectoriesStack returns the stack of directories that the
// component walker currently uses to perform resolution. This list can,
// for example, be copied if the directory walker needs to be cloned.
func (cw *DirectoryComponentWalker[TReference]) GetCurrentDirectoriesStack() ([]model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference], error) {
	if err := cw.dereferenceRootDirectory(); err != nil {
		return nil, err
	}
	return cw.directoriesStack, nil
}

// GetCurrentDirectory returns the lowest directory that the component
// walker currently uses to perform resolution.
func (cw *DirectoryComponentWalker[TReference]) GetCurrentDirectory() (model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference], error) {
	if err := cw.dereferenceRootDirectory(); err != nil {
		return model_core.Message[[]*model_filesystem_pb.DirectoryEntry, TReference]{}, err
	}
	return cw.directoriesStack[len(cw.directoriesStack)-1], nil
}

// GetCurrentFileProperties returns the properties of the file
// corresponding to the path that was performed. If the path resolved to
// a directory, the file properties will not be set.
func (cw *DirectoryComponentWalker[TReference]) GetCurrentFileProperties() model_core.Message[*model_filesystem_pb.FileProperties, TReference] {
	return cw.fileProperties
}
