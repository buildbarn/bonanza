package eval_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"bonanza.build/pkg/starlark/compile"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/resolve"
	"bonanza.build/pkg/starlark/syntax"

	"github.com/stretchr/testify/require"
)

// execForTesting executes a file, returning everything it prints.
func execForTesting(t *testing.T, src string, configure func(*eval.Thread)) (string, error) {
	f, err := syntax.Parse("test.star", []byte(src), syntax.Options{})
	require.NoError(t, err)

	universe := eval.Universe()
	predeclared := map[string]eval.Value{
		"predeclared_value": eval.String("predeclared"),
	}
	m, err := resolve.File(
		f,
		&resolve.BzlDialect,
		func(name string) bool { _, ok := predeclared[name]; return ok },
		func(name string) bool { _, ok := universe[name]; return ok },
	)
	require.NoError(t, err)
	program := compile.File(f, m)

	var output strings.Builder
	thread := eval.NewThread(context.Background(), universe)
	thread.Print = func(_ *eval.Thread, msg string) error {
		output.WriteString(msg)
		output.WriteByte('\n')
		return nil
	}
	if configure != nil {
		configure(thread)
	}
	module := eval.NewInMemoryModule("test.star", predeclared, func(_ *eval.Thread, imp compile.Import) (eval.Value, error) {
		return eval.String(imp.Module + "%" + imp.Name), nil
	})
	err = eval.ExecFile(thread, program, module)
	return output.String(), err
}

func TestExec(t *testing.T) {
	for _, test := range []struct {
		name, src, want string
	}{
		{"Arithmetic", `
print(1 + 2 * 3, 7 // 2, -7 // 2, 7 % -3, 7 / 2, 2.5 * 2, 1 << 70, (1 << 70) >> 68)
print(0x7fffffffffffffff + 1, -0x7fffffffffffffff - 2, 3037000500 * 3037000500)
print(1 & 3, 1 | 2, 5 ^ 1, ~5, -(-3), +4)
print(1 == 1.0, 1 < 1.5, 2 > 1.5, "a" < "b", [1, 2] < [1, 3], (1, 2) == (1, 2))
`, "7 3 -4 -2 3.5 5.0 1180591620717411303424 4\n9223372036854775808 -9223372036854775809 9223372037000250000\n1 3 4 -6 3 4\nTrue True True True True True\n"},
		{"Strings", `
s = "hello"
print(s + " world", s[1], s[-1], s[1:3], s[::-1], len(s), "ell" in s, s * 2)
print("%s=%d (%r) %x %%" % ("x", 42, "y", 255))
print("%(a)s-%(b)s" % {"a": 1, "b": 2})
print(repr("quote\"d\n"), str(1.0), str(1e100), str(None))
`, "hello world e o el olleh 5 True hellohello\nx=42 (\"y\") ff %\n1-2\n\"quote\\\"d\\n\" 1.0 1e+100 None\n"},
		{"Lists", `
def main():
    x = [1, 2, 3]
    x.append(4)
    x += [5]
    x.extend((6,))
    x[0] = 0
    print(x, x[1:4], x[::2], x[-2:], len(x), 3 in x, x * 2 == x + x)
    y = [x]
    x.append(y)
    print(y)
    a, (b, [c]) = 1, (2, [3])
    print(a, b, c)

main()
`, "[0, 2, 3, 4, 5, 6] [2, 3, 4] [0, 3, 5] [5, 6] 6 True True\n[[0, 2, 3, 4, 5, 6, [...]]]\n1 2 3\n"},
		{"Dicts", `
def main():
    d = {"a": 1, "b": 2}
    d["c"] = 3
    d |= {"a": 4}
    print(d, d["b"], d.get("x"), d.get("x", 5), "a" in d, len(d))
    print(d.items(), d.keys(), d.values())
    print({1: "x", 2.0: "y"}, dict(a = 1), dict([(1, 2)]), {"a": 1} == {"a": 1}, {1: 2} == {1.0: 2})
    print({k: v * 2 for k, v in d.items() if v > 2})

main()
`, "{\"a\": 4, \"b\": 2, \"c\": 3} 2 None 5 True 3\n[(\"a\", 4), (\"b\", 2), (\"c\", 3)] [\"a\", \"b\", \"c\"] [4, 2, 3]\n{1: \"x\", 2.0: \"y\"} {\"a\": 1} {1: 2} True True\n{\"a\": 8, \"c\": 6}\n"},
		{"ControlFlow", `
def f(n):
    total = 0
    for i in [1, 2, 3, 4, 5]:
        if i == n:
            continue
        if i > 4:
            break
        total += i
    for _ in [0] * 100:
        if total <= 5:
            break
        total -= 2
    return total if total else "zero"

print(f(1), f(3), [x * y for x in [1, 2] for y in [10, 20] if x != y])
print(1 and 2, 0 and 2, 0 or 3, None or 0, not None)
`, "5 5 [10, 20, 20, 40]\n2 0 3 0 True\n"},
		{"Functions", `
def f(a, b = 2, *args, c, d = 4, **kwargs):
    return (a, b, args, c, d, kwargs)

print(f(1, c = 3))
print(f(1, 5, 6, 7, c = 3, e = 8))
print(f(*[1, 2], **{"c": 3, "d": 5}))
print(type(f), f, len)
`, "(1, 2, (), 3, 4, {})\n(1, 5, (6, 7), 3, 4, {\"e\": 8})\n(1, 2, (), 3, 5, {})\n" + `function <function f> <built-in function len>` + "\n"},
		{"Closures", `
def counter():
    count = [0]
    def increment(n = 1):
        count[0] += n
        return count[0]
    return increment

c = counter()
c()
c(5)
print(c())

def adder(x):
    return lambda y: x + y

print(adder(1)(2), [adder(i)(10) for i in [1, 2, 3]])
`, "7\n3 [11, 12, 13]\n"},
		{"GlobalsAndImports", `
load("//a:a.bzl", "symbol")

def g():
    return later

later = "assigned"
print(g(), symbol, predeclared_value)
`, "assigned //a:a.bzl%symbol predeclared\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			output, err := execForTesting(t, test.src, nil)
			require.NoError(t, err)
			require.Equal(t, test.want, output)
		})
	}
}

