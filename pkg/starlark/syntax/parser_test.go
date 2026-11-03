package syntax_test

import (
	"strings"
	"testing"

	"bonanza.build/pkg/starlark/syntax"

	"github.com/stretchr/testify/require"
)

// Many of the test cases below were derived from go.starlark.net's
// syntax/parse_test.go, which is Copyright 2017 The Bazel Authors and
// governed by a BSD-style license.

// stripFilename removes the "foo.star:" prefix from error messages.
func stripFilename(err error) string {
	return strings.TrimPrefix(err.Error(), "foo.star:")
}

func TestParseExpr(t *testing.T) {
	for _, test := range []struct {
		input, want string
	}{
		{`print(1)`, `(CallExpr Fn=print Args=((Arg Value=1)))`},
		{"print(1)\n", `(CallExpr Fn=print Args=((Arg Value=1)))`},
		{`x + 1`, `(BinaryExpr X=x Op=+ Y=1)`},
		{`[x for x in y]`, `(Comprehension Body=x Clauses=((ForClause Vars=x X=y)))`},
		{`[x for x in (a if b else c)]`, `(Comprehension Body=x Clauses=((ForClause Vars=x X=(ParenExpr X=(CondExpr True=a Cond=b False=c)))))`},
		{`x[i].f(42)`, `(CallExpr Fn=(DotExpr X=(IndexExpr X=x Y=i) Name=f) Args=((Arg Value=42)))`},
		{`x.f()`, `(CallExpr Fn=(DotExpr X=x Name=f))`},
		{`x+y*z`, `(BinaryExpr X=x Op=+ Y=(BinaryExpr X=y Op=* Y=z))`},
		{`x%y-z`, `(BinaryExpr X=(BinaryExpr X=x Op=% Y=y) Op=- Y=z)`},
		{`a + b not in c`, `(BinaryExpr X=(BinaryExpr X=a Op=+ Y=b) Op=not in Y=c)`},
		{`lambda x, *args, **kwargs: None`, `(LambdaExpr Params=((Param Name=x) (Param Kind=*args Name=args) (Param Kind=**kwargs Name=kwargs)) Body=None)`},
		{`lambda: 0`, `(LambdaExpr Body=0)`},
		{`{"one": 1}`, `(DictExpr Entries=((DictEntry Key="one" Value=1)))`},
		{`a[i]`, `(IndexExpr X=a Y=i)`},
		{`a[i:]`, `(SliceExpr X=a Lo=i)`},
		{`a[:j]`, `(SliceExpr X=a Hi=j)`},
		{`a[::]`, `(SliceExpr X=a)`},
		{`a[::k]`, `(SliceExpr X=a Step=k)`},
		{`[]`, `(ListExpr)`},
		{`[1]`, `(ListExpr List=(1))`},
		{`[1,]`, `(ListExpr List=(1))`},
		{`[1, 2]`, `(ListExpr List=(1 2))`},
		{`()`, `(TupleExpr)`},
		{`(4,)`, `(ParenExpr X=(TupleExpr List=(4)))`},
		{`(4)`, `(ParenExpr X=4)`},
		{`(4, 5)`, `(ParenExpr X=(TupleExpr List=(4 5)))`},
		{`1, 2, 3`, `(TupleExpr List=(1 2 3))`},
		{`1, 2,`, `1:5: unparenthesized tuple with trailing comma`},
		{`{}`, `(DictExpr)`},
		{`{"a": 1,}`, `(DictExpr Entries=((DictEntry Key="a" Value=1)))`},
		{`{"a": 1, "b": 2}`, `(DictExpr Entries=((DictEntry Key="a" Value=1) (DictEntry Key="b" Value=2)))`},
		{`{x: y for (x, y) in z}`, `(Comprehension Curly Body=(DictEntry Key=x Value=y) Clauses=((ForClause Vars=(ParenExpr X=(TupleExpr List=(x y))) X=z)))`},
		{`{x: y for a in b if c}`, `(Comprehension Curly Body=(DictEntry Key=x Value=y) Clauses=((ForClause Vars=a X=b) (IfClause Cond=c)))`},
		{`-1 + +2`, `(BinaryExpr X=(UnaryExpr Op=- X=1) Op=+ Y=(UnaryExpr Op=+ X=2))`},
		{`"foo" + "bar"`, `(BinaryExpr X="foo" Op=+ Y="bar")`},
		{`-1 * 2`, `(BinaryExpr X=(UnaryExpr Op=- X=1) Op=* Y=2)`},
		{`-x[i]`, `(UnaryExpr Op=- X=(IndexExpr X=x Y=i))`},
		{`a | b & c | d`, `(BinaryExpr X=(BinaryExpr X=a Op=| Y=(BinaryExpr X=b Op=& Y=c)) Op=| Y=d)`},
		{`a or b and c or d`, `(BinaryExpr X=(BinaryExpr X=a Op=or Y=(BinaryExpr X=b Op=and Y=c)) Op=or Y=d)`},
		{`a and b or c and d`, `(BinaryExpr X=(BinaryExpr X=a Op=and Y=b) Op=or Y=(BinaryExpr X=c Op=and Y=d))`},
		{`f(1, x=y)`, `(CallExpr Fn=f Args=((Arg Value=1) (Arg Kind=named Name=x Value=y)))`},
		{`f(*args, **kwargs)`, `(CallExpr Fn=f Args=((Arg Kind=*args Value=args) (Arg Kind=**kwargs Value=kwargs)))`},
		{`f(x,)`, `(CallExpr Fn=f Args=((Arg Value=x)))`},
		{`f(a.b=c)`, `1:6: keyword argument must have form name=expr`},
		{`lambda *args, *, x=1, **kwargs: 0`, `(LambdaExpr Params=((Param Kind=*args Name=args) (Param Kind=*) (Param Name=x Default=1) (Param Kind=**kwargs Name=kwargs)) Body=0)`},
		{`lambda *, a, *b: 0`, `(LambdaExpr Params=((Param Kind=*) (Param Name=a) (Param Kind=*args Name=b)) Body=0)`},
		{`a if b else c`, `(CondExpr True=a Cond=b False=c)`},
		{`a if b`, `1:3: conditional expression without else clause`},
		{`a and not b`, `(BinaryExpr X=a Op=and Y=(UnaryExpr Op=not X=b))`},
		{`not not a`, `(UnaryExpr Op=not X=(UnaryExpr Op=not X=a))`},
		{`a not b`, `1:7: got identifier, want in`},
		{`a < b < c`, `1:7: < does not associate with < (use parentheses)`},
		{`[e for x in y if cond1 if cond2]`, `(Comprehension Body=e Clauses=((ForClause Vars=x X=y) (IfClause Cond=cond1) (IfClause Cond=cond2)))`},
		{`[e for x, y in z]`, `(Comprehension Body=e Clauses=((ForClause Vars=(TupleExpr List=(x y)) X=z)))`},
		{"[\n  1,\n  2,\n]", `(ListExpr List=(1 2))`},
		{`0x1F + 0o17 + 0b101 + 1.5e3 + .5`, `(BinaryExpr X=(BinaryExpr X=(BinaryExpr X=(BinaryExpr X=0x1F Op=+ Y=0o17) Op=+ Y=0b101) Op=+ Y=1.5e3) Op=+ Y=.5)`},
		{`...`, `1:1: '...' is only permitted in type expressions`},
		{`cast(int, x)`, `(CallExpr Fn=cast Args=((Arg Value=int) (Arg Value=x)))`},
	} {
		t.Run(test.input, func(t *testing.T) {
			x, err := syntax.ParseExpr("foo.star", []byte(test.input), syntax.Options{})
			var got string
			if err != nil {
				got = stripFilename(err)
			} else {
				got = treeString(x)
			}
			require.Equal(t, test.want, got)
		})
	}
}

