package filesystem

import (
	model_core "bonanza.build/pkg/model/core"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_parser "bonanza.build/pkg/model/parser"
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

// DecodeFileContentsList extracts the FileContents list that is stored
// in an object backed by storage.
//
// TODO: Maybe we should simply throw out this method? It doesn't
// provide a lot of value.
func (p *FileAccessParameters) DecodeFileContentsList(contents *object.Contents, decodingParameters []byte) ([]*model_filesystem_pb.FileContents, error) {
	fileContentsList, err := model_parser.NewChainedObjectParser(
		model_parser.NewEncodedObjectParser[object.LocalReference](p.fileContentsListEncoder),
		model_parser.NewProtoListObjectParser[object.LocalReference, model_filesystem_pb.FileContents](),
	).ParseObject(
		model_core.NewMessage(contents.GetPayload(), object.OutgoingReferences[object.LocalReference](contents)),
		decodingParameters,
	)
	if err != nil {
		return nil, err
	}
	return fileContentsList.Message, nil
}

func (p *FileAccessParameters) GetChunkEncoder() model_encoding.DeterministicBinaryEncoder {
	return p.chunkEncoder
}

func (p *FileAccessParameters) GetFileContentsListEncoder() model_encoding.DeterministicBinaryEncoder {
	return p.fileContentsListEncoder
}
