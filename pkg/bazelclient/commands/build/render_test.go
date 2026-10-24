package build

import (
	"strings"
	"testing"

	"bonanza.build/pkg/bazelclient/formatted"
	"bonanza.build/pkg/model/render"

	"github.com/stretchr/testify/require"
)

func plainText(t *testing.T, n formatted.Node) string {
	var sb strings.Builder
	_, err := formatted.WritePlainText(n, &sb)
	require.NoError(t, err)
	return sb.String()
}

func TestMessageFormatter(t *testing.T) {
	var mf messageFormatter
	doc := render.Sequence(
		render.Concat(render.Styled(render.RoleCallee, "struct"), render.Text("(")),
		render.Text(")"),
		[]render.Doc{
			render.Concat(render.Styled(render.RoleFieldName, "a"), render.Text(" = "), render.Styled(render.RoleLiteral, `"xxxxxxxxxx"`)),
			render.Concat(render.Styled(render.RoleFieldName, "b"), render.Text(" = "), render.Styled(render.RoleReference, `"0123456789abcdef"`)),
		},
		true,
		false,
	)

	t.Run("SingleLine", func(t *testing.T) {
		require.Equal(
			t,
			`struct(a = "xxxxxxxxxx", b = "01234567...")`,
			plainText(t, mf.formatSingleLine(doc)),
		)
	})

	t.Run("MultiLine", func(t *testing.T) {
		// Sequences that don't fit should be spread across
		// multiple lines, using the provided indentation.
		require.Equal(
			t,
			"struct(\n  a = \"xxxxxxxxxx\",\n  b = \"01234567...\",\n)\n",
			plainText(t, FormatDoc(doc, 20, "  ")),
		)
		require.Equal(
			t,
			"struct(a = \"xxxxxxxxxx\", b = \"01234567...\")\n",
			plainText(t, FormatDoc(doc, 80, "  ")),
		)
	})
}
