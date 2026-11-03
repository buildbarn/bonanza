package compile_test

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bonanza.build/pkg/starlark/compile"
	"bonanza.build/pkg/starlark/resolve"
	"bonanza.build/pkg/starlark/syntax"

	"github.com/stretchr/testify/require"
)

var update = flag.Bool("update", false, "Update the expected disassembly in testdata/*.txt")

func isPredeclared(name string) bool {
	return name == "M"
}

func isUniversal(name string) bool {
	switch name {
	case "None", "True", "False", "len", "print":
		return true
	}
	return false
}

// TestCompile compiles the files in testdata/*.star, and compares the
// disassembly of the resulting bytecode against testdata/*.txt. Files
// containing "option:build" are resolved using the dialect of BUILD
// files.
func TestCompile(t *testing.T) {
	sources, err := filepath.Glob("testdata/*.star")
	require.NoError(t, err)
	require.NotEmpty(t, sources)

	for _, source := range sources {
		t.Run(filepath.Base(source), func(t *testing.T) {
			src, err := os.ReadFile(source)
			require.NoError(t, err)
			f, err := syntax.Parse(filepath.Base(source), src, syntax.Options{TypeSyntax: true})
			require.NoError(t, err)

			dialect := resolve.BzlDialect
			if strings.Contains(string(src), "option:build") {
				dialect = resolve.BuildDialect
			}
			m, err := resolve.File(f, &dialect, isPredeclared, isUniversal)
			require.NoError(t, err)

			disassembly, err := compile.File(f, m).Toplevel.Disassemble()
			require.NoError(t, err)
			got := disassembly.String()
			golden := strings.TrimSuffix(source, ".star") + ".txt"
			if *update {
				require.NoError(t, os.WriteFile(golden, []byte(got), 0o644))
				return
			}
			want, err := os.ReadFile(golden)
			require.NoError(t, err)
			require.Equal(t, string(want), got)
		})
	}
}

func TestDisassembleMalformed(t *testing.T) {
	// Functions may be loaded from storage, meaning disassembly
	// should fail gracefully if they are malformed.
	for name, tc := range map[string]struct {
		funcode compile.Funcode
		err     string
	}{
		"IllegalOpcode": {
			funcode: compile.Funcode{Code: []byte{byte(compile.OpcodeMax) + 1}},
			err:     "illegal opcode",
		},
		"TruncatedOperand": {
			funcode: compile.Funcode{Code: []byte{byte(compile.STR)}},
			err:     "malformed operand",
		},
		"UnterminatedOperand": {
			funcode: compile.Funcode{Code: []byte{byte(compile.STR), 0x40}},
			err:     "malformed operand",
		},
		"OversizedOperand": {
			funcode: compile.Funcode{Code: []byte{byte(compile.STR), 0x09, 0x00, 0x00, 0x00, 0x00}},
			err:     "malformed operand",
		},
		"NegativeOperand": {
			funcode: compile.Funcode{Code: []byte{byte(compile.STR), 0xff}},
			err:     "malformed operand",
		},
		"TruncatedJump": {
			funcode: compile.Funcode{Code: []byte{byte(compile.JMP)}},
			err:     "truncated operand",
		},
		"TruncatedWideJump": {
			// The bytecode consists of 256 bytes, meaning
			// that addresses take up two bytes. The bytes
			// preceding the jump are instructions without
			// an operand.
			funcode: compile.Funcode{Code: append(bytes.Repeat([]byte{byte(compile.POP)}, 254), byte(compile.JMP), 0x00)},
			err:     "truncated operand",
		},
		"StringOutOfRange": {
			funcode: compile.Funcode{Code: []byte{byte(compile.STR), 0x80}},
			err:     "exceeds the table",
		},
		"IntOutOfRange": {
			funcode: compile.Funcode{Code: []byte{byte(compile.INT), 0x80}},
			err:     "exceeds the table",
		},
		"FloatOutOfRange": {
			funcode: compile.Funcode{Code: []byte{byte(compile.FLOAT), 0x80}},
			err:     "exceeds the table",
		},
		"FunctionOutOfRange": {
			funcode: compile.Funcode{Code: []byte{byte(compile.MAKEFUNC), 0x81}, Functions: []*compile.Funcode{{}}},
			err:     "exceeds the table",
		},
		"LocalOutOfRange": {
			funcode: compile.Funcode{Code: []byte{byte(compile.LOCAL), 0x80}},
			err:     "exceeds the table",
		},
		"FreeVariableOutOfRange": {
			funcode: compile.Funcode{Code: []byte{byte(compile.FREE), 0x80}},
			err:     "exceeds the table",
		},
		"NameOutOfRange": {
			funcode: compile.Funcode{Code: []byte{byte(compile.GLOBAL), 0x80}},
			err:     "exceeds the table",
		},
		"ImportOutOfRange": {
			funcode: compile.Funcode{Code: []byte{byte(compile.IMPORT), 0x80}},
			err:     "exceeds the table",
		},
		"TrailingLineTableData": {
			funcode: compile.Funcode{LineTable: []byte{0x80}},
			err:     "line table is malformed",
		},
		"MissingLineTableEntry": {
			funcode: compile.Funcode{Code: []byte{byte(compile.ATTR), 0x80}, Names: []string{"a"}},
			err:     "line table is malformed",
		},
		"MissingColumn": {
			funcode: compile.Funcode{Code: []byte{byte(compile.ATTR), 0x80}, Names: []string{"a"}, LineTable: []byte{0x81}},
			err:     "line table is malformed",
		},
		"TooManyParameters": {
			funcode: compile.Funcode{NumParams: 1},
			err:     "only having 0 local variables",
		},
		"CellOutOfRange": {
			funcode: compile.Funcode{Cells: []int{0}},
			err:     "does not exist",
		},
		"MalformedNestedFunction": {
			funcode: compile.Funcode{Functions: []*compile.Funcode{{Code: []byte{byte(compile.STR), 0x80}}}},
			err:     "exceeds the table",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := tc.funcode.Disassemble()
			require.ErrorContains(t, err, tc.err)
		})
	}
}

