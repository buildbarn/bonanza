// Package resolve performs name resolution on Starlark syntax trees,
// and checks that they only use the syntax permitted by the dialect of
// the file that is being processed (e.g., BUILD vs. .bzl files).
//
// Resolution follows the behavior of Bazel. In particular:
//
//   - Before resolving the names used in a block (the top level of the
//     file, a function or a comprehension), all bindings made within
//     that block are created. A name that is assigned anywhere at the
//     top level of a file thus refers to that global everywhere in the
//     file, even before the assignment.
//   - Symbols bound by load() are file-local, and have their own scope
//     (Import). Unlike go.starlark.net, functions referring to them do
//     not capture them as free variables, so that they can be resolved
//     lazily.
//
// The syntax tree is not modified. Results are returned in the form of
// a Module, which maps syntax tree nodes to bindings and functions.
package resolve

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"bonanza.build/pkg/starlark/syntax"
)

// MaximumCallArguments is the maximum number of positional and named
// arguments that can be passed to a function call.
const MaximumCallArguments = 1<<16 - 1

// Scope of a binding, indicating where its value is stored.
type Scope uint8

const (
	// Undefined is used for names that could not be resolved. A
	// resolution error is reported for these.
	Undefined Scope = iota
	// Local is a variable that is local to a function or
	// comprehension. Index refers to Function.Locals.
	Local
	// Cell is a local variable that is captured by a nested
	// function. Index refers to Function.Locals.
	Cell
	// Free is a variable that is captured from an enclosing
	// function. Index refers to Function.FreeVars.
	Free
	// Global is a variable that is assigned at the top level of the
	// module. Index refers to Module.Globals.
	Global
	// Import is a symbol that is bound by a load() statement. Index
	// refers to Module.Imports.
	Import
	// Predeclared is a name that is provided by the application,
	// such as glob() or native.
	Predeclared
	// Universal is a name that is provided by the core of the
	// language, such as None or len().
	Universal
	// TypeParameter is a type parameter of a function or type alias,
	// such as T in "def f[T](x: T) -> T". These only occur within
	// type expressions.
	TypeParameter
)

var scopeNames = [...]string{
	Undefined:     "undefined",
	Local:         "local",
	Cell:          "cell",
	Free:          "free",
	Global:        "global",
	Import:        "import",
	Predeclared:   "predeclared",
	Universal:     "universal",
	TypeParameter: "type parameter",
}

func (s Scope) String() string {
	return scopeNames[s]
}

// Binding of a name to a storage location.
//
// All uses of a name that refer to the same variable share a single
// Binding. A Local binding may thus turn into a Cell binding after it
// has been returned for some of its uses, if it turns out that a
// nested function captures it.
type Binding struct {
	Scope Scope
	// Index into the slice corresponding to the scope. Unused for
	// Predeclared, Universal, TypeParameter and Undefined.
	Index int
	// The identifier of the first binding use of the name, if any.
	First *syntax.Ident
}

// LoadedSymbol is a symbol that is bound by a load() statement.
type LoadedSymbol struct {
	// Module string, as provided to the load() statement. The
	// caller is responsible for converting it to a canonical label.
	Module string
	// Name of the symbol in the loaded module.
	Name string
	// Binding of the symbol in the loading module. Its scope is
	// Import.
	Binding *Binding
}

// Function contains the results of resolving a function, lambda, or
// the top level of a module.
type Function struct {
	// Name of the function. "lambda" for lambdas, and "<toplevel>"
	// for the top level of a module.
	Name string
	// Position of the "def" or "lambda" keyword. Unset for the
	// top level of a module.
	Pos syntax.Position
	// The *syntax.DefStmt or *syntax.LambdaExpr. Nil for the top
	// level of a module.
	Node   syntax.Node
	Params []*syntax.Param

	// Local variables of the function. Parameters come first, in
	// the order in which they are passed at runtime: positional
	// parameters, keyword-only parameters, *args, and **kwargs. The
	// bare "*" parameter does not have a local variable.
	Locals []*Binding
	// Variables captured from enclosing functions. Each entry
	// refers to the binding in the directly enclosing function,
	// which has scope Cell or Free.
	FreeVars []*Binding

	NumKeywordOnlyParams int
	HasArgs              bool
	HasKwargs            bool

	// Sorted indices into Module.Globals and Module.Imports of the
	// globals and imports referenced by this function, or any
	// function nested inside it.
	ReferencedGlobals []int
	ReferencedImports []int

	referencedGlobals map[int]struct{}
	referencedImports map[int]struct{}
}

// Module contains the results of resolving a file.
type Module struct {
	// Variables that are assigned at the top level, in the order in
	// which they are first bound.
	Globals []*Binding
	// Symbols bound by load() statements, in the order in which
	// they appear.
	Imports []*LoadedSymbol
	// The top level of the module. Its locals are the variables of
	// comprehensions at the top level.
	Toplevel *Function

	bindings  map[*syntax.Ident]*Binding
	functions map[syntax.Node]*Function
}

