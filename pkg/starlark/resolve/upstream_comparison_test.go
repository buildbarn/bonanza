package resolve_test

import (
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"bonanza.build/pkg/starlark/resolve"
	"bonanza.build/pkg/starlark/syntax"

	"github.com/stretchr/testify/require"

	upstream_resolve "go.starlark.net/resolve"
	upstream_syntax "go.starlark.net/syntax"
)

var upstreamComparisonCorpus = flag.String(
	"upstream_comparison_corpus",
	"",
	"Directory containing BUILD, MODULE.bazel and .bzl files that should be resolved, and compared against the results of go.starlark.net's resolver",
)

// TestUpstreamComparison resolves a corpus of Starlark files, which
// are assumed to be accepted by Bazel. It checks that no errors are
// reported, and for .bzl files, that the scopes of all identifiers
// match the ones computed by go.starlark.net/resolve.
//
// As all names are treated as predeclared, this test does not detect
// undefined names.
func TestUpstreamComparison(t *testing.T) {
	if *upstreamComparisonCorpus == "" {
		t.Skip("No corpus provided")
	}

	var filesResolved, filesCompared int
	walkFollowingSymlinks(t, *upstreamComparisonCorpus, map[string]struct{}{}, func(path string) {
		src, err := os.ReadFile(path)
		if err != nil {
			return
		}
		f, err := syntax.Parse(path, src, syntax.Options{})
		if err != nil {
			return
		}
		dialect := dialectForFilename(filepath.Base(path))
		m, err := resolve.File(f, dialect, func(string) bool { return true }, func(string) bool { return false })
		if err != nil {
			for _, e := range err.(resolve.ErrorList) {
				t.Logf("Resolution error: %s", e)
			}
			return
		}
		filesResolved++

		if !strings.HasSuffix(path, ".bzl") {
			return
		}
		upstreamFile, err := (&upstream_syntax.FileOptions{Set: true, Recursion: true}).Parse(path, src, 0)
		if err != nil {
			return
		}
		if err := upstream_resolve.File(upstreamFile, func(string) bool { return true }, func(string) bool { return false }); err != nil {
			t.Logf("Upstream resolution error: %s", err)
			return
		}
		filesCompared++
		compareScopes(t, path, src, f, m, upstreamFile)
	})
	t.Logf("Files resolved without errors: %d", filesResolved)
	t.Logf("Files whose scopes were compared: %d", filesCompared)
}

func dialectForFilename(name string) *resolve.Dialect {
	switch name {
	case "BUILD", "BUILD.bazel":
		return &resolve.BuildDialect
	case "MODULE.bazel":
		return &resolve.ModuleDotBazelDialect
	case "REPO.bazel":
		return &resolve.RepoDotBazelDialect
	default:
		return &resolve.BzlDialect
	}
}

type identKey struct {
	pos  syntax.Position
	name string
}

func compareScopes(t *testing.T, path string, src []byte, f *syntax.File, m *resolve.Module, upstreamFile *upstream_syntax.File) {
	ours := map[identKey]resolve.Scope{}
	syntax.Walk(f, func(n syntax.Node) bool {
		if id, ok := n.(*syntax.Ident); ok {
			if b := m.Binding(id); b != nil {
				ours[identKey{pos: id.NamePos, name: id.Name}] = b.Scope
			}
		}
		return true
	})

	lines := strings.Split(string(src), "\n")
	upstream_syntax.Walk(upstreamFile, func(n upstream_syntax.Node) bool {
		id, ok := n.(*upstream_syntax.Ident)
		if !ok {
			return true
		}
		b, ok := id.Binding.(*upstream_resolve.Binding)
		if !ok {
			return true
		}
		key := identKey{pos: convertPosition(lines, id.NamePos), name: id.Name}
		ourScope, ok := ours[key]
		if !ok {
			t.Errorf("%s:%s: identifier %s was not resolved", path, key.pos, id.Name)
			return true
		}
		if ourScope == resolve.Import {
			// go.starlark.net treats symbols bound by load()
			// as file-local variables, which are captured by
			// functions.
			switch b.Scope {
			case upstream_resolve.Local, upstream_resolve.Cell, upstream_resolve.Free:
			default:
				t.Errorf("%s:%s: identifier %s has scope %s, while upstream has scope %s", path, key.pos, id.Name, ourScope, b.Scope)
			}
			return true
		}
		if ourScope.String() != b.Scope.String() {
			t.Errorf("%s:%s: identifier %s has scope %s, while upstream has scope %s", path, key.pos, id.Name, ourScope, b.Scope)
		}
		return true
	})
}

// convertPosition converts an upstream position, which uses a column
// number expressed in Unicode code points, to one where the column is
// expressed in bytes.
func convertPosition(lines []string, p upstream_syntax.Position) syntax.Position {
	if p.Line < 1 || int(p.Line) > len(lines) {
		return syntax.Position{}
	}
	line := lines[p.Line-1]
	offset := 0
	for i := int32(1); i < p.Col && offset < len(line); i++ {
		_, width := utf8.DecodeRuneInString(line[offset:])
		offset += width
	}
	return syntax.Position{Line: p.Line, Column: int32(offset) + 1}
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
	case "BUILD", "BUILD.bazel", "MODULE.bazel", "REPO.bazel":
		return true
	}
	return strings.HasSuffix(name, ".bzl")
}
