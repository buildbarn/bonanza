package build

import (
	"testing"

	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"
)

func first[T, U any](t T, _ U) T {
	return t
}

func TestGetOutputSelection(t *testing.T) {
	t.Run("Default", func(t *testing.T) {
		// By default, only the files contained in
		// DefaultInfo.files are built.
		testutil.RequireEqualProto(t, model_analysis_pb.OutputSelection_builder{
			DefaultInfoFiles: true,
		}.Build(), first(getOutputSelection(nil, nil)))
	})

	t.Run("AddAndRemove", func(t *testing.T) {
		// Groups prefixed by "+" and "-" modify the default set.
		testutil.RequireEqualProto(t, model_analysis_pb.OutputSelection_builder{
			DefaultInfoFiles: true,
			OutputGroups:     []string{"line_counts"},
		}.Build(), first(getOutputSelection([]string{"+line_counts"}, nil)))
		testutil.RequireEqualProto(t, model_analysis_pb.OutputSelection_builder{
			OutputGroups: []string{"line_counts"},
		}.Build(), first(getOutputSelection([]string{"+line_counts", "-default"}, nil)))
	})

	t.Run("Replace", func(t *testing.T) {
		// If at least one group is not prefixed, the default
		// set is omitted. Values may be comma separated.
		testutil.RequireEqualProto(t, model_analysis_pb.OutputSelection_builder{
			OutputGroups: []string{"a", "b"},
		}.Build(), first(getOutputSelection([]string{"b,a"}, nil)))
		testutil.RequireEqualProto(t, model_analysis_pb.OutputSelection_builder{
			OutputGroups: []string{"a", "c"},
		}.Build(), first(getOutputSelection([]string{"a", "+c,-b"}, nil)))
		testutil.RequireEqualProto(t, model_analysis_pb.OutputSelection_builder{
			DefaultInfoFiles: true,
		}.Build(), first(getOutputSelection([]string{"default"}, nil)))
		testutil.RequireEqualProto(t, &model_analysis_pb.OutputSelection{}, first(getOutputSelection([]string{"a,-a"}, nil)))
	})
	t.Run("ExplicitOutputGroups", func(t *testing.T) {
		// Output groups that are requested explicitly are
		// returned, excluding "default" and removed groups.
		_, explicit := getOutputSelection([]string{"+a,b,default", "-b"}, nil)
		require.Equal(t, []string{"a"}, explicit)

		_, explicit = getOutputSelection(nil, nil)
		require.Empty(t, explicit)
	})
	t.Run("ExtraDefaultOutputGroups", func(t *testing.T) {
		// Extra default output groups are part of the default
		// set. They are omitted if an unprefixed group is
		// provided, and can be removed using "-".
		selection, explicit := getOutputSelection(nil, []string{"x,y"})
		testutil.RequireEqualProto(t, model_analysis_pb.OutputSelection_builder{
			DefaultInfoFiles: true,
			OutputGroups:     []string{"x", "y"},
		}.Build(), selection)
		require.Empty(t, explicit)

		selection, explicit = getOutputSelection([]string{"+z,-x"}, []string{"x,y"})
		testutil.RequireEqualProto(t, model_analysis_pb.OutputSelection_builder{
			DefaultInfoFiles: true,
			OutputGroups:     []string{"y", "z"},
		}.Build(), selection)
		require.Equal(t, []string{"z"}, explicit)

		selection, _ = getOutputSelection([]string{"z"}, []string{"x,y"})
		testutil.RequireEqualProto(t, model_analysis_pb.OutputSelection_builder{
			OutputGroups: []string{"z"},
		}.Build(), selection)
	})
}
