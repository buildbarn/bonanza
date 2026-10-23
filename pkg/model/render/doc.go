// Package render can be used to convert Protobuf messages to
// human-readable text, either as JSON or using a syntax that is
// specific to the type of message (e.g., Starlark values are displayed
// using Starlark syntax).
//
// Messages are first converted to a document (Doc). Documents only
// describe the structure of the output, and the semantic role of each
// piece of text. A document can subsequently be laid out (see Layout),
// either on a single line or across multiple lines of a given width.
// The resulting lines can then be written by a backend, such as one
// that emits HTML or VT100 escape sequences.
//
// The layout algorithm is based on Philip Wadler's "A prettier
// printer".
package render

import (
	model_core "bonanza.build/pkg/model/core"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	"bonanza.build/pkg/storage/object"
)

// Role describes the semantic meaning of a piece of text. Backends may
// use it to apply syntax highlighting.
type Role int

const (
	// RolePlain is used for punctuation and anything else that
	// does not need to be highlighted.
	RolePlain Role = iota
	// RoleFieldName is used for the names of fields of a JSON
	// object, keyword arguments of a function call, etc.
	RoleFieldName
	// RoleLiteral is used for string, number and boolean literals.
	RoleLiteral
	// RoleCallee is used for the names of functions that are
	// called, such as "struct" in "struct(a = 1)".
	RoleCallee
	// RoleDim is used for text that is less important, such as
	// values that have no literal representation, comments, or
	// repeated prefixes of paths.
	RoleDim
	// RoleReference is used for references to objects.
	RoleReference
	// RoleError is used for error messages that are embedded in
	// the output.
	RoleError
)

// LinkTarget is the target of a link contained in a document. As
// documents are independent of how they are displayed, links point to
// objects, as opposed to containing URLs. Backends are responsible for
// converting them to URLs.
type LinkTarget struct {
	Reference    model_core.Decodable[object.LocalReference]
	ObjectFormat *model_core_pb.ObjectFormat
}

// Doc is a document that can be laid out using Layout().
type Doc interface {
	isDoc()
}

type textDoc struct {
	text string
	role Role
}

type concatDoc struct {
	parts []Doc
}

type lineDoc struct {
	flat string
}

type nestDoc struct {
	child Doc
}

type groupDoc struct {
	child Doc
}

type ifBrokenDoc struct {
	broken Doc
	flat   Doc
}

type annotateDoc struct {
	link  *LinkTarget
	title string
	child Doc
}

type tableDoc struct {
	columns []TableColumn
	rows    [][]Doc
}

func (textDoc) isDoc()     {}
func (concatDoc) isDoc()   {}
func (lineDoc) isDoc()     {}
func (nestDoc) isDoc()     {}
func (groupDoc) isDoc()    {}
func (ifBrokenDoc) isDoc() {}
func (annotateDoc) isDoc() {}
func (tableDoc) isDoc()    {}

// Text emits literal text that does not need to be highlighted. The
// text must not contain any newline characters.
func Text(s string) Doc {
	return textDoc{text: s}
}

// Styled emits literal text that has a given semantic role.
func Styled(role Role, s string) Doc {
	return textDoc{text: s, role: role}
}

// Concat a sequence of documents.
func Concat(parts ...Doc) Doc {
	return concatDoc{parts: parts}
}

// Line is a line break that is replaced by a single space if the
// enclosing group is laid out on a single line.
func Line() Doc {
	return lineDoc{flat: " "}
}

// SoftLine is a line break that is removed if the enclosing group is
// laid out on a single line.
func SoftLine() Doc {
	return lineDoc{}
}

// Nest increases the indentation of any line breaks contained in a
// document by one level.
func Nest(child Doc) Doc {
	return nestDoc{child: child}
}

// Group a document, so that all line breaks contained in it are either
// replaced by their single line representation if the document fits on
// the current line, or retained otherwise. Nested groups make this
// decision independently.
func Group(child Doc) Doc {
	return groupDoc{child: child}
}

// IfBroken emits one of two documents, depending on whether the
// enclosing group is laid out across multiple lines. This can, for
// example, be used to emit trailing commas.
func IfBroken(broken, flat Doc) Doc {
	return ifBrokenDoc{broken: broken, flat: flat}
}

// Link causes the text contained in a document to link to an object.
func Link(target LinkTarget, child Doc) Doc {
	return annotateDoc{link: &target, child: child}
}

// Title attaches a description to the text contained in a document.
// Backends that support it may display it when hovering the text.
func Title(title string, child Doc) Doc {
	return annotateDoc{title: title, child: child}
}

// TableColumn describes a column of a table.
type TableColumn struct {
	// The name of the column, which may be displayed as a header.
	Name string
	// If set, the contents of the cells in this column are aligned
	// to the right. This is useful for columns containing numbers.
	AlignRight bool
}

// Table emits a table. Each row consists of cells whose contents are
// laid out on a single line. If a row contains fewer cells than the
// number of columns, the last cell spans the remaining columns.
//
// As tables cannot be displayed on a single line, a table always
// starts on a new line, causing the enclosing group to be broken.
func Table(columns []TableColumn, rows [][]Doc) Doc {
	return tableDoc{columns: columns, rows: rows}
}

// Sequence emits a sequence of elements, enclosed by delimiters and
// separated by commas. Examples include JSON arrays and objects, and
// the arguments of a Starlark function call. If the sequence does not
// fit on a single line, each element is placed on a separate line.
//
// If trailingComma is set, a trailing comma is emitted after the last
// element if the sequence is spread across multiple lines. Starlark
// syntax permits this, while JSON does not. If forceTrailingComma is
// set, it is also emitted if the sequence is placed on a single line.
// This is needed to render tuples containing a single element.
func Sequence(open, close Doc, elements []Doc, trailingComma, forceTrailingComma bool) Doc {
	if len(elements) == 0 {
		return Concat(open, close)
	}
	parts := make([]Doc, 0, 2*len(elements)+1)
	parts = append(parts, SoftLine())
	for i, element := range elements {
		if i > 0 {
			parts = append(parts, Text(","), Line())
		}
		parts = append(parts, element)
	}
	if forceTrailingComma {
		parts = append(parts, Text(","))
	} else if trailingComma {
		parts = append(parts, IfBroken(Text(","), Text("")))
	}
	return Group(
		Concat(
			open,
			Nest(Concat(parts...)),
			SoftLine(),
			close,
		),
	)
}
