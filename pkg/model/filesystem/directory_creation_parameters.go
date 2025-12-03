package filesystem

import (
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	"bonanza.build/pkg/storage/object"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// DirectoryCreationParameters contains parameters such as encoders, and
// maximum object sizes that need to be considered when creating Merkle
// trees of directories.
type DirectoryCreationParameters struct {
	*DirectoryAccessParameters
	referenceFormat           object.ReferenceFormat
	directoryMaximumSizeBytes int
}

// NewDirectoryCreationParametersFromProto converts the directory
// creation parameters that are stored in a Protobuf message to its
// native counterpart. It also validates that the provided sizes are in
// bounds.
func NewDirectoryCreationParametersFromProto(m *model_filesystem_pb.DirectoryCreationParameters, referenceFormat object.ReferenceFormat) (*DirectoryCreationParameters, error) {
	if m == nil {
		return nil, status.Error(codes.InvalidArgument, "No directory creation parameters provided")
	}

	accessParameters, err := NewDirectoryAccessParametersFromProto(m.Access, referenceFormat)
	if err != nil {
		return nil, err
	}

	maximumObjectSizeBytes := uint32(referenceFormat.GetMaximumObjectSizeBytes())
	if m.DirectoryMaximumSizeBytes > maximumObjectSizeBytes {
		return nil, status.Errorf(codes.InvalidArgument, "Maximum size of directories is above maximum object size of %d bytes", maximumObjectSizeBytes)
	}

	return &DirectoryCreationParameters{
		DirectoryAccessParameters: accessParameters,
		referenceFormat:           referenceFormat,
		directoryMaximumSizeBytes: int(m.DirectoryMaximumSizeBytes),
	}, nil
}
