package analysis

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"bonanza.build/pkg/evaluation"
	model_core "bonanza.build/pkg/model/core"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"

	"github.com/buildbarn/bb-storage/pkg/filesystem"
	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/ulikunitz/xz"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
)

type archiveFile struct {
	isExecutable bool
	offsetBytes  int64
	sizeBytes    int64
}

type archiveDirectory struct {
	directories map[path.Component]*archiveDirectory
	files       map[path.Component]archiveFile
	symlinks    map[path.Component]path.Parser
}

func (d *archiveDirectory) resolveNewDirectory(filePath string) error {
	return path.Resolve(
		path.UNIXFormat.NewParser(filePath),
		path.NewLoopDetectingScopeWalker(
			path.NewRelativeScopeWalker(&archiveDirectoryCreatingResolver{
				baseArchiveDirectoryCreatingResolver: baseArchiveDirectoryCreatingResolver{
					stack: util.NewNonEmptyStack(d),
				},
			}),
		),
	)
}

func (d *archiveDirectory) resolveNewFile(filePath string) (*archiveDirectory, path.Component, error) {
	r := archiveFileCreatingResolver{
		baseArchiveDirectoryCreatingResolver: baseArchiveDirectoryCreatingResolver{
			stack: util.NewNonEmptyStack(d),
		},
	}
	var badComponent path.Component
	if err := path.Resolve(
		path.UNIXFormat.NewParser(filePath),
		path.NewLoopDetectingScopeWalker(path.NewRelativeScopeWalker(&r)),
	); err != nil {
		return nil, badComponent, err
	}
	if r.TerminalName == nil {
		return nil, badComponent, errors.New("path resolves to a directory")
	}

	dChild := r.stack.Peek()
	if _, ok := dChild.files[*r.TerminalName]; ok {
		return nil, badComponent, errors.New("path resolves to an already existing file")
	}
	if _, ok := dChild.symlinks[*r.TerminalName]; ok {
		return nil, badComponent, errors.New("path resolves to an already existing symbolic link")
	}
	return dChild, *r.TerminalName, nil
}

func (d *archiveDirectory) addFile(name path.Component, extractedFilesWriter *model_filesystem.SectionWriter, f io.Reader, isExecutable bool) error {
	fileOffsetBytes := extractedFilesWriter.GetOffsetBytes()
	fileSizeBytes, err := io.Copy(extractedFilesWriter, f)
	if err != nil {
		return err
	}

	if d.files == nil {
		d.files = map[path.Component]archiveFile{}
	}
	d.files[name] = archiveFile{
		isExecutable: isExecutable,
		offsetBytes:  fileOffsetBytes,
		sizeBytes:    fileSizeBytes,
	}
	return nil
}

type baseArchiveDirectoryCreatingResolver struct {
	stack util.NonEmptyStack[*archiveDirectory]
}

func (r *baseArchiveDirectoryCreatingResolver) onDirectory(self path.ComponentWalker, name path.Component) (path.GotDirectoryOrSymlink, error) {
	d := r.stack.Peek()
	dChild, ok := d.directories[name]
	if !ok {
		if _, ok := d.files[name]; ok {
			return nil, errors.New("path resolves to an existing file")
		}
		if target, ok := d.symlinks[name]; ok {
			return path.GotSymlink{
				Parent: path.NewRelativeScopeWalker(self),
				Target: target,
			}, nil
		}

		// Create new directory.
		if d.directories == nil {
			d.directories = map[path.Component]*archiveDirectory{}
		}
		dChild = &archiveDirectory{}
		d.directories[name] = dChild
	}
	r.stack.Push(dChild)
	return path.GotDirectory{
		Child:        self,
		IsReversible: true,
	}, nil
}

func (r *baseArchiveDirectoryCreatingResolver) onUp(self path.ComponentWalker) (path.ComponentWalker, error) {
	if _, ok := r.stack.PopSingle(); ok {
		return self, nil
	}
	return nil, errors.New("path resolves to a location outside the build directory")
}

type archiveDirectoryCreatingResolver struct {
	baseArchiveDirectoryCreatingResolver
}

