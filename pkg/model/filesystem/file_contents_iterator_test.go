package filesystem_test

import (
	"testing"

	model_core "bonanza.build/pkg/model/core"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestFileContentsIterator(t *testing.T) {
	t.Run("SmallFile", func(t *testing.T) {
		// The FileContentsIterator API should even be usable
		// for files that are small enough that they don't use
		// any FileContentsLists.
		decodable, err := model_core.NewDecodable(object.MustNewSHA256V1LocalReference("d10d524d6144f4b0b0ffed862d43c19181b133bb149a560b2f86e3dc10f155b0", 21, 0, 0, 0), nil)
		require.NoError(t, err)

		iterator := model_filesystem.NewFileContentsIterator(
			model_filesystem.FileContentsEntry[object.LocalReference]{
				Reference: decodable,
				EndBytes:  21,
			},
			/* initialOffsetBytes = */ 14,
		)

		reference, offsetBytes, sizeBytes := iterator.GetCurrentPart()
		decodable2, err := model_core.NewDecodable(object.MustNewSHA256V1LocalReference("d10d524d6144f4b0b0ffed862d43c19181b133bb149a560b2f86e3dc10f155b0", 21, 0, 0, 0), nil)
		require.NoError(t, err)
		require.Equal(t, decodable2, reference)
		require.Equal(t, uint64(14), offsetBytes)
		require.Equal(t, uint64(21), sizeBytes)

		iterator.ToNextPart()
	})

	t.Run("LargeFile", func(t *testing.T) {
		// Because the file has a height of 2, we should
		// initially call PushFileContentsList() twice to get to
		// the first chunk contained in the file.
		newDecodable, err := model_core.NewDecodable(object.MustNewSHA256V1LocalReference("d10d524d6144f4b0b0ffed862d43c19181b133bb149a560b2f86e3dc10f155b0", 200, 2, 4, 250), nil)
		require.NoError(t, err)
		iterator := model_filesystem.NewFileContentsIterator(
			model_filesystem.FileContentsEntry[object.LocalReference]{
				Reference: newDecodable,
				EndBytes:  593838,
			},
			/* initialOffsetBytes = */ 328312,
		)

		reference, offsetBytes, sizeBytes := iterator.GetCurrentPart()
		decodable, err := model_core.NewDecodable(object.MustNewSHA256V1LocalReference("d10d524d6144f4b0b0ffed862d43c19181b133bb149a560b2f86e3dc10f155b0", 200, 2, 4, 250), nil)
		require.NoError(t, err)
		require.Equal(t, decodable, reference)
		require.Equal(t, uint64(328312), offsetBytes)
		require.Equal(t, uint64(593838), sizeBytes)

		d1, err := model_core.NewDecodable(object.MustNewSHA256V1LocalReference("519306c1f517f34986b2ec1e74fd425bc39ac3742f68904d849079ae39b64bac", 200, 1, 4, 0), nil)
		require.NoError(t, err)
		d2, err := model_core.NewDecodable(object.MustNewSHA256V1LocalReference("f8a601950adcc0c5a45f232defe3c2eb0710788359ea13a661241d55455c302c", 200, 1, 4, 0), nil)
		require.NoError(t, err)
		d3, err := model_core.NewDecodable(object.MustNewSHA256V1LocalReference("c6c3973b86875df3c305b3e61fee444ad24dc7fb9f143f21b5b6e9fe4cecf448", 200, 1, 4, 0), nil)
		require.NoError(t, err)
		d4, err := model_core.NewDecodable(object.MustNewSHA256V1LocalReference("c3288468acfaa31cc7da1ba8bdc25977e556f671a75cc73a54e855188ca18f2b", 200, 1, 4, 0), nil)
		require.NoError(t, err)

		require.NoError(t, iterator.PushFileContentsList(model_filesystem.FileContentsList[object.LocalReference]{
			{
				Reference: d1,
				EndBytes:  200322,
			},
			{
				Reference: d2,
				EndBytes:  329342,
			},
			{
				Reference: d3,
				EndBytes:  457449,
			},
			{
				Reference: d4,
				EndBytes:  593838,
			},
		}))

		reference, offsetBytes, sizeBytes = iterator.GetCurrentPart()
		d5, err := model_core.NewDecodable(object.MustNewSHA256V1LocalReference("f8a601950adcc0c5a45f232defe3c2eb0710788359ea13a661241d55455c302c", 200, 1, 4, 0), nil)
		require.NoError(t, err)
		require.Equal(t, d5, reference)
		require.Equal(t, uint64(127990), offsetBytes)
		require.Equal(t, uint64(129020), sizeBytes)

		d6, err := model_core.NewDecodable(object.MustNewSHA256V1LocalReference("970438fd5db3b492cecf04d2f34a78a6f0ddd6b144632f3965c522c2e46e2574", 31037, 0, 0, 0), nil)
		require.NoError(t, err)
		d7, err := model_core.NewDecodable(object.MustNewSHA256V1LocalReference("b9e4759a44275a94d8132227ce549c383f0b0199b8493e2b6840e1c4c2e47776", 33244, 0, 0, 0), nil)
		require.NoError(t, err)
		d8, err := model_core.NewDecodable(object.MustNewSHA256V1LocalReference("05ad148274f34ec28b730098f55dce0edccdec8b03afa9b0e3c45a7f894290b1", 30762, 0, 0, 0), nil)
		require.NoError(t, err)
		d9, err := model_core.NewDecodable(object.MustNewSHA256V1LocalReference("eb87d4b6244ac9634290ce59fe98c146468c422c761c001ebd5ea3ae94beba56", 33977, 0, 0, 0), nil)
		require.NoError(t, err)
		require.NoError(t, iterator.PushFileContentsList(model_filesystem.FileContentsList[object.LocalReference]{
			{
				Reference: d6,
				EndBytes:  31037,
			},
			{
				Reference: d7,
				EndBytes:  64281,
			},
			{
				Reference: d8,
				EndBytes:  95043,
			},
			{
				Reference: d9,
				EndBytes:  129020,
			},
		}))

		reference, offsetBytes, sizeBytes = iterator.GetCurrentPart()
		d10, err := model_core.NewDecodable(object.MustNewSHA256V1LocalReference("eb87d4b6244ac9634290ce59fe98c146468c422c761c001ebd5ea3ae94beba56", 33977, 0, 0, 0), nil)
		require.NoError(t, err)
		require.Equal(t, d10, reference)
		require.Equal(t, uint64(32947), offsetBytes)
		require.Equal(t, uint64(33977), sizeBytes)

		iterator.ToNextPart()

		// After the first chunk has been reached, we receive a
		// reference to another file contents list. Simulate the
		// case where the tree is malformed. Namely, the total
		// size encoded in the parent does match with the
		// combined size of all parts contained in the child.
		reference, offsetBytes, sizeBytes = iterator.GetCurrentPart()
		d11, err := model_core.NewDecodable(object.MustNewSHA256V1LocalReference("c6c3973b86875df3c305b3e61fee444ad24dc7fb9f143f21b5b6e9fe4cecf448", 200, 1, 4, 0), nil)
		require.Equal(t, d11, reference)
		require.Equal(t, uint64(0), offsetBytes)
		require.Equal(t, uint64(128107), sizeBytes)

		d12, err := model_core.NewDecodable(object.MustNewSHA256V1LocalReference("c501a73d54408966d253888d4e0f3e6cab3be40a575d4fc6bcd09b0163947f2f", 36492, 0, 0, 0), nil)
		require.NoError(t, err)
		d13, err := model_core.NewDecodable(object.MustNewSHA256V1LocalReference("7a9101a3210cfba0720d03554de90fc6fbe4dbf4080118d8cc9001c99e2acf01", 22708, 0, 0, 0), nil)
		require.NoError(t, err)
		d14, err := model_core.NewDecodable(object.MustNewSHA256V1LocalReference("3fbef86ae4f2aac0aeea2f384a2429213fc4e015a8c21e5ae849487bc0ef0f66", 28542, 0, 0, 0), nil)
		require.NoError(t, err)
		d15, err := model_core.NewDecodable(object.MustNewSHA256V1LocalReference("2bb05077513a3162196a872c334fd6d0ae1ce25b21ae17a57cb2be7a7bbab012", 31841, 0, 0, 0), nil)
		require.NoError(t, err)
		testutil.RequireEqualStatus(
			t,
			status.Error(codes.InvalidArgument, "Parts in the file contents list have a total size of 119583 bytes, while 128107 bytes were expected"),
			iterator.PushFileContentsList(model_filesystem.FileContentsList[object.LocalReference]{
				{
					Reference: d12,
					EndBytes:  36492,
				},
				{
					Reference: d13,
					EndBytes:  59200,
				},
				{
					Reference: d14,
					EndBytes:  87742,
				},
				{
					Reference: d15,
					EndBytes:  119583,
				},
			}),
		)
	})
}
