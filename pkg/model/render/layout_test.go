package render_test

import (
	"testing"

	"bonanza.build/pkg/model/render"

	"github.com/stretchr/testify/require"
)

func list(elements ...string) render.Doc {
	docs := make([]render.Doc, 0, len(elements))
	for _, element := range elements {
		docs = append(docs, render.Styled(render.RoleLiteral, element))
	}
	return render.Sequence(render.Text("["), render.Text("]"), docs, true, false)
}

func call(name string, arguments ...render.Doc) render.Doc {
	return render.Sequence(
		render.Concat(render.Styled(render.RoleCallee, name), render.Text("(")),
		render.Text(")"),
		arguments,
		true,
		false,
	)
}

func keyword(name string, value render.Doc) render.Doc {
	return render.Concat(render.Styled(render.RoleFieldName, name), render.Text(" = "), value)
}

func layout(doc render.Doc, options render.LayoutOptions) string {
	options.IndentWidth = 4
	return render.PlainText(render.Layout(doc, options), "    ")
}

func TestLayout(t *testing.T) {
	doc := call(
		"rule",
		keyword("implementation", render.Styled(render.RoleDim, "<function _impl>")),
		keyword("attrs", list(`"aaaaaaaaaa"`, `"bbbbbbbbbb"`, `"cccccccccc"`)),
		keyword("deps", list()),
	)

	t.Run("SingleLine", func(t *testing.T) {
		require.Equal(
			t,
			`rule(implementation = <function _impl>, attrs = ["aaaaaaaaaa", "bbbbbbbbbb", "cccccccccc"], deps = [])`,
			layout(doc, render.LayoutOptions{SingleLine: true, Width: 20}),
		)
	})

	t.Run("Unlimited", func(t *testing.T) {
		require.Equal(
			t,
			`rule(implementation = <function _impl>, attrs = ["aaaaaaaaaa", "bbbbbbbbbb", "cccccccccc"], deps = [])`,
			layout(doc, render.LayoutOptions{}),
		)
	})

	t.Run("OuterBroken", func(t *testing.T) {
		// The inner list fits on a line after breaking the
		// outer call. It should remain on a single line.
		require.Equal(
			t,
			"rule(\n"+
				"    implementation = <function _impl>,\n"+
				"    attrs = [\"aaaaaaaaaa\", \"bbbbbbbbbb\", \"cccccccccc\"],\n"+
				"    deps = [],\n"+
				")",
			layout(doc, render.LayoutOptions{Width: 60}),
		)
	})

	t.Run("AllBroken", func(t *testing.T) {
		// The width check must take the indentation and the
		// text preceding the list into account.
		require.Equal(
			t,
			"rule(\n"+
				"    implementation = <function _impl>,\n"+
				"    attrs = [\n"+
				"        \"aaaaaaaaaa\",\n"+
				"        \"bbbbbbbbbb\",\n"+
				"        \"cccccccccc\",\n"+
				"    ],\n"+
				"    deps = [],\n"+
				")",
			layout(doc, render.LayoutOptions{Width: 50}),
		)
	})

	t.Run("TrailingTextCounts", func(t *testing.T) {
		// "[1, 2]" is 6 characters and fits in a width of 6 on
		// its own. However, the text following it up to the
		// next line break also needs to fit on the same line.
		d := render.Concat(list("1", "2"), render.Text(")"))
		require.Equal(t, "[1, 2])", layout(d, render.LayoutOptions{Width: 7}))
		require.Equal(t, "[\n    1,\n    2,\n])", layout(d, render.LayoutOptions{Width: 6}))
	})

	t.Run("ForcedTrailingComma", func(t *testing.T) {
		d := render.Sequence(render.Text("("), render.Text(")"), []render.Doc{render.Text("1")}, true, true)
		require.Equal(t, "(1,)", layout(d, render.LayoutOptions{}))
	})
}

func TestLayoutAttributes(t *testing.T) {
	target := render.LinkTarget{}
	doc := render.Concat(
		render.Text("a"),
		render.Text("b"),
		render.Styled(render.RoleLiteral, "c"),
		render.Link(target, render.Concat(render.Text("d"), render.Styled(render.RoleLiteral, "e"))),
		render.Title("hello", render.Styled(render.RoleDim, "f")),
	)
	blocks := render.Layout(doc, render.LayoutOptions{})
	require.Len(t, blocks, 1)
	segments := blocks[0].Segments
	require.Len(t, segments, 5)
	require.Equal(t, render.Segment{Text: "ab"}, segments[0])
	require.Equal(t, render.Segment{Text: "c", Role: render.RoleLiteral}, segments[1])
	require.Equal(t, "d", segments[2].Text)
	require.NotNil(t, segments[2].Link)
	require.Equal(t, "e", segments[3].Text)
	require.Equal(t, render.RoleLiteral, segments[3].Role)
	require.NotNil(t, segments[3].Link)
	require.Equal(t, render.Segment{Text: "f", Role: render.RoleDim, Title: "hello"}, segments[4])
}

func TestLayoutTable(t *testing.T) {
	doc := call(
		"directory",
		render.Table(
			[]render.TableColumn{{Name: "Type"}, {Name: "Path"}},
			[][]render.Doc{
				{render.Text("dr-x"), render.Concat(render.Text("a"), render.Text("/"))},
				{render.Styled(render.RoleError, "error spanning both columns")},
			},
		),
	)
	blocks := render.Layout(doc, render.LayoutOptions{SingleLine: true})
	require.Len(t, blocks, 3)
	require.Equal(t, "directory(", render.PlainText(blocks[:1], ""))
	// As the table is preceded by text, it should be indented.
	require.Equal(t, 1, blocks[1].Indent)
	require.Equal(t, []render.TableColumn{{Name: "Type"}, {Name: "Path"}}, blocks[1].Table.Columns)
	require.Equal(t, [][][]render.Segment{
		{{{Text: "dr-x"}}, {{Text: "a/"}}},
		{{{Text: "error spanning both columns", Role: render.RoleError}}},
	}, blocks[1].Table.Rows)
	require.Equal(t, ")", render.PlainText(blocks[2:], ""))
}

func TestLayoutTableFollowedByLineBreak(t *testing.T) {
	// A line break directly following a table should not cause an
	// empty line to be emitted, as the table already ends a line.
	doc := render.Sequence(
		render.Text("{"),
		render.Text("}"),
		[]render.Doc{
			render.Concat(
				render.Text(`"rootDirectory": `),
				render.Table([]render.TableColumn{{Name: "Path"}}, [][]render.Doc{{render.Text("a")}}),
			),
		},
		false,
		false,
	)
	blocks := render.Layout(doc, render.LayoutOptions{Width: 80, IndentWidth: 4})
	require.Len(t, blocks, 4)
	require.Equal(t, "{", render.PlainText(blocks[0:1], "    "))
	require.Equal(t, `    "rootDirectory": `, render.PlainText(blocks[1:2], "    "))
	require.Equal(t, 2, blocks[2].Indent)
	require.NotNil(t, blocks[2].Table)
	require.Equal(t, render.Block{Indent: 0, Segments: []render.Segment{{Text: "}"}}}, blocks[3])
}
