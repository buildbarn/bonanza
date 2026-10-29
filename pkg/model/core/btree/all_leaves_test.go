package btree_test

import (
	"errors"
	"testing"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	"bonanza.build/pkg/storage/object"

	"github.com/stretchr/testify/require"
)

func TestAllLeaves(t *testing.T) {
	root := model_core.NewSimpleMessage[object.LocalReference]([]*model_filesystem_pb.FileContents{
		{TotalSizeBytes: 1},
		{TotalSizeBytes: 2},
		{TotalSizeBytes: 3},
	})
	allLeaves := func(traverser btree.Traverser[*model_filesystem_pb.FileContents, object.LocalReference], errOut *error) func(yield func(model_core.Message[*model_filesystem_pb.FileContents, object.LocalReference]) bool) {
		return btree.AllLeaves(t.Context(), nil, root, traverser, errOut)
	}
	noParents := func(model_core.Message[*model_filesystem_pb.FileContents, object.LocalReference]) (*model_core_pb.DecodableReference, error) {
		return nil, nil
	}

	t.Run("Success", func(t *testing.T) {
		var sizes []uint64
		var errIter error
		for entry := range allLeaves(noParents, &errIter) {
			sizes = append(sizes, entry.Message.TotalSizeBytes)
		}
		require.NoError(t, errIter)
		require.Equal(t, []uint64{1, 2, 3}, sizes)
	})

	t.Run("TraverserFailure", func(t *testing.T) {
		var sizes []uint64
		var errIter error
		for entry := range allLeaves(func(entry model_core.Message[*model_filesystem_pb.FileContents, object.LocalReference]) (*model_core_pb.DecodableReference, error) {
			if entry.Message.TotalSizeBytes == 2 {
				return nil, errors.New("invalid entry")
			}
			return nil, nil
		}, &errIter) {
			sizes = append(sizes, entry.Message.TotalSizeBytes)
		}
		require.EqualError(t, errIter, "invalid entry")
		require.Equal(t, []uint64{1}, sizes)
	})

	t.Run("StopEarlyRetainsError", func(t *testing.T) {
		// Callers may wrap the iterator, and store errors that
		// are raised while processing leaves in the same error
		// that is provided to AllLeaves(). These errors must
		// not be cleared when iteration is stopped as a
		// result.
		errIter := errors.New("error raised by caller")
		for range allLeaves(noParents, &errIter) {
			break
		}
		require.EqualError(t, errIter, "error raised by caller")
	})
}
