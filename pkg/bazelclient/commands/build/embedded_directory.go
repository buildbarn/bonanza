package build

import (
	"io"
	"io/fs"
	"sort"

	"bonanza.build/pkg/dotbazel"
	model_core "bonanza.build/pkg/model/core"
	model_filesystem "bonanza.build/pkg/model/filesystem"

	"github.com/buildbarn/bb-storage/pkg/filesystem"
	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	// Name under which BUILD files are stored in embedded modules,
	// and the name under which they are uploaded. Embedded modules
	// are stored in the Bonanza source tree, meaning they can't
	// contain files named BUILD or BUILD.bazel. Otherwise Bazel
	// would interpret them as packages.
	embeddedBuildFileName = path.MustNewComponent("BUILD.tools")
	uploadedBuildFileName = dotbazel.BuildComponent
)

// embeddedDirectory provides access to a directory of a Starlark
// module that is embedded into the bonanza_bazel binary (e.g.,
// bazel_tools, builtins_core). It allows these modules to be uploaded
// in the same way as modules stored on the local file system.
//
// It implements both model_filesystem.CapturableDirectory, so that a
// Merkle tree of the module can be created, and
// model_filesystem.CapturedDirectory, so that the module's contents
// can subsequently be uploaded to storage.
type embeddedDirectory[TDirectory, TFile model_core.ReferenceMetadata] struct {
	fsys fs.FS
	// Path of the directory within fsys. A nil trace refers to the
	// root directory of fsys.
	path    *path.Trace
	options *localCapturableDirectoryOptions[TFile]
}

// getChildPath returns the path of a child of the directory within
// fsys, translating the name of BUILD files. As fs.FS always uses
// slash separated paths, it can be converted to a string using
// GetUNIXString().
func (d *embeddedDirectory[TDirectory, TFile]) getChildPath(name path.Component) (*path.Trace, error) {
	if name == uploadedBuildFileName {
		name = embeddedBuildFileName
	} else if name == embeddedBuildFileName {
		// Prevent access to BUILD.tools under its
		// original name.
		return nil, status.Error(codes.NotFound, "Path does not exist")
	}
	return d.path.Append(name), nil
}

func (*embeddedDirectory[TDirectory, TFile]) Close() error {
	return nil
}

// openFile opens a file contained in the embedded directory. Unlike
// fs.ReadFile(), this does not copy the file's contents. Files in
// embed.FS can be read directly using io.ReaderAt.
func (d *embeddedDirectory[TDirectory, TFile]) openFile(name path.Component) (*embeddedFileReader, error) {
	childPath, err := d.getChildPath(name)
	if err != nil {
		return nil, err
	}
	f, err := d.fsys.Open(childPath.GetUNIXString())
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if info.IsDir() {
		f.Close()
		return nil, status.Error(codes.InvalidArgument, "Path is a directory")
	}
	readerAt, ok := f.(io.ReaderAt)
	if !ok {
		f.Close()
		return nil, status.Error(codes.Unimplemented, "File does not support random access reads")
	}
	return &embeddedFileReader{
		file:     f,
		readerAt: readerAt,
		size:     info.Size(),
	}, nil
}

// isExecutable returns whether a file in the embedded directory should
// be marked executable. Embedded files don't preserve permissions.
// Assume files are executable if and only if they start with a
// shebang.
func (d *embeddedDirectory[TDirectory, TFile]) isExecutable(name path.Component) (bool, error) {
	f, err := d.openFile(name)
	if err != nil {
		return false, err
	}
	defer f.Close()

	var prefix [2]byte
	n, err := f.ReadAt(prefix[:], 0)
	if err != nil && err != io.EOF {
		return false, err
	}
	return n == len(prefix) && string(prefix[:]) == "#!", nil
}

