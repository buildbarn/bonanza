package btree_test

import (
	"testing"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	"bonanza.build/pkg/storage/object"

	"github.com/stretchr/testify/require"
)

func TestFindWithPredecessors(t *testing.T) {
	// Leaves containing the values 10, 20, 30, 40 and 50.
	list := make([]*model_core_pb.DecodableReference, 0, 5)
	for i := byte(1); i <= 5; i++ {
		list = append(list, &model_core_pb.DecodableReference{DecodingParameters: []byte{i * 10}})
	}
	find := func(value byte) (*model_core_pb.DecodableReference, []int) {
		var predecessorCounts []int
		entry, err := btree.FindWithPredecessors(
			t.Context(),
			nil,
			model_core.NewSimpleMessage[object.LocalReference](list),
			func(entry model_core.Message[*model_core_pb.DecodableReference, object.LocalReference]) (int, *model_core_pb.DecodableReference) {
				return int(value) - int(entry.Message.DecodingParameters[0]), nil
			},
			func(predecessors model_core.Message[[]*model_core_pb.DecodableReference, object.LocalReference]) {
				predecessorCounts = append(predecessorCounts, len(predecessors.Message))
			},
		)
		require.NoError(t, err)
		return entry.Message, predecessorCounts
	}

	t.Run("Found", func(t *testing.T) {
		for i, value := range []byte{10, 20, 30, 40, 50} {
			entry, predecessorCounts := find(value)
			require.Equal(t, value, entry.DecodingParameters[0])
			require.Equal(t, []int{i}, predecessorCounts, "%d", value)
		}
	})

	t.Run("NotFound", func(t *testing.T) {
		// Predecessors should only be reported for entries that
		// are selected.
		for _, value := range []byte{5, 15, 55} {
			entry, predecessorCounts := find(value)
			require.Nil(t, entry)
			require.Empty(t, predecessorCounts, "%d", value)
		}
	})
}
