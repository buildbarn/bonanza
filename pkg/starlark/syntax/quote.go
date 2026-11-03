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
	for len(s) > 0 {
		r, width := utf8.DecodeRuneInString(s)
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
			for _, b := range []byte(s[:width]) {
				buf = append(buf, '\\', '0'+b>>6, '0'+b>>3&7, '0'+b&7)
			}
		default:
			buf = append(buf, s[:width]...)
		}
		s = s[width:]
	}
	buf = append(buf, '"')
	return string(buf)
}
