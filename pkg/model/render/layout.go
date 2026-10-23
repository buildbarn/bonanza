package render

import (
	"unicode/utf8"
)

// Segment is a piece of text on a line that has been laid out. All
// text contained in a segment has the same attributes.
type Segment struct {
	Text  string
	Role  Role
	Link  *LinkTarget
	Title string
}

// TableBlock is a table that has been laid out. The contents of each
// cell are laid out on a single line.
type TableBlock struct {
	Columns []TableColumn
	Rows    [][][]Segment
}

// Block is either a line of text, or a table.
type Block struct {
	// The indentation level of the line or table.
	Indent int
	// The text on the line, if the block is a line.
	Segments []Segment
	// The contents of the table, if the block is a table.
	Table *TableBlock
}

// LayoutOptions controls how a document is laid out.
type LayoutOptions struct {
	// If set, all groups are laid out on a single line, regardless
	// of their length. Only tables cause line breaks to occur.
	SingleLine bool
	// The maximum number of characters a line may contain. If
	// zero, lines are not limited in length, meaning that line
	// breaks only occur around tables.
	Width int
	// The number of characters by which each level of indentation
	// is indented. This is only used to determine whether groups
	// fit on a line.
	IndentWidth int
}

type attributes struct {
	role  Role
	link  *LinkTarget
	title string
}

type layoutItem struct {
	indent     int
	broken     bool
	attributes attributes
	doc        Doc
}

type layoutState struct {
	options LayoutOptions
	column  int
	current Block
	blocks  []Block
	// Set if the current line was started by a table, and no text
	// has been emitted on it yet.
	afterTable bool
}

func (ls *layoutState) emitText(s string, a attributes) {
	if s == "" {
		return
	}
	ls.afterTable = false
	segments := ls.current.Segments
	if n := len(segments); n > 0 {
		last := &segments[n-1]
		if last.Role == a.role && last.Link == a.link && last.Title == a.title {
			last.Text += s
			ls.column += utf8.RuneCountInString(s)
			return
		}
	}
	ls.current.Segments = append(segments, Segment{
		Text:  s,
		Role:  a.role,
		Link:  a.link,
		Title: a.title,
	})
	ls.column += utf8.RuneCountInString(s)
}

func (ls *layoutState) newLine(indent int) {
	if ls.afterTable {
		// A table always ends a line. Don't emit an empty
		// line if a line break directly follows it.
		ls.current.Indent = indent
		ls.column = indent * ls.options.IndentWidth
		return
	}
	ls.blocks = append(ls.blocks, ls.current)
	ls.current = Block{Indent: indent}
	ls.column = indent * ls.options.IndentWidth
}

// fits returns whether the document in the first item can be placed on
// the current line in its entirety, followed by the text in the
// remaining items up to the first line break.
func (ls *layoutState) fits(first layoutItem, rest []layoutItem) bool {
	if ls.options.SingleLine || ls.options.Width <= 0 {
		return true
	}
	remaining := ls.options.Width - ls.column
	stack := []layoutItem{first}
	for remaining >= 0 {
		var item layoutItem
		if n := len(stack); n > 0 {
			item = stack[n-1]
			stack = stack[:n-1]
		} else if n := len(rest); n > 0 {
			item = rest[n-1]
			rest = rest[:n-1]
		} else {
			return true
		}

		switch d := item.doc.(type) {
		case textDoc:
			remaining -= utf8.RuneCountInString(d.text)
		case concatDoc:
			for i := len(d.parts) - 1; i >= 0; i-- {
				stack = append(stack, layoutItem{indent: item.indent, broken: item.broken, doc: d.parts[i]})
			}
		case lineDoc:
			if item.broken {
				return true
			}
			remaining -= len(d.flat)
		case nestDoc:
			stack = append(stack, layoutItem{indent: item.indent + 1, broken: item.broken, doc: d.child})
		case groupDoc:
			stack = append(stack, layoutItem{indent: item.indent, doc: d.child})
		case ifBrokenDoc:
			if item.broken {
				stack = append(stack, layoutItem{indent: item.indent, broken: true, doc: d.broken})
			} else {
				stack = append(stack, layoutItem{indent: item.indent, doc: d.flat})
			}
		case annotateDoc:
			stack = append(stack, layoutItem{indent: item.indent, broken: item.broken, doc: d.child})
		case tableDoc:
			return false
		}
	}
	return false
}