func (d *embeddedDirectory[TDirectory, TFile]) enterDirectory(name path.Component) (*embeddedDirectory[TDirectory, TFile], error) {
	childPath, err := d.getChildPath(name)
	if err != nil {
		return nil, err
	}
	info, err := fs.Stat(d.fsys, childPath.GetUNIXString())
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, status.Error(codes.InvalidArgument, "Path is not a directory")
	}
	return &embeddedDirectory[TDirectory, TFile]{
		fsys:    d.fsys,
		path:    childPath,
		options: d.options,
	}, nil
}

func (d *embeddedDirectory[TDirectory, TFile]) ReadDir() ([]filesystem.FileInfo, error) {
	entries, err := fs.ReadDir(d.fsys, d.path.GetUNIXString())
	if err != nil {
		return nil, err
	}
	fileInfos := make(filesystem.FileInfoList, 0, len(entries))
	for _, entry := range entries {
		name, ok := path.NewComponent(entry.Name())
		if !ok {
			return nil, status.Errorf(codes.InvalidArgument, "Invalid filename %#v", entry.Name())
		}
		switch name {
		case uploadedBuildFileName:
			return nil, status.Errorf(codes.InvalidArgument, "File %#v conflicts with %#v", name.String(), embeddedBuildFileName.String())
		case embeddedBuildFileName:
			name = uploadedBuildFileName
		}

		if entry.IsDir() {
			fileInfos = append(fileInfos, filesystem.NewFileInfo(name, filesystem.FileTypeDirectory, false))
		} else {
			isExecutable, err := d.isExecutable(name)
			if err != nil {
				return nil, util.StatusWrapf(err, "Failed to read file %#v", name.String())
			}
			fileInfos = append(fileInfos, filesystem.NewFileInfo(name, filesystem.FileTypeRegularFile, isExecutable))
		}
	}
	sort.Sort(fileInfos)
	return fileInfos, nil
}

func (*embeddedDirectory[TDirectory, TFile]) Readlink(name path.Component) (path.Parser, error) {
	return nil, status.Error(codes.InvalidArgument, "Embedded modules do not contain symbolic links")
}

func (d *embeddedDirectory[TDirectory, TFile]) EnterCapturableDirectory(name path.Component) (*model_filesystem.CreatedDirectory[TDirectory], model_filesystem.CapturableDirectory[TDirectory, TFile], error) {
	child, err := d.enterDirectory(name)
	if err != nil {
		return nil, nil, err
	}
	return nil, child, nil
}

func (d *embeddedDirectory[TDirectory, TFile]) OpenForFileMerkleTreeCreation(name path.Component) (model_filesystem.CapturableFile[TFile], error) {
	f, err := d.OpenRead(name)
	if err != nil {
		return nil, err
	}
	return &localCapturableFile[TFile]{
		file:    f,
		options: d.options,
	}, nil
}

func (d *embeddedDirectory[TDirectory, TFile]) EnterCapturedDirectory(name path.Component) (model_filesystem.CapturedDirectory, error) {
	return d.enterDirectory(name)
}

func (d *embeddedDirectory[TDirectory, TFile]) OpenRead(name path.Component) (filesystem.FileReader, error) {
	f, err := d.openFile(name)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// embeddedFileReader is an implementation of filesystem.FileReader
// for files contained in embedded modules.
type embeddedFileReader struct {
	file     fs.File
	readerAt io.ReaderAt
	size     int64
}

func (f *embeddedFileReader) ReadAt(p []byte, offset int64) (int, error) {
	return f.readerAt.ReadAt(p, offset)
}

func (f *embeddedFileReader) Close() error {
	return f.file.Close()
}

func (f *embeddedFileReader) GetNextRegionOffset(offset int64, regionType filesystem.RegionType) (int64, error) {
	if offset >= f.size {
		return 0, io.EOF
	}
	switch regionType {
	case filesystem.Data:
		return offset, nil
	case filesystem.Hole:
		return f.size, nil
	default:
		panic("unknown region type")
	}
}

func (f *embeddedFileReader) Len() (int64, error) {
	return f.size, nil
}
