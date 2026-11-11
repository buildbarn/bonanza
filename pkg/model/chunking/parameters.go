package chunking

import (
	model_chunking_pb "bonanza.build/pkg/proto/model/chunking"
	"bonanza.build/pkg/storage/object"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// minimumMinimumSizeBytes is the lowest permitted value of the
	// minimum size. Creating objects that are tiny increases memory
	// usage and running times of the chunking algorithms.
	minimumMinimumSizeBytes = 1024

	// maximumHorizonMultiplier limits the horizon size relative to
	// the minimum size, thereby bounding the memory usage of the
	// chunking algorithms.
	maximumHorizonMultiplier = 32
)

// Parameters of a chunking algorithm that partitions data into objects
// whose sizes fall within a given range. This is the native counterpart
// of bonanza.model.chunking.Parameters.
type Parameters struct {
	minimumSizeBytes int
	horizonSizeBytes int
}

// NewParametersFromProto converts chunking parameters stored in a
// Protobuf message to their native counterpart. It validates that the
// provided sizes are in bounds, and that the resulting objects do not
// exceed the maximum object size of the reference format.
func NewParametersFromProto(m *model_chunking_pb.Parameters, referenceFormat object.ReferenceFormat) (Parameters, error) {
	if m == nil {
		return Parameters{}, status.Error(codes.InvalidArgument, "No chunking parameters provided")
	}

	minimumSizeBytes := uint64(m.MinimumSizeBytes)
	if minimumSizeBytes < minimumMinimumSizeBytes {
		return Parameters{}, status.Errorf(codes.InvalidArgument, "Minimum size is below %d bytes", minimumMinimumSizeBytes)
	}
	if maximumObjectSizeBytes := uint64(referenceFormat.GetMaximumObjectSizeBytes()); minimumSizeBytes*2-1 > maximumObjectSizeBytes {
		return Parameters{}, status.Errorf(codes.InvalidArgument, "Maximum size (twice the minimum size) is above maximum object size of %d bytes", maximumObjectSizeBytes)
	}
	horizonSizeBytes := uint64(m.HorizonSizeBytes)
	if horizonSizeBytes > maximumHorizonMultiplier*minimumSizeBytes {
		return Parameters{}, status.Errorf(codes.InvalidArgument, "Horizon size is more than %d times as large as the minimum size", maximumHorizonMultiplier)
	}

	return Parameters{
		minimumSizeBytes: int(minimumSizeBytes),
		horizonSizeBytes: int(horizonSizeBytes),
	}, nil
}

// GetMinimumSizeBytes returns the minimum size of objects in bytes.
func (p Parameters) GetMinimumSizeBytes() int {
	return p.minimumSizeBytes
}

// GetMaximumSizeBytes returns the approximate maximum size of objects
// in bytes. Chunking algorithms that are unable to split elements in
// the input may yield objects that slightly exceed this size.
func (p Parameters) GetMaximumSizeBytes() int {
	return p.minimumSizeBytes*2 - 1
}

// GetHorizonSizeBytes returns the amount of data in bytes the chunking
// algorithm may look ahead to determine the optimal set of cutting
// points.
func (p Parameters) GetHorizonSizeBytes() int {
	return p.horizonSizeBytes
}