func (r *archiveDirectoryCreatingResolver) OnDirectory(name path.Component) (path.GotDirectoryOrSymlink, error) {
	return r.onDirectory(r, name)
}

func (r *archiveDirectoryCreatingResolver) OnTerminal(name path.Component) (*path.GotSymlink, error) {
	return path.OnTerminalViaOnDirectory(r, name)
}

func (r *archiveDirectoryCreatingResolver) OnUp() (path.ComponentWalker, error) {
	return r.onUp(r)
}

type archiveFileCreatingResolver struct {
	baseArchiveDirectoryCreatingResolver
	path.TerminalNameTrackingComponentWalker
}

func (r *archiveFileCreatingResolver) OnDirectory(name path.Component) (path.GotDirectoryOrSymlink, error) {
	return r.onDirectory(r, name)
}

func (r *archiveFileCreatingResolver) OnUp() (path.ComponentWalker, error) {
	return r.onUp(r)
}

type capturableArchiveDirectoryOptions[TFile model_core.ReferenceMetadata] struct {
	contentsFile           filesystem.FileReader
	fileCreationParameters *model_filesystem.FileCreationParameters
	fileMerkleTreeCapturer model_filesystem.FileMerkleTreeCapturer[TFile]
}

type capturableArchiveDirectory[TDirectory, TFile model_core.ReferenceMetadata] struct {
	options   *capturableArchiveDirectoryOptions[TFile]
	directory *archiveDirectory
}

func (ad *capturableArchiveDirectory[TDirectory, TFile]) EnterCapturableDirectory(name path.Component) (*model_filesystem.CreatedDirectory[TDirectory], model_filesystem.CapturableDirectory[TDirectory, TFile], error) {
	dChild, ok := ad.directory.directories[name]
	if !ok {
		panic("attempted to enter non-existent directory")
	}
	return nil, &capturableArchiveDirectory[TDirectory, TFile]{
		options:   ad.options,
		directory: dChild,
	}, nil
}

func (ad *capturableArchiveDirectory[TDirectory, TFile]) OpenForFileMerkleTreeCreation(name path.Component) (model_filesystem.CapturableFile[TFile], error) {
	info, ok := ad.directory.files[name]
	if !ok {
		panic("attempted to enter non-existent file")
	}
	return &capturableArchiveFile[TFile]{
		options: ad.options,
		info:    info,
	}, nil
}

func (ad *capturableArchiveDirectory[TDirectory, TFile]) ReadDir() ([]filesystem.FileInfo, error) {
	d := ad.directory
	children := make(filesystem.FileInfoList, 0, len(d.directories)+len(d.files)+len(d.symlinks))
	for name := range d.directories {
		children = append(children, filesystem.NewFileInfo(name, filesystem.FileTypeDirectory, false))
	}
	for name, info := range d.files {
		children = append(children, filesystem.NewFileInfo(name, filesystem.FileTypeRegularFile, info.isExecutable))
	}
	for name := range d.symlinks {
		children = append(children, filesystem.NewFileInfo(name, filesystem.FileTypeSymlink, false))
	}
	sort.Sort(children)
	return children, nil
}

func (ad *capturableArchiveDirectory[TDirectory, TFile]) Readlink(name path.Component) (path.Parser, error) {
	target, ok := ad.directory.symlinks[name]
	if !ok {
		panic("attempted to read non-existent symbolic link")
	}
	return target, nil
}

func (ad *capturableArchiveDirectory[TDirectory, TFile]) Close() error {
	return nil
}

type capturableArchiveFile[TFile model_core.ReferenceMetadata] struct {
	options *capturableArchiveDirectoryOptions[TFile]
	info    archiveFile
}

func (af *capturableArchiveFile[TFile]) CreateFileMerkleTree(ctx context.Context) (model_core.PatchedMessage[*model_filesystem_pb.FileContents, TFile], error) {
	return model_filesystem.CreateFileMerkleTree(
		ctx,
		af.options.fileCreationParameters,
		io.NewSectionReader(af.options.contentsFile, int64(af.info.offsetBytes), int64(af.info.sizeBytes)),
		af.options.fileMerkleTreeCapturer,
	)
}

