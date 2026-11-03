package syntax_test

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"bonanza.build/pkg/starlark/syntax"

	"github.com/stretchr/testify/require"

	upstream "go.starlark.net/syntax"
)

var upstreamComparisonCorpus = flag.String(
	"upstream_comparison_corpus",
	"",
	"Directory containing BUILD, MODULE.bazel and .bzl files that should be parsed with both this parser and the one provided by go.starlark.net, so that the results can be compared",
)

// TestUpstreamComparison parses a corpus of Starlark files using both
// this package and go.starlark.net/syntax, and checks that both parsers
// yield the same syntax trees.
//
// The parsers intentionally differ in a couple of ways, as this
// package follows the behavior of Bazel. Files on which only one of
// the parsers fails are therefore reported, but don't cause the test
// to fail.
func TestUpstreamComparison(t *testing.T) {
	if *upstreamComparisonCorpus == "" {
		t.Skip("No corpus provided")
	}

	var filesCompared, filesFailingBoth int
	var onlyUpstreamFailed, onlyOursFailed []string
	walkFollowingSymlinks(t, *upstreamComparisonCorpus, map[string]struct{}{}, func(path string) {
		src, err := os.ReadFile(path)
		if err != nil {
			return
		}

		upstreamFile, upstreamErr := (&upstream.FileOptions{}).Parse(path, src, 0)
		ourFile, ourErr := syntax.Parse(path, src, syntax.Options{})
		switch {
		case upstreamErr != nil && ourErr != nil:
			filesFailingBoth++
		case upstreamErr != nil:
			onlyUpstreamFailed = append(onlyUpstreamFailed, upstreamErr.Error())
		case ourErr != nil:
			onlyOursFailed = append(onlyOursFailed, ourErr.Error())
		default:
			filesCompared++
			c := newUpstreamConverter(src)
			want := treeStringWithPositions(c.convertStmts(upstreamFile.Stmts))
			got := treeStringWithPositions(ourFile.Stmts)
			if strings.ContainsRune(string(src), '\r') {
				// go.starlark.net treats carriage returns
				// as newlines, while Bazel ignores them.
				// Only compare the structure.
				want = treeString(c.convertStmts(upstreamFile.Stmts))
				got = treeString(ourFile.Stmts)
			}
			if want != got {
				t.Errorf("%s: syntax trees differ\nupstream: %s\nours:     %s", path, want, got)
			}
		}
	})

	t.Logf("Files with identical syntax trees: %d", filesCompared)
	t.Logf("Files on which both parsers failed: %d", filesFailingBoth)
	for _, err := range onlyUpstreamFailed {
		t.Logf("Only upstream failed: %s", err)
	}
	for _, err := range onlyOursFailed {
		t.Logf("Only ours failed: %s", err)
	}
}

// walkFollowingSymlinks calls a function for every Starlark file in a
// directory hierarchy. Unlike filepath.WalkDir(), it follows symbolic
// links, as Bazel's output base uses them extensively.
func walkFollowingSymlinks(t *testing.T, root string, seen map[string]struct{}, f func(path string)) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return
	}
	if _, ok := seen[realRoot]; ok {
		return
	}
	seen[realRoot] = struct{}{}

	require.NoError(t, filepath.WalkDir(realRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			if info, err := os.Stat(path); err == nil {
				if info.IsDir() {
					walkFollowingSymlinks(t, path, seen, f)
				} else if isStarlarkFile(d.Name()) {
					f(path)
				}
			}
			return nil
		}
		if !d.IsDir() && isStarlarkFile(d.Name()) {
			f(path)
		}
		return nil
	}))
}

func isStarlarkFile(name string) bool {
	switch name {
	case "BUILD", "BUILD.bazel", "MODULE.bazel", "REPO.bazel", "WORKSPACE", "WORKSPACE.bazel", "WORKSPACE.bzlmod":
		return true
	}
	return strings.HasSuffix(name, ".bzl")
}

// upstreamConverter converts syntax trees produced by
// go.starlark.net/syntax to the representation used by this package.
type upstreamConverter struct {
	lines []string
}

func newUpstreamConverter(src []byte) *upstreamConverter {
	return &upstreamConverter{lines: strings.Split(string(src), "\n")}
}

