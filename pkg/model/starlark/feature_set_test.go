package starlark_test

import (
	"testing"

	model_starlark "bonanza.build/pkg/model/starlark"

	"github.com/stretchr/testify/require"
)

func TestFeatureSet(t *testing.T) {
	t.Run("Empty", func(t *testing.T) {
		fs := model_starlark.ParseFeatures(nil)
		require.Empty(t, fs.GetEnabled())
		require.Empty(t, fs.GetDisabled())

		var zero model_starlark.FeatureSet
		merged := zero.Merge(zero)
		require.Empty(t, merged.GetEnabled())
		require.Empty(t, merged.GetDisabled())
	})

	t.Run("Parse", func(t *testing.T) {
		// Disabling a feature takes precedence over enabling it
		// at the same level.
		fs := model_starlark.ParseFeatures([]string{"b", "-c", "a", "c", "b"})
		require.Equal(t, []string{"a", "b"}, fs.GetEnabled())
		require.Equal(t, []string{"c"}, fs.GetDisabled())
	})

	t.Run("NewFeatureSet", func(t *testing.T) {
		fs, err := model_starlark.NewFeatureSet([]string{"a", "c"}, []string{"b", "d"})
		require.NoError(t, err)
		require.Equal(t, []string{"a", "c"}, fs.GetEnabled())
		require.Equal(t, []string{"b", "d"}, fs.GetDisabled())

		_, err = model_starlark.NewFeatureSet([]string{"b", "a"}, nil)
		require.EqualError(t, err, `enabled features are not sorted and deduplicated: "b" is followed by "a"`)
		_, err = model_starlark.NewFeatureSet(nil, []string{"a", "a"})
		require.EqualError(t, err, `disabled features are not sorted and deduplicated: "a" is followed by "a"`)
		_, err = model_starlark.NewFeatureSet([]string{"a", "b"}, []string{"b"})
		require.EqualError(t, err, `feature "b" is both enabled and disabled`)
	})

	t.Run("Merge", func(t *testing.T) {
		// Features at the more specific level override the ones
		// at the coarser level. Features that are not mentioned
		// at the more specific level are left alone.
		coarse := model_starlark.ParseFeatures([]string{"a", "b", "-c", "-d"})
		fine := model_starlark.ParseFeatures([]string{"-a", "c", "e"})
		merged := coarse.Merge(fine)
		require.Equal(t, []string{"b", "c", "e"}, merged.GetEnabled())
		require.Equal(t, []string{"a", "d"}, merged.GetDisabled())

		// The inputs should not be modified.
		require.Equal(t, []string{"a", "b"}, coarse.GetEnabled())
		require.Equal(t, []string{"c", "d"}, coarse.GetDisabled())
	})
}
