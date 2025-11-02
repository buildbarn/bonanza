package filesystem

import (
	"context"
	"io"

	model_core "bonanza.build/pkg/model/core"
	model_parser "bonanza.build/pkg/model/parser"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type FileReader[TReference object.BasicReference] struct {
	fileContentsListReader model_parser.ParsedObjectReader[model_core.Decodable[TReference], FileContentsList[TReference]]
	fileChunkReader        model_parser.ParsedObjectReader[model_core.Decodable[TReference], []byte]
}

func NewFileReader[TReference object.BasicReference](
	fileContentsListReader model_parser.ParsedObjectReader[model_core.Decodable[TReference], FileContentsList[TReference]],
	fileChunkReader model_parser.ParsedObjectReader[model_core.Decodable[TReference], []byte],
) *FileReader[TReference] {
	return &FileReader[TReference]{
		fileContentsListReader: fileContentsListReader,
		fileChunkReader:        fileChunkReader,
	}
}

func (fr *FileReader[TReference]) GetDecodingParametersSizeBytes(isFileContentsList bool) int {
	if isFileContentsList {
		return fr.fileContentsListReader.GetDecodingParametersSizeBytes()
	}
	return fr.fileChunkReader.GetDecodingParametersSizeBytes()
}

func (fr *FileReader[TReference]) FileReadAll(ctx context.Context, fileContents FileContentsEntry[TReference], maximumSizeBytes uint64) ([]byte, error) {
	endBytes := fileContents.GetEndBytes()
	if endBytes > maximumSizeBytes {
		return nil, status.Errorf(codes.InvalidArgument, "File is %d bytes in size, which exceeds the permitted maximum of %d bytes", endBytes, maximumSizeBytes)
	}
	p := make([]byte, endBytes)
	if _, err := fr.FileReadAt(ctx, fileContents, p, 0); err != nil {
		return nil, err
	}
	return p, nil
}

func (fr *FileReader[TReference]) readNextChunkOrHole(ctx context.Context, fileContentsIterator *FileContentsIterator[TReference]) ([]byte, uint64, error) {
	for {
		partReference, partOffsetBytes, partSizeBytes := fileContentsIterator.GetCurrentPart()
		if partReference == nil {
			// Reached a hole.
			return nil, partSizeBytes - partOffsetBytes, nil
		}

		if partReference.Value.GetDegree() == 0 {
			// Reached a chunk.
			chunk, err := fr.fileChunkReader.ReadParsedObject(ctx, *partReference)
			if err != nil {
				return nil, 0, util.StatusWrapf(err, "Failed to read chunk with reference %s", model_core.DecodableLocalReferenceToString(*partReference))
			}
			if uint64(len(chunk)) != partSizeBytes {
				return nil, 0, status.Errorf(codes.InvalidArgument, "Chunk with reference %s is %d bytes in size, while %d bytes were expected", model_core.DecodableLocalReferenceToString(*partReference), len(chunk), partSizeBytes)
			}
			fileContentsIterator.ToNextPart()
			return chunk[partOffsetBytes:], 0, nil
		}

		// We need to push one or more file contents lists onto
		// the stack to reach a chunk.
		fileContentsList, err := fr.fileContentsListReader.ReadParsedObject(ctx, *partReference)
		if err != nil {
			return nil, 0, util.StatusWrapf(err, "Failed to read file contents list with reference %s", model_core.DecodableLocalReferenceToString(*partReference))
		}
		if err := fileContentsIterator.PushFileContentsList(fileContentsList); err != nil {
			return nil, 0, util.StatusWrapf(err, "Invalid file contents list with reference %s", model_core.DecodableLocalReferenceToString(*partReference))
		}
	}
}

func (fr *FileReader[TReference]) FileReadAt(ctx context.Context, fileContents FileContentsEntry[TReference], p []byte, offsetBytes uint64) (int, error) {
	// TODO: Any chance we can use parallelism here to read multiple chunks?
	fileContentsIterator := NewFileContentsIterator(fileContents, offsetBytes)
	nRead := 0
	for len(p) > 0 {
		chunk, holeSizeBytes, err := fr.readNextChunkOrHole(ctx, &fileContentsIterator)
		if err != nil {
			return nRead, err
		}

		nChunk := copy(p, chunk)
		p = p[nChunk:]
		nRead += nChunk

		nHole := int(min(holeSizeBytes, uint64(len(p))))
		clear(p[:nHole])
		p = p[nHole:]
		nRead += nHole
	}
	return nRead, nil
}

func (fr *FileReader[TReference]) FileOpenRead(ctx context.Context, fileContents FileContentsEntry[TReference], offsetBytes uint64) *SequentialFileReader[TReference] {
	return &SequentialFileReader[TReference]{
		context:              ctx,
		fileReader:           fr,
		fileContentsIterator: NewFileContentsIterator(fileContents, offsetBytes),
		offsetBytes:          offsetBytes,
		sizeBytes:            fileContents.GetEndBytes(),
	}
}

func (fr *FileReader[TReference]) FileOpenReadAt(ctx context.Context, fileContents FileContentsEntry[TReference]) io.ReaderAt {
	return &randomAccessFileReader[TReference]{
		context:      ctx,
		fileReader:   fr,
		fileContents: fileContents,
	}
}

type SequentialFileReader[TReference object.BasicReference] struct {
	context              context.Context
	fileReader           *FileReader[TReference]
	fileContentsIterator FileContentsIterator[TReference]
	chunk                []byte
	holeSizeBytes        uint64
	offsetBytes          uint64
	sizeBytes            uint64
}

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

		chunk, holeSizeBytes, err := r.fileReader.readNextChunkOrHole(r.context, &r.fileContentsIterator)
		if err != nil {
			return nRead, err
		}
		r.chunk = chunk
		r.holeSizeBytes = holeSizeBytes
		r.offsetBytes += holeSizeBytes + uint64(len(chunk))
	}
}

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
	FileContentsListReaderForTesting = model_parser.ParsedObjectReader[model_core.Decodable[object.LocalReference], FileContentsList[object.LocalReference]]
	FileChunkReaderForTesting        = model_parser.ParsedObjectReader[model_core.Decodable[object.LocalReference], []byte]
)
