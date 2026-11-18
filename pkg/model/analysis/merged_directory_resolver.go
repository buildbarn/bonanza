package analysis

import (
	"errors"

	model_filesystem "bonanza.build/pkg/model/filesystem"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
)

// mergedDirectoryResolver is an implementation of path.ComponentWalker
// that resolves paths within a MergedDirectory, creating all
// directories that are traversed. Upon completion, the directory field
// refers to the directory containing the final pathname component,
// whose name is stored in terminalName. If the path has a trailing
// slash, terminalName is nil and the directory field refers to the
// directory that was resolved.
type mergedDirectoryResolver[TReference object.BasicReference] struct {
	directory    *model_filesystem.MergedDirectory[TReference]
	terminalName *path.Component
}

var _ path.ComponentWalker = (*mergedDirectoryResolver[object.LocalReference])(nil)

func (r *mergedDirectoryResolver[TReference]) OnDirectory(name path.Component) (path.GotDirectoryOrSymlink, error) {
	r.directory = r.directory.GetOrCreateDirectory(name)
	return path.GotDirectory{
		Child:        r,
		IsReversible: false,
	}, nil
}

func (r *mergedDirectoryResolver[TReference]) OnTerminal(name path.Component) (*path.GotSymlink, error) {
	r.terminalName = &name
	return nil, nil
}

func (*mergedDirectoryResolver[TReference]) OnUp() (path.ComponentWalker, error) {
	return nil, errors.New("path cannot contain \"..\" components")
}
