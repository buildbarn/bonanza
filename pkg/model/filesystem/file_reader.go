package filesystem

import (
	"context"
	"io"

	model_core "bonanza.build/pkg/model/core"
	model_parser "bonanza.build/pkg/model/parser"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// FileReader is a helper type for reading the contents of files that
// have been written to storage, using traditional interfaces like
// io.Reader, io.ReaderAt, io.Writer, etc.
type FileReader[TReference object.BasicReference] struct {
	fileContentsListReader     model_parser.DecodingObjectReader[TReference, FileContentsList[TReference]]
	fileChunkReader            model_parser.DecodingObjectReader[TReference, []byte]
	fileChunkReaderConcurrency *semaphore.Weighted
}

// NewFileReader creates a FileReader that uses the specified object
// readers for chunks and file contents lists.
func NewFileReader[TReference object.BasicReference](
	fileContentsListReader model_parser.DecodingObjectReader[TReference, FileContentsList[TReference]],
	fileChunkReader model_parser.DecodingObjectReader[TReference, []byte],
	fileChunkReaderConcurrency *semaphore.Weighted,
) *FileReader[TReference] {
	return &FileReader[TReference]{
		fileContentsListReader:     fileContentsListReader,
		fileChunkReader:            fileChunkReader,
		fileChunkReaderConcurrency: fileChunkReaderConcurrency,
	}
}

// GetDecodingParametersSizeBytes returns the expected size of the
// decoding parameters that a reference to a file contents list or
// chunks should have.
func (fr *FileReader[TReference]) GetDecodingParametersSizeBytes(isFileContentsList bool) int {
	if isFileContentsList {
		return fr.fileContentsListReader.GetDecodingParametersSizeBytes()
	}
	return fr.fileChunkReader.GetDecodingParametersSizeBytes()
}

func (fr *FileReader[TReference]) getNextChunkOrHole(ctx context.Context, fileContentsIterator *FileContentsIterator[TReference]) (*model_core.Decodable[TReference], uint64, uint64, error) {
	for {
		partReference, partOffsetBytes, partSizeBytes := fileContentsIterator.GetCurrentPart()
		if partReference == nil || partReference.Value.GetDegree() == 0 {
			// Reached a hole or chunk.
			fileContentsIterator.ToNextPart()
			return partReference, partOffsetBytes, partSizeBytes, nil
		}

		// We need to push one or more file contents lists onto
		// the stack to reach a chunk.
		fileContentsList, err := fr.fileContentsListReader.ReadObject(ctx, *partReference)
		if err != nil {
			return nil, 0, 0, util.StatusWrapf(err, "Failed to read file contents list with reference %s", model_core.DecodableLocalReferenceToString(*partReference))
		}
		if err := fileContentsIterator.PushFileContentsList(fileContentsList); err != nil {
			return nil, 0, 0, util.StatusWrapf(err, "Invalid file contents list with reference %s", model_core.DecodableLocalReferenceToString(*partReference))
		}
	}
}

func (fr *FileReader[TReference]) readChunk(ctx context.Context, partReference model_core.Decodable[TReference], partOffsetBytes, partSizeBytes uint64) ([]byte, error) {
	chunk, err := fr.fileChunkReader.ReadObject(ctx, partReference)
	if err != nil {
		return nil, util.StatusWrapf(err, "Failed to read chunk with reference %s", model_core.DecodableLocalReferenceToString(partReference))
	}
	if uint64(len(chunk)) != partSizeBytes {
		return nil, status.Errorf(codes.InvalidArgument, "Chunk with reference %s is %d bytes in size, while %d bytes were expected", model_core.DecodableLocalReferenceToString(partReference), len(chunk), partSizeBytes)
	}
	return chunk[partOffsetBytes:], nil
}

// chunkAndHoleWriter is used by FileReader.walkChunksAndHoles() to
// report chunks and holes contained in a file. Due to contents of
// chunks being loaded in parallel, these methods can be called in
// random order.
type chunkAndHoleWriter interface {
	WriteChunk(offsetBytes uint64, data []byte) error
	WriteHole(offsetBytes, sizeBytes uint64) error
}

func (fr *FileReader[TReference]) walkChunksAndHoles(
	ctx context.Context,
	fileContentsIterator *FileContentsIterator[TReference],
	outputSizeBytes uint64,
	writer chunkAndHoleWriter,
) error {
	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error {
		outputOffsetBytes := uint64(0)
		for outputOffsetBytes < outputSizeBytes {
			partReference, partOffsetBytes, partSizeBytes, err := fr.getNextChunkOrHole(ctx, fileContentsIterator)
			if err != nil {
				return err
			}

			readSizeBytes := min(outputSizeBytes-outputOffsetBytes, partSizeBytes-partOffsetBytes)
			if partReference == nil {
				// Reached a hole.
				if err := writer.WriteHole(outputOffsetBytes, readSizeBytes); err != nil {
					return err
				}
			} else {
				// Reached a chunk.
				currentOutputOffsetBytes := outputOffsetBytes
				if err := util.AcquireSemaphore(groupCtx, fr.fileChunkReaderConcurrency, 1); err != nil {
					return err
				}
				group.Go(func() error {
					defer fr.fileChunkReaderConcurrency.Release(1)

					chunk, err := fr.readChunk(groupCtx, *partReference, partOffsetBytes, partSizeBytes)
					if err != nil {
						return err
					}
					return writer.WriteChunk(currentOutputOffsetBytes, chunk[:readSizeBytes])
				})
			}
			outputOffsetBytes += readSizeBytes
		}
		return nil
	})
	return group.Wait()
}