func TestExecErrors(t *testing.T) {
	for _, test := range []struct {
		name, src, want string
	}{
		{"DivisionByZero", `
def f(x):
    return 1 // x

def g():
    return f(0)

g()
`, `Traceback (most recent call last):
	File "test.star", line 8, column 2, in <toplevel>
	File "test.star", line 6, column 13, in g
	File "test.star", line 3, column 14, in f
Error: integer division by zero`},
		{"Fail", `
def f():
    fail("oops", 42)

f()
`, `Traceback (most recent call last):
	File "test.star", line 5, column 2, in <toplevel>
	File "test.star", line 3, column 9, in f
Error in fail: oops 42`},
		{"MissingArgument", `
def f(a, b, *, c):
    pass

f(1)
`, `Traceback (most recent call last):
	File "test.star", line 5, column 2, in <toplevel>
Error: f() missing 1 required positional argument: b`},
		{"UnexpectedKeyword", `
def f(alpha):
    pass

f(alphx = 1)
`, `Traceback (most recent call last):
	File "test.star", line 5, column 2, in <toplevel>
Error: f() got unexpected keyword argument 'alphx' (did you mean 'alpha'?)`},
		{"Recursion", `
def f(n):
    return f(n - 1) if n else 0

f(3)
`, `Traceback (most recent call last):
	File "test.star", line 5, column 2, in <toplevel>
	File "test.star", line 3, column 13, in f
Error: function f called recursively`},
		{"MutationDuringIteration", `
def f():
    x = [1, 2]
    for i in x:
        x.append(i)

f()
`, `Traceback (most recent call last):
	File "test.star", line 7, column 2, in <toplevel>
	File "test.star", line 5, column 17, in f
Error in append: list value is temporarily immutable due to active for-loop iteration`},
		{"UnboundLocal", `
def f():
    print(x)
    x = 1

f()
`, `Traceback (most recent call last):
	File "test.star", line 6, column 2, in <toplevel>
	File "test.star", line 3, column 11, in f
Error: local variable x referenced before assignment`},
		{"DuplicateKey", `
x = {"a": 1, "a": 2}
`, `Traceback (most recent call last):
	File "test.star", line 2, column 17, in <toplevel>
Error: dictionary expression has duplicate key: "a"`},
		{"Unhashable", `
x = {[]: 1}
`, `Traceback (most recent call last):
	File "test.star", line 2, column 8, in <toplevel>
Error: unhashable type: list`},
		{"UnsupportedOperation", `
x = 1 + "a"
`, `Traceback (most recent call last):
	File "test.star", line 2, column 7, in <toplevel>
Error: unsupported binary operation: int + string`},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := execForTesting(t, test.src, nil)
			var evalErr *eval.Error
			require.True(t, errors.As(err, &evalErr), "got %v", err)
			require.Equal(t, test.want, evalErr.Backtrace())
		})
	}
}

func TestExecFunctionOnFirstLine(t *testing.T) {
	// A function declared at the very start of a file should not be
	// confused with the top level of the file.
	output, err := execForTesting(t, `def f():
    return 42
print(f())
`, nil)
	require.NoError(t, err)
	require.Equal(t, "42\n", output)
}

func TestExecMaximumSteps(t *testing.T) {
	_, err := execForTesting(t, `
def f():
    x = 0
    for _ in [0] * 1000000:
        x += 1

f()
`, func(thread *eval.Thread) {
		thread.MaximumSteps = 1000
	})
	require.ErrorContains(t, err, "too many steps")
}

func TestExecErrorUnwrapping(t *testing.T) {
	sentinel := errors.New("sentinel")
	f, err := syntax.Parse("test.star", []byte("def f():\n    g()\n\nf()\n"), syntax.Options{})
	require.NoError(t, err)
	m, err := resolve.File(f, &resolve.BzlDialect, func(name string) bool { return name == "g" }, func(string) bool { return false })
	require.NoError(t, err)

	thread := eval.NewThread(context.Background(), nil)
	module := eval.NewInMemoryModule("test.star", map[string]eval.Value{
		"g": eval.NewBuiltin("g", func(*eval.Thread, *eval.Builtin, eval.Args) (eval.Value, error) {
			return nil, sentinel
		}),
	}, nil)
	err = eval.ExecFile(thread, compile.File(f, m), module)
	require.ErrorIs(t, err, sentinel)
}
