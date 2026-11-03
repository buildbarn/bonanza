package resolve_test

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"bonanza.build/pkg/starlark/resolve"
	"bonanza.build/pkg/starlark/syntax"

	"github.com/stretchr/testify/require"
)

func isPredeclared(name string) bool {
	return name == "M"
}

func isUniversal(name string) bool {
	switch name {
	case "U", "None", "int", "len", "print":
		return true
	}
	return false
}

// getDialect returns the dialect to use for a chunk of
// testdata/resolve.star, based on the options it contains.
func getDialect(chunk string) *resolve.Dialect {
	option := func(name string) bool {
		return strings.Contains(chunk, "option:"+name)
	}
	var d resolve.Dialect
	switch {
	case option("build"):
		d = resolve.BuildDialect
	case option("module"):
		d = resolve.ModuleDotBazelDialect
	default:
		d = resolve.BzlDialect
	}
	if option("rebinding") {
		d.AllowToplevelRebinding = true
	}
	if option("loadsanywhere") {
		d.RequireLoadStatementsFirst = false
	}
	if option("types") {
		d.ResolveTypeSyntax = true
	}
	return &d
}

var expectedErrorPattern = regexp.MustCompile("\"((?:[^\"\\\\]|\\\\.)*)\"|`([^`]*)`")

func TestResolveErrors(t *testing.T) {
	data, err := os.ReadFile("testdata/resolve.star")
	require.NoError(t, err)

	for i, chunk := range strings.Split(string(data), "\n---\n") {
		t.Run(strings.SplitN(strings.TrimLeft(chunk, "\n"), "\n", 2)[0], func(t *testing.T) {
			// Collect expected errors, keyed by line number.
			expected := map[int32][]*regexp.Regexp{}
			for lineIndex, line := range strings.Split(chunk, "\n") {
				if _, annotations, ok := strings.Cut(line, "###"); ok {
					for _, match := range expectedErrorPattern.FindAllStringSubmatch(annotations, -1) {
						pattern := match[1] + match[2]
						if match[1] != "" {
							pattern = strings.ReplaceAll(pattern, `\"`, `"`)
						}
						expected[int32(lineIndex+1)] = append(expected[int32(lineIndex+1)], regexp.MustCompile(pattern))
					}
				}
			}

			f, err := syntax.Parse("resolve.star", []byte(chunk), syntax.Options{TypeSyntax: true})
			require.NoError(t, err, "chunk %d", i)
			_, err = resolve.File(f, getDialect(chunk), isPredeclared, isUniversal)
			var errs resolve.ErrorList
			if err != nil {
				errs = err.(resolve.ErrorList)
			}

			for _, e := range errs {
				patterns := expected[e.Pos.Line]
				matched := -1
				for j, pattern := range patterns {
					if pattern.MatchString(e.Message) {
						matched = j
						break
					}
				}
				if matched < 0 {
					t.Errorf("line %d: unexpected error: %s", e.Pos.Line, e.Message)
					continue
				}
				expected[e.Pos.Line] = append(patterns[:matched], patterns[matched+1:]...)
			}
			for line, patterns := range expected {
				for _, pattern := range patterns {
					t.Errorf("line %d: expected error matching %#v", line, pattern.String())
				}
			}
		})
	}
}

func resolveForTesting(t *testing.T, src string, dialect *resolve.Dialect) (*syntax.File, *resolve.Module) {
	f, err := syntax.Parse("foo.star", []byte(src), syntax.Options{TypeSyntax: true})
	require.NoError(t, err)
	m, err := resolve.File(f, dialect, isPredeclared, isUniversal)
	require.NoError(t, err)
	return f, m
}

// bindingsString returns a description of the bindings of all
// identifiers in a syntax tree, in the order in which they appear.
func bindingsString(f *syntax.File, m *resolve.Module) string {
	var parts []string
	syntax.Walk(f, func(n syntax.Node) bool {
		if id, ok := n.(*syntax.Ident); ok {
			if b := m.Binding(id); b != nil {
				switch b.Scope {
				case resolve.Local, resolve.Cell, resolve.Free, resolve.Global, resolve.Import:
					parts = append(parts, fmt.Sprintf("%s:%s#%d", id.Name, b.Scope, b.Index))
				default:
					parts = append(parts, fmt.Sprintf("%s:%s", id.Name, b.Scope))
				}
			}
		}
		return true
	})
	return strings.Join(parts, " ")
}

