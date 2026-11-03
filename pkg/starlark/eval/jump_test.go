package eval_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"bonanza.build/pkg/starlark/compile"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/resolve"
	"bonanza.build/pkg/starlark/syntax"

	"github.com/stretchr/testify/require"
)

func TestJumpsOfAllSizes(t *testing.T) {
	// The size of jump operands depends on the size of the function.
	// Execute functions containing forward and backward jumps of
	// each of the sizes that can be tested in practice.
	for name, tc := range map[string]struct {
		statements  int
		operandSize int
	}{
		"OneByte":    {statements: 5, operandSize: 1},
		"TwoBytes":   {statements: 100, operandSize: 2},
		"ThreeBytes": {statements: 10000, operandSize: 3},
	} {
		t.Run(name, func(t *testing.T) {
			src := "def f(x):\n    total = 0\n    for i in [1, 2, 3]:\n        if x:\n" +
				strings.Repeat("            total += 1\n", tc.statements) +
				"        else:\n            total -= 1\n    return total\n\n" +
				fmt.Sprintf("def check():\n    if f(True) != %d:\n        fail(\"true\")\n    if f(False) != -3:\n        fail(\"false\")\n\ncheck()\n", 3*tc.statements)
			f, err := syntax.Parse("test.star", []byte(src), syntax.Options{})
			require.NoError(t, err)
			universe := eval.Universe()
			m, err := resolve.File(
				f,
				&resolve.BzlDialect,
				func(name string) bool { return false },
				func(name string) bool { _, ok := universe[name]; return ok },
			)
			require.NoError(t, err)
			program := compile.File(f, m)
			require.Equal(t, tc.operandSize, compile.JumpOperandSizeBytes(len(program.Toplevel.Functions[0].Code)))

			thread := eval.NewThread(context.Background(), universe)
			require.NoError(t, eval.ExecFile(thread, program, eval.NewInMemoryModule("test.star", nil, nil)))
		})
	}
}