func TestPosition(t *testing.T) {
	source := `def f(a):
    b = a
    c = g(
        b.x,
        a,
    ) + 1
    return c
`
	file, err := syntax.Parse("test.star", []byte(source), syntax.Options{})
	require.NoError(t, err)
	m, err := resolve.File(file, &resolve.BzlDialect, func(string) bool { return true }, isUniversal)
	require.NoError(t, err)
	function := compile.File(file, m).Toplevel.Functions[0]
	disassembly, err := function.Disassemble()
	require.NoError(t, err)

	// Instructions without a position of their own should report
	// the position of the closest preceding one that has one.
	own := map[compile.Opcode][]syntax.Position{}
	expected := function.Pos
	for _, instruction := range disassembly.Instructions {
		if instruction.Position.IsValid() {
			expected = instruction.Position
			own[instruction.Opcode] = append(own[instruction.Opcode], expected)
		}
		require.Equal(t, expected, function.Position(instruction.PC), "%d", instruction.PC)
	}

	require.Equal(t, map[compile.Opcode][]syntax.Position{
		// Accessing parameter "a" cannot fail.
		compile.LOCAL:       {{Line: 4, Column: 9}, {Line: 7, Column: 12}},
		compile.PREDECLARED: {{Line: 3, Column: 9}},
		compile.ATTR:        {{Line: 4, Column: 10}},
		compile.CALL:        {{Line: 3, Column: 10}},
		compile.PLUS:        {{Line: 6, Column: 7}},
	}, map[compile.Opcode][]syntax.Position{
		compile.LOCAL:       {own[compile.LOCAL][0], own[compile.LOCAL][len(own[compile.LOCAL])-1]},
		compile.PREDECLARED: own[compile.PREDECLARED],
		compile.ATTR:        own[compile.ATTR],
		compile.CALL:        own[compile.CALL],
		compile.PLUS:        own[compile.PLUS],
	})
}

func TestJumpOperandSizeBytes(t *testing.T) {
	for _, tc := range []struct {
		codeSizeBytes int
		expected      int
	}{
		{0, 1},
		{1, 1},
		{255, 1},
		{256, 2},
		{65535, 2},
		{65536, 3},
		{16777215, 3},
		{16777216, 4},
	} {
		require.Equal(t, tc.expected, compile.JumpOperandSizeBytes(tc.codeSizeBytes), "%d", tc.codeSizeBytes)
	}
}

