package syntax_test

import (
	"math/big"
	"testing"

	"bonanza.build/pkg/starlark/syntax"

	"github.com/stretchr/testify/require"
)

func TestScanLiterals(t *testing.T) {
	for _, test := range []struct {
		input string
		want  any
	}{
		// Strings.
		{`"hello"`, "hello"},
		{`'hello'`, "hello"},
		{`""`, ""},
		{`"""a"b'c"""`, `a"b'c`},
		{`'''a\'''b'''`, "a'''b"},
		{"'''a\nb'''", "a\nb"},
		{`"\a\b\f\n\r\t\v\\\'\""`, "\a\b\f\n\r\t\v\\'\""},
		{`"\0\7\101\1011"`, "\x00\x07A" + "A1"},
		{`"\377"`, "\xff"},
		{`"\303\251"`, "\xc3\xa9"},
		{"\"caf\xc3\xa9\"", "caf\xc3\xa9"},
		{"\"invalid \xff utf-8\"", "invalid \xff utf-8"},
		{"\"a\\\nb\"", "ab"},
		{"\"a\\\r\nb\"", "ab"},
		{"\"a\rb\"", "a\rb"},
		{`r"a\nb"`, `a\nb`},
		{`r"\""`, `\"`},
		{`r'\''`, `\'`},
		{"r'''a\\\nb'''", "a\\\nb"},
		{"r'a\\\r\nb'", "a\\\nb"},

		// Integers.
		{`0`, int64(0)},
		{`00`, int64(0)},
		{`123`, int64(123)},
		{`0x7fffffffffffffff`, int64(0x7fffffffffffffff)},
		{`0XAbC`, int64(0xabc)},
		{`0o755`, int64(0o755)},
		{`0O7`, int64(7)},
		{`0b101`, int64(5)},
		{`0B1`, int64(1)},

		// Floats.
		{`1.5`, 1.5},
		{`1.`, 1.0},
		{`.25`, 0.25},
		{`1e3`, 1e3},
		{`1.5E-3`, 1.5e-3},
		{`0.0`, 0.0},
		{`0e0`, 0.0},
	} {
		t.Run(test.input, func(t *testing.T) {
			x, err := syntax.ParseExpr("foo.star", []byte(test.input), syntax.Options{})
			require.NoError(t, err)
			require.Equal(t, test.want, x.(*syntax.Literal).Value)
			require.Equal(t, test.input, x.(*syntax.Literal).Raw)
		})
	}

	t.Run("BigInt", func(t *testing.T) {
		x, err := syntax.ParseExpr("foo.star", []byte("0x10000000000000000"), syntax.Options{})
		require.NoError(t, err)
		want, _ := new(big.Int).SetString("10000000000000000", 16)
		require.Equal(t, want, x.(*syntax.Literal).Value)
	})
}

func TestScanErrors(t *testing.T) {
	for _, test := range []struct {
		input, want string
	}{
		{`"abc`, `1:1: unclosed string literal`},
		{"\"abc\ndef\"", `1:1: unclosed string literal`},
		{`"""abc`, `1:1: unclosed string literal`},
		{`r"\`, `1:1: unclosed string literal`},
		{`"\x41"`, `1:2: invalid escape sequence \x; use '\\' to insert '\'`},
		{`"\u0041"`, `1:2: invalid escape sequence \u; use '\\' to insert '\'`},
		{`"\U00000041"`, `1:2: invalid escape sequence \U; use '\\' to insert '\'`},
		{`"\N{DASH}"`, `1:2: invalid escape sequence \N; use '\\' to insert '\'`},
		{`"\q"`, `1:2: invalid escape sequence \q; use '\\' to insert '\'`},
		{`"\400"`, `1:2: octal escape sequence out of range (maximum is \377)`},
		{`b"abc"`, `1:2: got string literal after expression, want end of file`},
		{`0755`, `1:1: invalid octal literal "0755"; use the 0o prefix instead`},
		{`0x`, `1:1: invalid hex literal`},
		{`0xg`, `1:1: invalid hex literal`},
		{`0o8`, `1:1: invalid octal literal`},
		{`0b2`, `1:1: invalid binary literal`},
		{`1e`, `1:1: invalid float literal`},
		{`1abc`, `1:1: invalid number literal "1a"`},
		{`1e999`, `1:1: floating-point literal too large`},
		{"caf\xc3\xa9", `1:4: unexpected input character 0xc3`},
		{`a ! b`, `1:3: unexpected input character "!"`},
		{`a $ b`, `1:3: unexpected input character "$"`},
		{`)`, `1:1: unexpected ")"`},
		{`a \ b`, `1:3: stray backslash in program`},
	} {
		t.Run(test.input, func(t *testing.T) {
			_, err := syntax.ParseExpr("foo.star", []byte(test.input), syntax.Options{})
			require.Error(t, err)
			require.Equal(t, test.want, stripFilename(err))
		})
	}
}

func TestQuote(t *testing.T) {
	for _, test := range []struct {
		input, want string
	}{
		{"", `""`},
		{"hello", `"hello"`},
		{"a\"b\\c", `"a\"b\\c"`},
		{"a\nb\rc\td", `"a\nb\rc\td"`},
		{"\x00\x07\x1f\x7f", `"\000\007\037\177"`},
		{"caf\xc3\xa9", "\"caf\xc3\xa9\""},
		{"\xff", `"\377"`},
		{"\xc3", `"\303"`},
		{"\u200b", `"\342\200\213"`},
	} {
		t.Run(test.input, func(t *testing.T) {
			got := syntax.Quote(test.input)
			require.Equal(t, test.want, got)

			// Quoted strings must parse back to the original.
			x, err := syntax.ParseExpr("foo.star", []byte(got), syntax.Options{})
			require.NoError(t, err)
			require.Equal(t, test.input, x.(*syntax.Literal).Value)
		})
	}
}