// Binding returns the binding of an identifier in the syntax tree,
// which may either be a use or a binding use of a name. It returns nil
// for identifiers that are not names, such as attribute names of dot
// expressions and keyword argument names, and for identifiers in type
// expressions when type syntax is not resolved.
func (m *Module) Binding(id *syntax.Ident) *Binding {
	return m.bindings[id]
}

// Function returns the resolved function corresponding to a
// *syntax.DefStmt or *syntax.LambdaExpr.
func (m *Module) Function(n syntax.Node) *Function {
	return m.functions[n]
}

// RestrictedSyntax contains the restrictions that apply to files that
// are only intended to declare things (BUILD, MODULE.bazel and
// REPO.bazel files), as opposed to .bzl files.
type RestrictedSyntax struct {
	// Description of the kind of file, used in error messages
	// (e.g., "BUILD files").
	FileDescription string
	// Whether load() statements are permitted.
	AllowLoad bool
	// Whether **kwargs arguments are permitted if they are dict
	// literals.
	AllowLiteralKwargs bool
}

// Dialect of the Starlark language that is accepted by the resolver.
type Dialect struct {
	// If set, functions, lambdas, if and for statements, and *args
	// and **kwargs arguments are not permitted.
	RestrictedSyntax *RestrictedSyntax
	// Permit multiple assignments to the same top-level name.
	AllowToplevelRebinding bool
	// Require load() statements to appear before any other
	// statements, except for string literals (docstrings).
	RequireLoadStatementsFirst bool
	// Resolve the names used in type annotations. If unset, type
	// annotations are ignored, and type alias statements bind no
	// names.
	ResolveTypeSyntax bool
}

var (
	// BzlDialect is the dialect of .bzl files.
	BzlDialect = Dialect{
		RequireLoadStatementsFirst: true,
	}
	// BuildDialect is the dialect of BUILD files.
	BuildDialect = Dialect{
		RestrictedSyntax: &RestrictedSyntax{
			FileDescription: "BUILD files",
			AllowLoad:       true,
		},
		AllowToplevelRebinding: true,
	}
	// ModuleDotBazelDialect is the dialect of MODULE.bazel files.
	ModuleDotBazelDialect = Dialect{
		RestrictedSyntax: &RestrictedSyntax{
			FileDescription:    "MODULE.bazel files",
			AllowLiteralKwargs: true,
		},
		RequireLoadStatementsFirst: true,
	}
	// RepoDotBazelDialect is the dialect of REPO.bazel files.
	RepoDotBazelDialect = Dialect{
		RestrictedSyntax: &RestrictedSyntax{
			FileDescription:    "REPO.bazel files",
			AllowLiteralKwargs: true,
		},
		RequireLoadStatementsFirst: true,
	}
)

// ErrorList contains all resolution errors of a file, sorted by
// position.
type ErrorList []*syntax.Error

func (e ErrorList) Error() string {
	if len(e) == 1 {
		return e[0].Error()
	}
	return fmt.Sprintf("%s (and %d more errors)", e[0].Error(), len(e)-1)
}

// File resolves the names in a file. The isPredeclared and isUniversal
// functions report whether a name is predeclared by the application
// or by the core of the language, respectively.
//
// If the file contains errors, an ErrorList is returned. The Module is
// returned in that case as well, though it may be incomplete.
func File(file *syntax.File, dialect *Dialect, isPredeclared, isUniversal func(name string) bool) (*Module, error) {
	r := &resolver{
		filename:      file.Path,
		dialect:       dialect,
		isPredeclared: isPredeclared,
		isUniversal:   isUniversal,
		module: &Module{
			Toplevel:  &Function{Name: "<toplevel>"},
			bindings:  map[*syntax.Ident]*Binding{},
			functions: map[syntax.Node]*Function{},
		},
		toplevel: map[string]*Binding{},
	}
	r.resolveFile(file)

	for _, function := range append(slices.Collect(maps.Values(r.module.functions)), r.module.Toplevel) {
		function.ReferencedGlobals = sortedKeys(function.referencedGlobals)
		function.ReferencedImports = sortedKeys(function.referencedImports)
		function.referencedGlobals = nil
		function.referencedImports = nil
	}

	if len(r.errors) > 0 {
		slices.SortStableFunc(r.errors, func(a, b *syntax.Error) int {
			return a.Pos.Compare(b.Pos)
		})
		return r.module, r.errors
	}
	return r.module, nil
}

func sortedKeys(m map[int]struct{}) []int {
	if len(m) == 0 {
		return nil
	}
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// describeExpr returns a description of an expression for use in error
// messages.
func describeExpr(x syntax.Expr) string {
	switch x.(type) {
	case *syntax.BinaryExpr, *syntax.UnaryExpr:
		return "an operator expression"
	case *syntax.CallExpr, *syntax.CastExpr, *syntax.IsInstanceExpr:
		return "a function call"
	case *syntax.Comprehension:
		return "a comprehension"
	case *syntax.CondExpr:
		return "a conditional expression"
	case *syntax.DictExpr:
		return "a dict expression"
	case *syntax.LambdaExpr:
		return "a lambda"
	case *syntax.Literal:
		return "a literal"
	case *syntax.SliceExpr:
		return "a slice expression"
	default:
		return strings.TrimPrefix(fmt.Sprintf("%T", x), "*syntax.")
	}
}
