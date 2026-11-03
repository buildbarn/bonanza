package syntax

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

// tokenValue contains the position and value associated with a token.
type tokenValue struct {
	// Text of the token, as it appears in the source file.
	raw string
	// Start position of the token.
	pos Position

	// Values of INT, FLOAT and STRING tokens.
	int    int64
	bigInt *big.Int
	float  float64
	string string
}

// scanner converts the contents of a Starlark source file to a
// sequence of tokens.
//
// The scanner follows the behavior of Bazel's lexer, which differs
// from go.starlark.net in a couple of ways:
//
//   - Source files are sequences of bytes, not Unicode code points.
//     String literals may thus contain arbitrary bytes, and octal
//     escape sequences like "\377" yield a single byte.
//   - Escape sequences \x, \u, \U and \N are not supported.
//   - Identifiers may only contain ASCII letters, digits and
//     underscores.
//   - Tabs may not be used for indentation.
//   - Carriage returns are ignored, except within string literals.
//   - Bytes literals (b"...") are not supported.
type scanner struct {
	filename string
	options  *Options

	// Unprocessed input and the position at which it starts.
	rest []byte
	pos  Position

	// Nesting depth of brackets. Newlines and indentation are
	// ignored while greater than zero.
	depth int
	// Stack of indentation levels.
	indentStack []int
	// Number of pending INDENT (>0) or OUTDENT (<0) tokens.
	dents int
	// Whether the next token is the first one on a line, meaning
	// that indentation needs to be processed.
	lineStart bool
}

func newScanner(filename string, src []byte, options *Options) *scanner {
	return &scanner{
		filename:    filename,
		options:     options,
		rest:        src,
		pos:         Position{Line: 1, Column: 1},
		indentStack: []int{0},
		lineStart:   true,
	}
}

// errorf reports a syntax error. It does not return, as it panics
// with an *Error, which is recovered by Parse().
func (sc *scanner) errorf(pos Position, format string, args ...any) {
	panic(&Error{
		Filename: sc.filename,
		Pos:      pos,
		Message:  fmt.Sprintf(format, args...),
	})
}

// peek returns the byte at the provided offset from the current
// position, or zero if the end of the input has been reached.
func (sc *scanner) peek(offset int) byte {
	if offset < len(sc.rest) {
		return sc.rest[offset]
	}
	return 0
}

func (sc *scanner) eof() bool {
	return len(sc.rest) == 0
}

// skip consumes n bytes of input that do not contain any newlines.
func (sc *scanner) skip(n int) {
	sc.rest = sc.rest[n:]
	sc.pos.Column += int32(n)
}

// readByte consumes a single byte of input, updating the current
// position.
func (sc *scanner) readByte() byte {
	b := sc.rest[0]
	sc.rest = sc.rest[1:]
	if b == '\n' {
		sc.pos.Line++
		sc.pos.Column = 1
	} else {
		sc.pos.Column++
	}
	return b
}

