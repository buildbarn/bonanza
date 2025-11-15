package filesystem

import (
	"context"
	"sort"
	"strings"

	model_core "bonanza.build/pkg/model/core"
	model_parser "bonanza.build/pkg/model/parser"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/filesystem/path"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type DirectoryComponentWalker[TReference object.BasicReference] struct {
	// Constant fields.
	context                 context.Context
	directoryContentsReader model_parser.MessageObjectReader[TReference, *model_filesystem_pb.DirectoryContents]
	leavesReader            model_parser.MessageObjectReader[TReference, *model_filesystem_pb.Leaves]
	onUpHandler             func() (path.ComponentWalker, error)

	// Variable fields.
	currentDirectory model_core.Message[*model_filesystem_pb.Directory, TReference]
	directoriesStack []model_core.Message[*model_filesystem_pb.DirectoryContents, TReference]
	fileProperties   model_core.Message[*model_filesystem_pb.FileProperties, TReference]
}

var _ path.ComponentWalker = (*DirectoryComponentWalker[object.BasicReference])(nil)

func NewDirectoryComponentWalker[TReference object.BasicReference](
	ctx context.Context,
	directoryContentsReader model_parser.MessageObjectReader[TReference, *model_filesystem_pb.DirectoryContents],
	leavesReader model_parser.MessageObjectReader[TReference, *model_filesystem_pb.Leaves],
	onUpHandler func() (path.ComponentWalker, error),
	currentDirectory model_core.Message[*model_filesystem_pb.Directory, TReference],
	directoriesStack []model_core.Message[*model_filesystem_pb.DirectoryContents, TReference],
) *DirectoryComponentWalker[TReference] {
	return &DirectoryComponentWalker[TReference]{
		context:                 ctx,
		directoryContentsReader: directoryContentsReader,
		leavesReader:            leavesReader,
		onUpHandler:             onUpHandler,

		currentDirectory: currentDirectory,
		directoriesStack: directoriesStack,
	}
}

func (cw *DirectoryComponentWalker[TReference]) dereferenceCurrentDirectory() error {
	if cw.currentDirectory.IsSet() {
		d, err := DirectoryGetContents(cw.context, cw.directoryContentsReader, cw.currentDirectory)
		if err != nil {
			return err
		}
		cw.directoriesStack = append(cw.directoriesStack, d)
		cw.currentDirectory.Clear()
	}
	return nil
}

func (cw *DirectoryComponentWalker[TReference]) OnDirectory(name path.Component) (path.GotDirectoryOrSymlink, error) {
	if err := cw.dereferenceCurrentDirectory(); err != nil {
		return nil, err
	}

	d := cw.directoriesStack[len(cw.directoriesStack)-1]
	n := name.String()
	directories := d.Message.Directories
	if i, ok := sort.Find(
		len(directories),
		func(i int) int { return strings.Compare(n, directories[i].Name) },
	); ok {
		cw.currentDirectory = model_core.Nested(d, directories[i].Directory)
		return path.GotDirectory{
			Child:        cw,
			IsReversible: true,
		}, nil
	}

	leaves, err := DirectoryGetLeaves(cw.context, cw.leavesReader, d)
	if err != nil {
		return nil, err
	}

	files := leaves.Message.Files
	if _, ok := sort.Find(
		len(files),
		func(i int) int { return strings.Compare(n, files[i].Name) },
	); ok {
		return nil, status.Error(codes.InvalidArgument, "Path resolves to a regular file, while a directory was expected")
	}

	symlinks := leaves.Message.Symlinks
	if i, ok := sort.Find(
		len(symlinks),
		func(i int) int { return strings.Compare(n, symlinks[i].Name) },
	); ok {
		return path.GotSymlink{
			Parent: path.NewRelativeScopeWalker(cw),
			Target: path.UNIXFormat.NewParser(symlinks[i].Target),
		}, nil
	}

	return nil, status.Error(codes.NotFound, "Path does not exist")
}

