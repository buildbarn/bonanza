package filesystem

import (
	"context"
	"io"
	"math"
	"sync/atomic"

	model_core "bonanza.build/pkg/model/core"
	model_parser "bonanza.build/pkg/model/parser"
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
	entriesParser  model_parser.MessageObjectParser[object.LocalReference, []*model_filesystem_pb.DirectoryEntry]
	fileParameters *FileCreationParameters
	rootDirectory  CapturedDirectory
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

// gatherWalkersForFile creates an ObjectContentsWalker for the contents
// of a file, if the file is non-empty.
func (o *capturedDirectoryWalkerOptions) gatherWalkersForFile(object *model_core.CreatedObjectTree, properties *model_filesystem_pb.FileProperties, pathTrace *path.Trace, walkers []dag.ObjectContentsWalker) error {
	if properties == nil {
		return status.Errorf(codes.InvalidArgument, "File %#v does not have any properties", pathTrace.GetUNIXString())
	}
	if contents := properties.Contents; contents != nil {
		switch level := contents.Level.(type) {
		case *model_filesystem_pb.FileContents_List_:
			index, err := model_core.GetIndexFromReferenceMessage(level.List.Reference.GetReference(), len(walkers))
			if err != nil {
				return util.StatusWrapf(err, "Invalid file contents list reference index for file %#v", pathTrace.GetUNIXString())
			}
			walkers[index] = &recomputingConcatenatedFileWalker{
				options:   o,
				reference: object.Contents.GetOutgoingReference(index),
				pathTrace: pathTrace,
			}
		case *model_filesystem_pb.FileContents_ChunkReference:
			index, err := model_core.GetIndexFromReferenceMessage(level.ChunkReference.Reference, len(walkers))
			if err != nil {
				return util.StatusWrapf(err, "Invalid chunk reference index for file %#v", pathTrace.GetUNIXString())
			}
			walkers[index] = &smallFileWalker{
				options:   o,
				reference: object.Contents.GetOutgoingReference(index),
				pathTrace: pathTrace,
				sizeBytes: uint32(contents.TotalSizeBytes),
			}
		case *model_filesystem_pb.FileContents_Hole:
		default:
			return status.Errorf(codes.InvalidArgument, "File %#v has an unknown file contents type", pathTrace.GetUNIXString())
		}
	}
	return nil
}

// capturedDirectoryWalker is an ObjectContentsWalker that is backed by
// a CreatedObjectTree corresponding to the top level of a directory's
// B-tree.
type capturedDirectoryWalker struct {
	options            *capturedDirectoryWalkerOptions
	object             *model_core.CreatedObjectTree
	decodingParameters []byte
	pathTrace          *path.Trace
}

// NewCapturedDirectoryWalker returns an implementation of
// ObjectContentsWalker that is capable of walking over a hierarchy of
// directory objects that were created using CreateDirectoryMerkleTree
// and captured using FileDiscardingDirectoryMerkleTreeCapturer. This
// makes it possible to upload such directory hierarchies to a storage
// server.
//
// These Merkle trees do not contain any file contents, but it is
// permitted for the storage server to request them. If that happens, we
// must reobtain them from the underlying file system. This is why the
// caller must provide a handle to the root directory on which the
// provided Merkle tree is based.
func NewCapturedDirectoryWalker(directoryParameters *DirectoryAccessParameters, fileParameters *FileCreationParameters, rootDirectory CapturedDirectory, rootObject *model_core.CreatedObjectTree, decodingParameters []byte) dag.ObjectContentsWalker {
	return &capturedDirectoryWalker{
		options: &capturedDirectoryWalkerOptions{
			entriesParser: model_parser.NewChainedObjectParser(
				model_parser.NewEncodedObjectParser[object.LocalReference](
					directoryParameters.GetEncoder(),
				),
				model_parser.NewProtoListObjectParser[object.LocalReference, model_filesystem_pb.DirectoryEntry](),
			),
			fileParameters: fileParameters,
			rootDirectory:  rootDirectory,
		},
		object:             rootObject,
		decodingParameters: decodingParameters,
	}
}

func (o *capturedDirectoryWalkerOptions) parseEntries(object *model_core.CreatedObjectTree, decodingParameters []byte, pathTrace *path.Trace) ([]*model_filesystem_pb.DirectoryEntry, error) {
	entries, err := o.entriesParser.ParseObject(model_core.NewRawMessage(object.Contents), decodingParameters)
	if err != nil {
		return nil, util.StatusWrapf(err, "Failed to decode entries of directory %#v", pathTrace.GetUNIXString())
	}
	return entries.Message, nil
}

func (w *capturedDirectoryWalker) GetContents(ctx context.Context) (*object.Contents, []dag.ObjectContentsWalker, error) {
	entries, err := w.options.parseEntries(w.object, w.decodingParameters, w.pathTrace)
	if err != nil {
		return nil, nil, err
	}
	walkers := make([]dag.ObjectContentsWalker, w.object.GetDegree())
	if err := w.options.gatherWalkersForDirectory(w.object, entries, w.pathTrace, walkers); err != nil {
		return nil, nil, err
	}
	return w.object.Contents, walkers, nil
}

func (capturedDirectoryWalker) Discard() {}

// collectDirectoryNames returns the names of all subdirectories of a
// directory, in the order in which their contents are stored in the
// first section of the directory's B-tree. As all objects are
// available in memory, parts of the B-tree stored in other objects can
// be parsed directly.
func (o *capturedDirectoryWalkerOptions) collectDirectoryNames(object *model_core.CreatedObjectTree, entries []*model_filesystem_pb.DirectoryEntry, pathTrace *path.Trace, names []string) ([]string, error) {
	for _, entry := range entries {
		switch level := entry.Level.(type) {
		case *model_filesystem_pb.DirectoryEntry_Parent_:
			// Only traverse into parts of the B-tree that
			// contain subdirectory nodes.
			if level.Parent.DirectoryNodesCount > 0 {
				index, err := model_core.GetIndexFromReferenceMessage(level.Parent.Reference.GetReference(), object.GetDegree())
				if err != nil {
					return nil, util.StatusWrapf(err, "Invalid parent node reference index in directory %#v", pathTrace.GetUNIXString())
				}
				childObject := &object.Metadata[index]
				childEntries, err := o.parseEntries(childObject, level.Parent.Reference.DecodingParameters, pathTrace)
				if err != nil {
					return nil, err
				}
				names, err = o.collectDirectoryNames(childObject, childEntries, pathTrace, names)
				if err != nil {
					return nil, err
				}
			}
		case *model_filesystem_pb.DirectoryEntry_Node_:
			if level.Node.GetDirectory() != nil {
				names = append(names, level.Node.Name)
			}
		case *model_filesystem_pb.DirectoryEntry_DirectoryContents_:
		default:
			return nil, status.Errorf(codes.InvalidArgument, "Invalid entry level type in directory %#v", pathTrace.GetUNIXString())
		}
	}
	return names, nil
}

// gatherWalkersForDirectory creates ObjectContentsWalkers for all
// objects referenced by the top level of a directory's B-tree.
func (o *capturedDirectoryWalkerOptions) gatherWalkersForDirectory(object *model_core.CreatedObjectTree, entries []*model_filesystem_pb.DirectoryEntry, pathTrace *path.Trace, walkers []dag.ObjectContentsWalker) error {
	names, err := o.collectDirectoryNames(object, entries, pathTrace, nil)
	if err != nil {
		return err
	}
	return o.gatherWalkersForEntries(object, entries, pathTrace, names, walkers)
}

// gatherWalkersForEntries creates ObjectContentsWalkers for all objects
// referenced by a list of entries that are part of a directory's
// B-tree. As directory_contents elements do not contain the names of
// the subdirectories, these need to be provided separately. They are
// needed to be able to reopen files.
func (o *capturedDirectoryWalkerOptions) gatherWalkersForEntries(object *model_core.CreatedObjectTree, entries []*model_filesystem_pb.DirectoryEntry, pathTrace *path.Trace, names []string, walkers []dag.ObjectContentsWalker) error {
	for _, entry := range entries {
		switch level := entry.Level.(type) {
		case *model_filesystem_pb.DirectoryEntry_Parent_:
			contentsCount := uint64(level.Parent.DirectoryContentsCount)
			if uint64(len(names)) < contentsCount {
				return status.Errorf(codes.InvalidArgument, "Directory %#v contains more subdirectory contents than names", pathTrace.GetUNIXString())
			}
			index, err := model_core.GetIndexFromReferenceMessage(level.Parent.Reference.GetReference(), len(walkers))
			if err != nil {
				return util.StatusWrapf(err, "Invalid parent node reference index in directory %#v", pathTrace.GetUNIXString())
			}
			walkers[index] = &capturedEntriesWalker{
				options:            o,
				object:             &object.Metadata[index],
				decodingParameters: level.Parent.Reference.DecodingParameters,
				pathTrace:          pathTrace,
				names:              names[:contentsCount],
			}
			names = names[contentsCount:]
		case *model_filesystem_pb.DirectoryEntry_Node_:
			if file, ok := level.Node.Kind.(*model_filesystem_pb.DirectoryEntry_Node_File); ok {
				name, ok := path.NewComponent(level.Node.Name)
				if !ok {
					return status.Errorf(codes.InvalidArgument, "Directory %#v contains a file with invalid name %#v", pathTrace.GetUNIXString(), level.Node.Name)
				}
				if err := o.gatherWalkersForFile(object, file.File, pathTrace.Append(name), walkers); err != nil {
					return err
				}
			}
		case *model_filesystem_pb.DirectoryEntry_DirectoryContents_:
			if len(names) < 1 {
				return status.Errorf(codes.InvalidArgument, "Directory %#v contains more subdirectory contents than names", pathTrace.GetUNIXString())
			}
			name, ok := path.NewComponent(names[0])
			if !ok {
				return status.Errorf(codes.InvalidArgument, "Directory %#v contains a subdirectory with invalid name %#v", pathTrace.GetUNIXString(), names[0])
			}
			names = names[1:]
			if err := o.gatherWalkersForDirectory(object, level.DirectoryContents.Entries, pathTrace.Append(name), walkers); err != nil {
				return err
			}
		default:
			return status.Errorf(codes.InvalidArgument, "Invalid entry level type in directory %#v", pathTrace.GetUNIXString())
		}
	}
	if len(names) > 0 {
		return status.Errorf(codes.InvalidArgument, "Directory %#v contains fewer subdirectory contents than names", pathTrace.GetUNIXString())
	}
	return nil
}

// capturedEntriesWalker is an ObjectContentsWalker that is backed by a
// CreatedObjectTree corresponding to a list of DirectoryEntry messages,
// which is part of a directory's B-tree.
type capturedEntriesWalker struct {
	options            *capturedDirectoryWalkerOptions
	object             *model_core.CreatedObjectTree
	decodingParameters []byte
	pathTrace          *path.Trace
	names              []string
}

func (w *capturedEntriesWalker) GetContents(ctx context.Context) (*object.Contents, []dag.ObjectContentsWalker, error) {
	entries, err := w.options.parseEntries(w.object, w.decodingParameters, w.pathTrace)
	if err != nil {
		return nil, nil, err
	}
	walkers := make([]dag.ObjectContentsWalker, w.object.GetDegree())
	if err := w.options.gatherWalkersForEntries(w.object, entries, w.pathTrace, w.names, walkers); err != nil {
		return nil, nil, err
	}
	return w.object.Contents, walkers, nil
}

func (capturedEntriesWalker) Discard() {}

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

	options := newComputedConcatenatedFileObjectOptions(w.options.fileParameters, w.pathTrace, r)
	options.referenceCount.Store(1)
	wComputed := &computedConcatenatedFileWalker{
		options:            options,
		object:             &objects[0],
		decodingParameters: fileContents.Message.Level.(*model_filesystem_pb.FileContents_List_).List.Reference.GetDecodingParameters(),
	}
	return wComputed.GetContents(ctx)
}

