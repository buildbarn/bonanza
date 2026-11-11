package chunking

import (
	"bonanza.build/pkg/model/core/inlinedtree"
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
	referenceFormat  object.ReferenceFormat
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
		referenceFormat:  referenceFormat,
		minimumSizeBytes: int(minimumSizeBytes),
		horizonSizeBytes: int(horizonSizeBytes),
	}, nil
}

// GetMinimumSizeBytes returns the minimum size of objects in bytes.
func (p Parameters) GetMinimumSizeBytes() int {
	return p.minimumSizeBytes
}

// GetHorizonSizeBytes returns the amount of data in bytes the chunking
// algorithm may look ahead to determine the optimal set of cutting
// points.
func (p Parameters) GetHorizonSizeBytes() int {
	return p.horizonSizeBytes
}

// GetInlinedTreeOptions returns options that can be provided to
// inlinedtree.Build(), so that it constructs messages whose size does
// not exceed the maximum size of objects (twice the minimum size,
// minus one byte).
func (p Parameters) GetInlinedTreeOptions() inlinedtree.Options {
	return inlinedtree.Options{
		ReferenceFormat:  p.referenceFormat,
		MaximumSizeBytes: p.minimumSizeBytes*2 - 1,
	}
}
