package eval_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"bonanza.build/pkg/starlark/compile"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/resolve"
	"bonanza.build/pkg/starlark/syntax"

	"github.com/stretchr/testify/require"
)

// TestConformance runs the tests in testdata/*.star, which are derived
// from go.starlark.net's test suite. Files are split into chunks
// separated by "---", which are executed independently. Lines may
// contain annotations of the form ### "regexp", indicating that an
// error matching the regular expression is expected to be reported
// on that line. Errors are reported by calling error(), typically
// through the assertions in testdata/assert.star.
func TestConformance(t *testing.T) {
	files, err := filepath.Glob("testdata/*.star")
	require.NoError(t, err)
	for _, path := range files {
		if filepath.Base(path) == "assert.star" {
			continue
		}
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		lineOffset := 0
		for i, chunk := range strings.Split(string(data), "\n---\n") {
			chunkLineOffset := lineOffset
			lineOffset += strings.Count(chunk, "\n") + 2
			t.Run(fmt.Sprintf("%s:%d", filepath.Base(path), chunkLineOffset+1), func(t *testing.T) {
				runChunk(t, filepath.Base(path), chunk, chunkLineOffset)
			})
			_ = i
		}
	}
}

var annotationPattern = regexp.MustCompile("### \"((?:[^\"\\\\]|\\\\.)*)\"|### `([^`]*)`")

type reportedError struct {
	line    int32
	message string
}