func (ls *layoutState) layout(doc Doc, indent int) {
	stack := []layoutItem{{indent: indent, broken: true, doc: doc}}
	for len(stack) > 0 {
		item := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		switch d := item.doc.(type) {
		case textDoc:
			a := item.attributes
			if d.role != RolePlain {
				a.role = d.role
			}
			ls.emitText(d.text, a)
		case concatDoc:
			for i := len(d.parts) - 1; i >= 0; i-- {
				stack = append(stack, layoutItem{
					indent:     item.indent,
					broken:     item.broken,
					attributes: item.attributes,
					doc:        d.parts[i],
				})
			}
		case lineDoc:
			if item.broken {
				ls.newLine(item.indent)
			} else {
				ls.emitText(d.flat, attributes{})
			}
		case nestDoc:
			stack = append(stack, layoutItem{
				indent:     item.indent + 1,
				broken:     item.broken,
				attributes: item.attributes,
				doc:        d.child,
			})
		case groupDoc:
			child := layoutItem{
				indent:     item.indent,
				attributes: item.attributes,
				doc:        d.child,
			}
			if item.broken && !ls.fits(child, stack) {
				child.broken = true
			}
			stack = append(stack, child)
		case ifBrokenDoc:
			child := layoutItem{
				indent:     item.indent,
				broken:     item.broken,
				attributes: item.attributes,
				doc:        d.flat,
			}
			if item.broken {
				child.doc = d.broken
			}
			stack = append(stack, child)
		case annotateDoc:
			a := item.attributes
			if d.link != nil {
				a.link = d.link
			}
			if d.title != "" {
				a.title = d.title
			}
			stack = append(stack, layoutItem{
				indent:     item.indent,
				broken:     item.broken,
				attributes: a,
				doc:        d.child,
			})
		case tableDoc:
			ls.layoutTable(d, item.indent)
		}
	}
}

func (ls *layoutState) layoutTable(d tableDoc, indent int) {
	// Tables always start on a line of their own. If the table is
	// preceded by text on the current line (e.g., the name of a
	// field), ensure the table is indented relative to that line,
	// to indicate that it belongs to it.
	tableIndent := indent
	if len(ls.current.Segments) > 0 {
		ls.blocks = append(ls.blocks, ls.current)
		tableIndent = max(tableIndent, ls.current.Indent+1)
	}
	table := &TableBlock{
		Columns: d.columns,
		Rows:    make([][][]Segment, 0, len(d.rows)),
	}
	for _, row := range d.rows {
		cells := make([][]Segment, 0, len(row))
		for _, cell := range row {
			cellState := layoutState{
				options: LayoutOptions{SingleLine: true},
			}
			cellState.layout(cell, 0)
			cells = append(cells, cellState.current.Segments)
		}
		table.Rows = append(table.Rows, cells)
	}
	ls.blocks = append(ls.blocks, Block{Indent: tableIndent, Table: table})
	ls.current = Block{Indent: indent}
	ls.column = indent * ls.options.IndentWidth
	ls.afterTable = true
}

// Layout a document, converting it to a sequence of lines and tables.
func Layout(doc Doc, options LayoutOptions) []Block {
	ls := layoutState{options: options}
	ls.layout(doc, 0)
	if len(ls.current.Segments) > 0 || len(ls.blocks) == 0 {
		ls.blocks = append(ls.blocks, ls.current)
	}
	return ls.blocks
}

// PlainText converts blocks of laid out text to a string, discarding
// all highlighting and links. Tables are emitted with tab separated
// columns. This is mainly intended for testing.
func PlainText(blocks []Block, indentation string) string {
	var out []byte
	writeSegments := func(segments []Segment) {
		for _, segment := range segments {
			out = append(out, segment.Text...)
		}
	}
	for i, block := range blocks {
		if i > 0 {
			out = append(out, '\n')
		}
		if block.Table == nil {
			if len(block.Segments) > 0 {
				for range block.Indent {
					out = append(out, indentation...)
				}
			}
			writeSegments(block.Segments)
		} else {
			for j, row := range block.Table.Rows {
				if j > 0 {
					out = append(out, '\n')
				}
				for range block.Indent {
					out = append(out, indentation...)
				}
				for k, cell := range row {
					if k > 0 {
						out = append(out, '\t')
					}
					writeSegments(cell)
				}
			}
		}
	}
	return string(out)
}
