package filesystem

import (
	"bufio"
	"bytes"
	"io"

	"bonanza.build/pkg/model/chunking"
	model_core "bonanza.build/pkg/model/core"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/buildbarn/go-cdc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// FileCreationParameters contains parameters such as encoders, and
// minimum object sizes that need to be considered when creating
// Merkle trees of files.
type FileCreationParameters struct {
	*FileAccessParameters
	referenceFormat            object.ReferenceFormat
	contentDefinedChunker      cdc.ContentDefinedChunker
	fileContentsListParameters chunking.Parameters
}

// NewFileCreationParametersFromProto converts the file creation
// parameters that are stored in a Protobuf message to its native
// counterpart. It also validates that the provided sizes are in bounds.
func NewFileCreationParametersFromProto(m *model_filesystem_pb.FileCreationParameters, referenceFormat object.ReferenceFormat, binaryEncoderFactory model_encoding.BinaryEncoderFactory) (*FileCreationParameters, error) {
	if m == nil {
		return nil, status.Error(codes.InvalidArgument, "No file creation parameters provided")
	}

	accessParameters, err := NewFileAccessParametersFromProto(m.GetAccess(), referenceFormat, binaryEncoderFactory)
	if err != nil {
		return nil, err
	}

	chunkParameters, err := chunking.NewParametersFromProto(m.GetChunkParameters(), referenceFormat)
	if err != nil {
		return nil, util.StatusWrap(err, "Invalid chunk parameters")
	}
	fileContentsListParameters, err := chunking.NewParametersFromProto(m.GetFileContentsListParameters(), referenceFormat)
	if err != nil {
		return nil, util.StatusWrap(err, "Invalid file contents list parameters")
	}

	return &FileCreationParameters{
		FileAccessParameters: accessParameters,
		referenceFormat:      referenceFormat,
		contentDefinedChunker: cdc.NewRepMaxContentDefinedChunker(
			cdc.NewSeededGearTable(m.GetChunkGearTableSeed()),
			chunkParameters.GetMinimumSizeBytes(),
			chunkParameters.GetHorizonSizeBytes(),
		),
		fileContentsListParameters: fileContentsListParameters,
	}, nil
}

// NewChunkingReader creates a buffered reader that can be provided to
// CreateFileMerkleTree(). Its buffer is large enough to perform
// chunking. As this buffer may be large, callers that create Merkle
// trees of many files sequentially should reuse the buffered reader by
// calling Reset() on it.
func (p *FileCreationParameters) NewChunkingReader(r io.Reader) *bufio.Reader {
	return bufio.NewReaderSize(
		r,
		max(
			p.referenceFormat.GetMaximumObjectSizeBytes(),
			p.contentDefinedChunker.GetMaximumPeekSizeBytes(),
		),
	)
}

// EncodeChunk encodes the data of a small file, or a region of a large
// file into an object that can be written to storage.
func (p *FileCreationParameters) EncodeChunk(data []byte) (model_core.Decodable[*object.Contents], error) {
	encodedChunk, decodingParameters, err := p.chunkEncoder.EncodeBinary(data)
	if err != nil {
		return model_core.Decodable[*object.Contents]{}, err
	}
	if len(encodedChunk) > 0 && len(data) > 0 && &encodedChunk[0] == &data[0] {
		// No encoders are configured, meaning the encoded
		// chunk refers to the data provided by the caller.
		// Callers like CreateFileMerkleTree() reuse that
		// buffer, while the resulting object may outlive this
		// call. Make a copy.
		encodedChunk = bytes.Clone(encodedChunk)
	}
	contents, err := p.referenceFormat.NewContents(nil, encodedChunk)
	if err != nil {
		return model_core.Decodable[*object.Contents]{}, err
	}
	return model_core.NewDecodable(contents, decodingParameters)
}