func TestParseStmts(t *testing.T) {
	for _, test := range []struct {
		input, want string
	}{
		{`print(1)`, `((ExprStmt X=(CallExpr Fn=print Args=((Arg Value=1)))))`},
		{`return 1, 2`, `((ReturnStmt Result=(TupleExpr List=(1 2))))`},
		{`return`, `((ReturnStmt))`},
		{`for i in "abc": break`, `((ForStmt Vars=i X="abc" Body=((BranchStmt Token=break))))`},
		{`for i in "abc": continue`, `((ForStmt Vars=i X="abc" Body=((BranchStmt Token=continue))))`},
		{`for x, y in z: pass`, `((ForStmt Vars=(TupleExpr List=(x y)) X=z Body=((BranchStmt Token=pass))))`},
		{`if True: pass`, `((IfStmt Cond=True True=((BranchStmt Token=pass))))`},
		{"if True: pass\nelse:\n  pass", `((IfStmt Cond=True True=((BranchStmt Token=pass)) False=((BranchStmt Token=pass))))`},
		{"if a: pass\nelif b: pass\nelse: pass", `((IfStmt Cond=a True=((BranchStmt Token=pass)) False=((IfStmt Cond=b True=((BranchStmt Token=pass)) False=((BranchStmt Token=pass))))))`},
		{"while x:\n  x -= 1", `1:1: while statements are not permitted`},
		{`x, y = 1, 2`, `((AssignStmt LHS=(TupleExpr List=(x y)) Op== RHS=(TupleExpr List=(1 2))))`},
		{`x[i] = 1`, `((AssignStmt LHS=(IndexExpr X=x Y=i) Op== RHS=1))`},
		{`x.f = 1`, `((AssignStmt LHS=(DotExpr X=x Name=f) Op== RHS=1))`},
		{`(x, y) = 1`, `((AssignStmt LHS=(ParenExpr X=(TupleExpr List=(x y))) Op== RHS=1))`},
		{`x |= y`, `((AssignStmt LHS=x Op=|= RHS=y))`},
		{`load("", "a", b="c")`, `((LoadStmt Module="" Bindings=((LoadBinding Local=a Name="a") (LoadBinding Local=b Name="c"))))`},
		{`load("", "a",)`, `((LoadStmt Module="" Bindings=((LoadBinding Local=a Name="a"))))`},
		{`if True: load("", "a", b="c")`, `((IfStmt Cond=True True=((LoadStmt Module="" Bindings=((LoadBinding Local=a Name="a") (LoadBinding Local=b Name="c"))))))`},
		{`load("")`, `1:5: load statement must import at least one symbol`},
		{`load(x, "a")`, `1:6: first operand of load statement must be a string literal`},
		{`load("", a)`, `1:11: load operand must be "a" or a = "originalname" (want '=' after a)`},
		{`load("", a=b)`, `1:12: original name of loaded symbol must be quoted: a = "originalname"`},
		{"def f(x, *args, **kwargs):\n  pass", `((DefStmt Name=f Params=((Param Name=x) (Param Kind=*args Name=args) (Param Kind=**kwargs Name=kwargs)) Body=((BranchStmt Token=pass))))`},
		{`def f(**kwargs, *args): pass`, `((DefStmt Name=f Params=((Param Kind=**kwargs Name=kwargs) (Param Kind=*args Name=args)) Body=((BranchStmt Token=pass))))`},
		{`def f(a, b, c=d): pass`, `((DefStmt Name=f Params=((Param Name=a) (Param Name=b) (Param Name=c Default=d)) Body=((BranchStmt Token=pass))))`},
		{`def f(a, b=c, d): pass`, `((DefStmt Name=f Params=((Param Name=a) (Param Name=b Default=c) (Param Name=d)) Body=((BranchStmt Token=pass))))`},
		{`def f(a,): pass`, `((DefStmt Name=f Params=((Param Name=a)) Body=((BranchStmt Token=pass))))`},
		{"def f():\n  def g():\n    pass\n  pass\ndef h():\n  pass", `((DefStmt Name=f Body=((DefStmt Name=g Body=((BranchStmt Token=pass))) (BranchStmt Token=pass))) (DefStmt Name=h Body=((BranchStmt Token=pass))))`},
		{"f();g()", `((ExprStmt X=(CallExpr Fn=f)) (ExprStmt X=(CallExpr Fn=g)))`},
		{"f();", `((ExprStmt X=(CallExpr Fn=f)))`},
		{"f();g()\n", `((ExprStmt X=(CallExpr Fn=f)) (ExprStmt X=(CallExpr Fn=g)))`},
		{"f();\n", `((ExprStmt X=(CallExpr Fn=f)))`},
		{"x = 1  # comment\n# another\n\ny = 2", `((AssignStmt LHS=x Op== RHS=1) (AssignStmt LHS=y Op== RHS=2))`},
		{"x = [\n  1,\n\n  2]", `((AssignStmt LHS=x Op== RHS=(ListExpr List=(1 2))))`},
		{"x = 1 + \\\n  2", `((AssignStmt LHS=x Op== RHS=(BinaryExpr X=1 Op=+ Y=2)))`},
		{"x = 1\r\ny = 2\r\n", `((AssignStmt LHS=x Op== RHS=1) (AssignStmt LHS=y Op== RHS=2))`},
		{"if x:\n    pass\n  pass", `3:3: unindent does not match any outer indentation level`},
		{"if x:\n\tpass", `2:1: tab characters are not allowed for indentation; use spaces instead`},
		{"f(\n\tx)", `((ExprStmt X=(CallExpr Fn=f Args=((Arg Value=x)))))`},
		{"x = 1 = 2", `1:7: got '=', want newline`},
		{`x: int = 1`, `1:2: variable type annotations are not permitted, as type syntax is not enabled`},
		{`def f(x: int): pass`, `1:8: parameter type annotations are not permitted, as type syntax is not enabled`},
		{`def f() -> int: pass`, `1:9: return type annotations are not permitted, as type syntax is not enabled`},
		{`def f[T](): pass`, `1:6: type parameters are not permitted, as type syntax is not enabled`},
		{`type = 1`, `((AssignStmt LHS=type Op== RHS=1))`},
		{`class = 1`, `1:1: keyword "class" is reserved for future use`},
	} {
		t.Run(test.input, func(t *testing.T) {
			f, err := syntax.Parse("foo.star", []byte(test.input), syntax.Options{})
			var got string
			if err != nil {
				got = stripFilename(err)
			} else {
				got = treeString(f.Stmts)
			}
			require.Equal(t, test.want, got)
		})
	}
}