// NewCapturedFileWalker returns an implementation of
// ObjectContentsWalker that is capable of walking over a hierarchy of
// file contents list and chunk objects that were created using
// CreateFileMerkleTree and captured using
// ChunkDiscardingFileMerkleTreeCapturer. This makes it possible to
// upload such files to a storage server.
//
// These Merkle trees only contain file contents lists and no chunks,
// but it is permitted for the storage server to request chunks. If that
// happens, we must reobtain them from the underlying file system. This
// is why the caller must provide a handle to the file on which the
// provided Merkle tree is based.
func NewCapturedFileWalker(fileParameters *FileCreationParameters, r filesystem.FileReader, fileReference object.LocalReference, fileSizeBytes uint64, fileObject *model_core.CreatedObjectTree, decodingParameters []byte) dag.ObjectContentsWalker {
	options := newComputedConcatenatedFileObjectOptions(fileParameters, nil, r)
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
	fileParameters         *FileCreationParameters
	fileContentsListParser model_parser.MessageObjectParser[object.LocalReference, []*model_filesystem_pb.FileContents]
	pathTrace              *path.Trace
	referenceCount         atomic.Uint64
	file                   filesystem.FileReader
}

func newComputedConcatenatedFileObjectOptions(fileParameters *FileCreationParameters, pathTrace *path.Trace, file filesystem.FileReader) *computedConcatenatedFileObjectOptions {
	options := &computedConcatenatedFileObjectOptions{
		fileParameters: fileParameters,
		fileContentsListParser: model_parser.NewChainedObjectParser(
			model_parser.NewEncodedObjectParser[object.LocalReference](
				fileParameters.GetFileContentsListEncoder(),
			),
			model_parser.NewProtoListObjectParser[object.LocalReference, model_filesystem_pb.FileContents](),
		),
		pathTrace: pathTrace,
		file:      file,
	}
	options.referenceCount.Store(1)
	return options
}

type computedConcatenatedFileWalker struct {
	options            *computedConcatenatedFileObjectOptions
	object             *model_core.CreatedObjectTree
	decodingParameters []byte
	offsetBytes        uint64
}

func (w *computedConcatenatedFileWalker) GetContents(ctx context.Context) (*object.Contents, []dag.ObjectContentsWalker, error) {
	fileContentsList, err := w.options.fileContentsListParser.ParseObject(model_core.NewRawMessage(w.object.Contents), w.decodingParameters)
	if err != nil {
		return nil, nil, util.StatusWrapf(err, "Failed to decode file contents list for file %#v at offset %d", w.options.pathTrace.GetUNIXString(), w.offsetBytes)
	}
	walkers := make([]dag.ObjectContentsWalker, w.object.GetDegree())
	offsetBytes := w.offsetBytes
	for _, part := range fileContentsList.Message {
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
