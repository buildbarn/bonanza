package label_test

import (
	"cmp"
	"testing"

	"bonanza.build/pkg/label"

	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/assert"
)

func TestCanonicalLabel(t *testing.T) {
	t.Run("ValidNormalized", func(t *testing.T) {
		for _, input := range []string{
			"@@com_github_buildbarn_bb_storage+",
			"@@com_github_buildbarn_bb_storage+//:foo",
			"@@com_github_buildbarn_bb_storage+//cmd/hello_world",
			"@@com_github_buildbarn_bb_storage+//cmd/hello_world:go_default_library",
			`@@com_github_buildbarn_bb_storage+//cmd/! "#$%&'()*+,-.;<=>?@[]^_{|}` + "`",
			`@@com_github_buildbarn_bb_storage+//cmd/ℕ ⊆ ℕ₀ ⊂ ℤ ⊂ ℚ ⊂ ℝ ⊂ ℂ`,
			`@@com_github_buildbarn_bb_storage+//cmd/hello_world:ℕ ⊆ ℕ₀ ⊂ ℤ ⊂ ℚ ⊂ ℝ ⊂ ℂ`,
		} {
			canonicalLabel := util.Must(label.NewCanonicalLabel(input))
			assert.Equal(t, input, canonicalLabel.String())
		}
	})

	t.Run("ValidDenormalized", func(t *testing.T) {
		for input, output := range map[string]string{
			"@@com_github_buildbarn_bb_storage+//:com_github_buildbarn_bb_storage+": "@@com_github_buildbarn_bb_storage+",
			"@@com_github_buildbarn_bb_storage+//cmd:cmd":                           "@@com_github_buildbarn_bb_storage+//cmd",
			"@@com_github_buildbarn_bb_storage+//cmd/hello_world:hello_world":       "@@com_github_buildbarn_bb_storage+//cmd/hello_world",
		} {
			canonicalLabel := util.Must(label.NewCanonicalLabel(input))
			assert.Equal(t, output, canonicalLabel.String())
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
			"@@com_github_buildbarn_bb_storage+:target",
			"@@com_github_buildbarn_bb_storage+//cmd//hello_world",
			"@@com_github_buildbarn_bb_storage+//cmd/./hello_world",
			"@@com_github_buildbarn_bb_storage+//cmd/../hello_world",
			"@@com_github_buildbarn_bb_storage+//cmd/.../hello_world",
			"@@com_github_buildbarn_bb_storage+//cmd/..../hello_world",
			"@@com_github_buildbarn_bb_storage+///cmd/hello_world",
			"@@com_github_buildbarn_bb_storage+//cmd/hello_world/",
			"@@com_github_buildbarn_bb_storage+//foo\nbar",
		} {
			_, err := label.NewCanonicalLabel(input)
			assert.ErrorContains(t, err, "canonical label must match ", input)
		}
	})

	t.Run("GetCanonicalPackage", func(t *testing.T) {
		for input, output := range map[string]string{
			"@@com_github_buildbarn_bb_storage+":                                     "@@com_github_buildbarn_bb_storage+",
			"@@com_github_buildbarn_bb_storage+//:foo":                               "@@com_github_buildbarn_bb_storage+",
			"@@com_github_buildbarn_bb_storage+//cmd/hello_world":                    "@@com_github_buildbarn_bb_storage+//cmd/hello_world",
			"@@com_github_buildbarn_bb_storage+//cmd/hello_world:go_default_library": "@@com_github_buildbarn_bb_storage+//cmd/hello_world",
		} {
			canonicalLabel := util.Must(label.NewCanonicalLabel(input))
			assert.Equal(t, output, canonicalLabel.GetCanonicalPackage().String())
		}
	})

	t.Run("GetTargetName", func(t *testing.T) {
		for input, output := range map[string]string{
			"@@com_github_buildbarn_bb_storage+":                  "com_github_buildbarn_bb_storage+",
			"@@com_github_buildbarn_bb_storage+//:foo":            "foo",
			"@@com_github_buildbarn_bb_storage+//cmd":             "cmd",
			"@@com_github_buildbarn_bb_storage+//cmd/hello_world": "hello_world",
		} {
			canonicalLabel := util.Must(label.NewCanonicalLabel(input))
			assert.Equal(t, output, canonicalLabel.GetTargetName().String())
		}
	})

	t.Run("GetExternalRelativePath", func(t *testing.T) {
		for input, output := range map[string]string{
			"@@com_github_buildbarn_bb_storage+":                      "com_github_buildbarn_bb_storage+/com_github_buildbarn_bb_storage+",
			"@@com_github_buildbarn_bb_storage+//:foo":                "com_github_buildbarn_bb_storage+/foo",
			"@@com_github_buildbarn_bb_storage+//cmd":                 "com_github_buildbarn_bb_storage+/cmd/cmd",
			"@@com_github_buildbarn_bb_storage+//cmd:bar":             "com_github_buildbarn_bb_storage+/cmd/bar",
			"@@com_github_buildbarn_bb_storage+//cmd/hello_world":     "com_github_buildbarn_bb_storage+/cmd/hello_world/hello_world",
			"@@com_github_buildbarn_bb_storage+//cmd/hello_world:baz": "com_github_buildbarn_bb_storage+/cmd/hello_world/baz",
		} {
			canonicalLabel := util.Must(label.NewCanonicalLabel(input))
			assert.Equal(t, output, canonicalLabel.GetExternalRelativePath())
		}
	})

	t.Run("GetRepoRelativePath", func(t *testing.T) {
		for input, output := range map[string]string{
			"@@com_github_buildbarn_bb_storage+":                      "com_github_buildbarn_bb_storage+",
			"@@com_github_buildbarn_bb_storage+//:foo":                "foo",
			"@@com_github_buildbarn_bb_storage+//cmd":                 "cmd/cmd",
			"@@com_github_buildbarn_bb_storage+//cmd:bar":             "cmd/bar",
			"@@com_github_buildbarn_bb_storage+//cmd/hello_world":     "cmd/hello_world/hello_world",
			"@@com_github_buildbarn_bb_storage+//cmd/hello_world:baz": "cmd/hello_world/baz",
		} {
			canonicalLabel := util.Must(label.NewCanonicalLabel(input))
			assert.Equal(t, output, canonicalLabel.GetRepoRelativePath())
		}
	})
}

func TestCanonicalLabelCompare(t *testing.T) {
	// Labels in sorted order. Package paths are compared by their
	// components, and targets in a package precede the targets in
	// its subpackages. Labels with redundant target names (e.g.,
	// "@@a+//foo" for "@@a+//foo:foo") are ordered by their target
	// name.
	labels := []string{
		"@@a+//:z",
		"@@a+//foo:a",
		"@@a+//foo",
		"@@a+//foo:z",
		"@@a+//foo/bar:x",
		"@@a+//foo/bar/baz:x",
		"@@a+//foo/bar-x:x",
		"@@a+//foo/bar2:x",
		"@@a+//foo2:x",
		"@@b+//:a",
	}
	for i, a := range labels {
		for j, b := range labels {
			t.Run(a+" "+b, func(t *testing.T) {
				assert.Equal(
					t,
					cmp.Compare(i, j),
					util.Must(label.NewCanonicalLabel(a)).Compare(util.Must(label.NewCanonicalLabel(b))),
				)
			})
		}
	}
}
