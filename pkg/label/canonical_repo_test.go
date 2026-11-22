package label_test

import (
	"testing"

	"bonanza.build/pkg/label"

	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCanonicalRepo(t *testing.T) {
	t.Run("AppendLabel", func(t *testing.T) {
		base := util.Must(label.NewCanonicalRepo("example+"))
		for input, output := range map[string]string{
			"//baz":               "@@example+//baz",
			"//baz:wiz":           "@@example+//baz:wiz",
			"//:foo":              "@@example+//:foo",
			"//baz:baz":           "@@example+//baz",
			"//:example+":         "@@example+",
			"@@other1+":           "@@other1+",
			"@@other1+//:other1+": "@@other1+",
			"@@other1+//:foo":     "@@other1+//:foo",
			"@other2":             "@other2",
			"@other2//:other2":    "@other2",
			"@other2//:foo":       "@other2//:foo",
		} {
			assert.Equal(t, output, base.AppendLabel(util.Must(label.NewAbsoluteLabel(input))).String())
		}
	})

	t.Run("GetModule", func(t *testing.T) {
		for input, output := range map[string]string{
			"com_github_buildbarn_bb_storage+":                    "com_github_buildbarn_bb_storage",
			"com_github_buildbarn_bb_storage+1.0":                 "com_github_buildbarn_bb_storage",
			"gazelle++go_deps+bazel_gazelle_go_repository_config": "gazelle",
		} {
			assert.Equal(t, output, util.Must(label.NewCanonicalRepo(input)).GetModuleInstance().GetModule().String())
		}
	})

	t.Run("GetModuleVersion", func(t *testing.T) {
		t.Run("WithVersion", func(t *testing.T) {
			for input, output := range map[string]string{
				"com_github_buildbarn_bb_storage+1.0":                       "1.0",
				"com_github_buildbarn_bb_storage+1.0-rc.3":                  "1.0-rc.3",
				"gazelle+0.39.1+go_deps+bazel_gazelle_go_repository_config": "0.39.1",
			} {
				moduleVersion, ok := util.Must(label.NewCanonicalRepo(input)).GetModuleInstance().GetModuleVersion()
				require.True(t, ok)
				assert.Equal(t, output, moduleVersion.String())
			}
		})

		t.Run("WithoutVersion", func(t *testing.T) {
			for _, input := range []string{
				"com_github_buildbarn_bb_storage+",
				"gazelle++go_deps+bazel_gazelle_go_repository_config",
			} {
				_, ok := util.Must(label.NewCanonicalRepo(input)).GetModuleInstance().GetModuleVersion()
				assert.False(t, ok)
			}
		})
	})

	t.Run("GetModuleExtension", func(t *testing.T) {
		t.Run("False", func(t *testing.T) {
			for _, input := range []string{
				"com_github_buildbarn_bb_storage+",
				"com_github_buildbarn_bb_storage+1.0",
			} {
				_, _, ok := util.Must(label.NewCanonicalRepo(input)).GetModuleExtension()
				assert.False(t, ok)
			}
		})

		t.Run("True", func(t *testing.T) {
			moduleExtension, apparentRepo, ok := util.Must(label.NewCanonicalRepo("gazelle++go_deps+bazel_gazelle_go_repository_config")).GetModuleExtension()
			require.True(t, ok)
			assert.Equal(t, "gazelle++go_deps", moduleExtension.String())
			assert.Equal(t, "bazel_gazelle_go_repository_config", apparentRepo.String())

			moduleExtension, apparentRepo, ok = util.Must(label.NewCanonicalRepo("gazelle+0.39.1+go_deps+bazel_gazelle_go_repository_config")).GetModuleExtension()
			require.True(t, ok)
			assert.Equal(t, "gazelle+0.39.1+go_deps", moduleExtension.String())
			assert.Equal(t, "bazel_gazelle_go_repository_config", apparentRepo.String())
		})
	})
}
