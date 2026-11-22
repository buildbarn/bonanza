package starlark_test

import (
	"testing"

	model_core "bonanza.build/pkg/model/core"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/storage/object"

	"github.com/stretchr/testify/require"
)

func TestNewDepsetContents(t *testing.T) {
	thread := eval.NewThread(t.Context(), eval.Universe())

	t.Run("UnhashableStructFields", func(t *testing.T) {
		// Bazel permits depset elements that are immutable,
		// but not hashable. For example, rules_cc places
		// providers containing dicts in depsets. Such elements
		// should still be deduplicated by value.
		newStruct := func(env map[string]string) eval.Value {
			d := eval.NewDict()
			for k, v := range env {
				require.NoError(t, d.SetKey(thread, eval.String(k), eval.String(v)))
			}
			return model_starlark.NewStructFromDict[object.LocalReference, model_core.ReferenceMetadata](nil, map[string]any{
				"entries": d,
			})
		}

		contents, err := model_starlark.NewDepsetContents[object.LocalReference, model_core.ReferenceMetadata](
			thread,
			[]eval.Value{
				newStruct(map[string]string{"FOO": "1"}),
				newStruct(map[string]string{"FOO": "1"}),
				newStruct(map[string]string{"FOO": "2"}),
				newStruct(nil),
			},
			nil,
			model_starlark_pb.Depset_DEFAULT,
		)
		require.NoError(t, err)
		list, err := contents.ToList(thread)
		require.NoError(t, err)
		require.Len(t, list, 3)
	})
}

func TestStructPartialHash(t *testing.T) {
	thread := eval.NewThread(t.Context(), eval.Universe())
	newStruct := func(name string) eval.Value {
		return model_starlark.NewStructFromDict[object.LocalReference, model_core.ReferenceMetadata](nil, map[string]any{
			"entries": eval.NewDict(),
			"name":    eval.String(name),
		})
	}
	partialHash := func(v eval.Value) uint32 {
		h, err := eval.PartialHash(thread, v)
		require.NoError(t, err)
		return h
	}

	// Even though the "entries" field is unhashable, the "name"
	// field should still contribute to the hash.
	require.Equal(t, partialHash(newStruct("a")), partialHash(newStruct("a")))
	require.NotEqual(t, partialHash(newStruct("a")), partialHash(newStruct("b")))
}