func TestParseTypeSyntax(t *testing.T) {
	for _, test := range []struct {
		input, want string
	}{
		{`def f(x: int, y: str = "", *args: int, **kwargs: bool) -> None: pass`, `((DefStmt Name=f Params=((Param Name=x Type=int) (Param Name=y Type=str Default="") (Param Kind=*args Name=args Type=int) (Param Kind=**kwargs Name=kwargs Type=bool)) ReturnType=None Body=((BranchStmt Token=pass))))`},
		{`def f(*, x: int): pass`, `((DefStmt Name=f Params=((Param Kind=*) (Param Name=x Type=int)) Body=((BranchStmt Token=pass))))`},
		{`def f[T, U](x: T) -> U: pass`, `((DefStmt Name=f TypeParams=(T U) Params=((Param Name=x Type=T)) ReturnType=U Body=((BranchStmt Token=pass))))`},
		{`x: int = 1`, `((AssignStmt LHS=x Type=int Op== RHS=1))`},
		{`x: list[str]`, `((VarStmt Name=x Type=(TypeApplication Type=list Args=(str))))`},
		{`x: dict[str, int | None] = {}`, `((AssignStmt LHS=x Type=(TypeApplication Type=dict Args=(str (BinaryExpr X=int Op=| Y=None))) Op== RHS=(DictExpr)))`},
		{`x: a.b.C[...] = y`, `((AssignStmt LHS=x Type=(TypeApplication Type=(DotExpr X=(DotExpr X=a Name=b) Name=C) Args=((EllipsisExpr))) Op== RHS=y))`},
		{`x: () | (int) | (int,) | (int, str)`, `((VarStmt Name=x Type=(BinaryExpr X=(BinaryExpr X=(BinaryExpr X=(TupleExpr) Op=| Y=(ParenExpr X=int)) Op=| Y=(ParenExpr X=(TupleExpr List=(int)))) Op=| Y=(ParenExpr X=(TupleExpr List=(int str))))))`},
		{`x: Callable[[int, str,], bool]`, `((VarStmt Name=x Type=(TypeApplication Type=Callable Args=((ListExpr List=(int str)) bool))))`},
		{`x: TypedDict[{"a": int, "b": str,}]`, `((VarStmt Name=x Type=(TypeApplication Type=TypedDict Args=((DictExpr Entries=((DictEntry Key="a" Value=int) (DictEntry Key="b" Value=str)))))))`},
		{`x: Literal["foo"]`, `((VarStmt Name=x Type=(TypeApplication Type=Literal Args=("foo"))))`},
		{`x: dict[str, int,]`, `1:18: trailing commas are not permitted in type applications`},
		{`x: 1`, `1:4: got int literal, want type expression`},
		{`x: f()`, `1:5: got '(', want newline`},
		{`x.y: int = 1`, `1:4: type annotations must have a single identifier on the left-hand side`},
		{`x: int += 1`, `1:8: type annotations are not permitted on augmented assignments`},
		{`type X = int | str`, `((TypeAliasStmt Name=X Value=(BinaryExpr X=int Op=| Y=str)))`},
		{`type X[T] = list[T]`, `((TypeAliasStmt Name=X TypeParams=(T) Value=(TypeApplication Type=list Args=(T))))`},
		{`type = 1`, `((AssignStmt LHS=type Op== RHS=1))`},
		{`type(x)`, `((ExprStmt X=(CallExpr Fn=type Args=((Arg Value=x)))))`},
		{`y = cast(list[int], x)`, `((AssignStmt LHS=y Op== RHS=(CastExpr Type=(TypeApplication Type=list Args=(int)) Value=x)))`},
		{`y = isinstance(x, int | str,)`, `((AssignStmt LHS=y Op== RHS=(IsInstanceExpr Value=x Type=(BinaryExpr X=int Op=| Y=str))))`},
		{`cast = 1`, `1:6: got '=', want '('`},
		{`f = lambda x: x`, `((AssignStmt LHS=f Op== RHS=(LambdaExpr Params=((Param Name=x)) Body=x)))`},
	} {
		t.Run(test.input, func(t *testing.T) {
			f, err := syntax.Parse("foo.star", []byte(test.input), syntax.Options{TypeSyntax: true})
			var got string
			if err != nil {
				got = stripFilename(err)
			} else {
				got = treeString(f.Stmts)
			}
			require.Equal(t, test.want, got)
		})
	}
}

