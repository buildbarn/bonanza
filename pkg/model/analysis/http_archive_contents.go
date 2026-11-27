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

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_evaluation_pb "bonanza.build/pkg/proto/model/evaluation"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/filesystem"
	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/ulikunitz/xz"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
)

// archiveProgressReporter reports the number of bytes of an archive
// that have been extracted. To limit overhead, progress is only
// reported each time another mebibyte has been extracted.
type archiveProgressReporter struct {
	report       func(*model_analysis_pb.HttpArchiveContents_Progress)
	totalBytes   uint64
	currentBytes uint64
}

func newArchiveProgressReporter(report func(*model_analysis_pb.HttpArchiveContents_Progress), totalBytes uint64) *archiveProgressReporter {
	pr := &archiveProgressReporter{
		report:     report,
		totalBytes: totalBytes,
	}
	pr.reportCurrent()
	return pr
}

func (pr *archiveProgressReporter) reportCurrent() {
	pr.report(&model_analysis_pb.HttpArchiveContents_Progress{
		Extracted: model_evaluation_pb.ProgressCounter_builder{
			Current: pr.currentBytes,
			Total:   pr.totalBytes,
			Unit:    model_evaluation_pb.ProgressCounter_BYTES,
		}.Build(),
	})
}

func (pr *archiveProgressReporter) add(n uint64) {
	previousBytes := pr.currentBytes
	pr.currentBytes += n
	if previousBytes>>20 != pr.currentBytes>>20 {
		pr.reportCurrent()
	}
}

// progressReportingReader is a decorator for io.Reader that reports
// the number of bytes read through an archiveProgressReporter.
type progressReportingReader struct {
	r  io.Reader
	pr *archiveProgressReporter
}

func (r progressReportingReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.pr.add(uint64(n))
	return n, err
}

type archiveFile[TReference any] struct {
	isExecutable bool
	contents     model_core.Message[*model_filesystem_pb.FileContents, TReference]
}

type archiveDirectory[TReference any] struct {
	directories map[path.Component]*archiveDirectory[TReference]
	files       map[path.Component]archiveFile[TReference]
	symlinks    map[path.Component]path.Parser
}

func (d *archiveDirectory[TReference]) resolveNewDirectory(filePath string) error {
	return path.Resolve(
		path.UNIXFormat.NewParser(filePath),
		path.NewLoopDetectingScopeWalker(
			path.NewRelativeScopeWalker(&archiveDirectoryCreatingResolver[TReference]{
				baseArchiveDirectoryCreatingResolver: baseArchiveDirectoryCreatingResolver[TReference]{
					stack: util.NewNonEmptyStack(d),
				},
			}),
		),
	)
}

// resolveFile resolves the parent directory and name of a file.
// Directories that don't exist yet are created.
func (d *archiveDirectory[TReference]) resolveFile(filePath string) (*archiveDirectory[TReference], path.Component, error) {
	r := archiveFileCreatingResolver[TReference]{
		baseArchiveDirectoryCreatingResolver: baseArchiveDirectoryCreatingResolver[TReference]{
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
	return r.stack.Peek(), *r.TerminalName, nil
}

func (d *archiveDirectory[TReference]) resolveNewFile(filePath string) (*archiveDirectory[TReference], path.Component, error) {
	dChild, name, err := d.resolveFile(filePath)
	if err != nil {
		return nil, name, err
	}
	if _, ok := dChild.files[name]; ok {
		return nil, name, errors.New("path resolves to an already existing file")
	}
	if _, ok := dChild.symlinks[name]; ok {
		return nil, name, errors.New("path resolves to an already existing symbolic link")
	}
	return dChild, name, nil
}

// lookupFile returns the properties of a regular file that was
// extracted previously. This is used to process hard links.
func (d *archiveDirectory[TReference]) lookupFile(filePath string) (archiveFile[TReference], error) {
	dChild, name, err := d.resolveFile(filePath)
	if err != nil {
		return archiveFile[TReference]{}, err
	}
	f, ok := dChild.files[name]
	if !ok {
		return archiveFile[TReference]{}, errors.New("path does not resolve to a previously extracted regular file")
	}
	return f, nil
}

func (d *archiveDirectory[TReference]) addFile(name path.Component, contents model_core.Message[*model_filesystem_pb.FileContents, TReference], isExecutable bool) {
	if d.files == nil {
		d.files = map[path.Component]archiveFile[TReference]{}
	}
	d.files[name] = archiveFile[TReference]{
		isExecutable: isExecutable,
		contents:     contents,
	}
}

type baseArchiveDirectoryCreatingResolver[TReference any] struct {
	stack util.NonEmptyStack[*archiveDirectory[TReference]]
}

func (r *baseArchiveDirectoryCreatingResolver[TReference]) onDirectory(self path.ComponentWalker, name path.Component) (path.GotDirectoryOrSymlink, error) {
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
			d.directories = map[path.Component]*archiveDirectory[TReference]{}
		}
		dChild = &archiveDirectory[TReference]{}
		d.directories[name] = dChild
	}
	r.stack.Push(dChild)
	return path.GotDirectory{
		Child:        self,
		IsReversible: true,
	}, nil
}