// byteSliceChunkAndHoleWriter is an implementation of
// chunkAndHoleWriter that writes the contents of chunks and holes into
// a byte slice. It can be used to implement things like io.ReaderAt and
// io.ReadAll().
type byteSliceChunkAndHoleWriter struct {
	output []byte
}

func (w byteSliceChunkAndHoleWriter) WriteChunk(offsetBytes uint64, data []byte) error {
	copy(w.output[offsetBytes:], data)
	return nil
}

func (w byteSliceChunkAndHoleWriter) WriteHole(offsetBytes, sizeBytes uint64) error {
	clear(w.output[offsetBytes:][:sizeBytes])
	return nil
}

// FileReadAll reads the entire contents of a file that has been written
// to storage, returning the contents as a byte slice.
func (fr *FileReader[TReference]) FileReadAll(ctx context.Context, fileContents FileContentsEntry[TReference], maximumSizeBytes uint64) ([]byte, error) {
	endBytes := fileContents.GetEndBytes()
	if endBytes > maximumSizeBytes {
		return nil, status.Errorf(codes.InvalidArgument, "File is %d bytes in size, which exceeds the permitted maximum of %d bytes", endBytes, maximumSizeBytes)
	}
	p := make([]byte, endBytes)
	fileContentsIterator := NewFileContentsIterator(fileContents, 0)
	if err := fr.walkChunksAndHoles(
		ctx,
		&fileContentsIterator,
		endBytes,
		byteSliceChunkAndHoleWriter{
			output: p,
		},
	); err != nil {
		return nil, err
	}
	return p, nil
}

// FileReadAt performs a single read of a portion of a file that has
// been written to storage. The provided offset and output array MUST
// reside within the boundaries of the file.
func (fr *FileReader[TReference]) FileReadAt(ctx context.Context, fileContents FileContentsEntry[TReference], p []byte, offsetBytes uint64) (int, error) {
	fileContentsIterator := NewFileContentsIterator(fileContents, offsetBytes)
	if err := fr.walkChunksAndHoles(
		ctx,
		&fileContentsIterator,
		uint64(len(p)),
		byteSliceChunkAndHoleWriter{
			output: p,
		},
	); err != nil {
		return 0, err
	}
	return len(p), nil
}

// FileOpenRead opens a file that has been written to storage, returning
// a handle that allows the file to be read sequentially.
func (fr *FileReader[TReference]) FileOpenRead(ctx context.Context, fileContents FileContentsEntry[TReference], offsetBytes uint64) *SequentialFileReader[TReference] {
	return &SequentialFileReader[TReference]{
		context:              ctx,
		fileReader:           fr,
		fileContentsIterator: NewFileContentsIterator(fileContents, offsetBytes),
		offsetBytes:          offsetBytes,
		sizeBytes:            fileContents.GetEndBytes(),
	}
}

// FileOpenReadAt opens a file that has been written to storage,
// returning an io.ReaderAt so that the file can be read at random.
func (fr *FileReader[TReference]) FileOpenReadAt(ctx context.Context, fileContents FileContentsEntry[TReference]) io.ReaderAt {
	return &randomAccessFileReader[TReference]{
		context:      ctx,
		fileReader:   fr,
		fileContents: fileContents,
	}
}

type fileChunkAndHoleWriter struct {
	file io.WriterAt
}