func (af *capturableArchiveFile[TFile]) Discard() {}

func (c *baseComputer[TReference, TMetadata]) ComputeHttpArchiveContentsValue(ctx context.Context, key *model_analysis_pb.HttpArchiveContents_Key, e HttpArchiveContentsEnvironment[TReference, TMetadata]) (PatchedHttpArchiveContentsValue[TMetadata], error) {
	fileReader, gotFileReader := e.GetFileReaderValue(&model_analysis_pb.FileReader_Key{})
	directoryCreationParameters, gotDirectoryCreationParameters := e.GetDirectoryCreationParametersObjectValue(&model_analysis_pb.DirectoryCreationParametersObject_Key{})
	fileCreationParameters, gotFileCreationParameters := e.GetFileCreationParametersObjectValue(&model_analysis_pb.FileCreationParametersObject_Key{})
	httpFileContentsValue := e.GetHttpFileContentsValue(&model_analysis_pb.HttpFileContents_Key{
		FetchOptions: key.FetchOptions,
	})
	if !gotFileReader || !gotDirectoryCreationParameters || !gotFileCreationParameters || !httpFileContentsValue.IsSet() {
		return PatchedHttpArchiveContentsValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	if httpFileContentsValue.Message.Exists == nil {
		return PatchedHttpArchiveContentsValue[TMetadata]{}, errors.New("file does not exist")
	}

	httpFileContentsEntry, err := model_filesystem.NewFileContentsEntryFromProto(
		model_core.Nested(httpFileContentsValue, httpFileContentsValue.Message.Exists.Contents),
	)
	if err != nil {
		return PatchedHttpArchiveContentsValue[TMetadata]{}, fmt.Errorf("invalid file contents: %w", err)
	}

	// Create a temporary file for storing copies of extracted files.
	extractedFiles, err := c.filePool.NewFile()
	if err != nil {
		return PatchedHttpArchiveContentsValue[TMetadata]{}, err
	}
	defer extractedFiles.Close()
	extractedFilesWriter := model_filesystem.NewSectionWriter(extractedFiles)

	var rootDirectory archiveDirectory

	switch key.Format {
	case model_analysis_pb.HttpArchiveContents_Key_TAR_GZ, model_analysis_pb.HttpArchiveContents_Key_TAR_XZ:
		compressedReader := fileReader.FileOpenRead(ctx, httpFileContentsEntry, 0)
		var decompressedReader io.Reader
		switch key.Format {
		case model_analysis_pb.HttpArchiveContents_Key_TAR_GZ:
			decompressedReader, err = gzip.NewReader(compressedReader)
			if err != nil {
				return PatchedHttpArchiveContentsValue[TMetadata]{}, err
			}
		case model_analysis_pb.HttpArchiveContents_Key_TAR_XZ:
			decompressedReader, err = xz.NewReader(compressedReader)
			if err != nil {
				return PatchedHttpArchiveContentsValue[TMetadata]{}, err
			}
		default:
			panic("unhandled compression format")
		}
		tarReader := tar.NewReader(decompressedReader)
		for {
			header, err := tarReader.Next()
			if err != nil {
				if err == io.EOF {
					break
				}
				return PatchedHttpArchiveContentsValue[TMetadata]{}, err
			}

			switch header.Typeflag {
			case tar.TypeDir:
				if err := rootDirectory.resolveNewDirectory(header.Name); err != nil {
					return PatchedHttpArchiveContentsValue[TMetadata]{}, fmt.Errorf("invalid path %#v: %w", header.Name, err)
				}
			case tar.TypeLink:
				panic("TODO")
			case tar.TypeReg, tar.TypeSymlink:
				d, name, err := rootDirectory.resolveNewFile(header.Name)
				if err != nil {
					return PatchedHttpArchiveContentsValue[TMetadata]{}, fmt.Errorf("invalid path %#v: %w", header.Name, err)
				}

				switch header.Typeflag {
				case tar.TypeReg:
					if err := d.addFile(name, extractedFilesWriter, tarReader, header.Mode&0o111 != 0); err != nil {
						return PatchedHttpArchiveContentsValue[TMetadata]{}, err
					}
				case tar.TypeSymlink:
					if d.symlinks == nil {
						d.symlinks = map[path.Component]path.Parser{}
					}
					d.symlinks[name] = path.UNIXFormat.NewParser(header.Linkname)
				default:
					panic("switch statement should have matched one of the cases above")
				}
			}
		}
	case model_analysis_pb.HttpArchiveContents_Key_ZIP:
		zipReader, err := zip.NewReader(fileReader.FileOpenReadAt(ctx, httpFileContentsEntry), int64(httpFileContentsEntry.EndBytes))
		if err != nil {
			return PatchedHttpArchiveContentsValue[TMetadata]{}, err
		}
		for _, file := range zipReader.File {
			if strings.HasSuffix(file.Name, "/") {
				if err := rootDirectory.resolveNewDirectory(file.Name); err != nil {
					return PatchedHttpArchiveContentsValue[TMetadata]{}, fmt.Errorf("invalid path %#v: %w", file.Name, err)
				}
			} else {
				d, name, err := rootDirectory.resolveNewFile(file.Name)
				if err != nil {
					return PatchedHttpArchiveContentsValue[TMetadata]{}, fmt.Errorf("invalid path %#v: %w", file.Name, err)
				}

				f, err := file.Open()
				if err != nil {
					return PatchedHttpArchiveContentsValue[TMetadata]{}, err
				}
				errAdd := d.addFile(name, extractedFilesWriter, f, true)
				f.Close()
				if errAdd != nil {
					return PatchedHttpArchiveContentsValue[TMetadata]{}, errAdd
				}
			}
		}
	default:
		return PatchedHttpArchiveContentsValue[TMetadata]{}, errors.New("unknown archive format")
	}

	group, groupCtx := errgroup.WithContext(ctx)
	var createdRootDirectory model_filesystem.CreatedDirectory[TMetadata]
	group.Go(func() error {
		return model_filesystem.CreateDirectoryMerkleTree(
			groupCtx,
			semaphore.NewWeighted(1),
			group,
			directoryCreationParameters,
			&capturableArchiveDirectory[TMetadata, TMetadata]{
				options: &capturableArchiveDirectoryOptions[TMetadata]{
					contentsFile:           extractedFiles,
					fileCreationParameters: fileCreationParameters,
					fileMerkleTreeCapturer: model_filesystem.NewSimpleFileMerkleTreeCapturer(e),
				},
				directory: &rootDirectory,
			},
			model_filesystem.NewSimpleDirectoryMerkleTreeCapturer(e),
			&createdRootDirectory,
		)
	})
	if err := group.Wait(); err != nil {
		return PatchedHttpArchiveContentsValue[TMetadata]{}, err
	}

	// Store the root directory itself. We don't embed it into the
	// response, as that prevents it from being accessed separately.
	if l := createdRootDirectory.MaximumSymlinkEscapementLevels; l == nil || l.Value != 0 {
		return PatchedHttpArchiveContentsValue[TMetadata]{}, errors.New("archive contains one or more symbolic links that potentially escape the archive's root directory")
	}
	createdRootDirectoryObject, err := model_core.MarshalAndEncode(
		model_core.ProtoToMarshalable(createdRootDirectory.Message),
		c.getReferenceFormat(),
		directoryCreationParameters.GetEncoder(),
	)
	if err != nil {
		return PatchedHttpArchiveContentsValue[TMetadata]{}, err
	}

	return model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.HttpArchiveContents_Value {
		return &model_analysis_pb.HttpArchiveContents_Value{
			Exists: &model_analysis_pb.HttpArchiveContents_Value_Exists{
				Contents: createdRootDirectory.ToDirectoryReference(
					patcher.CaptureAndAddDecodableReference(createdRootDirectoryObject, e),
				),
				Sha256: httpFileContentsValue.Message.Exists.Sha256,
			},
		}
	}), nil
}
