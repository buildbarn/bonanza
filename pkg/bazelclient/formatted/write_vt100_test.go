package formatted_test

import (
	"strings"
	"testing"

	"bonanza.build/pkg/bazelclient/formatted"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteVT100(t *testing.T) {
	t.Run("Text", func(t *testing.T) {
		var b strings.Builder
		n, err := formatted.WriteVT100(
			formatted.Text("Hello, world!"),
			&b,
		)
		require.NoError(t, err)
		assert.Equal(t, 13, n)
		assert.Equal(t, "Hello, world!", b.String())
	})

	t.Run("Textf", func(t *testing.T) {
		var b strings.Builder
		n, err := formatted.WriteVT100(
			formatted.Textf("%d + %d = %d", 2, 2, 4),
			&b,
		)
		require.NoError(t, err)
		assert.Equal(t, 9, n)
		assert.Equal(t, "2 + 2 = 4", b.String())
	})

	t.Run("Join", func(t *testing.T) {
		var b strings.Builder
		n, err := formatted.WriteVT100(
			formatted.Join(
				formatted.Text("Hello"),
				formatted.Text(", "),
				formatted.Text("world"),
				formatted.Text("!"),
			),
			&b,
		)
		require.NoError(t, err)
		assert.Equal(t, 13, n)
		assert.Equal(t, "Hello, world!", b.String())
	})

	t.Run("BoldRed", func(t *testing.T) {
		var b strings.Builder
		n, err := formatted.WriteVT100(
			formatted.Bold(formatted.Red(formatted.Text("Danger"))),
			&b,
		)
		require.NoError(t, err)
		assert.Equal(t, 21, n)
		assert.Equal(t, "\x1b[1;31mDanger\x1b[22;39m", b.String())
	})

	t.Run("ProgressBarPartiallyFilled", func(t *testing.T) {
		var b strings.Builder
		_, err := formatted.WriteVT100(
			formatted.ProgressBar(
				7,
				formatted.Join(
					formatted.Text("Do"),
					formatted.Text(""),
					formatted.Text("wn"),
					formatted.Bold(formatted.Text("loading")),
				),
			),
			&b,
		)
		require.NoError(t, err)
		assert.Equal(t, "\x1b[100mDown\x1b[1mloa\x1b[49mding\x1b[22m", b.String())
	})

	t.Run("ProgressBarPadded", func(t *testing.T) {
		var b strings.Builder
		_, err := formatted.WriteVT100(
			formatted.ProgressBar(6, formatted.Text("Done")),
			&b,
		)
		require.NoError(t, err)
		assert.Equal(t, "\x1b[100mDone  \x1b[49m", b.String())
	})

	t.Run("ProgressBarPlainText", func(t *testing.T) {
		var b strings.Builder
		_, err := formatted.WritePlainText(
			formatted.ProgressBar(6, formatted.Text("Done")),
			&b,
		)
		require.NoError(t, err)
		assert.Equal(t, "Done", b.String())
	})

	t.Run("TruncateFits", func(t *testing.T) {
		var b strings.Builder
		_, err := formatted.WriteVT100(
			formatted.Truncate(5, formatted.Text("Hello")),
			&b,
		)
		require.NoError(t, err)
		assert.Equal(t, "Hello", b.String())
	})

	t.Run("TruncateAcrossNodes", func(t *testing.T) {
		var b strings.Builder
		_, err := formatted.WriteVT100(
			formatted.Truncate(
				6,
				formatted.Join(
					formatted.Text("Get "),
					formatted.Bold(formatted.Text("https://example.com")),
				),
			),
			&b,
		)
		require.NoError(t, err)
		assert.Equal(t, "Get \x1b[1mh\x1b[22m…", b.String())
	})

	t.Run("TruncateWithProgressBar", func(t *testing.T) {
		var b strings.Builder
		_, err := formatted.WriteVT100(
			formatted.ProgressBar(
				4,
				formatted.Join(
					formatted.Truncate(4, formatted.Text("Downloading")),
					formatted.Text(" 50%"),
				),
			),
			&b,
		)
		require.NoError(t, err)
		assert.Equal(t, "\x1b[100mDow…\x1b[49m 50%", b.String())
	})

	t.Run("TruncatePlainText", func(t *testing.T) {
		var b strings.Builder
		_, err := formatted.WritePlainText(
			formatted.Truncate(4, formatted.Text("Downloading")),
			&b,
		)
		require.NoError(t, err)
		assert.Equal(t, "Dow…", b.String())
	})
}
