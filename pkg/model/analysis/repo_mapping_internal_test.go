package analysis

import (
	"testing"

	"bonanza.build/pkg/label"

	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"
)

func TestRepoMapping(t *testing.T) {
	t.Run("ModuleDefault", func(t *testing.T) {
		// Dependencies on modules without a version number
		// should have the canonical repo omitted.
		defaultRepo := getModuleRepoMappingDefault("rules_go")
		require.Equal(t, util.Must(label.NewModule("rules_go")).ToModuleInstance(nil).GetBareCanonicalRepo().String(), defaultRepo)
		mapping := newRepoMapping("rules_go", "rules_go+", defaultRepo)
		require.Empty(t, mapping.GetToCanonicalRepo())
		require.Equal(t, "rules_go+", getRepoMappingCanonicalRepo(mapping, defaultRepo))
	})

	t.Run("ModuleNonDefault", func(t *testing.T) {
		// Dependencies on modules with a version number, or
		// with a custom repo name, should be stored explicitly.
		for _, tc := range [][2]string{
			{"rules_go", "rules_go+1.0"},
			{"go", "rules_go+"},
		} {
			defaultRepo := getModuleRepoMappingDefault(tc[0])
			mapping := newRepoMapping(tc[0], tc[1], defaultRepo)
			require.Equal(t, tc[1], mapping.GetToCanonicalRepo())
			require.Equal(t, tc[1], getRepoMappingCanonicalRepo(mapping, defaultRepo))
		}
	})

	t.Run("ModuleExtensionDefault", func(t *testing.T) {
		// Repos declared by the module extension should have
		// the canonical repo omitted.
		moduleExtension := util.Must(label.NewCanonicalStarlarkIdentifier("@@rules_foo+//:extensions.bzl%ext")).ToModuleExtension()
		defaultRepo := getModuleExtensionRepoMappingDefault(moduleExtension, "repo")
		require.Equal(t, moduleExtension.GetCanonicalRepoWithModuleExtension(util.Must(label.NewApparentRepo("repo"))).String(), defaultRepo)
		mapping := newRepoMapping("repo", defaultRepo, defaultRepo)
		require.Empty(t, mapping.GetToCanonicalRepo())
		require.Equal(t, defaultRepo, getRepoMappingCanonicalRepo(mapping, defaultRepo))
	})
}
