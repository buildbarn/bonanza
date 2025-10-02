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
	if fileContents.EndBytes > maximumSizeBytes {
		return nil, status.Errorf(codes.InvalidArgument, "File is %d bytes in size, which exceeds the permitted maximum of %d bytes", fileContents.EndBytes, maximumSizeBytes)
	}
	p := make([]byte, fileContents.EndBytes)
	if _, err := fr.FileReadAt(ctx, fileContents, p, 0); err != nil {
		return nil, err
	}
	return p, nil
}

func (fr *FileReader[TReference]) readNextChunk(ctx context.Context, fileContentsIterator *FileContentsIterator[TReference]) ([]byte, error) {
	for {
		partReference, partOffsetBytes, partSizeBytes := fileContentsIterator.GetCurrentPart()
		if partReference.Value.GetDegree() == 0 {
			// Reached a chunk.
			chunk, err := fr.fileChunkReader.ReadParsedObject(ctx, partReference)
			if err != nil {
				return nil, util.StatusWrapf(err, "Failed to read chunk with reference %s", model_core.DecodableLocalReferenceToString(partReference))
			}
			if uint64(len(chunk)) != partSizeBytes {
				return nil, status.Errorf(codes.InvalidArgument, "Chunk with reference %s is %d bytes in size, while %d bytes were expected", model_core.DecodableLocalReferenceToString(partReference), len(chunk), partSizeBytes)
			}
			fileContentsIterator.ToNextPart()
			return chunk[partOffsetBytes:], nil
		}

		// We need to push one or more file contents lists onto
		// the stack to reach a chunk.
		fileContentsList, err := fr.fileContentsListReader.ReadParsedObject(ctx, partReference)
		if err != nil {
			return nil, util.StatusWrapf(err, "Failed to read file contents list with reference %s", model_core.DecodableLocalReferenceToString(partReference))
		}
		if err := fileContentsIterator.PushFileContentsList(fileContentsList); err != nil {
			return nil, util.StatusWrapf(err, "Invalid file contents list with reference %s", model_core.DecodableLocalReferenceToString(partReference))
		}
	}
}

func (fr *FileReader[TReference]) FileReadAt(ctx context.Context, fileContents FileContentsEntry[TReference], p []byte, offsetBytes uint64) (int, error) {
	// TODO: Any chance we can use parallelism here to read multiple chunks?
	fileContentsIterator := NewFileContentsIterator(fileContents, offsetBytes)
	nRead := 0
	for len(p) > 0 {
		chunk, err := fr.readNextChunk(ctx, &fileContentsIterator)
		if err != nil {
			return nRead, err
		}
		n := copy(p, chunk)
		p = p[n:]
		nRead += n
	}
	return nRead, nil
}

func (fr *FileReader[TReference]) FileOpenRead(ctx context.Context, fileContents FileContentsEntry[TReference], offsetBytes uint64) *SequentialFileReader[TReference] {
	return &SequentialFileReader[TReference]{
		context:              ctx,
		fileReader:           fr,
		fileContentsIterator: NewFileContentsIterator(fileContents, offsetBytes),
		offsetBytes:          offsetBytes,
		sizeBytes:            fileContents.EndBytes,
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
	offsetBytes          uint64
	sizeBytes            uint64
}

func (r *SequentialFileReader[TReference]) Read(p []byte) (int, error) {
	nRead := 0
	for {
		// Copy data from a previously read chunk.
		n := copy(p, r.chunk)
		p = p[n:]
		r.chunk = r.chunk[n:]
		nRead += n
		if len(p) == 0 {
			return nRead, nil
		}

		// Read the next chunk if we're not at end of file.
		if r.offsetBytes >= r.sizeBytes {
			return nRead, io.EOF
		}
		chunk, err := r.fileReader.readNextChunk(r.context, &r.fileContentsIterator)
		if err != nil {
			return nRead, err
		}
		r.chunk = chunk
		r.offsetBytes += uint64(len(chunk))
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
	if uint64(offsetBytes) > r.fileContents.EndBytes {
		return 0, io.EOF
	}
	remainingBytes := r.fileContents.EndBytes - uint64(offsetBytes)
	if uint64(len(p)) > remainingBytes {
		p = p[:remainingBytes]
	}

	return r.fileReader.FileReadAt(r.context, r.fileContents, p, uint64(offsetBytes))
}

type (
	FileContentsListReaderForTesting = model_parser.ParsedObjectReader[model_core.Decodable[object.LocalReference], FileContentsList[object.LocalReference]]
	FileChunkReaderForTesting        = model_parser.ParsedObjectReader[model_core.Decodable[object.LocalReference], []byte]
)
