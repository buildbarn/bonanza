package filesystem

import (
	"context"
	"io"
	"math"
	"sync/atomic"

	model_core "bonanza.build/pkg/model/core"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	"bonanza.build/pkg/storage/dag"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/filesystem"
	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// CapturedDirectory is called into by CapturedDirectoryWalker to
// traverse a directory hierarchy and read file contents.
type CapturedDirectory interface {
	Close() error
	EnterCapturedDirectory(name path.Component) (CapturedDirectory, error)
	OpenRead(name path.Component) (filesystem.FileReader, error)
}

// capturedDirectoryWalkerOptions contains all state that is shared by
// all the transitively created instances of ObjectContentsWalker that
// are returned by NewCapturedDirectoryWalker().
type capturedDirectoryWalkerOptions struct {
	directoryParameters *DirectoryAccessParameters
	fileParameters      *FileCreationParameters
	rootDirectory       CapturedDirectory
}

// openFile opens a file underneath the root directory for reading. This
// method is called whenever GetContents() is called against an
// ObjectContentsWalker that is backed by a file.
func (o *capturedDirectoryWalkerOptions) openFile(pathTrace *path.Trace) (filesystem.FileReader, error) {
	var dPathTrace *path.Trace
	d := o.rootDirectory
	var dCloser io.Closer = io.NopCloser(nil)
	defer func() {
		dCloser.Close()
	}()

	components := pathTrace.ToList()
	for _, component := range components[:len(components)-1] {
		dPathTrace = dPathTrace.Append(component)
		dChild, err := d.EnterCapturedDirectory(component)
		if err != nil {
			return nil, util.StatusWrapf(err, "Failed to enter directory %#v", dPathTrace.GetUNIXString())
		}
		dCloser.Close()
		d = dChild
		dCloser = dChild
	}

	return d.OpenRead(components[len(components)-1])
}

// gatherWalkersForLeaves creates ObjectContentsWalkers for all files
// contained in the provided Leaves message.
func (o *capturedDirectoryWalkerOptions) gatherWalkersForLeaves(leaves model_core.Message[*model_filesystem_pb.Leaves, object.LocalReference], pathTrace *path.Trace, walkers []dag.ObjectContentsWalker) error {
	for _, childFile := range leaves.Message.Files {
		childPathTrace := pathTrace.Append(path.MustNewComponent(childFile.Name))
		properties := childFile.Properties
		if properties == nil {
			return status.Errorf(codes.InvalidArgument, "File %#v does not have any properties", childPathTrace.GetUNIXString())
		}
		if contents := properties.Contents; contents != nil {
			switch level := contents.Level.(type) {
			case *model_filesystem_pb.FileContents_List_:
				index, err := model_core.GetIndexFromReferenceMessage(level.List.Reference.GetReference(), len(walkers))
				if err != nil {
					return util.StatusWrapf(err, "Invalid file contents list reference index for file %#v", childPathTrace.GetUNIXString())
				}
				walkers[index] = &recomputingConcatenatedFileWalker{
					options:   o,
					reference: leaves.OutgoingReferences.GetOutgoingReference(index),
					pathTrace: childPathTrace,
				}
			case *model_filesystem_pb.FileContents_ChunkReference:
				index, err := model_core.GetIndexFromReferenceMessage(level.ChunkReference.Reference, len(walkers))
				if err != nil {
					return util.StatusWrapf(err, "Invalid chunk reference index for file %#v", childPathTrace.GetUNIXString())
				}
				walkers[index] = &smallFileWalker{
					options:   o,
					reference: leaves.OutgoingReferences.GetOutgoingReference(index),
					pathTrace: childPathTrace,
					sizeBytes: uint32(contents.TotalSizeBytes),
				}
			case *model_filesystem_pb.FileContents_Hole:
			default:
				return status.Errorf(codes.InvalidArgument, "File %#v has an unknown file contents type", childPathTrace.GetUNIXString())
			}
		}
	}
	return nil
}

// capturedDirectoryWalker is an ObjectContentsWalker that is backed by
// a CreatedObjectTree corresponding to a Directory message.
type capturedDirectoryWalker struct {
	options            *capturedDirectoryWalkerOptions
	object             *model_core.CreatedObjectTree
	decodingParameters []byte
	pathTrace          *path.Trace
}

// NewCapturedDirectoryWalker returns an implementation of
// ObjectContentsWalker that is capable of walking over a hierarchy over
// Directory and Leaves objects that were created using
// CreateDirectoryMerkleTree and captured using
// FileDiscardingDirectoryMerkleTreeCapturer. This makes it possible to
// upload such directory hierarchies to a storage server.
//
// These Merkle trees do not contain any file contents, but it is
// permitted for the storage server to request them. If that happens, we
// must reobtain them from the underlying file system. This is why the
// caller must provide a handle to the root directory on which the
// provided Merkle tree is based.
func NewCapturedDirectoryWalker(directoryParameters *DirectoryAccessParameters, fileParameters *FileCreationParameters, rootDirectory CapturedDirectory, rootObject *model_core.CreatedObjectTree, decodingParameters []byte) dag.ObjectContentsWalker {
	return &capturedDirectoryWalker{
		options: &capturedDirectoryWalkerOptions{
			directoryParameters: directoryParameters,
			fileParameters:      fileParameters,
			rootDirectory:       rootDirectory,
		},
		object:             rootObject,
		decodingParameters: decodingParameters,
	}
}

func (w *capturedDirectoryWalker) GetContents(ctx context.Context) (*object.Contents, []dag.ObjectContentsWalker, error) {
	directory, err := w.options.directoryParameters.DecodeDirectory(w.object.Contents, w.decodingParameters)
	if err != nil {
		return nil, nil, util.StatusWrapf(err, "Failed to decode directory %#v", w.pathTrace.GetUNIXString())
	}

	walkers := make([]dag.ObjectContentsWalker, w.object.GetDegree())
	if err := w.gatherWalkers(directory, w.pathTrace, walkers); err != nil {
		return nil, nil, err
	}
	return w.object.Contents, walkers, nil
}

func (w *capturedDirectoryWalker) gatherWalkers(directory *model_filesystem_pb.DirectoryContents, pathTrace *path.Trace, walkers []dag.ObjectContentsWalker) error {
	switch leaves := directory.Leaves.(type) {
	case *model_filesystem_pb.DirectoryContents_LeavesExternal:
		index, err := model_core.GetIndexFromReferenceMessage(leaves.LeavesExternal.Reference.GetReference(), len(walkers))
		if err != nil {
			return util.StatusWrapf(err, "Invalid reference index for leaves of directory %#v", pathTrace.GetUNIXString())
		}
		walkers[index] = &capturedLeavesWalker{
			options:            w.options,
			object:             &w.object.Metadata[index],
			decodingParameters: leaves.LeavesExternal.Reference.DecodingParameters,
			pathTrace:          pathTrace,
		}
	case *model_filesystem_pb.DirectoryContents_LeavesInline:
		if err := w.options.gatherWalkersForLeaves(model_core.NewMessage(leaves.LeavesInline, w.object.Contents), pathTrace, walkers); err != nil {
			return err
		}
	default:
		return status.Errorf(codes.InvalidArgument, "Invalid leaves type for directory %#v", pathTrace.GetUNIXString())
	}

	for _, childDirectory := range directory.Directories {
		childPathTrace := pathTrace.Append(path.MustNewComponent(childDirectory.Name))
		switch contents := childDirectory.Directory.GetContents().(type) {
		case *model_filesystem_pb.Directory_ContentsExternal:
			index, err := model_core.GetIndexFromReferenceMessage(contents.ContentsExternal.Reference.GetReference(), len(walkers))
			if err != nil {
				return util.StatusWrapf(err, "Invalid reference index for directory %#v", childPathTrace.GetUNIXString())
			}
			walkers[index] = &capturedDirectoryWalker{
				options:            w.options,
				object:             &w.object.Metadata[index],
				decodingParameters: contents.ContentsExternal.Reference.DecodingParameters,
				pathTrace:          childPathTrace,
			}
		case *model_filesystem_pb.Directory_ContentsInline:
			if err := w.gatherWalkers(contents.ContentsInline, childPathTrace, walkers); err != nil {
				return err
			}
		default:
			return status.Errorf(codes.InvalidArgument, "Invalid contents type for directory %#v", childPathTrace.GetUNIXString())
		}
	}
	return nil
}

func (capturedDirectoryWalker) Discard() {}

// capturedLeaves is an ObjectContentsWalker that is backed by a
// CreatedObjectTree corresponding to a Leaves message.
type capturedLeavesWalker struct {
	options            *capturedDirectoryWalkerOptions
	object             *model_core.CreatedObjectTree
	decodingParameters []byte
	pathTrace          *path.Trace
}

func (w *capturedLeavesWalker) GetContents(ctx context.Context) (*object.Contents, []dag.ObjectContentsWalker, error) {
	leaves, err := w.options.directoryParameters.DecodeLeaves(w.object.Contents, w.decodingParameters)
	if err != nil {
		return nil, nil, util.StatusWrapf(err, "Failed to decode leaves of directory %#v", w.pathTrace.GetUNIXString())
	}

	walkers := make([]dag.ObjectContentsWalker, w.object.GetDegree())
	if err := w.options.gatherWalkersForLeaves(model_core.NewMessage(leaves, w.object.Contents), w.pathTrace, walkers); err != nil {
		return nil, nil, err
	}
	return w.object.Contents, walkers, nil
}

func (capturedLeavesWalker) Discard() {}

// smallFileWalker is an ObjectContentsWalker that is backed by a file
// that is small enough to store in a single chunk.
type smallFileWalker struct {
	options   *capturedDirectoryWalkerOptions
	reference object.LocalReference
	pathTrace *path.Trace
	sizeBytes uint32
}

func (w *smallFileWalker) GetContents(ctx context.Context) (*object.Contents, []dag.ObjectContentsWalker, error) {
	r, err := w.options.openFile(w.pathTrace)
	if err != nil {
		return nil, nil, util.StatusWrapf(err, "Failed to open %#v", w.pathTrace.GetUNIXString())
	}
	defer r.Close()

	data := make([]byte, w.sizeBytes)
	if n, err := r.ReadAt(data, 0); n != len(data) {
		return nil, nil, util.StatusWrapf(err, "Failed to read %#v", w.pathTrace.GetUNIXString())
	}
	contents, err := w.options.fileParameters.EncodeChunk(data)
	if err != nil {
		return nil, nil, util.StatusWrapf(err, "Failed to encode %#v", w.pathTrace.GetUNIXString())
	}
	if actualReference := contents.Value.GetLocalReference(); actualReference != w.reference {
		return nil, nil, status.Errorf(codes.InvalidArgument, "File %#v has reference %s, while %s was expected", w.pathTrace.GetUNIXString(), actualReference, w.reference)
	}
	return contents.Value, nil, nil
}

func (smallFileWalker) Discard() {}

// recomputingConcatenatedFileWalker is an ObjectContentsWalker that is
// backed by a file that is too large to store in a single chunk. When
// GetContents() is called, the Merkle tree corresponding to this file
// is recomputed.
type recomputingConcatenatedFileWalker struct {
	options   *capturedDirectoryWalkerOptions
	reference object.LocalReference
	pathTrace *path.Trace
}

func (w *recomputingConcatenatedFileWalker) GetContents(ctx context.Context) (*object.Contents, []dag.ObjectContentsWalker, error) {
	r, err := w.options.openFile(w.pathTrace)
	if err != nil {
		return nil, nil, util.StatusWrapf(err, "Failed to open %#v", w.pathTrace.GetUNIXString())
	}
	fileContents, err := CreateFileMerkleTree(
		ctx,
		w.options.fileParameters,
		io.NewSectionReader(r, 0, math.MaxInt64),
		ChunkDiscardingFileMerkleTreeCapturer,
	)
	if err != nil {
		r.Close()
		return nil, nil, err
	}

	if fileContents.Message == nil {
		return nil, nil, status.Errorf(codes.InvalidArgument, "File %#v no longer has any contents", w.pathTrace.GetUNIXString())
	}
	references, objects := fileContents.Patcher.SortAndSetReferences()
	if references[0] != w.reference {
		r.Close()
		return nil, nil, status.Errorf(codes.InvalidArgument, "File %#v has reference %s, while %s was expected", w.pathTrace.GetUNIXString(), references[0], w.reference)
	}

	options := &computedConcatenatedFileObjectOptions{
		fileParameters: w.options.fileParameters,
		pathTrace:      w.pathTrace,
		file:           r,
	}
	options.referenceCount.Store(1)
	wComputed := &computedConcatenatedFileWalker{
		options:            options,
		object:             &objects[0],
		decodingParameters: fileContents.Message.Level.(*model_filesystem_pb.FileContents_List_).List.Reference.GetDecodingParameters(),
	}
	return wComputed.GetContents(ctx)
}

func NewCapturedFileWalker(fileParameters *FileCreationParameters, r filesystem.FileReader, fileReference object.LocalReference, fileSizeBytes uint64, fileObject *model_core.CreatedObjectTree, decodingParameters []byte) dag.ObjectContentsWalker {
	options := &computedConcatenatedFileObjectOptions{
		fileParameters: fileParameters,
		file:           r,
	}
	options.referenceCount.Store(1)
	if fileReference.GetHeight() == 0 {
		return &concatenatedFileChunkWalker{
			options:            options,
			reference:          fileReference,
			decodingParameters: decodingParameters,
			sizeBytes:          uint32(fileSizeBytes),
		}
	}
	return &computedConcatenatedFileWalker{
		options:            options,
		object:             fileObject,
		decodingParameters: decodingParameters,
	}
}

func (recomputingConcatenatedFileWalker) Discard() {}

// computedConcatenatedFileObjectOptions is an ObjectContentsWalker that
// is backed by a part of a large file for which the Merkle tree was
// recomputed.
type computedConcatenatedFileObjectOptions struct {
	fileParameters *FileCreationParameters
	pathTrace      *path.Trace
	referenceCount atomic.Uint64
	file           filesystem.FileReader
}

type computedConcatenatedFileWalker struct {
	options            *computedConcatenatedFileObjectOptions
	object             *model_core.CreatedObjectTree
	decodingParameters []byte
	offsetBytes        uint64
}

func (w *computedConcatenatedFileWalker) GetContents(ctx context.Context) (*object.Contents, []dag.ObjectContentsWalker, error) {
	fileContentsList, err := w.options.fileParameters.DecodeFileContentsList(w.object.Contents, w.decodingParameters)
	if err != nil {
		return nil, nil, util.StatusWrapf(err, "Failed to decode file contents list for file %#v at offset %d", w.options.pathTrace.GetUNIXString(), w.offsetBytes)
	}
	walkers := make([]dag.ObjectContentsWalker, w.object.GetDegree())
	offsetBytes := w.offsetBytes
	for _, part := range fileContentsList {
		switch level := part.Level.(type) {
		case *model_filesystem_pb.FileContents_List_:
			index, err := model_core.GetIndexFromReferenceMessage(level.List.Reference.GetReference(), len(walkers))
			if err != nil {
				return nil, nil, util.StatusWrapf(err, "Invalid file contents list reference index for part of file %#v at offset %d", w.options.pathTrace.GetUNIXString(), offsetBytes)
			}
			walkers[index] = &computedConcatenatedFileWalker{
				options:            w.options,
				object:             &w.object.Metadata[index],
				decodingParameters: level.List.Reference.GetDecodingParameters(),
				offsetBytes:        offsetBytes,
			}
		case *model_filesystem_pb.FileContents_ChunkReference:
			index, err := model_core.GetIndexFromReferenceMessage(level.ChunkReference.Reference, len(walkers))
			if err != nil {
				return nil, nil, util.StatusWrapf(err, "Invalid chunk reference index for part of file %#v at offset %d", w.options.pathTrace.GetUNIXString(), offsetBytes)
			}
			walkers[index] = &concatenatedFileChunkWalker{
				options:            w.options,
				reference:          w.object.GetOutgoingReference(index),
				decodingParameters: level.ChunkReference.DecodingParameters,
				offsetBytes:        offsetBytes,
				sizeBytes:          uint32(part.TotalSizeBytes),
			}
		case *model_filesystem_pb.FileContents_Hole:
		default:
			return nil, nil, status.Errorf(codes.InvalidArgument, "Part of %#v at offset %d has an unknown file contents type", w.options.pathTrace.GetUNIXString(), offsetBytes)
		}
		offsetBytes += part.TotalSizeBytes
	}
	w.options.referenceCount.Add(uint64(len(walkers) - 1))
	return w.object.Contents, walkers, nil
}

func (w *computedConcatenatedFileWalker) Discard() {
	if w.options.referenceCount.Add(^uint64(0)) == 0 {
		w.options.file.Close()
		w.options.file = nil
	}
	w.options = nil
}

// concatenatedFileChunkWalker is an ObjectContentsWalker that is backed
// by a single chunk of data contained in a large file.
type concatenatedFileChunkWalker struct {
	options            *computedConcatenatedFileObjectOptions
	reference          object.LocalReference
	decodingParameters []byte
	offsetBytes        uint64
	sizeBytes          uint32
}

func (w *concatenatedFileChunkWalker) GetContents(ctx context.Context) (*object.Contents, []dag.ObjectContentsWalker, error) {
	defer w.Discard()

	data := make([]byte, w.sizeBytes)
	if n, err := w.options.file.ReadAt(data, int64(w.offsetBytes)); n != len(data) {
		return nil, nil, util.StatusWrapf(err, "Failed to read %#v at offset %d", w.options.pathTrace.GetUNIXString(), w.offsetBytes)
	}
	contents, err := w.options.fileParameters.EncodeChunk(data)
	if err != nil {
		return nil, nil, util.StatusWrapf(err, "Failed to encode %#v", w.options.pathTrace.GetUNIXString())
	}
	if actualReference := contents.Value.GetLocalReference(); actualReference != w.reference {
		return nil, nil, status.Errorf(codes.InvalidArgument, "Chunk at offset %d in file %#v has reference %s, while %s was expected", w.offsetBytes, w.options.pathTrace.GetUNIXString(), actualReference, w.reference)
	}
	return contents.Value, nil, nil
}

func (w *concatenatedFileChunkWalker) Discard() {
	if w.options.referenceCount.Add(^uint64(0)) == 0 {
		w.options.file.Close()
		w.options.file = nil
	}
	w.options = nil
}