func TestParseSpans(t *testing.T) {
	f, err := syntax.Parse("foo.star", []byte("def f(x, *, y = 1):\n  return [x + y]\n\nz = f(1, y = 2)\n"), syntax.Options{})
	require.NoError(t, err)

	var got []string
	syntax.Walk(f, func(n syntax.Node) bool {
		if _, ok := n.(*syntax.File); !ok {
			start, end := n.Span()
			got = append(got, treeString(n)+" "+start.String()+"-"+end.String())
		}
		return true
	})
	require.Equal(t, []string{
		"(DefStmt Name=f Params=((Param Name=x) (Param Kind=*) (Param Name=y Default=1)) Body=((ReturnStmt Result=(ListExpr List=((BinaryExpr X=x Op=+ Y=y)))))) 1:1-2:17",
		"f 1:5-1:6",
		"(Param Name=x) 1:7-1:8",
		"x 1:7-1:8",
		"(Param Kind=*) 1:10-1:11",
		"(Param Name=y Default=1) 1:13-1:18",
		"y 1:13-1:14",
		"1 1:17-1:18",
		"(ReturnStmt Result=(ListExpr List=((BinaryExpr X=x Op=+ Y=y)))) 2:3-2:17",
		"(ListExpr List=((BinaryExpr X=x Op=+ Y=y))) 2:10-2:17",
		"(BinaryExpr X=x Op=+ Y=y) 2:11-2:16",
		"x 2:11-2:12",
		"y 2:15-2:16",
		"(AssignStmt LHS=z Op== RHS=(CallExpr Fn=f Args=((Arg Value=1) (Arg Kind=named Name=y Value=2)))) 4:1-4:16",
		"z 4:1-4:2",
		"(CallExpr Fn=f Args=((Arg Value=1) (Arg Kind=named Name=y Value=2))) 4:5-4:16",
		"f 4:5-4:6",
		"(Arg Value=1) 4:7-4:8",
		"1 4:7-4:8",
		"(Arg Kind=named Name=y Value=2) 4:10-4:15",
		"y 4:10-4:11",
		"2 4:14-4:15",
	}, got)
}

