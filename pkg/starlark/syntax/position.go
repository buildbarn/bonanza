package syntax

import (
	"cmp"
	"fmt"
	"strings"
)

// Position of a byte within a source file.
//
// As Starlark source files are interpreted as sequences of bytes, the
// column number counts bytes, not Unicode code points.
type Position struct {
	// 1-based line number. Zero if the position is unknown.
	Line int32
	// 1-based column number in bytes. Zero if the column is unknown.
	Column int32
}

// IsValid returns true if the position is known.
func (p Position) IsValid() bool {
	return p.Line > 0
}

// String returns the position in "line:column" form.
func (p Position) String() string {
	return fmt.Sprintf("%d:%d", p.Line, p.Column)
}

// Compare two positions, returning a negative value if p comes before
// q, a positive value if p comes after q, and zero if they are equal.
func (p Position) Compare(q Position) int {
	return cmp.Or(cmp.Compare(p.Line, q.Line), cmp.Compare(p.Column, q.Column))
}

// add returns the position at the end of s, assuming s starts at p.
func (p Position) add(s string) Position {
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		p.Line += int32(strings.Count(s, "\n"))
		p.Column = 1
		s = s[i+1:]
	}
	p.Column += int32(len(s))
	return p
}

// Error that is returned by the parser in case a source file contains
// a syntax error.
type Error struct {
	Filename string
	Pos      Position
	Message  string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s:%s: %s", e.Filename, e.Pos, e.Message)
}
