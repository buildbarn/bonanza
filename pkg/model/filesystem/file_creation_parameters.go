package filesystem

import (
	model_core "bonanza.build/pkg/model/core"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	"bonanza.build/pkg/storage/object"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// FileCreationParameters contains parameters such as encoders, and
// minimum/maximum object sizes that need to be considered when creating
// Merkle trees of files.
type FileCreationParameters struct {
	*FileAccessParameters
	referenceFormat                  object.ReferenceFormat
	chunkMinimumSizeBytes            int
	chunkMaximumSizeBytes            int
	fileContentsListMinimumSizeBytes int
	fileContentsListMaximumSizeBytes int
}

// NewFileCreationParametersFromProto converts the file creation
// parameters that are stored in a Protobuf message to its native
// counterpart. It also validates that the provided sizes are in bounds.
func NewFileCreationParametersFromProto(m *model_filesystem_pb.FileCreationParameters, referenceFormat object.ReferenceFormat) (*FileCreationParameters, error) {
	if m == nil {
		return nil, status.Error(codes.InvalidArgument, "No file creation parameters provided")
	}

	accessParameters, err := NewFileAccessParametersFromProto(m.Access, referenceFormat)
	if err != nil {
		return nil, err
	}

	// Ensure that the provided object size limits are within
	// bounds. Prevent creating objects that are tiny, as that
	// increases memory usage and running times of the content
	// defined chunking and B-tree algorithms.
	maximumObjectSizeBytes := uint32(referenceFormat.GetMaximumObjectSizeBytes())
	if limit := uint32(1024); m.ChunkMinimumSizeBytes < limit {
		return nil, status.Errorf(codes.InvalidArgument, "Minimum size of chunks is below %d bytes", limit)
	}
	if m.ChunkMaximumSizeBytes > maximumObjectSizeBytes {
		return nil, status.Errorf(codes.InvalidArgument, "Maximum size of chunks is above maximum object size of %d bytes", maximumObjectSizeBytes)
	}
	if uint64(m.ChunkMaximumSizeBytes) < 2*uint64(m.ChunkMinimumSizeBytes) {
		return nil, status.Error(codes.InvalidArgument, "Maximum size of chunks must be at least twice as large as the minimum")
	}

	if limit := uint32(1024); m.FileContentsListMinimumSizeBytes < limit {
		return nil, status.Errorf(codes.InvalidArgument, "Minimum size of file contents list is below %d bytes", limit)
	}
	if m.FileContentsListMaximumSizeBytes > maximumObjectSizeBytes {
		return nil, status.Errorf(codes.InvalidArgument, "Maximum size of file contents list is above maximum object size of %d bytes", maximumObjectSizeBytes)
	}
	if m.FileContentsListMaximumSizeBytes < m.FileContentsListMinimumSizeBytes {
		return nil, status.Error(codes.InvalidArgument, "Maximum size of file contents list must be at least as large as the minimum")
	}

	return &FileCreationParameters{
		FileAccessParameters:             accessParameters,
		referenceFormat:                  referenceFormat,
		chunkMinimumSizeBytes:            int(m.ChunkMinimumSizeBytes),
		chunkMaximumSizeBytes:            int(m.ChunkMaximumSizeBytes),
		fileContentsListMinimumSizeBytes: int(m.FileContentsListMinimumSizeBytes),
		fileContentsListMaximumSizeBytes: int(m.FileContentsListMaximumSizeBytes),
	}, nil
}

// EncodeChunk encodes the data of a small file, or a region of a large
// file into an object that can be written to storage.
func (p *FileCreationParameters) EncodeChunk(data []byte) (model_core.Decodable[*object.Contents], error) {
	encodedChunk, decodingParameters, err := p.chunkEncoder.EncodeBinary(data)
	if err != nil {
		return model_core.Decodable[*object.Contents]{}, err
	}
	contents, err := p.referenceFormat.NewContents(nil, encodedChunk)
	if err != nil {
		return model_core.Decodable[*object.Contents]{}, err
	}
	return model_core.NewDecodable(contents, decodingParameters)
}