// nextToken returns the next token of the input, and stores its
// position and value in val.
func (sc *scanner) nextToken(val *tokenValue) Token {
start:
	blank := false
	atLineStart := sc.lineStart
	if sc.lineStart {
		// Compute the indentation of the current line.
		sc.lineStart = false
		column := 0
	indentation:
		for !sc.eof() {
			switch sc.peek(0) {
			case ' ':
				column++
				sc.skip(1)
			case '\r', '\f':
				sc.skip(1)
			case '\t':
				// Within brackets, indentation is
				// insignificant, meaning tabs are
				// permitted.
				if sc.depth == 0 {
					sc.errorf(sc.pos, "tab characters are not allowed for indentation; use spaces instead")
				}
				sc.skip(1)
			default:
				break indentation
			}
		}

		// Blank lines and lines only containing a comment
		// don't affect indentation.
		switch sc.peek(0) {
		case '#', '\n', 0:
			blank = true
		}
		if !blank && sc.depth == 0 {
			if current := sc.indentStack[len(sc.indentStack)-1]; column > current {
				sc.dents++
				sc.indentStack = append(sc.indentStack, column)
			} else if column < current {
				for column < sc.indentStack[len(sc.indentStack)-1] {
					sc.dents--
					sc.indentStack = sc.indentStack[:len(sc.indentStack)-1]
				}
				if column != sc.indentStack[len(sc.indentStack)-1] {
					sc.errorf(sc.pos, "unindent does not match any outer indentation level")
				}
			}
		}
	}

	// Return pending indentation tokens.
	if sc.dents != 0 {
		val.pos = sc.pos
		val.raw = ""
		if sc.dents < 0 {
			sc.dents++
			return OUTDENT
		}
		sc.dents--
		return INDENT
	}

	// Skip whitespace and comments.
	for {
		switch sc.peek(0) {
		case ' ', '\t', '\r', '\f':
			sc.skip(1)
			continue
		case '#':
			for !sc.eof() && sc.peek(0) != '\n' {
				sc.skip(1)
			}
		}
		break
	}

	val.pos = sc.pos
	val.raw = ""
	c := sc.peek(0)

	switch {
	case c == '\n':
		sc.readByte()
		sc.lineStart = true
		// Newlines within brackets and on blank lines are
		// insignificant.
		if sc.depth > 0 || blank {
			goto start
		}
		val.raw = "\n"
		return NEWLINE

	case sc.eof():
		// Terminate the final line and emit OUTDENTs for any
		// unfinished indentation.
		if len(sc.indentStack) > 1 {
			if atLineStart {
				sc.dents = 1 - len(sc.indentStack)
				sc.indentStack = sc.indentStack[:1]
				goto start
			}
			sc.lineStart = true
			return NEWLINE
		}
		return EOF

	case c == '\\':
		// Line continuation.
		sc.skip(1)
		if sc.peek(0) == '\r' && sc.peek(1) == '\n' {
			sc.skip(1)
		}
		if sc.peek(0) != '\n' {
			sc.errorf(val.pos, "stray backslash in program")
		}
		sc.readByte()
		goto start

	case c == '"' || c == '\'':
		return sc.scanString(val, false)

	case c == 'r' && (sc.peek(1) == '"' || sc.peek(1) == '\''):
		return sc.scanString(val, true)

	case isIdentStart(c):
		n := 1
		for isIdent(sc.peek(n)) {
			n++
		}
		val.raw = string(sc.rest[:n])
		sc.skip(n)
		if t, ok := keywords[val.raw]; ok {
			return t
		}
		if sc.options.TypeSyntax {
			if t, ok := typeSyntaxKeywords[val.raw]; ok {
				return t
			}
		}
		if _, ok := reservedWords[val.raw]; ok {
			sc.errorf(val.pos, "keyword %#v is reserved for future use", val.raw)
		}
		return IDENT

	case isDigit(c) || (c == '.' && isDigit(sc.peek(1))):
		return sc.scanNumber(val)
	}

	// Punctuation.
	for _, p := range punctuation {
		if sc.hasPrefix(p.text) {
			val.raw = p.text
			sc.skip(len(p.text))
			switch p.token {
			case LPAREN, LBRACK, LBRACE:
				sc.depth++
			case RPAREN, RBRACK, RBRACE:
				if sc.depth == 0 {
					sc.errorf(val.pos, "unexpected %#v", p.text)
				}
				sc.depth--
			}
			return p.token
		}
	}
	if c < 0x80 && strconv.IsPrint(rune(c)) {
		sc.errorf(val.pos, "unexpected input character %#v", string(rune(c)))
	}
	sc.errorf(val.pos, "unexpected input character 0x%02x", c)
	panic("unreachable")
}

func (sc *scanner) hasPrefix(s string) bool {
	return len(sc.rest) >= len(s) && string(sc.rest[:len(s)]) == s
}

