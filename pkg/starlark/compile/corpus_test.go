package compile_test

import (
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bonanza.build/pkg/starlark/compile"
	"bonanza.build/pkg/starlark/resolve"
	"bonanza.build/pkg/starlark/syntax"

	"github.com/stretchr/testify/require"
)

var corpus = flag.String(
	"corpus",
	"",
	"Directory containing BUILD, MODULE.bazel and .bzl files that should be compiled, to check that the compiler is able to process them",
)

// TestCorpus compiles a corpus of Starlark files, and checks that the
// resulting bytecode is well formed.
func TestCorpus(t *testing.T) {
	if *corpus == "" {
		t.Skip("No corpus provided")
	}

	filesCompiled := 0
	walkFollowingSymlinks(t, *corpus, map[string]struct{}{}, func(path string) {
		src, err := os.ReadFile(path)
		if err != nil {
			return
		}
		f, err := syntax.Parse(path, src, syntax.Options{})
		if err != nil {
			return
		}
		m, err := resolve.File(f, dialectForFilename(filepath.Base(path)), func(string) bool { return true }, func(string) bool { return false })
		if err != nil {
			return
		}
		checkFuncode(t, path, compile.File(f, m).Toplevel)
		filesCompiled++
	})
	t.Logf("Files compiled: %d", filesCompiled)
}

// checkFuncode checks that all instructions of a function can be
// decoded, and that all jumps refer to the start of an instruction.
func checkFuncode(t *testing.T, path string, fn *compile.Funcode) {
	starts := map[uint32]struct{}{}
	var jumpTargets []uint32
	for pc := uint32(0); pc < uint32(len(fn.Code)); {
		starts[pc] = struct{}{}
		op, arg, next := compile.DecodeInstruction(fn.Code, pc)
		if op > compile.OpcodeMax {
			t.Errorf("%s: function %s: invalid opcode at %d", path, fn.Name, pc)
			return
		}
		switch op {
		case compile.JMP, compile.CJMP, compile.ITERJMP:
			jumpTargets = append(jumpTargets, arg)
		}
		pc = next
	}
	for _, target := range jumpTargets {
		if _, ok := starts[target]; !ok {
			t.Errorf("%s: function %s: jump to %d, which is not the start of an instruction", path, fn.Name, target)
		}
	}
	if _, err := fn.Disassemble(); err != nil {
		t.Errorf("%s: function %s: %s", path, fn.Name, err)
	}
	for _, nested := range fn.Functions {
		checkFuncode(t, path, nested)
	}
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
