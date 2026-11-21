package analysis

import (
	"testing"

	"bonanza.build/pkg/dotbazel"
	"bonanza.build/pkg/label"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"
)

func TestRepoMappingCapturingModuleDotBazelHandlerRepoOverrides(t *testing.T) {
	rootModuleInstance := util.Must(label.NewModule("root")).ToModuleInstance(nil)
	parse := func(t *testing.T, ignoreDevDependencies bool, contents string) *repoMappingCapturingModuleDotBazelHandler {
		handler := newRepoMappingCapturingModuleDotBazelHandler(
			rootModuleInstance,
			/* modulesWithMultipleVersions = */ nil,
			ignoreDevDependencies,
		)
		require.NoError(t, dotbazel.ParseModule(
			contents,
			rootModuleInstance.GetBareCanonicalRepo().GetRootPackage().AppendTargetName(dotbazel.ModuleTargetName),
			/* localPathFormat = */ nil,
			dotbazel.NewOverrideIgnoringRootHandler(handler),
		))
		return handler
	}

	ext1 := util.Must(label.NewCanonicalStarlarkIdentifier("@@rules_foo+//:extensions.bzl%ext1")).ToModuleExtension()
	ext2 := util.Must(label.NewCanonicalStarlarkIdentifier("@@root+//:extensions.bzl%ext2")).ToModuleExtension()
	ext3 := util.Must(label.NewCanonicalStarlarkIdentifier("@@root+//:extensions.bzl%ext3")).ToModuleExtension()

	t.Run("Success", func(t *testing.T) {
		handler := parse(t, false, `
module(name = "root")
bazel_dep(name = "rules_foo", version = "1.0")
bazel_dep(name = "rules_bar", version = "1.0", repo_name = "bar")

ext1 = use_extension("@rules_foo//:extensions.bzl", "ext1")
use_repo(ext1, "repo1", local_repo2 = "repo2")
override_repo(ext1, "bar", baz = "rules_foo")

ext2 = use_extension("//:extensions.bzl", "ext2")
inject_repo(ext2, "repo1", injected = "local_repo2")
`)

		// Overrides should only be returned for the requested
		// module extension, and refer to the repos visible to
		// the root module.
		overrides, err := handler.getRepoOverrides(ext1)
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_analysis_pb.ModuleExtensionRepoOverrides_Value{
			Overrides: []*model_analysis_pb.ModuleExtensionRepoOverrides_Value_Override{
				{FromApparentRepo: "bar", ToCanonicalRepo: "rules_bar+", MustExist: true},
				{FromApparentRepo: "baz", ToCanonicalRepo: "rules_foo+", MustExist: true},
			},
		}, &model_analysis_pb.ModuleExtensionRepoOverrides_Value{Overrides: overrides})

		// Targets that are repos of another module extension
		// should resolve to the repo declared by that module
		// extension.
		overrides, err = handler.getRepoOverrides(ext2)
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_analysis_pb.ModuleExtensionRepoOverrides_Value{
			Overrides: []*model_analysis_pb.ModuleExtensionRepoOverrides_Value_Override{
				{FromApparentRepo: "injected", ToCanonicalRepo: ext1.GetCanonicalRepoWithModuleExtension(util.Must(label.NewApparentRepo("repo2"))).String()},
				{FromApparentRepo: "repo1", ToCanonicalRepo: ext1.GetCanonicalRepoWithModuleExtension(util.Must(label.NewApparentRepo("repo1"))).String()},
			},
		}, &model_analysis_pb.ModuleExtensionRepoOverrides_Value{Overrides: overrides})

		overrides, err = handler.getRepoOverrides(ext3)
		require.NoError(t, err)
		require.Empty(t, overrides)

		// Repos imported using use_repo() should map to the
		// override if one exists, and to the repo declared by
		// the module extension otherwise.
		overriddenRepo := util.Must(label.NewCanonicalRepo("overridden+"))
		getRepoOverrides := func(moduleExtension label.ModuleExtension) (map[label.ApparentRepo]label.CanonicalRepo, error) {
			require.Equal(t, ext1, moduleExtension)
			return map[label.ApparentRepo]label.CanonicalRepo{
				util.Must(label.NewApparentRepo("repo2")): overriddenRepo,
			}, nil
		}
		canonicalRepo, err := handler.repos[util.Must(label.NewApparentRepo("repo1"))].toCanonicalRepo(handler.repos, getRepoOverrides)
		require.NoError(t, err)
		require.Equal(t, ext1.GetCanonicalRepoWithModuleExtension(util.Must(label.NewApparentRepo("repo1"))), canonicalRepo)
		canonicalRepo, err = handler.repos[util.Must(label.NewApparentRepo("local_repo2"))].toCanonicalRepo(handler.repos, getRepoOverrides)
		require.NoError(t, err)
		require.Equal(t, overriddenRepo, canonicalRepo)
	})

	t.Run("IgnoredDevDependencies", func(t *testing.T) {
		// override_repo() and inject_repo() should be ignored
		// if dev dependencies are ignored, which is also the
		// case for modules other than the root module.
		handler := parse(t, true, `
module(name = "root")
bazel_dep(name = "rules_foo", version = "1.0")
ext1 = use_extension("@rules_foo//:extensions.bzl", "ext1")
override_repo(ext1, "rules_foo")
inject_repo(ext1, injected = "rules_foo")
`)
		overrides, err := handler.getRepoOverrides(ext1)
		require.NoError(t, err)
		require.Empty(t, overrides)
	})

	t.Run("OverriddenMultipleTimes", func(t *testing.T) {
		handler := parse(t, false, `
module(name = "root")
bazel_dep(name = "rules_foo", version = "1.0")
ext1a = use_extension("@rules_foo//:extensions.bzl", "ext1")
override_repo(ext1a, "rules_foo")
ext1b = use_extension("@rules_foo//:extensions.bzl", "ext1")
inject_repo(ext1b, "rules_foo")
`)
		_, err := handler.getRepoOverrides(ext1)
		require.EqualError(t, err, "repo \"rules_foo\" of module extension \""+ext1.String()+"\" is overridden multiple times")
	})

	t.Run("UnknownTarget", func(t *testing.T) {
		handler := parse(t, false, `
module(name = "root")
bazel_dep(name = "rules_foo", version = "1.0")
ext1 = use_extension("@rules_foo//:extensions.bzl", "ext1")
override_repo(ext1, foo = "nonexistent")
`)
		_, err := handler.getRepoOverrides(ext1)
		require.EqualError(t, err, "repo \"foo\" of module extension \""+ext1.String()+"\" is overridden with repo \"nonexistent\", but no repo is visible under that name")
	})

	t.Run("ChainedOverride", func(t *testing.T) {
		// Repos used as an override may not be overridden
		// themselves.
		handler := parse(t, false, `
module(name = "root")
bazel_dep(name = "rules_foo", version = "1.0")
ext1 = use_extension("@rules_foo//:extensions.bzl", "ext1")
use_repo(ext1, "repo1")
override_repo(ext1, repo1 = "rules_foo")
ext2 = use_extension("//:extensions.bzl", "ext2")
override_repo(ext2, foo = "repo1")
`)
		_, err := handler.getRepoOverrides(ext2)
		require.EqualError(t, err, "repo \"foo\" of module extension \""+ext2.String()+"\" is overridden with repo \"repo1\", which is overridden itself")
	})
}

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
		canonicalRepo, err := handler.repos[util.Must(label.NewApparentRepo(apparentRepo))].toCanonicalRepo(handler.repos, noRepoOverrides)
		require.NoError(t, err)
		return canonicalRepo.String()
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
