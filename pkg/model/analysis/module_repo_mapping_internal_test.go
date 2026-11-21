package analysis

import (
	"testing"

	"bonanza.build/pkg/dotbazel"
	"bonanza.build/pkg/label"

	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"
)

func TestRepoMappingCapturingModuleDotBazelHandlerBazelDep(t *testing.T) {
	rootModuleInstance := util.Must(label.NewModule("root")).ToModuleInstance(nil)
	parse := func(modulesWithMultipleVersions map[label.Module]OverrideVersions, contents string) (*repoMappingCapturingModuleDotBazelHandler, error) {
		handler := newRepoMappingCapturingModuleDotBazelHandler(
			rootModuleInstance,
			modulesWithMultipleVersions,
			/* ignoreDevDependencies = */ false,
		)
		return handler, dotbazel.ParseModule(
			contents,
			rootModuleInstance.GetBareCanonicalRepo().GetRootPackage().AppendTargetName(dotbazel.ModuleTargetName),
			/* localPathFormat = */ nil,
			dotbazel.NewOverrideIgnoringRootHandler(handler),
		)
	}
	getCanonicalRepo := func(t *testing.T, handler *repoMappingCapturingModuleDotBazelHandler, apparentRepo string) string {
		from := util.Must(label.NewApparentRepo(apparentRepo))
		mapping, err := handler.repos[from].toProto(from, handler.repos)
		require.NoError(t, err)
		return mapping.ToCanonicalRepo
	}
	rulesGo := util.Must(label.NewModule("rules_go"))
	multipleVersions := map[label.Module]OverrideVersions{
		rulesGo: {
			util.Must(label.NewModuleVersion("1.0")),
			util.Must(label.NewModuleVersion("2.0")),
		},
	}

	t.Run("Nodep", func(t *testing.T) {
		// Nodep dependencies should not cause repos to become
		// visible, and may coexist with regular dependencies.
		handler, err := parse(nil, `
bazel_dep(name = "rules_go", version = "0.65.0")
bazel_dep(name = "rules_go", version = "0.64.0", repo_name = None)
bazel_dep(name = "rules_go", version = "0.63.0", repo_name = None)
`)
		require.NoError(t, err)
		require.Len(t, handler.repos, 1)
		require.Equal(t, "rules_go+", getCanonicalRepo(t, handler, "rules_go"))
	})

	t.Run("SameModuleTwice", func(t *testing.T) {
		_, err := parse(nil, `
bazel_dep(name = "rules_go", version = "0.65.0")
bazel_dep(name = "rules_go", version = "0.64.0", repo_name = "a")
`)
		require.ErrorContains(t, err, "module depends on module \"rules_go\" at least twice (with repo names \"rules_go\" and \"a\"); consider adding a multiple_version_override()")
	})

	t.Run("MultipleVersionsDistinct", func(t *testing.T) {
		// With multiple_version_override(), dependencies on
		// the same module are permitted, as long as they
		// resolve to different versions.
		handler, err := parse(multipleVersions, `
bazel_dep(name = "rules_go", version = "1.0", repo_name = "go1")
bazel_dep(name = "rules_go", version = "1.5", repo_name = "go2")
`)
		require.NoError(t, err)
		require.Equal(t, "rules_go+1.0", getCanonicalRepo(t, handler, "go1"))
		require.Equal(t, "rules_go+2.0", getCanonicalRepo(t, handler, "go2"))
	})

	t.Run("MultipleVersionsSame", func(t *testing.T) {
		_, err := parse(multipleVersions, `
bazel_dep(name = "rules_go", version = "1.5", repo_name = "go1")
bazel_dep(name = "rules_go", version = "2.0", repo_name = "go2")
`)
		require.ErrorContains(t, err, "module depends on version 2.0 of module \"rules_go\" at least twice (with repo names \"go1\" and \"go2\")")
	})

	t.Run("MultipleVersionsExceeded", func(t *testing.T) {
		_, err := parse(multipleVersions, `
bazel_dep(name = "rules_go", version = "3.0")
`)
		require.ErrorContains(t, err, "dependency on module \"rules_go\": module depends on version 3.0, which exceeds maximum version 2.0")
	})
}
