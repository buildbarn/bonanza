package chunking_test

import (
	"testing"

	"bonanza.build/pkg/model/chunking"
	model_chunking_pb "bonanza.build/pkg/proto/model/chunking"
	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestNewParametersFromProto(t *testing.T) {
	referenceFormat := util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1))
	maximumObjectSizeBytes := uint32(referenceFormat.GetMaximumObjectSizeBytes())

	t.Run("Nil", func(t *testing.T) {
		_, err := chunking.NewParametersFromProto(nil, referenceFormat)
		testutil.RequireEqualStatus(t, status.Error(codes.InvalidArgument, "No chunking parameters provided"), err)
	})

	t.Run("MinimumSizeTooSmall", func(t *testing.T) {
		_, err := chunking.NewParametersFromProto(&model_chunking_pb.Parameters{
			MinimumSizeBytes: 1023,
			HorizonSizeBytes: 8 * 1023,
		}, referenceFormat)
		testutil.RequireEqualStatus(t, status.Error(codes.InvalidArgument, "Minimum size is below 1024 bytes"), err)
	})

	t.Run("MaximumSizeTooLarge", func(t *testing.T) {
		_, err := chunking.NewParametersFromProto(&model_chunking_pb.Parameters{
			MinimumSizeBytes: maximumObjectSizeBytes/2 + 1,
		}, referenceFormat)
		testutil.RequireEqualStatus(t, status.Errorf(codes.InvalidArgument, "Maximum size (twice the minimum size) is above maximum object size of %d bytes", maximumObjectSizeBytes), err)
	})

	t.Run("HorizonSizeTooLarge", func(t *testing.T) {
		_, err := chunking.NewParametersFromProto(&model_chunking_pb.Parameters{
			MinimumSizeBytes: 4096,
			HorizonSizeBytes: 32*4096 + 1,
		}, referenceFormat)
		testutil.RequireEqualStatus(t, status.Error(codes.InvalidArgument, "Horizon size is more than 32 times as large as the minimum size"), err)
	})

	t.Run("Success", func(t *testing.T) {
		for _, m := range []*model_chunking_pb.Parameters{
			{MinimumSizeBytes: 1024, HorizonSizeBytes: 0},
			{MinimumSizeBytes: 4096, HorizonSizeBytes: 32 * 4096},
			{MinimumSizeBytes: maximumObjectSizeBytes / 2, HorizonSizeBytes: maximumObjectSizeBytes * 4},
		} {
			parameters, err := chunking.NewParametersFromProto(m, referenceFormat)
			require.NoError(t, err)
			require.Equal(t, int(m.MinimumSizeBytes), parameters.GetMinimumSizeBytes())
			require.Equal(t, int(m.MinimumSizeBytes)*2-1, parameters.GetMaximumSizeBytes())
			require.Equal(t, int(m.HorizonSizeBytes), parameters.GetHorizonSizeBytes())
		}
	})
}