func (r *baseArchiveDirectoryCreatingResolver[TReference]) onUp(self path.ComponentWalker) (path.ComponentWalker, error) {
	if _, ok := r.stack.PopSingle(); ok {
		return self, nil
	}
	return nil, errors.New("path resolves to a location outside the build directory")
}

type archiveDirectoryCreatingResolver[TReference any] struct {
	baseArchiveDirectoryCreatingResolver[TReference]
}

func (r *archiveDirectoryCreatingResolver[TReference]) OnDirectory(name path.Component) (path.GotDirectoryOrSymlink, error) {
	return r.onDirectory(r, name)
}

func (r *archiveDirectoryCreatingResolver[TReference]) OnTerminal(name path.Component) (*path.GotSymlink, error) {
	return path.OnTerminalViaOnDirectory(r, name)
}

func (r *archiveDirectoryCreatingResolver[TReference]) OnUp() (path.ComponentWalker, error) {
	return r.onUp(r)
}

type archiveFileCreatingResolver[TReference any] struct {
	baseArchiveDirectoryCreatingResolver[TReference]
	path.TerminalNameTrackingComponentWalker
}

func (r *archiveFileCreatingResolver[TReference]) OnDirectory(name path.Component) (path.GotDirectoryOrSymlink, error) {
	return r.onDirectory(r, name)
}

func (r *archiveFileCreatingResolver[TReference]) OnUp() (path.ComponentWalker, error) {
	return r.onUp(r)
}

// capturableArchiveDirectory is used to create a Merkle tree of the
// directory hierarchy of an extracted archive. Merkle trees of files
// are created while extracting the archive, meaning they merely need
// to be attached to the directory hierarchy.
type capturableArchiveDirectory[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	objectCapturer model_core.ExistingObjectCapturer[TReference, TMetadata]
	directory      *archiveDirectory[TReference]
}

func (ad *capturableArchiveDirectory[TReference, TMetadata]) EnterCapturableDirectory(name path.Component) (*model_filesystem.CreatedDirectory[TMetadata], model_filesystem.CapturableDirectory[TMetadata, TMetadata], error) {
	dChild, ok := ad.directory.directories[name]
	if !ok {
		panic("attempted to enter non-existent directory")
	}
	return nil, &capturableArchiveDirectory[TReference, TMetadata]{
		objectCapturer: ad.objectCapturer,
		directory:      dChild,
	}, nil
}

func (ad *capturableArchiveDirectory[TReference, TMetadata]) OpenForFileMerkleTreeCreation(name path.Component) (model_filesystem.CapturableFile[TMetadata], error) {
	info, ok := ad.directory.files[name]
	if !ok {
		panic("attempted to enter non-existent file")
	}
	return model_filesystem.NewSimpleCapturableFile(model_core.Patch(ad.objectCapturer, info.contents)), nil
}