// convertPosition converts an upstream position, which uses a column
// number expressed in Unicode code points, to one where the column is
// expressed in bytes.
func (c *upstreamConverter) convertPosition(p upstream.Position) syntax.Position {
	if !p.IsValid() || p.Line < 1 || int(p.Line) > len(c.lines) {
		return syntax.Position{}
	}
	line := c.lines[p.Line-1]
	offset := 0
	for i := int32(1); i < p.Col && offset < len(line); i++ {
		_, width := utf8.DecodeRuneInString(line[offset:])
		offset += width
	}
	return syntax.Position{Line: p.Line, Column: int32(offset) + 1}
}

func (c *upstreamConverter) convertStmts(stmts []upstream.Stmt) []syntax.Stmt {
	out := make([]syntax.Stmt, 0, len(stmts))
	for _, stmt := range stmts {
		out = append(out, c.convertStmt(stmt))
	}
	return out
}

func (c *upstreamConverter) convertStmt(stmt upstream.Stmt) syntax.Stmt {
	switch s := stmt.(type) {
	case *upstream.AssignStmt:
		return &syntax.AssignStmt{
			LHS:   c.convertExpr(s.LHS),
			OpPos: c.convertPosition(s.OpPos),
			Op:    c.convertToken(s.Op),
			RHS:   c.convertExpr(s.RHS),
		}
	case *upstream.BranchStmt:
		return &syntax.BranchStmt{
			Token:    c.convertToken(s.Token),
			TokenPos: c.convertPosition(s.TokenPos),
		}
	case *upstream.DefStmt:
		return &syntax.DefStmt{
			Def:    c.convertPosition(s.Def),
			Name:   c.convertIdent(s.Name),
			Lparen: c.convertPosition(s.Lparen),
			Params: c.convertParams(s.Params),
			Rparen: c.convertPosition(s.Rparen),
			Body:   c.convertStmts(s.Body),
		}
	case *upstream.ExprStmt:
		return &syntax.ExprStmt{X: c.convertExpr(s.X)}
	case *upstream.ForStmt:
		return &syntax.ForStmt{
			For:  c.convertPosition(s.For),
			Vars: c.convertExpr(s.Vars),
			X:    c.convertExpr(s.X),
			Body: c.convertStmts(s.Body),
		}
	case *upstream.IfStmt:
		return &syntax.IfStmt{
			If:      c.convertPosition(s.If),
			Cond:    c.convertExpr(s.Cond),
			True:    c.convertStmts(s.True),
			ElsePos: c.convertPosition(s.ElsePos),
			False:   c.convertStmts(s.False),
		}
	case *upstream.LoadStmt:
		bindings := make([]*syntax.LoadBinding, 0, len(s.To))
		for i, to := range s.To {
			from := s.From[i]
			namePos := c.convertPosition(from.NamePos)
			namePos.Column--
			bindings = append(bindings, &syntax.LoadBinding{
				Local: c.convertIdent(to),
				Name: &syntax.Literal{
					Token:    syntax.STRING,
					TokenPos: namePos,
					Value:    from.Name,
				},
			})
		}
		return &syntax.LoadStmt{
			Load:     c.convertPosition(s.Load),
			Module:   c.convertLiteral(s.Module),
			Bindings: bindings,
			Rparen:   c.convertPosition(s.Rparen),
		}
	case *upstream.ReturnStmt:
		return &syntax.ReturnStmt{
			Return: c.convertPosition(s.Return),
			Result: c.convertExpr(s.Result),
		}
	default:
		panic(fmt.Sprintf("unknown statement type %T", stmt))
	}
}

func (c *upstreamConverter) convertExprs(exprs []upstream.Expr) []syntax.Expr {
	if len(exprs) == 0 {
		return nil
	}
	out := make([]syntax.Expr, 0, len(exprs))
	for _, x := range exprs {
		out = append(out, c.convertExpr(x))
	}
	return out
}

func (c *upstreamConverter) convertIdent(id *upstream.Ident) *syntax.Ident {
	return &syntax.Ident{
		NamePos: c.convertPosition(id.NamePos),
		Name:    id.Name,
	}
}