// punctuation contains all punctuation tokens. Longer tokens are
// listed before tokens that are a prefix of them.
var punctuation = []struct {
	text  string
	token Token
}{
	{"**", STARSTAR},
	{"*=", STAREQ},
	{"*", STAR},
	{"//=", SLASHSLASHEQ},
	{"//", SLASHSLASH},
	{"/=", SLASHEQ},
	{"/", SLASH},
	{"<<=", LTLTEQ},
	{"<<", LTLT},
	{"<=", LE},
	{"<", LT},
	{">>=", GTGTEQ},
	{">>", GTGT},
	{">=", GE},
	{">", GT},
	{"==", EQL},
	{"=", EQ},
	{"!=", NEQ},
	{"+=", PLUSEQ},
	{"+", PLUS},
	{"->", ARROW},
	{"-=", MINUSEQ},
	{"-", MINUS},
	{"%=", PERCENTEQ},
	{"%", PERCENT},
	{"&=", AMPEQ},
	{"&", AMP},
	{"|=", PIPEEQ},
	{"|", PIPE},
	{"^=", CIRCUMFLEXEQ},
	{"^", CIRCUMFLEX},
	{"~", TILDE},
	{"...", ELLIPSIS},
	{".", DOT},
	{",", COMMA},
	{";", SEMI},
	{":", COLON},
	{"(", LPAREN},
	{")", RPAREN},
	{"[", LBRACK},
	{"]", RBRACK},
	{"{", LBRACE},
	{"}", RBRACE},
}

// scanString scans a string literal, including its optional "r"
// prefix.
func (sc *scanner) scanString(val *tokenValue, isRaw bool) Token {
	start := sc.rest
	if isRaw {
		sc.skip(1)
	}
	quote := sc.peek(0)
	triple := sc.peek(1) == quote && sc.peek(2) == quote
	if triple {
		sc.skip(3)
	} else {
		sc.skip(1)
	}

	var value strings.Builder
	for {
		if sc.eof() {
			sc.errorf(val.pos, "unclosed string literal")
		}
		c := sc.peek(0)
		switch {
		case c == quote && (!triple || (sc.peek(1) == quote && sc.peek(2) == quote)):
			if triple {
				sc.skip(3)
			} else {
				sc.skip(1)
			}
			val.raw = string(start[:len(start)-len(sc.rest)])
			val.string = value.String()
			return STRING

		case c == '\n':
			if !triple {
				sc.errorf(val.pos, "unclosed string literal")
			}
			value.WriteByte(sc.readByte())

		case c == '\\' && isRaw:
			// Raw string literals retain escape sequences.
			// A backslash still prevents the next character
			// from terminating the string literal.
			sc.skip(1)
			value.WriteByte('\\')
			if sc.eof() {
				sc.errorf(val.pos, "unclosed string literal")
			}
			if sc.peek(0) == '\r' && sc.peek(1) == '\n' {
				sc.skip(1)
			}
			value.WriteByte(sc.readByte())

		case c == '\\':
			escapePos := sc.pos
			sc.skip(1)
			if sc.eof() {
				sc.errorf(val.pos, "unclosed string literal")
			}
			c := sc.peek(0)
			switch c {
			case '\r':
				// Line continuation.
				sc.skip(1)
				if sc.peek(0) == '\n' {
					sc.readByte()
				}
			case '\n':
				// Line continuation.
				sc.readByte()
			case 'a', 'b', 'f', 'n', 'r', 't', 'v', '\\', '\'', '"':
				sc.skip(1)
				value.WriteByte(unescapes[c])
			case '0', '1', '2', '3', '4', '5', '6', '7':
				n := 0
				for i := 0; i < 3 && isOctalDigit(sc.peek(0)); i++ {
					n = n*8 + int(sc.peek(0)-'0')
					sc.skip(1)
				}
				if n > 0xff {
					sc.errorf(escapePos, "octal escape sequence out of range (maximum is \\377)")
				}
				value.WriteByte(byte(n))
			default:
				sc.errorf(escapePos, "invalid escape sequence \\%s; use '\\\\' to insert '\\'", escapeCharacter(c))
			}

		default:
			value.WriteByte(sc.readByte())
		}
	}
}