func TestResolveBindings(t *testing.T) {
	t.Run("Closures", func(t *testing.T) {
		f, m := resolveForTesting(t, `
def outer(a, b):
    c = 1
    def middle():
        def inner():
            return a + c + U
        return inner
    return middle
`, &resolve.BzlDialect)
		require.Equal(t, "outer:global#0 a:cell#0 b:local#1 c:cell#2 middle:local#3 inner:local#0 a:free#0 c:free#1 U:universal inner:local#0 middle:local#3", bindingsString(f, m))

		outer := m.Function(f.Stmts[0])
		require.Equal(t, "outer", outer.Name)
		require.Len(t, outer.Locals, 4)
		require.Empty(t, outer.FreeVars)

		middle := m.Function(f.Stmts[0].(*syntax.DefStmt).Body[1])
		require.Equal(t, []*resolve.Binding{outer.Locals[0], outer.Locals[2]}, middle.FreeVars)

		inner := m.Function(middle.Node.(*syntax.DefStmt).Body[0])
		require.Len(t, inner.FreeVars, 2)
		for i, freeVar := range inner.FreeVars {
			require.Equal(t, resolve.Free, freeVar.Scope)
			require.Equal(t, i, freeVar.Index)
		}
	})

	t.Run("Parameters", func(t *testing.T) {
		f, m := resolveForTesting(t, `
def f(a, b = 1, *args, c, d = 2, **kwargs):
    pass

g = lambda *, x: x
`, &resolve.BzlDialect)
		require.Equal(t, "f:global#0 a:local#0 b:local#1 args:local#4 c:local#2 d:local#3 kwargs:local#5 g:global#1 x:local#0 x:local#0", bindingsString(f, m))

		function := m.Function(f.Stmts[0])
		require.Equal(t, 2, function.NumKeywordOnlyParams)
		require.True(t, function.HasArgs)
		require.True(t, function.HasKwargs)

		lambda := m.Function(f.Stmts[1].(*syntax.AssignStmt).RHS)
		require.Equal(t, "lambda", lambda.Name)
		require.Equal(t, 1, lambda.NumKeywordOnlyParams)
		require.False(t, lambda.HasArgs)
		require.False(t, lambda.HasKwargs)
	})

	t.Run("ImportsAndGlobals", func(t *testing.T) {
		f, m := resolveForTesting(t, `
load("//a:a.bzl", "a", b = "c")
load("//d:d.bzl", "d")

x = a
_y = 1

def f():
    return b + _y

def g():
    def h():
        return d + f()
    return h
`, &resolve.BzlDialect)
		require.Equal(t, "a:import#0 b:import#1 d:import#2 x:global#0 a:import#0 _y:global#1 f:global#2 b:import#1 _y:global#1 g:global#3 h:local#0 d:import#2 f:global#2 h:local#0", bindingsString(f, m))

		require.Len(t, m.Imports, 3)
		require.Equal(t, "//a:a.bzl", m.Imports[1].Module)
		require.Equal(t, "c", m.Imports[1].Name)
		require.Equal(t, resolve.Import, m.Imports[1].Binding.Scope)

		fFunction := m.Function(f.Stmts[4])
		require.Equal(t, []int{1}, fFunction.ReferencedGlobals)
		require.Equal(t, []int{1}, fFunction.ReferencedImports)
		require.Empty(t, fFunction.FreeVars)

		// References made by nested functions are also
		// attributed to the enclosing function.
		gFunction := m.Function(f.Stmts[5])
		require.Equal(t, []int{2}, gFunction.ReferencedGlobals)
		require.Equal(t, []int{2}, gFunction.ReferencedImports)
		require.Empty(t, gFunction.FreeVars)
	})

	t.Run("RepeatedLoadInBuildFile", func(t *testing.T) {
		// Repeatedly loading the same symbol should yield a
		// single import.
		f, m := resolveForTesting(t, `
load("//a:a.bzl", "x")
load("//a:a.bzl", "x")
y = x
`, &resolve.BuildDialect)
		require.Equal(t, "x:import#0 x:import#0 y:global#0 x:import#0", bindingsString(f, m))
		require.Len(t, m.Imports, 1)
		require.Empty(t, m.Toplevel.Locals)
	})

	t.Run("ToplevelComprehensions", func(t *testing.T) {
		f, m := resolveForTesting(t, `
x = [a + b for a in M for b in a]
y = [lambda: a for a in M]
`, &resolve.BzlDialect)
		require.Equal(t, "x:global#0 a:local#0 b:local#1 a:local#0 M:predeclared b:local#1 a:local#0 y:global#1 a:free#0 a:cell#2 M:predeclared", bindingsString(f, m))
		require.Len(t, m.Toplevel.Locals, 3)
	})
}

func TestResolveCallArgumentCounts(t *testing.T) {
	resolveCall := func(arguments string) error {
		src := "print(" + arguments + ")\n"
		f, err := syntax.Parse("test.star", []byte(src), syntax.Options{})
		require.NoError(t, err)
		_, err = resolve.File(f, &resolve.BzlDialect, isPredeclared, isUniversal)
		return err
	}
	namedArguments := func(count int) string {
		var sb strings.Builder
		for i := range count {
			fmt.Fprintf(&sb, "a%d = 0, ", i)
		}
		return sb.String()
	}

	t.Run("Positional", func(t *testing.T) {
		require.NoError(t, resolveCall(strings.Repeat("0, ", resolve.MaximumCallArguments)))
		require.ErrorContains(t, resolveCall(strings.Repeat("0, ", resolve.MaximumCallArguments+1)), "call has 65536 positional arguments, while at most 65535 are permitted")
	})

	t.Run("Named", func(t *testing.T) {
		require.NoError(t, resolveCall(namedArguments(resolve.MaximumCallArguments)))
		require.ErrorContains(t, resolveCall(namedArguments(resolve.MaximumCallArguments+1)), "call has 65536 named arguments, while at most 65535 are permitted")
	})
}
