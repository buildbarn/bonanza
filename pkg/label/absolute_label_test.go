package label_test

import (
	"testing"

	"bonanza.build/pkg/label"

	"github.com/stretchr/testify/assert"
)

func TestNewAbsoluteLabel(t *testing.T) {
	t.Run("Valid", func(t *testing.T) {
		for _, input := range []string{
			"//baz",
			"//:example+",
			"@other",
			"@other//:foo",
			"@@other+",
			"@@other+//foo:bar",
			"@@//foo:bar",
		} {
			l, err := label.NewAbsoluteLabel(input)
			if assert.NoError(t, err, input) {
				assert.Equal(t, input, l.String())
			}
		}
	})

	t.Run("RedundantTargetName", func(t *testing.T) {
		for input, output := range map[string]string{
			"//foo:foo":           "//foo",
			"//foo/bar:bar":       "//foo/bar",
			"//:foo":              "//:foo",
			"@other//:other":      "@other",
			"@other//foo:foo":     "@other//foo",
			"@@other+//:other+":   "@@other+",
			"@@other+//foo:other": "@@other+//foo:other",
			"@@//:foo":            "@@//:foo",
		} {
			l, err := label.NewAbsoluteLabel(input)
			if assert.NoError(t, err, input) {
				assert.Equal(t, output, l.String(), input)
			}
		}
	})

	t.Run("Invalid", func(t *testing.T) {
		for _, input := range []string{
			"",
			"foo",
			":foo",
			"bar:wiz",
			"@foo/bar/baz",
			"@foo:@bar",
			"//",
			"@foo//",
			"//foo/...",
		} {
			_, err := label.NewAbsoluteLabel(input)
			assert.Error(t, err, input)
		}
	})
}
