package label_test

import (
	"testing"

	"bonanza.build/pkg/label"

	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCanonicalPackage(t *testing.T) {
	t.Run("Valid", func(t *testing.T) {
		for _, input := range []string{
			"@@com_github_buildbarn_bb_storage+",
			"@@com_github_buildbarn_bb_storage+//cmd",
			"@@com_github_buildbarn_bb_storage+//cmd/hello_world",
			`@@com_github_buildbarn_bb_storage+//cmd/! "#$%&'()*+,-.;<=>?@[]^_{|}` + "`",
		} {
			canonicalPackage := util.Must(label.NewCanonicalPackage(input))
			assert.Equal(t, input, canonicalPackage.String())
		}
	})

	t.Run("Invalid", func(t *testing.T) {
		for _, input := range []string{
			"",
			"hello",
			"//cmd/hello_world",
			"@repo//cmd/hello_world",
			"@//cmd/hello_world",
			"@@//cmd/hello_world",
			"@@com_github_buildbarn_bb_storage+//",
			"@@com_github_buildbarn_bb_storage+//cmd/hello_world:target",
			"@@com_github_buildbarn_bb_storage+:target",
			"@@com_github_buildbarn_bb_storage+//cmd//hello_world",
			"@@com_github_buildbarn_bb_storage+//cmd/./hello_world",
			"@@com_github_buildbarn_bb_storage+//cmd/../hello_world",
			"@@com_github_buildbarn_bb_storage+//cmd/.../hello_world",
			"@@com_github_buildbarn_bb_storage+//cmd/..../hello_world",
			"@@com_github_buildbarn_bb_storage+///cmd/hello_world",
			"@@com_github_buildbarn_bb_storage+//cmd/hello_world/",
		} {
			_, err := label.NewCanonicalPackage(input)
			assert.ErrorContains(t, err, "canonical package name must match ", input)
		}
	})

	t.Run("GetCanonicalRepo", func(t *testing.T) {
		for _, input := range []string{
			"@@com_github_buildbarn_bb_storage+",
			"@@com_github_buildbarn_bb_storage+//cmd",
			"@@com_github_buildbarn_bb_storage+//cmd/hello_world",
		} {
			canonicalPackage := util.Must(label.NewCanonicalPackage(input))
			assert.Equal(t, "com_github_buildbarn_bb_storage+", canonicalPackage.GetCanonicalRepo().String())
		}
	})

	t.Run("AsCanonicalLabel", func(t *testing.T) {
		for input, targetName := range map[string]string{
			"@@com_github_buildbarn_bb_storage+":                  "com_github_buildbarn_bb_storage+",
			"@@com_github_buildbarn_bb_storage+//cmd":             "cmd",
			"@@com_github_buildbarn_bb_storage+//cmd/hello_world": "hello_world",
		} {
			canonicalLabel := util.Must(label.NewCanonicalPackage(input)).AsCanonicalLabel()
			assert.Equal(t, input, canonicalLabel.String())
			assert.Equal(t, input, canonicalLabel.GetCanonicalPackage().String())
			assert.Equal(t, targetName, canonicalLabel.GetTargetName().String())
		}
	})

	t.Run("GetPackagePath", func(t *testing.T) {
		for input, output := range map[string]string{
			"@@com_github_buildbarn_bb_storage+":                  "",
			"@@com_github_buildbarn_bb_storage+//cmd":             "cmd",
			"@@com_github_buildbarn_bb_storage+//cmd/hello_world": "cmd/hello_world",
		} {
			canonicalPackage := util.Must(label.NewCanonicalPackage(input))
			assert.Equal(t, output, canonicalPackage.GetPackagePath())
		}
	})

	t.Run("AppendLabel", func(t *testing.T) {
		t.Run("AtRoot", func(t *testing.T) {
			base := util.Must(label.NewCanonicalPackage("@@example+"))
			for input, output := range map[string]string{
				":foo":         "@@example+//:foo",
				"bar:wiz":      "@@example+//bar:wiz",
				"bar/wiz":      "@@example+//:bar/wiz",
				"bar:all":      "@@example+//bar:all",
				":all":         "@@example+//:all",
				"//baz":        "@@example+//baz",
				"//:example+":  "@@example+",
				"@foo/bar/baz": "@@example+//:@foo/bar/baz",
				"@foo:@bar":    "@@example+//@foo:@bar",
			} {
				newLabel, err := base.AppendLabel(input)
				require.NoError(t, err)
				assert.Equal(t, output, newLabel.String())
			}
		})
		t.Run("InsidePackage", func(t *testing.T) {
			base := util.Must(label.NewCanonicalPackage("@@example+//foo"))
			for input, output := range map[string]string{
				":foo":                "@@example+//foo",
				"bar:wiz":             "@@example+//foo/bar:wiz",
				"bar/wiz":             "@@example+//foo:bar/wiz",
				"bar:all":             "@@example+//foo/bar:all",
				":all":                "@@example+//foo:all",
				"//baz":               "@@example+//baz",
				"//:example+":         "@@example+",
				"@@other1+":           "@@other1+",
				"@@other1+//:other1+": "@@other1+",
				"@@other1+//:foo":     "@@other1+//:foo",
				"@other2":             "@other2",
				"@other2//:other2":    "@other2",
				"@other2//:foo":       "@other2//:foo",
			} {
				newLabel, err := base.AppendLabel(input)
				require.NoError(t, err)
				assert.Equal(t, output, newLabel.String())
			}
		})
	})

	t.Run("AppendTargetName", func(t *testing.T) {
		require.Equal(
			t,
			"@@example+//:foo",
			util.Must(label.NewCanonicalPackage("@@example+")).
				AppendTargetName(util.Must(label.NewTargetName("foo"))).
				String(),
		)
		require.Equal(
			t,
			"@@example+",
			util.Must(label.NewCanonicalPackage("@@example+")).
				AppendTargetName(util.Must(label.NewTargetName("example+"))).
				String(),
		)
		require.Equal(
			t,
			"@@example+//hello_world:foo",
			util.Must(label.NewCanonicalPackage("@@example+//hello_world")).
				AppendTargetName(util.Must(label.NewTargetName("foo"))).
				String(),
		)
		require.Equal(
			t,
			"@@example+//hello_world",
			util.Must(label.NewCanonicalPackage("@@example+//hello_world")).
				AppendTargetName(util.Must(label.NewTargetName("hello_world"))).
				String(),
		)
	})

	t.Run("AppendTargetPattern", func(t *testing.T) {
		base := util.Must(label.NewCanonicalPackage("@@example+//foo"))
		for input, output := range map[string]string{
			"//foo/bar:wiz":         "@@example+//foo/bar:wiz",
			"//foo/bar":             "@@example+//foo/bar",
			"//foo/bar:bar":         "@@example+//foo/bar",
			"//foo/bar:all":         "@@example+//foo/bar:all",
			"//foo/...":             "@@example+//foo/...",
			"//foo/...:all":         "@@example+//foo/...",
			"//foo/...:*":           "@@example+//foo/...:*",
			"//foo/...:all-targets": "@@example+//foo/...:*",
			"//...":                 "@@example+//...",
			"//:all":                "@@example+//:all",
			":foo":                  "@@example+//foo",
			"bar:wiz":               "@@example+//foo/bar:wiz",
			// According to the Bazel documentation, "bar/wiz"
			// may also expand to @example+//foo/bar/wiz or
			// @example+//foo/bar:wiz, depending on whether
			// those packages exist.
			//
			// This is something we likely do not want to
			// support, as such relative patterns do not have
			// an absolute representation.
			"bar/wiz":     "@@example+//foo:bar/wiz",
			"bar:all":     "@@example+//foo/bar:all",
			":all":        "@@example+//foo:all",
			"...:all":     "@@example+//foo/...",
			"...":         "@@example+//foo/...",
			"bar/...":     "@@example+//foo/bar/...",
			"bar/...:all": "@@example+//foo/bar/...",

			"//all:all":     "@@example+//all:all",
			"//all":         "@@example+//all",
			"@@foo+":        "@@foo+",
			"@@foo+//:foo+": "@@foo+",
			"@@foo+//:bar":  "@@foo+//:bar",
			"@foo":          "@foo",
			"@foo//:foo":    "@foo",
			"@foo//:bar":    "@foo//:bar",
			"@all//:all":    "@all//:all",
			"@@//...":       "@@//...",
		} {
			newLabel, err := base.AppendTargetPattern(input)
			require.NoError(t, err)
			assert.Equal(t, output, newLabel.String())
		}
	})

	t.Run("AppendPackagePath", func(t *testing.T) {
		t.Run("Invalid", func(t *testing.T) {
			base := util.Must(label.NewCanonicalPackage("@@repo+"))
			for _, input := range []string{
				"",
				"foo//bar",
				"./foo",
				"foo/",
			} {
				_, err := base.AppendPackagePath(input)
				assert.ErrorContains(t, err, "non-empty package name must match ", input)
			}
		})

		t.Run("Valid", func(t *testing.T) {
			canonicalPackage, err := util.Must(label.NewCanonicalPackage("@@repo+")).AppendPackagePath("foo/bar")
			require.NoError(t, err)
			assert.Equal(t, "@@repo+//foo/bar", canonicalPackage.String())

			canonicalPackage, err = util.Must(label.NewCanonicalPackage("@@repo+//foo")).AppendPackagePath("bar")
			require.NoError(t, err)
			assert.Equal(t, "@@repo+//foo/bar", canonicalPackage.String())
		})
	})

	t.Run("IsAtOrBelow", func(t *testing.T) {
		for _, testCase := range []struct {
			p, other string
			expected bool
		}{
			{"@@repo+", "@@repo+", true},
			{"@@repo+//foo", "@@repo+", true},
			{"@@repo+//foo/bar", "@@repo+", true},
			{"@@repo+//foo", "@@repo+//foo", true},
			{"@@repo+//foo/bar", "@@repo+//foo", true},
			{"@@repo+", "@@repo+//foo", false},
			{"@@repo+//foobar", "@@repo+//foo", false},
			{"@@repo+//bar", "@@repo+//foo", false},
			{"@@repo+x", "@@repo+", false},
			{"@@repo+x//foo", "@@repo+", false},
		} {
			assert.Equal(
				t,
				testCase.expected,
				util.Must(label.NewCanonicalPackage(testCase.p)).IsAtOrBelow(util.Must(label.NewCanonicalPackage(testCase.other))),
				"%s at or below %s", testCase.p, testCase.other,
			)
		}
	})

	t.Run("ToRecursiveTargetPattern", func(t *testing.T) {
		// Target patterns should be convertible back to the
		// package from which they were created.
		for _, input := range []string{
			"@@repo+",
			"@@repo+//foo",
			"@@repo+//foo/bar",
		} {
			t.Run(input, func(t *testing.T) {
				canonicalPackage := util.Must(label.NewCanonicalPackage(input))
				for _, includeFileTargets := range []bool{false, true} {
					basePackage, gotIncludeFileTargets, ok := canonicalPackage.ToRecursiveTargetPattern(includeFileTargets).AsRecursiveTargetPattern()
					require.True(t, ok)
					assert.Equal(t, input, basePackage.String())
					assert.Equal(t, includeFileTargets, gotIncludeFileTargets)
				}
			})
		}

		canonicalPackage := util.Must(label.NewCanonicalPackage("@@repo+"))
		assert.Equal(t, "@@repo+//...", canonicalPackage.ToRecursiveTargetPattern(false).String())
		assert.Equal(t, "@@repo+//...:*", canonicalPackage.ToRecursiveTargetPattern(true).String())

		canonicalPackage = util.Must(label.NewCanonicalPackage("@@repo+//foo"))
		assert.Equal(t, "@@repo+//foo/...", canonicalPackage.ToRecursiveTargetPattern(false).String())
		assert.Equal(t, "@@repo+//foo/...:*", canonicalPackage.ToRecursiveTargetPattern(true).String())
	})

	t.Run("ToTargetsQuery", func(t *testing.T) {
		for input, expected := range map[string][2]string{
			"@@repo+":          {"rules(siblings(@@repo+//:*))", "siblings(@@repo+//:*)"},
			"@@repo+//foo/bar": {"rules(siblings(@@repo+//foo/bar:*))", "siblings(@@repo+//foo/bar:*)"},
		} {
			t.Run(input, func(t *testing.T) {
				canonicalPackage := util.Must(label.NewCanonicalPackage(input))
				for i, includeFileTargets := range []bool{false, true} {
					query := canonicalPackage.ToTargetsQuery(includeFileTargets)
					assert.Equal(t, expected[i], label.QueryToString(query))

					// The query should be in normal form.
					parsed, err := label.NewQuery(expected[i], label.NewCanonicalTargetPattern)
					require.NoError(t, err)
					assert.Equal(t, parsed, query)
				}
			})
		}
	})
}
