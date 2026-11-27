package analysis

import (
	"testing"

	"bonanza.build/pkg/label"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"
)

func TestGetModuleExtensionRepoMappings(t *testing.T) {
	moduleExtension := util.Must(label.NewCanonicalStarlarkIdentifier("@@rules_foo+//:extensions.bzl%ext")).ToModuleExtension()
	requireMappings := func(t *testing.T, expected, actual []*model_analysis_pb.RepoMapping) {
		testutil.RequireEqualProto(
			t,
			model_analysis_pb.ModuleRepoMapping_Value_builder{Mappings: expected}.Build(),
			model_analysis_pb.ModuleRepoMapping_Value_builder{Mappings: actual}.Build(),
		)
	}

	t.Run("NoOverrides", func(t *testing.T) {
		// Repos declared by the module extension map to the
		// default canonical repo, meaning it can be omitted.
		mappings, err := getModuleExtensionRepoMappings(moduleExtension, []string{"a", "b"}, nil)
		require.NoError(t, err)
		requireMappings(t, []*model_analysis_pb.RepoMapping{
			model_analysis_pb.RepoMapping_builder{FromApparentRepo: "a"}.Build(),
			model_analysis_pb.RepoMapping_builder{FromApparentRepo: "b"}.Build(),
		}, mappings)
	})

	t.Run("Merged", func(t *testing.T) {
		// Injected repos may sort before, in between and
		// after declared repos. Overridden repos should
		// replace the declared ones. Overrides that map to the
		// default canonical repo should have it omitted.
		mappings, err := getModuleExtensionRepoMappings(
			moduleExtension,
			[]string{"b", "d", "f"},
			[]*model_analysis_pb.ModuleExtensionRepoOverrides_Value_Override{
				model_analysis_pb.ModuleExtensionRepoOverrides_Value_Override_builder{FromApparentRepo: "a", ToCanonicalRepo: "inject_a+"}.Build(),
				model_analysis_pb.ModuleExtensionRepoOverrides_Value_Override_builder{FromApparentRepo: "c", ToCanonicalRepo: "inject_c+"}.Build(),
				model_analysis_pb.ModuleExtensionRepoOverrides_Value_Override_builder{FromApparentRepo: "d", ToCanonicalRepo: "override_d+", MustExist: true}.Build(),
				model_analysis_pb.ModuleExtensionRepoOverrides_Value_Override_builder{FromApparentRepo: "f", ToCanonicalRepo: moduleExtension.String() + "+f", MustExist: true}.Build(),
				model_analysis_pb.ModuleExtensionRepoOverrides_Value_Override_builder{FromApparentRepo: "g", ToCanonicalRepo: "inject_g+"}.Build(),
			},
		)
		require.NoError(t, err)
		requireMappings(t, []*model_analysis_pb.RepoMapping{
			model_analysis_pb.RepoMapping_builder{FromApparentRepo: "a", ToCanonicalRepo: "inject_a+"}.Build(),
			model_analysis_pb.RepoMapping_builder{FromApparentRepo: "b"}.Build(),
			model_analysis_pb.RepoMapping_builder{FromApparentRepo: "c", ToCanonicalRepo: "inject_c+"}.Build(),
			model_analysis_pb.RepoMapping_builder{FromApparentRepo: "d", ToCanonicalRepo: "override_d+"}.Build(),
			model_analysis_pb.RepoMapping_builder{FromApparentRepo: "f"}.Build(),
			model_analysis_pb.RepoMapping_builder{FromApparentRepo: "g", ToCanonicalRepo: "inject_g+"}.Build(),
		}, mappings)
	})

	t.Run("OverrideNonexistent", func(t *testing.T) {
		_, err := getModuleExtensionRepoMappings(
			moduleExtension,
			[]string{"a"},
			[]*model_analysis_pb.ModuleExtensionRepoOverrides_Value_Override{
				model_analysis_pb.ModuleExtensionRepoOverrides_Value_Override_builder{FromApparentRepo: "b", ToCanonicalRepo: "override_b+", MustExist: true}.Build(),
			},
		)
		require.EqualError(t, err, "module extension \""+moduleExtension.String()+"\" does not declare repo \"b\", yet it is overridden using override_repo(); use inject_repo() to inject a new repo")
	})

	t.Run("InjectExisting", func(t *testing.T) {
		_, err := getModuleExtensionRepoMappings(
			moduleExtension,
			[]string{"a"},
			[]*model_analysis_pb.ModuleExtensionRepoOverrides_Value_Override{
				model_analysis_pb.ModuleExtensionRepoOverrides_Value_Override_builder{FromApparentRepo: "a", ToCanonicalRepo: "inject_a+"}.Build(),
			},
		)
		require.EqualError(t, err, "module extension \""+moduleExtension.String()+"\" declares repo \"a\", yet it is injected using inject_repo(); use override_repo() to override an existing repo")
	})
}