func (w fileChunkAndHoleWriter) WriteChunk(offsetBytes uint64, data []byte) error {
	_, err := w.file.WriteAt(data, int64(offsetBytes))
	return err
}

func (fileChunkAndHoleWriter) WriteHole(offsetBytes, sizeBytes uint64) error {
	return nil
}

// FileWriteTo writes the contents of a file residing in the Object
// Store to a local file. It is assumed that the file already has the
// desired size and consists of a single hole.
func (fr *FileReader[TReference]) FileWriteTo(ctx context.Context, fileContents FileContentsEntry[TReference], file io.WriterAt) error {
	fileContentsIterator := NewFileContentsIterator(fileContents, 0)
	return fr.walkChunksAndHoles(
		ctx,
		&fileContentsIterator,
		fileContents.GetEndBytes(),
		fileChunkAndHoleWriter{
			file: file,
		},
	)
}

// SequentialFileReader can be used to sequentially read data from a
// file that has been written to storage. SequentialFileReader buffers
// the last read chunk of data, meaning that small reads will not
// trigger redundant reads against the underlying storage backend.
type SequentialFileReader[TReference object.BasicReference] struct {
	context              context.Context
	fileReader           *FileReader[TReference]
	fileContentsIterator FileContentsIterator[TReference]
	chunk                []byte
	holeSizeBytes        uint64
	offsetBytes          uint64
	sizeBytes            uint64
}

var (
	_ io.Reader     = (*SequentialFileReader[object.LocalReference])(nil)
	_ io.ByteReader = (*SequentialFileReader[object.LocalReference])(nil)
)

// Read data from a file that has been written to the Object Store.
func (r *SequentialFileReader[TReference]) Read(p []byte) (int, error) {
	nRead := 0
	for {
		// Copy data from a previously read chunk.
		nChunk := copy(p, r.chunk)
		p = p[nChunk:]
		r.chunk = r.chunk[nChunk:]
		nRead += nChunk

		// Clear data from a previously observed hole.
		nHole := int(min(r.holeSizeBytes, uint64(len(p))))
		clear(p[:nHole])
		p = p[nHole:]
		r.holeSizeBytes -= uint64(nHole)
		nRead += nHole

		// Read the next chunk if we still have space in the
		// buffer or are not at end of file.
		if len(p) == 0 {
			return nRead, nil
		}
		if r.offsetBytes >= r.sizeBytes {
			return nRead, io.EOF
		}

		partReference, partOffsetBytes, partSizeBytes, err := r.fileReader.getNextChunkOrHole(r.context, &r.fileContentsIterator)
		if err != nil {
			return nRead, err
		}
		if partReference == nil {
			r.holeSizeBytes = partSizeBytes - partOffsetBytes
			r.offsetBytes += r.holeSizeBytes
		} else {
			chunk, err := r.fileReader.readChunk(r.context, *partReference, partOffsetBytes, partSizeBytes)
			if err != nil {
				return nRead, err
			}
			r.chunk = chunk
			r.offsetBytes += uint64(len(chunk))
		}
	}
}

// ReadByte reads a single byte of data from a file that has been
// written to the Object Store.
func (r *SequentialFileReader[TReference]) ReadByte() (byte, error) {
	var b [1]byte
	if n, err := r.Read(b[:]); n == 0 {
		return 0, err
	}
	return b[0], nil
}

type randomAccessFileReader[TReference object.BasicReference] struct {
	context      context.Context
	fileReader   *FileReader[TReference]
	fileContents FileContentsEntry[TReference]
}

func (r *randomAccessFileReader[TReference]) ReadAt(p []byte, offsetBytes int64) (int, error) {
	// Limit the read operation to the size of the file.
	endBytes := r.fileContents.GetEndBytes()
	if uint64(offsetBytes) > endBytes {
		return 0, io.EOF
	}
	remainingBytes := endBytes - uint64(offsetBytes)
	if uint64(len(p)) > remainingBytes {
		p = p[:remainingBytes]
	}

	return r.fileReader.FileReadAt(r.context, r.fileContents, p, uint64(offsetBytes))
}

type (
	// FileContentsListReaderForTesting is used for generating mocks
	// that are used by FileReader's unit tests.
	FileContentsListReaderForTesting = model_parser.DecodingObjectReader[object.LocalReference, FileContentsList[object.LocalReference]]
	// FileChunkReaderForTesting is used for generating mocks that
	// are used by FileReader's unit tests.
	FileChunkReaderForTesting = model_parser.DecodingObjectReader[object.LocalReference, []byte]
)
