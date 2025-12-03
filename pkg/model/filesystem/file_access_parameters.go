package filesystem

import (
	model_encoding "bonanza.build/pkg/model/encoding"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// FileAccessParameters contains parameters that were used when creating
// Merkle trees of files that should also be applied when attempting to
// access its contents afterwards. Parameters include whether files were
// compressed or encrypted.
type FileAccessParameters struct {
	chunkEncoder            model_encoding.DeterministicBinaryEncoder
	fileContentsListEncoder model_encoding.DeterministicBinaryEncoder
}

// NewFileAccessParametersFromProto creates an instance of
// FileAccessParameters that matches the configuration stored in a
// Protobuf message. This, for example, permits a server to access files
// that were uploaded by a client.
func NewFileAccessParametersFromProto(m *model_filesystem_pb.FileAccessParameters, referenceFormat object.ReferenceFormat) (*FileAccessParameters, error) {
	if m == nil {
		return nil, status.Error(codes.InvalidArgument, "No file access parameters provided")
	}

	maximumObjectSizeBytes := uint32(referenceFormat.GetMaximumObjectSizeBytes())
	chunkEncoder, err := model_encoding.NewDeterministicBinaryEncoderFromProto(m.ChunkEncoders, maximumObjectSizeBytes)
	if err != nil {
		return nil, util.StatusWrap(err, "Invalid chunk encoder")
	}
	fileContentsListEncoder, err := model_encoding.NewDeterministicBinaryEncoderFromProto(m.FileContentsListEncoders, maximumObjectSizeBytes)
	if err != nil {
		return nil, util.StatusWrap(err, "Invalid file contents list encoder")
	}

	return &FileAccessParameters{
		chunkEncoder:            chunkEncoder,
		fileContentsListEncoder: fileContentsListEncoder,
	}, nil
}

// GetChunkEncoder returns the encoder that was used to create chunks of
// a file. This can be used to subsequently decode the chunks.
func (p *FileAccessParameters) GetChunkEncoder() model_encoding.DeterministicBinaryEncoder {
	return p.chunkEncoder
}

// GetFileContentsListEncoder returns the encoder that was used to
// create file contents lists of large files. This can be used to
// subsequently decode these lists.
func (p *FileAccessParameters) GetFileContentsListEncoder() model_encoding.DeterministicBinaryEncoder {
	return p.fileContentsListEncoder
}
