package syntax

import (
	"strconv"
	"unicode/utf8"
)

// Quote returns a double quoted Starlark string literal that denotes
// s.
//
// Printable Unicode characters are emitted as is. Other characters,
// and bytes that are not part of valid UTF-8 sequences, are emitted as
// escape sequences. As octal escape sequences denote single bytes, the
// resulting literal is always parsed back to s.
func Quote(s string) string {
	buf := make([]byte, 0, len(s)+2)
	buf = append(buf, '"')
	buf, _ = AppendQuotedBody(buf, []byte(s), true)
	buf = append(buf, '"')
	return string(buf)
}

// AppendQuotedBody appends the body of a quoted string literal
// (i.e., without the surrounding quotes) for the bytes in s to buf. It
// can be used to quote strings that are processed in pieces. If final
// is false, processing stops before a trailing incomplete UTF-8
// sequence, so that it can be completed by the next piece. The number
// of bytes of s that were processed is returned.
func AppendQuotedBody(buf, s []byte, final bool) ([]byte, int) {
	processed := 0
	for len(s) > 0 {
		if !final && !utf8.FullRune(s) {
			break
		}
		r, width := utf8.DecodeRune(s)
		switch {
		case r == '"' || r == '\\':
			buf = append(buf, '\\', byte(r))
		case r == '\n':
			buf = append(buf, `\n`...)
		case r == '\r':
			buf = append(buf, `\r`...)
		case r == '\t':
			buf = append(buf, `\t`...)
		case r == utf8.RuneError && width == 1, !strconv.IsPrint(r):
			// Emit all bytes of the character as octal escape
			// sequences.
			for _, b := range s[:width] {
				buf = append(buf, '\\', '0'+b>>6, '0'+b>>3&7, '0'+b&7)
			}
		default:
			buf = append(buf, s[:width]...)
		}
		s = s[width:]
		processed += width
	}
	return buf, processed
}
