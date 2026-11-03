package syntax

// Token is a lexical token of the Starlark language.
type Token int8

// List of all tokens that can be returned by the scanner.
const (
	ILLEGAL Token = iota
	EOF

	NEWLINE
	INDENT
	OUTDENT

	// Tokens with values.
	IDENT  // x
	INT    // 123
	FLOAT  // 1.23e45
	STRING // "foo", 'foo', """foo""", r"foo"

	// Punctuation.
	PLUS         // +
	MINUS        // -
	STAR         // *
	SLASH        // /
	SLASHSLASH   // //
	PERCENT      // %
	AMP          // &
	PIPE         // |
	CIRCUMFLEX   // ^
	LTLT         // <<
	GTGT         // >>
	TILDE        // ~
	DOT          // .
	ELLIPSIS     // ...
	COMMA        // ,
	EQ           // =
	SEMI         // ;
	COLON        // :
	ARROW        // ->
	LPAREN       // (
	RPAREN       // )
	LBRACK       // [
	RBRACK       // ]
	LBRACE       // {
	RBRACE       // }
	LT           // <
	GT           // >
	GE           // >=
	LE           // <=
	EQL          // ==
	NEQ          // !=
	PLUSEQ       // +=   (keep order consistent with PLUS..GTGT)
	MINUSEQ      // -=
	STAREQ       // *=
	SLASHEQ      // /=
	SLASHSLASHEQ // //=
	PERCENTEQ    // %=
	AMPEQ        // &=
	PIPEEQ       // |=
	CIRCUMFLEXEQ // ^=
	LTLTEQ       // <<=
	GTGTEQ       // >>=
	STARSTAR     // **

	// Keywords.
	AND
	BREAK
	CONTINUE
	DEF
	ELIF
	ELSE
	FOR
	IF
	IN
	LAMBDA
	LOAD
	NOT
	NOTIN // Synthesized by the parser from NOT IN.
	OR
	PASS
	RETURN
	WHILE

	// Keywords that are only present if type syntax is enabled.
	CAST
	ISINSTANCE

	maxToken
)

var tokenNames = [...]string{
	ILLEGAL:      "illegal token",
	EOF:          "end of file",
	NEWLINE:      "newline",
	INDENT:       "indent",
	OUTDENT:      "outdent",
	IDENT:        "identifier",
	INT:          "int literal",
	FLOAT:        "float literal",
	STRING:       "string literal",
	PLUS:         "+",
	MINUS:        "-",
	STAR:         "*",
	SLASH:        "/",
	SLASHSLASH:   "//",
	PERCENT:      "%",
	AMP:          "&",
	PIPE:         "|",
	CIRCUMFLEX:   "^",
	LTLT:         "<<",
	GTGT:         ">>",
	TILDE:        "~",
	DOT:          ".",
	ELLIPSIS:     "...",
	COMMA:        ",",
	EQ:           "=",
	SEMI:         ";",
	COLON:        ":",
	ARROW:        "->",
	LPAREN:       "(",
	RPAREN:       ")",
	LBRACK:       "[",
	RBRACK:       "]",
	LBRACE:       "{",
	RBRACE:       "}",
	LT:           "<",
	GT:           ">",
	GE:           ">=",
	LE:           "<=",
	EQL:          "==",
	NEQ:          "!=",
	PLUSEQ:       "+=",
	MINUSEQ:      "-=",
	STAREQ:       "*=",
	SLASHEQ:      "/=",
	SLASHSLASHEQ: "//=",
	PERCENTEQ:    "%=",
	AMPEQ:        "&=",
	PIPEEQ:       "|=",
	CIRCUMFLEXEQ: "^=",
	LTLTEQ:       "<<=",
	GTGTEQ:       ">>=",
	STARSTAR:     "**",
	AND:          "and",
	BREAK:        "break",
	CONTINUE:     "continue",
	DEF:          "def",
	ELIF:         "elif",
	ELSE:         "else",
	FOR:          "for",
	IF:           "if",
	IN:           "in",
	LAMBDA:       "lambda",
	LOAD:         "load",
	NOT:          "not",
	NOTIN:        "not in",
	OR:           "or",
	PASS:         "pass",
	RETURN:       "return",
	WHILE:        "while",
	CAST:         "cast",
	ISINSTANCE:   "isinstance",
}

// String returns the textual representation of a token, such as "+"
// or "def".
func (t Token) String() string {
	return tokenNames[t]
}

// GoString is like String, except that punctuation is quoted. It is
// used when formatting error messages using "%#v".
func (t Token) GoString() string {
	if t >= PLUS && t <= STARSTAR {
		return "'" + tokenNames[t] + "'"
	}
	return tokenNames[t]
}

// keywords contains the identifiers that are always treated as
// keywords.
var keywords = map[string]Token{
	"and":      AND,
	"break":    BREAK,
	"continue": CONTINUE,
	"def":      DEF,
	"elif":     ELIF,
	"else":     ELSE,
	"for":      FOR,
	"if":       IF,
	"in":       IN,
	"lambda":   LAMBDA,
	"load":     LOAD,
	"not":      NOT,
	"or":       OR,
	"pass":     PASS,
	"return":   RETURN,
	"while":    WHILE,
}

// typeSyntaxKeywords contains the identifiers that are treated as
// keywords if type syntax is enabled.
var typeSyntaxKeywords = map[string]Token{
	"cast":       CAST,
	"isinstance": ISINSTANCE,
}

// reservedWords contains identifiers that are not keywords of the
// Starlark language, but are reserved by Bazel to permit future
// extensions of the language.
var reservedWords = map[string]struct{}{
	"as":       {},
	"assert":   {},
	"class":    {},
	"del":      {},
	"except":   {},
	"finally":  {},
	"from":     {},
	"global":   {},
	"import":   {},
	"is":       {},
	"nonlocal": {},
	"raise":    {},
	"try":      {},
	"with":     {},
	"yield":    {},
}
