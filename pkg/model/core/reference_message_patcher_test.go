package core_test

import (
	"testing"

	model_core "bonanza.build/pkg/model/core"
	core_pb "bonanza.build/pkg/proto/model/core"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

	"go.uber.org/mock/gomock"
)

func TestReferenceMessagePatcher(t *testing.T) {
	ctrl := gomock.NewController(t)

	t.Run("Empty", func(t *testing.T) {
		patcher := model_core.NewReferenceMessagePatcher[model_core.ReferenceMetadata]()

		require.Equal(t, 0, patcher.GetHeight())
		require.Equal(t, 0, patcher.GetReferencesSizeBytes())

		references, metadata := patcher.SortAndSetReferences()
		require.Empty(t, references)
		require.Empty(t, metadata)
	})

	t.Run("SingleReference", func(t *testing.T) {
		patcher := model_core.NewReferenceMessagePatcher[model_core.ReferenceMetadata]()
		metadata1 := NewMockReferenceMetadata(ctrl)
		m := patcher.AddReference(model_core.CapturedObject[model_core.ReferenceMetadata]{
			LocalReference: object.MustNewSHA256V1LocalReference("2c74499dc9175f1f5b1024a6752ce3daffd2a48de94a0d2af153fe0734fa8995", 594844, 12, 7, 686866),
			Metadata:       metadata1,
		})

		require.Equal(t, 13, patcher.GetHeight())
		require.Equal(t, 40, patcher.GetReferencesSizeBytes())

		references, metadata := patcher.SortAndSetReferences()
		require.Equal(t, object.OutgoingReferencesList[object.LocalReference]{
			object.MustNewSHA256V1LocalReference("2c74499dc9175f1f5b1024a6752ce3daffd2a48de94a0d2af153fe0734fa8995", 594844, 12, 7, 686866),
		}, references)
		require.Equal(t, []model_core.ReferenceMetadata{metadata1}, metadata)
		testutil.RequireEqualProto(t, &core_pb.Reference{Index: 1}, m)
	})

	t.Run("MultipleReferences", func(t *testing.T) {
		// If an outgoing reference is added multiple times, it
		// should get deduplicated in the results.
		patcher := model_core.NewReferenceMessagePatcher[model_core.ReferenceMetadata]()
		metadata1 := NewMockReferenceMetadata(ctrl)
		m1 := patcher.AddReference(model_core.CapturedObject[model_core.ReferenceMetadata]{
			LocalReference: object.MustNewSHA256V1LocalReference("66d155875c92ef21bf5dbfdd26750ad864cfff703acfcf8fd039e4f2562e55fc", 594844, 5, 7, 686866),
			Metadata:       metadata1,
		})
		metadata2 := NewMockReferenceMetadata(ctrl)
		m2 := patcher.AddReference(model_core.CapturedObject[model_core.ReferenceMetadata]{
			LocalReference: object.MustNewSHA256V1LocalReference("3bfc6a655365b7ea9a7e8aeb48a6dbdfc5172ee7496a4b9ed2642ea340fc2ebc", 594844, 8, 7, 686866),
			Metadata:       metadata2,
		})
		metadata3 := NewMockReferenceMetadata(ctrl)
		metadata3.EXPECT().Discard()
		m3 := patcher.AddReference(model_core.CapturedObject[model_core.ReferenceMetadata]{
			LocalReference: object.MustNewSHA256V1LocalReference("66d155875c92ef21bf5dbfdd26750ad864cfff703acfcf8fd039e4f2562e55fc", 594844, 5, 7, 686866),
			Metadata:       metadata3,
		})

		require.Equal(t, 9, patcher.GetHeight())
		require.Equal(t, 80, patcher.GetReferencesSizeBytes())

		references, metadata := patcher.SortAndSetReferences()
		require.Equal(t, object.OutgoingReferencesList[object.LocalReference]{
			object.MustNewSHA256V1LocalReference("3bfc6a655365b7ea9a7e8aeb48a6dbdfc5172ee7496a4b9ed2642ea340fc2ebc", 594844, 8, 7, 686866),
			object.MustNewSHA256V1LocalReference("66d155875c92ef21bf5dbfdd26750ad864cfff703acfcf8fd039e4f2562e55fc", 594844, 5, 7, 686866),
		}, references)
		require.Equal(t, []model_core.ReferenceMetadata{metadata2, metadata1}, metadata)
		testutil.RequireEqualProto(t, &core_pb.Reference{Index: 2}, m1)
		testutil.RequireEqualProto(t, &core_pb.Reference{Index: 1}, m2)
		testutil.RequireEqualProto(t, &core_pb.Reference{Index: 2}, m3)
	})

	t.Run("Merge", func(t *testing.T) {
		// Merging should move all references in patcher2 to
		// patcher1. patcher2 should be empty afterwards.
		patcher1 := model_core.NewReferenceMessagePatcher[model_core.ReferenceMetadata]()
		metadata1 := NewMockReferenceMetadata(ctrl)
		m1 := patcher1.AddReference(model_core.CapturedObject[model_core.ReferenceMetadata]{
			LocalReference: object.MustNewSHA256V1LocalReference("03dbe46984e6a5938cc8f500a4df0f97f68a10064a60a3bf62e0ed3e939db57f", 594844, 25, 7, 686866),
			Metadata:       metadata1,
		})
		patcher2 := model_core.NewReferenceMessagePatcher[model_core.ReferenceMetadata]()
		metadata2 := NewMockReferenceMetadata(ctrl)
		m2 := patcher2.AddReference(model_core.CapturedObject[model_core.ReferenceMetadata]{
			LocalReference: object.MustNewSHA256V1LocalReference("402646f2e80dfda09cd97bcf53802a012585499afa2c08e33b05c039db1eb0e2", 594844, 13, 7, 686866),
			Metadata:       metadata2,
		})
		patcher1.Merge(patcher2)

		require.Equal(t, 26, patcher1.GetHeight())
		require.Equal(t, 80, patcher1.GetReferencesSizeBytes())

		references, metadata := patcher1.SortAndSetReferences()
		require.Equal(t, object.OutgoingReferencesList[object.LocalReference]{
			object.MustNewSHA256V1LocalReference("03dbe46984e6a5938cc8f500a4df0f97f68a10064a60a3bf62e0ed3e939db57f", 594844, 25, 7, 686866),
			object.MustNewSHA256V1LocalReference("402646f2e80dfda09cd97bcf53802a012585499afa2c08e33b05c039db1eb0e2", 594844, 13, 7, 686866),
		}, references)
		require.Equal(t, []model_core.ReferenceMetadata{metadata1, metadata2}, metadata)
		testutil.RequireEqualProto(t, &core_pb.Reference{Index: 1}, m1)
		testutil.RequireEqualProto(t, &core_pb.Reference{Index: 2}, m2)

		require.Equal(t, 0, patcher2.GetHeight())
		require.Equal(t, 0, patcher2.GetReferencesSizeBytes())

		references, metadata = patcher2.SortAndSetReferences()
		require.Empty(t, references)
		require.Empty(t, metadata)
	})
}