func TestParseDepthLimits(t *testing.T) {
	// repeat creates an expression consisting of a prefix and suffix
	// that are repeated n times.
	repeat := func(prefix, body, suffix string, n int) string {
		return strings.Repeat(prefix, n) + body + strings.Repeat(suffix, n)
	}
	parse := func(input string) error {
		_, err := syntax.Parse("foo.star", []byte(input), syntax.Options{TypeSyntax: true})
		return err
	}

	t.Run("Nesting", func(t *testing.T) {
		// Exceeding the limit should yield a regular syntax
		// error, regardless of the kind of construct.
		for name, nested := range map[string]func(n int) string{
			"Parentheses": func(n int) string { return repeat("(", "1", ")", n) },
			"List":        func(n int) string { return repeat("[", "1", "]", n) },
			"Dict":        func(n int) string { return repeat("{1: ", "1", "}", n) },
			"Call":        func(n int) string { return repeat("f(", "1", ")", n) },
			"Index":       func(n int) string { return repeat("x[", "1", "]", n) },
			"Minus":       func(n int) string { return strings.Repeat("-", n) + "1" },
			"Not":         func(n int) string { return strings.Repeat("not ", n) + "x" },
			"Lambda":      func(n int) string { return strings.Repeat("lambda: ", n) + "1" },
			"Conditional": func(n int) string { return strings.Repeat("1 if 1 else ", n) + "1" },
			"Comprehension": func(n int) string {
				return repeat("[1 for x in ", "y", "]", n)
			},
		} {
			t.Run(name, func(t *testing.T) {
				require.NoError(t, parse("x = "+nested(900)+"\n"))
				require.ErrorContains(t, parse("x = "+nested(1100)+"\n"), "exceeds the maximum nesting depth of 1000")
			})
		}

		t.Run("Types", func(t *testing.T) {
			require.NoError(t, parse("x: "+repeat("list[", "int", "]", 400)+" = 1\n"))
			require.ErrorContains(t, parse("x: "+repeat("list[", "int", "]", 1100)+" = 1\n"), "exceeds the maximum nesting depth of 1000")
		})

		t.Run("Statements", func(t *testing.T) {
			var sb strings.Builder
			for i := 0; i < 1100; i++ {
				sb.WriteString(strings.Repeat(" ", i) + "if x:\n")
			}
			sb.WriteString(strings.Repeat(" ", 1100) + "pass\n")
			require.ErrorContains(t, parse(sb.String()), "exceeds the maximum nesting depth of 1000")
		})
	})

	t.Run("Chains", func(t *testing.T) {
		for name, chain := range map[string]func(n int) string{
			"Binary": func(n int) string { return "x = 1" + strings.Repeat(" + 1", n) + "\n" },
			"Dot":    func(n int) string { return "x = y" + strings.Repeat(".a", n) + "\n" },
			"Call":   func(n int) string { return "x = y" + strings.Repeat("()", n) + "\n" },
			"Index":  func(n int) string { return "x = y" + strings.Repeat("[0]", n) + "\n" },
			"Elif":   func(n int) string { return "if x:\n pass\n" + strings.Repeat("elif x:\n pass\n", n) },
			"TypeUnion": func(n int) string {
				return "x: int" + strings.Repeat(" | int", n) + " = 1\n"
			},
		} {
			t.Run(name, func(t *testing.T) {
				require.NoError(t, parse(chain(90000)))
				require.ErrorContains(t, parse(chain(110000)), "exceeds the maximum syntax tree depth of 100000")
			})
		}
	})

	t.Run("ChainsInsideParentheses", func(t *testing.T) {
		// A chain that follows an operand that is itself deep
		// yields a tree whose depth is the sum of the two, even
		// if neither exceeds the limit on its own.
		chain := strings.Repeat(" + 1", 40000)
		require.NoError(t, parse("x = (1"+chain+")\n"))
		require.ErrorContains(t, parse("x = (((1"+chain+")"+chain+")"+chain+")\n"), "exceeds the maximum syntax tree depth of 100000")
		require.ErrorContains(t, parse("x = (y"+strings.Repeat(".a", 60000)+")"+strings.Repeat(".a", 60000)+"\n"), "exceeds the maximum syntax tree depth of 100000")
	})

	t.Run("WideCodeIsNotAffected", func(t *testing.T) {
		// Only the depth of the tree should be limited, not its
		// size. Neither siblings, nor the right hand operands of
		// a chain should count towards the depth of each other.
		require.NoError(t, parse("x = ["+strings.Repeat("f(g(y)).a, ", 200000)+"]\n"))
		require.NoError(t, parse("x = 1"+strings.Repeat(" + f(g(h(y)))", 30000)+"\n"))
		require.NoError(t, parse(strings.Repeat("x = f(g(y))\n", 200000)))
	})

	t.Run("LargeInputs", func(t *testing.T) {
		// Input of the maximum size permitted for .bzl files
		// that previously caused the process to terminate.
		const sizeBytes = 1 << 22
		for name, input := range map[string]string{
			"Unbalanced":  "x = " + strings.Repeat("(", sizeBytes),
			"Balanced":    "x = " + repeat("(", "1", ")", sizeBytes/2),
			"Minus":       "x = " + strings.Repeat("-", sizeBytes) + "1\n",
			"Binary":      "x = 1" + strings.Repeat("+1", sizeBytes/2) + "\n",
			"Dot":         "x = y" + strings.Repeat(".a", sizeBytes/2) + "\n",
			"Conditional": "x = " + strings.Repeat("1 if 1 else ", sizeBytes/12) + "1\n",
		} {
			t.Run(name, func(t *testing.T) {
				require.ErrorContains(t, parse(input), "code is nested too deeply")
			})
		}
	})
}