func (ad *capturableArchiveDirectory[TReference, TMetadata]) ReadDir() ([]filesystem.FileInfo, error) {
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

func (ad *capturableArchiveDirectory[TReference, TMetadata]) Readlink(name path.Component) (path.Parser, error) {
	target, ok := ad.directory.symlinks[name]
	if !ok {
		panic("attempted to read non-existent symbolic link")
	}
	return target, nil
}

func (capturableArchiveDirectory[TReference, TMetadata]) Close() error {
	return nil
}

func (*baseComputer[TReference, TMetadata]) ComputeHTTPArchiveContentsValue(ctx context.Context, key *model_analysis_pb.HttpArchiveContents_Key, e HTTPArchiveContentsEnvironment[TReference, TMetadata]) (PatchedHTTPArchiveContentsValue[TMetadata], error) {
	fileReader, gotFileReader := e.GetFileAccessParametersObject(&model_analysis_pb.FileAccessParameters_Key{})
	directoryCreationParameters, gotDirectoryCreationParameters := e.GetDirectoryCreationParametersObject(&model_analysis_pb.DirectoryCreationParameters_Key{})
	fileCreationParameters, gotFileCreationParameters := e.GetFileCreationParametersObject(&model_analysis_pb.FileCreationParameters_Key{})
	httpFileContentsValue := e.GetHTTPFileContentsValue(&model_analysis_pb.HttpFileContents_Key{
		FetchOptions: key.FetchOptions,
	})
	if !gotFileReader || !gotDirectoryCreationParameters || !gotFileCreationParameters || !httpFileContentsValue.IsSet() {
		return PatchedHTTPArchiveContentsValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	if httpFileContentsValue.Message.Exists == nil {
		return PatchedHTTPArchiveContentsValue[TMetadata]{}, errors.New("file does not exist")
	}

	httpFileContentsEntry, err := model_filesystem.NewFileContentsEntryFromProto(
		model_core.Nested(httpFileContentsValue, httpFileContentsValue.Message.Exists.Contents),
	)
	if err != nil {
		return PatchedHTTPArchiveContentsValue[TMetadata]{}, fmt.Errorf("invalid file contents: %w", err)
	}

	// Create Merkle trees of files while extracting them. Objects
	// are captured by the environment, which takes care of writing
	// them to storage. Convert the resulting messages to ones
	// containing references, so that no metadata needs to be
	// tracked until the directory hierarchy is complete.
	//
	// Files are extracted sequentially, so a single buffered reader
	// can be used for all of them. This prevents allocating a large
	// buffer for every file.
	fileMerkleTreeCapturer := model_filesystem.NewSimpleFileMerkleTreeCapturer(e)
	fileBufferedReader := fileCreationParameters.NewChunkingReader(nil)
	createFileMerkleTree := func(r io.Reader) (model_core.Message[*model_filesystem_pb.FileContents, TReference], error) {
		fileBufferedReader.Reset(r)
		fileContents, err := model_filesystem.CreateFileMerkleTree(ctx, fileCreationParameters, fileBufferedReader, fileMerkleTreeCapturer)
		if err != nil {
			return model_core.Message[*model_filesystem_pb.FileContents, TReference]{}, err
		}
		return model_core.Unpatch(e, fileContents).Decay(), nil
	}

	var rootDirectory archiveDirectory[TReference]

	switch key.Format {
	case model_analysis_pb.HttpArchiveContents_Key_TAR_GZ, model_analysis_pb.HttpArchiveContents_Key_TAR_XZ:
		compressedReader := progressReportingReader{
			r: fileReader.FileOpenRead(ctx, httpFileContentsEntry, 0),
			pr: newArchiveProgressReporter(
				e.ReportHTTPArchiveContentsProgress,
				httpFileContentsEntry.GetEndBytes(),
			),
		}
		var decompressedReader io.Reader
		switch key.Format {
		case model_analysis_pb.HttpArchiveContents_Key_TAR_GZ:
			decompressedReader, err = gzip.NewReader(compressedReader)
			if err != nil {
				return PatchedHTTPArchiveContentsValue[TMetadata]{}, err
			}
		case model_analysis_pb.HttpArchiveContents_Key_TAR_XZ:
			decompressedReader, err = xz.NewReader(compressedReader)
			if err != nil {
				return PatchedHTTPArchiveContentsValue[TMetadata]{}, err
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
				return PatchedHTTPArchiveContentsValue[TMetadata]{}, err
			}

			switch header.Typeflag {
			case tar.TypeDir:
				if err := rootDirectory.resolveNewDirectory(header.Name); err != nil {
					return PatchedHTTPArchiveContentsValue[TMetadata]{}, fmt.Errorf("invalid path %#v: %w", header.Name, err)
				}
			case tar.TypeReg, tar.TypeLink, tar.TypeSymlink:
				d, name, err := rootDirectory.resolveNewFile(header.Name)
				if err != nil {
					return PatchedHTTPArchiveContentsValue[TMetadata]{}, fmt.Errorf("invalid path %#v: %w", header.Name, err)
				}

				switch header.Typeflag {
				case tar.TypeReg:
					contents, err := createFileMerkleTree(tarReader)
					if err != nil {
						return PatchedHTTPArchiveContentsValue[TMetadata]{}, fmt.Errorf("failed to extract file %#v: %w", header.Name, err)
					}
					d.addFile(name, contents, header.Mode&0o111 != 0)
				case tar.TypeLink:
					// Hard links refer to a file that was
					// extracted previously. As files are
					// immutable, they can share contents.
					target, err := rootDirectory.lookupFile(header.Linkname)
					if err != nil {
						return PatchedHTTPArchiveContentsValue[TMetadata]{}, fmt.Errorf("invalid hard link target %#v: %w", header.Linkname, err)
					}
					d.addFile(name, target.contents, target.isExecutable)
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
		zipReader, err := zip.NewReader(fileReader.FileOpenReadAt(ctx, httpFileContentsEntry), int64(httpFileContentsEntry.GetEndBytes()))
		if err != nil {
			return PatchedHTTPArchiveContentsValue[TMetadata]{}, err
		}
		var compressedSizeBytes uint64
		for _, file := range zipReader.File {
			compressedSizeBytes += file.CompressedSize64
		}
		progressReporter := newArchiveProgressReporter(e.ReportHTTPArchiveContentsProgress, compressedSizeBytes)
		for _, file := range zipReader.File {
			if strings.HasSuffix(file.Name, "/") {
				if err := rootDirectory.resolveNewDirectory(file.Name); err != nil {
					return PatchedHTTPArchiveContentsValue[TMetadata]{}, fmt.Errorf("invalid path %#v: %w", file.Name, err)
				}
			} else {
				d, name, err := rootDirectory.resolveNewFile(file.Name)
				if err != nil {
					return PatchedHTTPArchiveContentsValue[TMetadata]{}, fmt.Errorf("invalid path %#v: %w", file.Name, err)
				}

				f, err := file.Open()
				if err != nil {
					return PatchedHTTPArchiveContentsValue[TMetadata]{}, err
				}
				contents, err := createFileMerkleTree(f)
				f.Close()
				if err != nil {
					return PatchedHTTPArchiveContentsValue[TMetadata]{}, fmt.Errorf("failed to extract file %#v: %w", file.Name, err)
				}
				d.addFile(name, contents, true)
			}
			progressReporter.add(file.CompressedSize64)
		}
	default:
		return PatchedHTTPArchiveContentsValue[TMetadata]{}, errors.New("unknown archive format")
	}

	group, groupCtx := errgroup.WithContext(ctx)
	var createdRootDirectory model_filesystem.CreatedDirectory[TMetadata]
	group.Go(func() error {
		return model_filesystem.CreateDirectoryMerkleTree(
			groupCtx,
			semaphore.NewWeighted(1),
			group,
			directoryCreationParameters,
			&capturableArchiveDirectory[TReference, TMetadata]{
				objectCapturer: e,
				directory:      &rootDirectory,
			},
			model_filesystem.NewSimpleDirectoryMerkleTreeCapturer(e),
			&createdRootDirectory,
		)
	})
	if err := group.Wait(); err != nil {
		return PatchedHTTPArchiveContentsValue[TMetadata]{}, err
	}

	// Store the root directory itself. We don't embed it into the
	// response, as that prevents it from being accessed separately.
	if l := createdRootDirectory.MaximumSymlinkEscapementLevels; l == nil || l.Value != 0 {
		return PatchedHTTPArchiveContentsValue[TMetadata]{}, errors.New("archive contains one or more symbolic links that potentially escape the archive's root directory")
	}
	contentsReference, err := createdRootDirectory.CaptureDirectoryReference(ctx, directoryCreationParameters, e)
	if err != nil {
		return PatchedHTTPArchiveContentsValue[TMetadata]{}, err
	}

	return model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (*model_analysis_pb.HttpArchiveContents_Value, error) {
		return &model_analysis_pb.HttpArchiveContents_Value{
			Exists: &model_analysis_pb.HttpArchiveContents_Value_Exists{
				Contents: contentsReference.Merge(patcher),
				Sha256:   httpFileContentsValue.Message.Exists.Sha256,
			},
		}, nil
	})
}

// HTTPArchiveContentsEnvironmentForTesting is used to generate mocks
// for unit testing BaseComputer.
type HTTPArchiveContentsEnvironmentForTesting HTTPArchiveContentsEnvironment[model_core.CreatedObjectTree, model_core.CreatedObjectTree]
