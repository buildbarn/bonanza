package core_test

import (
	"testing"

	model_core "bonanza.build/pkg/model/core"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	"bonanza.build/pkg/storage/object"

	"github.com/stretchr/testify/require"
)

func TestFlattenReferenceSet(t *testing.T) {
	reference1 := object.MustNewSHA256V1LocalReference("31233528b0ccc08d56724b2f132154967a89c4fb79de65fc65e3eeb42d9f89e4", 100, 0, 0, 0)
	reference2 := object.MustNewSHA256V1LocalReference("46d71098267fa33992257c061ba8fc48017e2bcac8f9ac3be8853c8337ec896e", 200, 0, 0, 0)
	reference3 := object.MustNewSHA256V1LocalReference("e1d1549332e44eddf28662dda4ca1aae36c3dcd597cd63b3c69737f88afd75d5", 300, 0, 0, 0)
	outer := object.OutgoingReferencesList[object.LocalReference]{reference1, reference2, reference3}

	t.Run("Empty", func(t *testing.T) {
		outgoingReferences, err := model_core.FlattenReferenceSet(
			model_core.NewMessage(&model_core_pb.ReferenceSet{}, object.OutgoingReferences[object.LocalReference](outer)),
		)
		require.NoError(t, err)
		require.Equal(t, 0, outgoingReferences.GetDegree())
	})

	t.Run("Unsorted", func(t *testing.T) {
		_, err := model_core.FlattenReferenceSet(
			model_core.NewMessage(model_core_pb.ReferenceSet_builder{Indices: []uint32{3, 1}}.Build(), object.OutgoingReferences[object.LocalReference](outer)),
		)
		require.Error(t, err)
	})

	t.Run("OutOfBounds", func(t *testing.T) {
		_, err := model_core.FlattenReferenceSet(
			model_core.NewMessage(model_core_pb.ReferenceSet_builder{Indices: []uint32{1, 4}}.Build(), object.OutgoingReferences[object.LocalReference](outer)),
		)
		require.Error(t, err)
	})

	t.Run("Subset", func(t *testing.T) {
		outgoingReferences, err := model_core.FlattenReferenceSet(
			model_core.NewMessage(model_core_pb.ReferenceSet_builder{Indices: []uint32{1, 3}}.Build(), object.OutgoingReferences[object.LocalReference](outer)),
		)
		require.NoError(t, err)
		require.Equal(t, 2, outgoingReferences.GetDegree())
		require.Equal(t, reference1, outgoingReferences.GetOutgoingReference(0))
		require.Equal(t, reference3, outgoingReferences.GetOutgoingReference(1))

		// Detaching the references should yield a list that
		// only contains the references in the set.
		require.Equal(
			t,
			object.OutgoingReferencesList[object.LocalReference]{reference1, reference3},
			outgoingReferences.DetachOutgoingReferences(),
		)
	})
}
