package starlark_test

import (
	"testing"

	model_core "bonanza.build/pkg/model/core"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/storage/object"

	"github.com/stretchr/testify/require"
)

func TestAllDictLeafEntries(t *testing.T) {
	strValue := func(s string) *model_starlark_pb.Value {
		return &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Str{Str: s}}
	}
	rootDict := model_core.NewSimpleMessage[object.LocalReference](&model_starlark_pb.Dict{
		Entries: []*model_starlark_pb.Dict_Entry{
			{
				Level: &model_starlark_pb.Dict_Entry_Leaf_{
					Leaf: &model_starlark_pb.Dict_Entry_Leaf{Key: strValue("a"), Value: strValue("1")},
				},
			},
			{},
		},
	})

	t.Run("InvalidEntry", func(t *testing.T) {
		// Errors raised while processing entries should be
		// reported, as opposed to the dict being truncated.
		var keys []string
		var errIter error
		for key := range model_starlark.AllDictLeafEntries(t.Context(), nil, rootDict, &errIter) {
			keys = append(keys, key.Message.GetStr())
		}
		require.Equal(t, []string{"a"}, keys)
		require.EqualError(t, errIter, "not a valid leaf entry")
	})

	t.Run("StopEarly", func(t *testing.T) {
		// Stopping iteration before reaching the invalid
		// entry should not cause an error to be reported.
		var errIter error
		for key := range model_starlark.AllDictLeafEntries(t.Context(), nil, rootDict, &errIter) {
			require.Equal(t, "a", key.Message.GetStr())
			break
		}
		require.NoError(t, errIter)
	})
}