func TestCompileJumpOperandSizes(t *testing.T) {
	// Compile functions whose sizes cross the boundaries at which
	// jump operands need to grow. All jumps should remain decodable
	// and target the start of an instruction.
	for _, statements := range []int{0, 1, 50, 60, 61, 62, 63, 64, 65, 66, 70, 80, 16300, 16380, 16381, 16382, 16383, 16384, 16385, 16390, 20000} {
		source := "def f(x):\n    for i in x:\n        if i:\n" +
			strings.Repeat("            y = i\n", statements) +
			"            pass\n"
		f, err := syntax.Parse("test.star", []byte(source), syntax.Options{})
		require.NoError(t, err)
		m, err := resolve.File(f, &resolve.BzlDialect, isPredeclared, isUniversal)
		require.NoError(t, err)
		function := compile.File(f, m).Toplevel.Functions[0]

		disassembly, err := function.Disassemble()
		require.NoError(t, err, "%d statements", statements)
		instructions := map[uint32]struct{}{}
		for _, instruction := range disassembly.Instructions {
			instructions[instruction.PC] = struct{}{}
		}
		jumps := 0
		for _, instruction := range disassembly.Instructions {
			if op := instruction.Opcode; op == compile.JMP || op == compile.CJMP || op == compile.ITERJMP {
				jumps++
				require.Contains(t, instructions, instruction.Operand, "%d statements", statements)
			}
		}
		require.NotZero(t, jumps)
	}
}

func TestCompileDeepSyntaxTrees(t *testing.T) {
	// The parser permits chains up to a limit, yielding syntax trees
	// that are as deep as they are long. The resolver and compiler
	// should be able to process these.
	for name, source := range map[string]string{
		"Binary": "x = 1" + strings.Repeat(" + 1", 99000) + "\n",
		"Dot":    "x = None" + strings.Repeat(".a", 99000) + "\n",
		"Call":   "x = None" + strings.Repeat("()", 99000) + "\n",
		"Index":  "x = None" + strings.Repeat("[0]", 99000) + "\n",
		"Elif":   "def f(x):\n    if x:\n        pass\n" + strings.Repeat("    elif x:\n        pass\n", 99000),
	} {
		t.Run(name, func(t *testing.T) {
			f, err := syntax.Parse("foo.bzl", []byte(source), syntax.Options{})
			require.NoError(t, err)
			m, err := resolve.File(
				f,
				&resolve.BzlDialect,
				func(string) bool { return false },
				func(name string) bool { return name == "None" },
			)
			require.NoError(t, err)
			require.NotNil(t, compile.File(f, m))
		})
	}
}

func TestCompileMaximumCallArguments(t *testing.T) {
	// The largest call permitted by the resolver should compile,
	// and its operand should be preserved.
	var named strings.Builder
	for i := range resolve.MaximumCallArguments {
		fmt.Fprintf(&named, "a%d = 0, ", i)
	}
	source := "x = len(" + strings.Repeat("0, ", resolve.MaximumCallArguments) + named.String() + ")\n"
	f, err := syntax.Parse("test.star", []byte(source), syntax.Options{})
	require.NoError(t, err)
	m, err := resolve.File(f, &resolve.BzlDialect, isPredeclared, isUniversal)
	require.NoError(t, err)
	disassembly, err := compile.File(f, m).Toplevel.Disassemble()
	require.NoError(t, err)
	for _, instruction := range disassembly.Instructions {
		if instruction.Opcode == compile.CALL {
			positional, named := compile.SplitCallOperand(instruction.Operand)
			require.Equal(t, resolve.MaximumCallArguments, positional)
			require.Equal(t, resolve.MaximumCallArguments, named)
			return
		}
	}
	require.Fail(t, "No call instruction found")
}

func TestCallOperand(t *testing.T) {
	t.Run("Layout", func(t *testing.T) {
		for _, tc := range []struct {
			positional int
			named      int
			operand    uint32
		}{
			{0, 0, 0},
			{1, 0, 0b01},
			{0, 1, 0b10},
			{2, 0, 0b0100},
			{0, 2, 0b1000},
			{3, 5, 0b100111},
			{0xffff, 0, 0x55555555},
			{0, 0xffff, 0xaaaaaaaa},
			{0xffff, 0xffff, 0xffffffff},
		} {
			require.Equal(t, tc.operand, compile.MakeCallOperand(tc.positional, tc.named), "%d %d", tc.positional, tc.named)
		}
	})

	t.Run("RoundTrip", func(t *testing.T) {
		for positional := 0; positional <= resolve.MaximumCallArguments; positional += 251 {
			for named := 0; named <= resolve.MaximumCallArguments; named += 257 {
				gotPositional, gotNamed := compile.SplitCallOperand(compile.MakeCallOperand(positional, named))
				require.Equal(t, positional, gotPositional)
				require.Equal(t, named, gotNamed)
			}
		}
	})
}
