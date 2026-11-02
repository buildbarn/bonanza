package starlark_test

import (
	"testing"

	model_core "bonanza.build/pkg/model/core"
	model_starlark "bonanza.build/pkg/model/starlark"
	"bonanza.build/pkg/starlark/unpack"
	"bonanza.build/pkg/storage/object"

	"github.com/stretchr/testify/require"

	"go.starlark.net/starlark"
)

func TestNonConfigurableSelectUnpackerInto(t *testing.T) {
	unpacker := model_starlark.NewNonConfigurableSelectUnpackerInto[object.LocalReference, model_core.CreatedObjectTree](unpack.String)
	thread := &starlark.Thread{}

	t.Run("Value", func(t *testing.T) {
		// Plain values should be converted to a select() having
		// only a default condition.
		expected := model_starlark.NewSelect[object.LocalReference, model_core.CreatedObjectTree](
			[]model_starlark.SelectGroup{model_starlark.NewSelectGroup(nil, starlark.String("a"), "")},
			0,
		)

		var unpacked *model_starlark.Select[object.LocalReference, model_core.CreatedObjectTree]
		require.NoError(t, unpacker.UnpackInto(thread, starlark.String("a"), &unpacked))
		require.Equal(t, expected, unpacked)

		canonicalized, err := unpacker.Canonicalize(thread, starlark.String("a"))
		require.NoError(t, err)
		require.Equal(t, expected, canonicalized)
	})

	t.Run("InvalidValue", func(t *testing.T) {
		// Values should be canonicalized using the provided
		// unpacker.
		var unpacked *model_starlark.Select[object.LocalReference, model_core.CreatedObjectTree]
		require.EqualError(t, unpacker.UnpackInto(thread, starlark.MakeInt(42), &unpacked), "got int, want string")
	})

	t.Run("Select", func(t *testing.T) {
		// select() objects should be rejected, even if they only
		// have a default condition.
		value := model_starlark.NewSelect[object.LocalReference, model_core.CreatedObjectTree](
			[]model_starlark.SelectGroup{model_starlark.NewSelectGroup(nil, starlark.String("a"), "")},
			0,
		)

		var unpacked *model_starlark.Select[object.LocalReference, model_core.CreatedObjectTree]
		require.EqualError(t, unpacker.UnpackInto(thread, value, &unpacked), "attribute is not configurable, meaning select() cannot be used")

		_, err := unpacker.Canonicalize(thread, value)
		require.EqualError(t, err, "attribute is not configurable, meaning select() cannot be used")
	})
}