func runChunk(t *testing.T, filename, chunk string, lineOffset int) {
	option := func(name string) bool {
		return strings.Contains(chunk, "option:"+name)
	}
	dialect := resolve.BzlDialect
	if option("globalreassign") {
		dialect.AllowToplevelRebinding = true
		dialect.RequireLoadStatementsFirst = false
	}
	if option("loadsanywhere") {
		dialect.RequireLoadStatementsFirst = false
	}

	// Collect the expected errors.
	expected := map[int32][]*regexp.Regexp{}
	for i, line := range strings.Split(chunk, "\n") {
		for _, match := range annotationPattern.FindAllStringSubmatch(line, -1) {
			pattern := match[2]
			if match[1] != "" {
				pattern = strings.ReplaceAll(match[1], `\"`, `"`)
				pattern = strings.ReplaceAll(pattern, `\\`, `\`)
			}
			expected[int32(i+1)] = append(expected[int32(i+1)], regexp.MustCompile(pattern))
		}
	}

	var reported []reportedError
	report := func(line int32, message string) {
		reported = append(reported, reportedError{line: line, message: message})
	}
	err := execChunk(filename, chunk, &dialect, report)
	if err != nil {
		var evalErr *eval.Error
		var syntaxErr *syntax.Error
		var resolveErr resolve.ErrorList
		switch {
		case errors.As(err, &resolveErr):
			for _, e := range resolveErr {
				report(e.Pos.Line, e.Message)
			}
		case errors.As(err, &syntaxErr):
			report(syntaxErr.Pos.Line, syntaxErr.Message)
		case errors.As(err, &evalErr):
			line := int32(0)
			for _, fr := range evalErr.CallStack {
				if fr.Filename == filename {
					line = fr.Pos.Line
				}
			}
			report(line, evalErr.Error())
		default:
			report(0, err.Error())
		}
	}

	sort.SliceStable(reported, func(i, j int) bool { return reported[i].line < reported[j].line })
	for _, e := range reported {
		patterns := expected[e.line]
		matched := -1
		for j, pattern := range patterns {
			if pattern.MatchString(e.message) {
				matched = j
				break
			}
		}
		if matched < 0 {
			t.Errorf("%s:%d: unexpected error: %s", filename, int(e.line)+lineOffset, e.message)
			continue
		}
		expected[e.line] = append(patterns[:matched], patterns[matched+1:]...)
	}
	for line, patterns := range expected {
		for _, pattern := range patterns {
			t.Errorf("%s:%d: expected error matching %#v", filename, int(line)+lineOffset, pattern.String())
		}
	}
}

// execChunk parses, resolves, compiles and executes a chunk of a test
// file. Errors reported by error() are passed to report.
func execChunk(filename, chunk string, dialect *resolve.Dialect, report func(int32, string)) error {
	universe := eval.Universe()
	predeclared := map[string]eval.Value{
		"error": eval.NewBuiltin("error", func(t *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
			if len(args.Positional) != 1 {
				return nil, errors.New("error() takes exactly one argument")
			}
			msg, err := eval.Str(t, args.Positional[0])
			if err != nil {
				return nil, err
			}
			line := int32(0)
			for _, fr := range t.CallStack() {
				if fr.Filename == filename {
					line = fr.Pos.Line
				}
			}
			report(line, msg)
			return eval.None, nil
		}),
		"catch": eval.NewBuiltin("catch", func(t *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
			if len(args.Positional) != 1 {
				return nil, errors.New("catch() takes exactly one argument")
			}
			if _, err := eval.Call(t, args.Positional[0], eval.Args{}); err != nil {
				return eval.String(err.Error()), nil
			}
			return eval.None, nil
		}),
		"matches": eval.NewBuiltin("matches", func(t *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
			pattern, ok1 := args.Positional[0].(eval.String)
			s, ok2 := args.Positional[1].(eval.String)
			if !ok1 || !ok2 {
				return nil, errors.New("matches() takes two strings")
			}
			ok, err := regexp.MatchString(string(pattern), string(s))
			return eval.Bool(ok), err
		}),
		"module": eval.NewBuiltin("module", func(t *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
			members := map[string]eval.Value{}
			for _, arg := range args.Named {
				members[arg.Name] = arg.Value
			}
			return eval.NewBuiltinModule(string(args.Positional[0].(eval.String)), members), nil
		}),
		"_freeze": eval.NewBuiltin("freeze", func(t *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
			eval.FreezeForTesting(args.Positional[0])
			return args.Positional[0], nil
		}),
		"struct": eval.NewBuiltin("struct", func(t *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
			if len(args.Positional) > 0 {
				return nil, errors.New("struct() does not accept positional arguments")
			}
			attrs := map[string]eval.Value{}
			for _, arg := range args.Named {
				attrs[arg.Name] = arg.Value
			}
			return &hasFields{typeName: "struct", attrs: attrs, frozen: true}, nil
		}),
		"hasfields": eval.NewBuiltin("hasfields", func(t *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
			return &hasFields{typeName: "hasfields", attrs: map[string]eval.Value{}}, nil
		}),
		"_floateq": eval.NewBuiltin("floateq", func(t *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
			x, y := float64(args.Positional[0].(eval.Float)), float64(args.Positional[1].(eval.Float))
			return eval.Bool(x == y || math.Abs(x-y) <= math.Abs(x)*1e-15 || math.IsNaN(x) && math.IsNaN(y)), nil
		}),
	}
	isPredeclared := func(name string) bool { _, ok := predeclared[name]; return ok }
	isUniversal := func(name string) bool { _, ok := universe[name]; return ok }

	load := func(filename, src string, dialect *resolve.Dialect) (*compile.Program, error) {
		f, err := syntax.Parse(filename, []byte(src), syntax.Options{})
		if err != nil {
			return nil, err
		}
		m, err := resolve.File(f, dialect, isPredeclared, isUniversal)
		if err != nil {
			return nil, err
		}
		return compile.File(f, m), nil
	}

	thread := eval.NewThread(context.Background(), universe)

	// Execute assert.star, so that its globals can be loaded.
	assertSource, err := os.ReadFile("testdata/assert.star")
	if err != nil {
		return err
	}
	assertProgram, err := load("assert.star", string(assertSource), &resolve.BzlDialect)
	if err != nil {
		return err
	}
	assertModule := eval.NewInMemoryModule("assert.star", predeclared, nil)
	if err := eval.ExecFile(thread, assertProgram, assertModule); err != nil {
		return err
	}

	// Check that all loaded symbols exist, as Bazel does when
	// load() statements are executed.
	f, err := syntax.Parse(filename, []byte(chunk), syntax.Options{})
	if err != nil {
		return err
	}
	m, err := resolve.File(f, dialect, isPredeclared, isUniversal)
	if err != nil {
		return err
	}
	for _, loadedSymbol := range m.Imports {
		if _, err := assertModule.Global(thread, loadedSymbol.Name); err != nil {
			report(loadedSymbol.Binding.First.NamePos.Line, fmt.Sprintf("file '%s' does not contain symbol '%s'", loadedSymbol.Module, loadedSymbol.Name))
		}
	}
	program := compile.File(f, m)
	module := eval.NewInMemoryModule(filename, predeclared, func(t *eval.Thread, imp compile.Import) (eval.Value, error) {
		if imp.Module != "assert.star" {
			return nil, fmt.Errorf("cannot load %s", imp.Module)
		}
		return assertModule.Global(t, imp.Name)
	})
	return eval.ExecFile(thread, program, module)
}

// hasFields is a value with arbitrary settable fields, used by tests
// that exercise attribute access.
type hasFields struct {
	typeName string
	attrs    map[string]eval.Value
	frozen   bool
}

func (hf *hasFields) Type() string                   { return hf.typeName }
func (*hasFields) Truth(*eval.Thread) (bool, error)  { return true, nil }
func (*hasFields) Hash(*eval.Thread) (uint32, error) { return 42, nil }
func (hf *hasFields) FreezeForTesting()              { hf.frozen = true }

func (hf *hasFields) Repr(_ *eval.Thread, p *eval.Printer) error {
	p.WriteString(hf.typeName)
	return nil
}

func (hf *hasFields) Attr(_ *eval.Thread, name string) (eval.Value, error) {
	if v, ok := hf.attrs[name]; ok {
		return v, nil
	}
	return nil, eval.NoSuchAttrError{Value: hf, Name: name}
}

func (hf *hasFields) AttrNames(*eval.Thread) ([]string, error) {
	names := make([]string, 0, len(hf.attrs))
	for name := range hf.attrs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func (hf *hasFields) SetField(_ *eval.Thread, name string, v eval.Value) error {
	if hf.frozen {
		return errors.New("cannot set field on a frozen hasfields")
	}
	hf.attrs[name] = v
	return nil
}

// Binary implements list + hasfields, which evaluates to 42.
func (*hasFields) Binary(_ *eval.Thread, op syntax.Token, y eval.Value, side eval.Side) (eval.Value, error) {
	if _, ok := y.(eval.List); ok && op == syntax.PLUS && side == eval.Right {
		return eval.MakeInt(42), nil
	}
	return nil, nil
}