func (c *upstreamConverter) convertLiteral(l *upstream.Literal) *syntax.Literal {
	return &syntax.Literal{
		Token:    c.convertToken(l.Token),
		TokenPos: c.convertPosition(l.TokenPos),
		Raw:      l.Raw,
		Value:    l.Value,
	}
}

func (c *upstreamConverter) convertParams(params []upstream.Expr) []*syntax.Param {
	out := make([]*syntax.Param, 0, len(params))
	for _, param := range params {
		switch p := param.(type) {
		case *upstream.Ident:
			out = append(out, &syntax.Param{
				Kind: syntax.ParamNormal,
				Name: c.convertIdent(p),
			})
		case *upstream.BinaryExpr:
			out = append(out, &syntax.Param{
				Kind:    syntax.ParamNormal,
				Name:    c.convertIdent(p.X.(*upstream.Ident)),
				Eq:      c.convertPosition(p.OpPos),
				Default: c.convertExpr(p.Y),
			})
		case *upstream.UnaryExpr:
			converted := &syntax.Param{StarPos: c.convertPosition(p.OpPos)}
			switch {
			case p.Op == upstream.STARSTAR:
				converted.Kind = syntax.ParamKwargs
			case p.X != nil:
				converted.Kind = syntax.ParamArgs
			default:
				converted.Kind = syntax.ParamStar
			}
			if p.X != nil {
				converted.Name = c.convertIdent(p.X.(*upstream.Ident))
			}
			out = append(out, converted)
		default:
			panic(fmt.Sprintf("unknown parameter type %T", param))
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (c *upstreamConverter) convertArgs(args []upstream.Expr) []*syntax.Arg {
	out := make([]*syntax.Arg, 0, len(args))
	for _, arg := range args {
		switch a := arg.(type) {
		case *upstream.BinaryExpr:
			if a.Op == upstream.EQ {
				out = append(out, &syntax.Arg{
					Kind:  syntax.ArgNamed,
					Name:  c.convertIdent(a.X.(*upstream.Ident)),
					Eq:    c.convertPosition(a.OpPos),
					Value: c.convertExpr(a.Y),
				})
				continue
			}
		case *upstream.UnaryExpr:
			switch a.Op {
			case upstream.STAR:
				out = append(out, &syntax.Arg{
					Kind:    syntax.ArgArgs,
					StarPos: c.convertPosition(a.OpPos),
					Value:   c.convertExpr(a.X),
				})
				continue
			case upstream.STARSTAR:
				out = append(out, &syntax.Arg{
					Kind:    syntax.ArgKwargs,
					StarPos: c.convertPosition(a.OpPos),
					Value:   c.convertExpr(a.X),
				})
				continue
			}
		}
		out = append(out, &syntax.Arg{
			Kind:  syntax.ArgPositional,
			Value: c.convertExpr(arg),
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (c *upstreamConverter) convertDictEntry(e *upstream.DictEntry) *syntax.DictEntry {
	return &syntax.DictEntry{
		Key:   c.convertExpr(e.Key),
		Colon: c.convertPosition(e.Colon),
		Value: c.convertExpr(e.Value),
	}
}

func (c *upstreamConverter) convertExpr(expr upstream.Expr) syntax.Expr {
	switch x := expr.(type) {
	case nil:
		return nil
	case *upstream.BinaryExpr:
		return &syntax.BinaryExpr{
			X:     c.convertExpr(x.X),
			OpPos: c.convertPosition(x.OpPos),
			Op:    c.convertToken(x.Op),
			Y:     c.convertExpr(x.Y),
		}
	case *upstream.CallExpr:
		return &syntax.CallExpr{
			Fn:     c.convertExpr(x.Fn),
			Lparen: c.convertPosition(x.Lparen),
			Args:   c.convertArgs(x.Args),
			Rparen: c.convertPosition(x.Rparen),
		}
	case *upstream.Comprehension:
		var body syntax.Node
		if entry, ok := x.Body.(*upstream.DictEntry); ok {
			body = c.convertDictEntry(entry)
		} else {
			body = c.convertExpr(x.Body)
		}
		clauses := make([]syntax.Node, 0, len(x.Clauses))
		for _, clause := range x.Clauses {
			switch cl := clause.(type) {
			case *upstream.ForClause:
				clauses = append(clauses, &syntax.ForClause{
					For:  c.convertPosition(cl.For),
					Vars: c.convertExpr(cl.Vars),
					In:   c.convertPosition(cl.In),
					X:    c.convertExpr(cl.X),
				})
			case *upstream.IfClause:
				clauses = append(clauses, &syntax.IfClause{
					If:   c.convertPosition(cl.If),
					Cond: c.convertExpr(cl.Cond),
				})
			}
		}
		return &syntax.Comprehension{
			Curly:   x.Curly,
			Lbrack:  c.convertPosition(x.Lbrack),
			Body:    body,
			Clauses: clauses,
			Rbrack:  c.convertPosition(x.Rbrack),
		}
	case *upstream.CondExpr:
		return &syntax.CondExpr{
			True:    c.convertExpr(x.True),
			If:      c.convertPosition(x.If),
			Cond:    c.convertExpr(x.Cond),
			ElsePos: c.convertPosition(x.ElsePos),
			False:   c.convertExpr(x.False),
		}
	case *upstream.DictExpr:
		var entries []*syntax.DictEntry
		for _, entry := range x.List {
			entries = append(entries, c.convertDictEntry(entry.(*upstream.DictEntry)))
		}
		return &syntax.DictExpr{
			Lbrace:  c.convertPosition(x.Lbrace),
			Entries: entries,
			Rbrace:  c.convertPosition(x.Rbrace),
		}
	case *upstream.DotExpr:
		return &syntax.DotExpr{
			X:    c.convertExpr(x.X),
			Dot:  c.convertPosition(x.Dot),
			Name: c.convertIdent(x.Name),
		}
	case *upstream.Ident:
		return c.convertIdent(x)
	case *upstream.IndexExpr:
		return &syntax.IndexExpr{
			X:      c.convertExpr(x.X),
			Lbrack: c.convertPosition(x.Lbrack),
			Y:      c.convertExpr(x.Y),
			Rbrack: c.convertPosition(x.Rbrack),
		}
	case *upstream.LambdaExpr:
		return &syntax.LambdaExpr{
			Lambda: c.convertPosition(x.Lambda),
			Params: c.convertParams(x.Params),
			Body:   c.convertExpr(x.Body),
		}
	case *upstream.ListExpr:
		return &syntax.ListExpr{
			Lbrack: c.convertPosition(x.Lbrack),
			List:   c.convertExprs(x.List),
			Rbrack: c.convertPosition(x.Rbrack),
		}
	case *upstream.Literal:
		return c.convertLiteral(x)
	case *upstream.ParenExpr:
		return &syntax.ParenExpr{
			Lparen: c.convertPosition(x.Lparen),
			X:      c.convertExpr(x.X),
			Rparen: c.convertPosition(x.Rparen),
		}
	case *upstream.SliceExpr:
		return &syntax.SliceExpr{
			X:      c.convertExpr(x.X),
			Lbrack: c.convertPosition(x.Lbrack),
			Lo:     c.convertExpr(x.Lo),
			Hi:     c.convertExpr(x.Hi),
			Step:   c.convertExpr(x.Step),
			Rbrack: c.convertPosition(x.Rbrack),
		}
	case *upstream.TupleExpr:
		return &syntax.TupleExpr{
			Lparen: c.convertPosition(x.Lparen),
			List:   c.convertExprs(x.List),
			Rparen: c.convertPosition(x.Rparen),
		}
	case *upstream.UnaryExpr:
		return &syntax.UnaryExpr{
			OpPos: c.convertPosition(x.OpPos),
			Op:    c.convertToken(x.Op),
			X:     c.convertExpr(x.X),
		}
	default:
		panic(fmt.Sprintf("unknown expression type %T", expr))
	}
}

// convertToken converts an upstream token to the equivalent token of
// this package. Tokens are matched by their textual representation.
func (*upstreamConverter) convertToken(t upstream.Token) syntax.Token {
	s := t.String()
	for ours := syntax.Token(0); ours < syntax.MaxToken; ours++ {
		if ours.String() == s {
			return ours
		}
	}
	panic(fmt.Sprintf("unknown token %s", s))
}