func (cw *DirectoryComponentWalker[TReference]) OnTerminal(name path.Component) (*path.GotSymlink, error) {
	if err := cw.dereferenceCurrentDirectory(); err != nil {
		return nil, err
	}

	d := cw.directoriesStack[len(cw.directoriesStack)-1]
	n := name.String()
	directories := d.Message.Directories
	if i, ok := sort.Find(
		len(directories),
		func(i int) int { return strings.Compare(n, directories[i].Name) },
	); ok {
		cw.currentDirectory = model_core.Nested(d, directories[i].Directory)
		return nil, nil
	}

	leaves, err := DirectoryGetLeaves(cw.context, cw.leavesReader, d)
	if err != nil {
		return nil, err
	}

	files := leaves.Message.Files
	if i, ok := sort.Find(
		len(files),
		func(i int) int { return strings.Compare(n, files[i].Name) },
	); ok {
		properties := files[i].Properties
		if properties == nil {
			return nil, status.Error(codes.InvalidArgument, "Path resolves to file that does not have any properties")
		}
		cw.fileProperties = model_core.Nested(leaves, properties)
		return nil, nil
	}

	symlinks := leaves.Message.Symlinks
	if i, ok := sort.Find(
		len(symlinks),
		func(i int) int { return strings.Compare(n, symlinks[i].Name) },
	); ok {
		return &path.GotSymlink{
			Parent: path.NewRelativeScopeWalker(cw),
			Target: path.UNIXFormat.NewParser(symlinks[i].Target),
		}, nil
	}

	return nil, status.Error(codes.NotFound, "Path does not exist")
}

func (cw *DirectoryComponentWalker[TReference]) OnUp() (path.ComponentWalker, error) {
	if cw.currentDirectory.IsSet() {
		cw.currentDirectory.Clear()
	} else if len(cw.directoriesStack) == 1 {
		return cw.onUpHandler()
	} else {
		cw.directoriesStack = cw.directoriesStack[:len(cw.directoriesStack)-1]
	}
	return cw, nil
}

// GetCurrentDirectoriesStack returns the stack of directories that the
// component walker currently uses to perform resolution. This list can,
// for example, be copied if the directory walker needs to be cloned.
func (cw *DirectoryComponentWalker[TReference]) GetCurrentDirectoriesStack() ([]model_core.Message[*model_filesystem_pb.DirectoryContents, TReference], error) {
	if err := cw.dereferenceCurrentDirectory(); err != nil {
		return nil, err
	}
	return cw.directoriesStack, nil
}

// GetCurrentDirectory returns the lowest directory that the component
// walker currently uses to perform resolution.
func (cw *DirectoryComponentWalker[TReference]) GetCurrentDirectory() model_core.Message[*model_filesystem_pb.Directory, TReference] {
	if cw.currentDirectory.IsSet() {
		return cw.currentDirectory
	}
	currentDirectoryContents := cw.directoriesStack[len(cw.directoriesStack)-1]
	return model_core.Nested(
		currentDirectoryContents,
		&model_filesystem_pb.Directory{
			Contents: &model_filesystem_pb.Directory_ContentsInline{
				ContentsInline: currentDirectoryContents.Message,
			},
		},
	)
}

func (cw *DirectoryComponentWalker[TReference]) GetCurrentFileProperties() model_core.Message[*model_filesystem_pb.FileProperties, TReference] {
	return cw.fileProperties
}

// DirectoryGetLeaves is a helper function for obtaining the leaves
// contained in a directory.
func DirectoryGetLeaves[TReference any](
	ctx context.Context,
	reader model_parser.MessageObjectReader[TReference, *model_filesystem_pb.Leaves],
	directory model_core.Message[*model_filesystem_pb.DirectoryContents, TReference],
) (model_core.Message[*model_filesystem_pb.Leaves, TReference], error) {
	switch leaves := directory.Message.Leaves.(type) {
	case *model_filesystem_pb.DirectoryContents_LeavesExternal:
		return model_parser.Dereference(ctx, reader, model_core.Nested(directory, leaves.LeavesExternal.Reference))
	case *model_filesystem_pb.DirectoryContents_LeavesInline:
		return model_core.Nested(directory, leaves.LeavesInline), nil
	default:
		return model_core.Message[*model_filesystem_pb.Leaves, TReference]{}, status.Error(codes.InvalidArgument, "Directory has no leaves")
	}
}

// DirectoryGetContents is a helper function for obtaining the contents
// of a directory.
func DirectoryGetContents[TReference any](
	ctx context.Context,
	reader model_parser.MessageObjectReader[TReference, *model_filesystem_pb.DirectoryContents],
	directory model_core.Message[*model_filesystem_pb.Directory, TReference],
) (model_core.Message[*model_filesystem_pb.DirectoryContents, TReference], error) {
	switch contents := directory.Message.GetContents().(type) {
	case *model_filesystem_pb.Directory_ContentsExternal:
		return model_parser.Dereference(ctx, reader, model_core.Nested(directory, contents.ContentsExternal.Reference))
	case *model_filesystem_pb.Directory_ContentsInline:
		return model_core.Nested(directory, contents.ContentsInline), nil
	default:
		return model_core.Message[*model_filesystem_pb.DirectoryContents, TReference]{}, status.Error(codes.InvalidArgument, "Directory node has no contents")
	}
}