// unescapes maps the characters following a backslash in a single
// character escape sequence to the byte value they represent.
var unescapes = [256]byte{
	'a':  '\a',
	'b':  '\b',
	'f':  '\f',
	'n':  '\n',
	'r':  '\r',
	't':  '\t',
	'v':  '\v',
	'\\': '\\',
	'\'': '\'',
	'"':  '"',
}

// escapeCharacter returns a printable representation of a character
// that follows a backslash, for use in error messages.
func escapeCharacter(c byte) string {
	if c < 0x80 && strconv.IsPrint(rune(c)) {
		return string(rune(c))
	}
	return fmt.Sprintf("<0x%02x>", c)
}

// scanNumber scans an integer or floating point literal.
func (sc *scanner) scanNumber(val *tokenValue) Token {
	n := 0
	isFloat := false
	base := 10
	if sc.peek(0) == '0' && (sc.peek(1)|0x20 == 'x' || sc.peek(1)|0x20 == 'o' || sc.peek(1)|0x20 == 'b') {
		// Hexadecimal, octal or binary integer literal.
		var isValid func(byte) bool
		switch sc.peek(1) | 0x20 {
		case 'x':
			base, isValid = 16, isHexDigit
		case 'o':
			base, isValid = 8, isOctalDigit
		case 'b':
			base, isValid = 2, isBinaryDigit
		}
		n = 2
		for isIdent(sc.peek(n)) {
			if !isValid(sc.peek(n)) {
				sc.errorf(val.pos, "invalid %s literal", map[int]string{16: "hex", 8: "octal", 2: "binary"}[base])
			}
			n++
		}
		if n == 2 {
			sc.errorf(val.pos, "invalid %s literal", map[int]string{16: "hex", 8: "octal", 2: "binary"}[base])
		}
	} else {
		// Decimal integer or floating point literal.
		for isDigit(sc.peek(n)) {
			n++
		}
		if sc.peek(n) == '.' {
			isFloat = true
			n++
			for isDigit(sc.peek(n)) {
				n++
			}
		}
		if sc.peek(n)|0x20 == 'e' {
			isFloat = true
			n++
			if sc.peek(n) == '+' || sc.peek(n) == '-' {
				n++
			}
			if !isDigit(sc.peek(n)) {
				sc.errorf(val.pos, "invalid float literal")
			}
			for isDigit(sc.peek(n)) {
				n++
			}
		}
		if isIdent(sc.peek(n)) {
			sc.errorf(val.pos, "invalid number literal %#v", string(sc.rest[:n+1]))
		}
	}
	val.raw = string(sc.rest[:n])
	sc.skip(n)

	if isFloat {
		f, err := strconv.ParseFloat(val.raw, 64)
		if err != nil {
			if numErr, ok := err.(*strconv.NumError); ok && numErr.Err == strconv.ErrRange {
				sc.errorf(val.pos, "floating-point literal too large")
			}
			sc.errorf(val.pos, "invalid float literal")
		}
		val.float = f
		return FLOAT
	}

	digits := val.raw
	if base != 10 {
		digits = digits[2:]
	} else if len(digits) > 1 && digits[0] == '0' {
		if strings.Trim(digits, "0") != "" {
			sc.errorf(val.pos, "invalid octal literal %#v; use the 0o prefix instead", val.raw)
		}
		digits = "0"
	}
	val.bigInt = nil
	if i, err := strconv.ParseInt(digits, base, 64); err == nil {
		val.int = i
	} else {
		i, ok := new(big.Int).SetString(digits, base)
		if !ok {
			sc.errorf(val.pos, "invalid int literal")
		}
		val.bigInt = i
	}
	return INT
}

func isIdentStart(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c == '_'
}

func isIdent(c byte) bool {
	return isIdentStart(c) || isDigit(c)
}

func isDigit(c byte) bool       { return '0' <= c && c <= '9' }
func isOctalDigit(c byte) bool  { return '0' <= c && c <= '7' }
func isBinaryDigit(c byte) bool { return c == '0' || c == '1' }
func isHexDigit(c byte) bool    { return isDigit(c) || 'a' <= c|0x20 && c|0x20 <= 'f' }
