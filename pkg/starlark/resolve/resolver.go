package resolve

import (
	"fmt"

	"bonanza.build/pkg/starlark/syntax"
)

// block is a lexical scope in which names can be bound: the top level
// of the file, the body of a function, a comprehension, or the type
// parameters of a function or type alias.
type block struct {
	parent *block
	// Function in which local variables bound in this block are
	// allocated. For comprehensions, this is the enclosing
	// function.
	function *Function
	// Whether this block is the body of a function. Uses of local
	// variables of enclosing functions within this block cause
	// them to be captured as free variables.
	isFunction bool
	bindings   map[string]*Binding
}

type resolver struct {
	filename      string
	dialect       *Dialect
	isPredeclared func(string) bool
	isUniversal   func(string) bool
	module        *Module
	errors        ErrorList

	fileBlock *block
	env       *block
	// Predeclared and universal names that have been resolved.
	toplevel map[string]*Binding
	// Number of loops enclosing the current statement within the
	// current function.
	loops int
}

func (r *resolver) errorf(pos syntax.Position, format string, args ...any) {
	r.errors = append(r.errors, &syntax.Error{
		Filename: r.filename,
		Pos:      pos,
		Message:  fmt.Sprintf(format, args...),
	})
}

func (r *resolver) push(b *block) {
	b.parent = r.env
	if b.bindings == nil {
		b.bindings = map[string]*Binding{}
	}
	r.env = b
}

func (r *resolver) pop() {
	r.env = r.env.parent
}

func (r *resolver) atToplevel() bool {
	return r.env == r.fileBlock
}

func (r *resolver) resolveFile(file *syntax.File) {
	r.fileBlock = &block{
		function: r.module.Toplevel,
		bindings: map[string]*Binding{},
	}
	r.env = r.fileBlock

	// First create bindings for all names that are bound at the
	// top level, as names may be used before they are bound.
	r.createToplevelBindings(file.Stmts)
	if r.dialect.RequireLoadStatementsFirst {
		r.checkLoadStatementsFirst(file.Stmts)
	}
	r.stmts(file.Stmts)
}

// createToplevelBindings creates bindings for all names that are bound
// by the provided top-level statements.
func (r *resolver) createToplevelBindings(stmts []syntax.Stmt) {
	for _, stmt := range stmts {
		switch s := stmt.(type) {
		case *syntax.AssignStmt:
			forEachBoundIdent(s.LHS, func(id *syntax.Ident) {
				r.bindToplevel(id, false)
			})
		case *syntax.DefStmt:
			r.bindToplevel(s.Name, false)
		case *syntax.ForStmt:
			forEachBoundIdent(s.Vars, func(id *syntax.Ident) {
				r.bindToplevel(id, false)
			})
			r.createToplevelBindings(s.Body)
		case *syntax.IfStmt:
			r.createToplevelBindings(s.True)
			r.createToplevelBindings(s.False)
		case *syntax.LoadStmt:
			seen := map[string]struct{}{}
			for _, binding := range s.Bindings {
				if _, ok := seen[binding.Local.Name]; ok {
					r.errorf(binding.Local.NamePos, "load statement defines '%s' more than once", binding.Local.Name)
					continue
				}
				seen[binding.Local.Name] = struct{}{}

				module, name := s.Module.Value.(string), binding.Name.Value.(string)
				if previous, ok := r.fileBlock.bindings[binding.Local.Name]; ok && previous.Scope == Import && r.dialect.AllowToplevelRebinding {
					// Repeatedly loading the same symbol
					// is permitted. As symbols are loaded
					// lazily, a name can only refer to a
					// single symbol.
					r.module.bindings[binding.Local] = previous
					if loaded := r.module.Imports[previous.Index]; loaded.Module != module || loaded.Name != name {
						r.errorf(binding.Local.NamePos, "'%s' is loaded as a different symbol than before", binding.Local.Name)
						r.errorf(previous.First.NamePos, "'%s' previously loaded here", binding.Local.Name)
					}
					continue
				}

				b := r.bindToplevel(binding.Local, true)
				r.module.Imports = append(r.module.Imports, &LoadedSymbol{
					Module:  module,
					Name:    name,
					Binding: b,
				})
			}
		case *syntax.TypeAliasStmt:
			if r.dialect.ResolveTypeSyntax {
				r.bindToplevel(s.Name, false)
			}
		case *syntax.VarStmt:
			r.bindToplevel(s.Name, false)
		}
	}
}

// bindToplevel creates a binding for a name at the top level of the
// file, or returns the existing binding if the name was bound before.
func (r *resolver) bindToplevel(id *syntax.Ident, isImport bool) *Binding {
	if previous, ok := r.fileBlock.bindings[id.Name]; ok {
		if previousIsImport := previous.Scope == Import; previousIsImport != isImport {
			r.errorf(id.NamePos, "conflicting %s declaration of '%s'", toplevelBindingKind(isImport), id.Name)
			r.errorf(previous.First.NamePos, "'%s' previously declared as %s here", id.Name, toplevelBindingKind(previousIsImport))
		} else if !r.dialect.AllowToplevelRebinding {
			r.errorf(id.NamePos, "'%s' redeclared at top level", id.Name)
			r.errorf(previous.First.NamePos, "'%s' previously declared here", id.Name)
		}
		r.module.bindings[id] = previous
		return previous
	}

	b := &Binding{First: id}
	if isImport {
		b.Scope = Import
		b.Index = len(r.module.Imports)
	} else {
		b.Scope = Global
		b.Index = len(r.module.Globals)
		r.module.Globals = append(r.module.Globals, b)
	}
	r.fileBlock.bindings[id.Name] = b
	r.module.bindings[id] = b
	return b
}

func toplevelBindingKind(isImport bool) string {
	if isImport {
		return "file-local"
	}
	return "global"
}

// forEachBoundIdent calls a function for every identifier that is
// bound by an assignment to an expression.
func forEachBoundIdent(lhs syntax.Expr, f func(*syntax.Ident)) {
	switch x := lhs.(type) {
	case *syntax.Ident:
		f(x)
	case *syntax.ListExpr:
		for _, element := range x.List {
			forEachBoundIdent(element, f)
		}
	case *syntax.ParenExpr:
		forEachBoundIdent(x.X, f)
	case *syntax.TupleExpr:
		for _, element := range x.List {
			forEachBoundIdent(element, f)
		}
	}
}

// createLocalBindings creates local variables for all names that are
// bound by the statements in the body of a function. As in Python, a
// name that is bound anywhere within a function is local to that
// function.
func (r *resolver) createLocalBindings(stmts []syntax.Stmt) {
	for _, stmt := range stmts {
		switch s := stmt.(type) {
		case *syntax.AssignStmt:
			forEachBoundIdent(s.LHS, r.bindLocal)
		case *syntax.DefStmt:
			r.bindLocal(s.Name)
		case *syntax.ForStmt:
			forEachBoundIdent(s.Vars, r.bindLocal)
			r.createLocalBindings(s.Body)
		case *syntax.IfStmt:
			r.createLocalBindings(s.True)
			r.createLocalBindings(s.False)
		case *syntax.VarStmt:
			r.bindLocal(s.Name)
		}
	}
}

// bindLocal creates a local variable in the current block, unless a
// variable with the same name already exists.
func (r *resolver) bindLocal(id *syntax.Ident) {
	if _, ok := r.env.bindings[id.Name]; !ok {
		r.env.bindings[id.Name] = r.newLocal(id)
	}
}

func (r *resolver) newLocal(id *syntax.Ident) *Binding {
	function := r.env.function
	b := &Binding{
		Scope: Local,
		Index: len(function.Locals),
		First: id,
	}
	function.Locals = append(function.Locals, b)
	return b
}

// checkLoadStatementsFirst checks that load statements are not preceded
// by any other statements, except for docstrings.
func (r *resolver) checkLoadStatementsFirst(stmts []syntax.Stmt) {
	var firstNonLoad syntax.Stmt
	for _, stmt := range stmts {
		switch s := stmt.(type) {
		case *syntax.LoadStmt:
			if firstNonLoad != nil {
				r.errorf(s.Load, "load statements must appear before any other statement (first non-load statement at line %d)", syntax.Start(firstNonLoad).Line)
				return
			}
		case *syntax.ExprStmt:
			if literal, ok := s.X.(*syntax.Literal); ok && literal.Token == syntax.STRING {
				continue
			}
			if firstNonLoad == nil {
				firstNonLoad = stmt
			}
		default:
			if firstNonLoad == nil {
				firstNonLoad = stmt
			}
		}
	}
}

func (r *resolver) stmts(stmts []syntax.Stmt) {
	for _, stmt := range stmts {
		r.stmt(stmt)
	}
}

func (r *resolver) stmt(stmt syntax.Stmt) {
	restricted := r.dialect.RestrictedSyntax
	switch s := stmt.(type) {
	case *syntax.AssignStmt:
		r.expr(s.RHS)
		r.typeExpr(s.Type)
		if s.Op != syntax.EQ {
			switch unparen(s.LHS).(type) {
			case *syntax.ListExpr, *syntax.TupleExpr:
				r.errorf(s.OpPos, "cannot perform augmented assignment on a list or tuple expression")
			}
		}
		r.assign(s.LHS)

	case *syntax.BranchStmt:
		if s.Token != syntax.PASS && r.loops == 0 {
			r.errorf(s.TokenPos, "%s statement must be inside a for loop", s.Token)
		}

	case *syntax.DefStmt:
		if restricted != nil {
			r.errorf(s.Def, "functions may not be defined in %s.%s", restricted.FileDescription, r.restrictedSuggestion(" You may move the function to a .bzl file and load it.", ""))
			return
		}
		r.bindingUse(s.Name)
		r.function(s, s.Name.Name, s.Def, s.TypeParams, s.Params, s.ReturnType, func() {
			r.createLocalBindings(s.Body)
			r.stmts(s.Body)
		})

	case *syntax.ExprStmt:
		r.expr(s.X)

	case *syntax.ForStmt:
		if r.atToplevel() {
			if restricted != nil {
				r.errorf(s.For, "`for` statements are not allowed in %s. You may inline the loop%s or as a last resort use a list comprehension.", restricted.FileDescription, r.restrictedSuggestion(", move it to a function definition (in a .bzl file),", ""))
				return
			}
			r.errorf(s.For, "for loops are not allowed at the top level. You may move it inside a function or use a comprehension, [f(x) for x in sequence]")
		}
		r.expr(s.X)
		r.assign(s.Vars)
		r.loops++
		r.stmts(s.Body)
		r.loops--

	case *syntax.IfStmt:
		if r.atToplevel() {
			if restricted != nil {
				r.errorf(s.If, "`if` statements are not allowed in %s. You may %suse an `if` expression for simple cases.", restricted.FileDescription, r.restrictedSuggestion("move conditional logic to a function definition (in a .bzl file), or ", ""))
				return
			}
			r.errorf(s.If, "if statements are not allowed at the top level. You may move it inside a function or use an if expression (x if condition else y).")
		}
		r.expr(s.Cond)
		r.stmts(s.True)
		r.stmts(s.False)

	case *syntax.LoadStmt:
		if restricted != nil && !restricted.AllowLoad {
			r.errorf(s.Load, "`load` statements may not be used in %s", restricted.FileDescription)
			return
		}
		if !r.atToplevel() {
			r.errorf(s.Load, "load statement not at top level")
			return
		}
		for _, binding := range s.Bindings {
			name := binding.Name.Value.(string)
			if !isIdentifier(name) {
				r.errorf(binding.Name.TokenPos, "load: %s is not a valid identifier", syntax.Quote(name))
			} else if name[0] == '_' {
				r.errorf(binding.Name.TokenPos, "symbol '%s' is private and cannot be imported", name)
			}
		}

	case *syntax.ReturnStmt:
		if r.atToplevel() {
			r.errorf(s.Return, "return statements must be inside a function")
		}
		if s.Result != nil {
			r.expr(s.Result)
		}

	case *syntax.TypeAliasStmt:
		if !r.atToplevel() {
			r.errorf(s.TypePos, "type alias statement not at top level")
			return
		}
		if r.dialect.ResolveTypeSyntax {
			r.bindingUse(s.Name)
			r.pushTypeParams(s.TypeParams)
			r.typeExpr(s.Value)
			if len(s.TypeParams) > 0 {
				r.pop()
			}
		}

	case *syntax.VarStmt:
		r.typeExpr(s.Type)
		r.bindingUse(s.Name)

	default:
		panic(fmt.Sprintf("unexpected statement type %T", stmt))
	}
}

// restrictedSuggestion returns one of two suggestions that are part of
// error messages for restricted syntax, depending on whether the file
// can load .bzl files.
func (r *resolver) restrictedSuggestion(canLoad, cannotLoad string) string {
	if r.dialect.RestrictedSyntax.AllowLoad {
		return canLoad
	}
	return cannotLoad
}

// assign resolves the target of an assignment.
func (r *resolver) assign(lhs syntax.Expr) {
	switch x := lhs.(type) {
	case *syntax.Ident:
		r.bindingUse(x)
	case *syntax.IndexExpr:
		r.expr(x.X)
		r.expr(x.Y)
	case *syntax.DotExpr:
		r.expr(x.X)
	case *syntax.ListExpr:
		for _, element := range x.List {
			r.assign(element)
		}
	case *syntax.ParenExpr:
		r.assign(x.X)
	case *syntax.TupleExpr:
		for _, element := range x.List {
			r.assign(element)
		}
	default:
		r.errorf(syntax.Start(lhs), "cannot assign to %s", describeExpr(lhs))
	}
}

// pushTypeParams pushes a block containing the type parameters of a
// function or type alias, if any.
func (r *resolver) pushTypeParams(typeParams []*syntax.Ident) {
	if len(typeParams) == 0 {
		return
	}
	r.push(&block{function: r.env.function})
	for _, id := range typeParams {
		if _, ok := r.env.bindings[id.Name]; ok {
			r.errorf(id.NamePos, "duplicate type parameter: %s", id.Name)
			continue
		}
		b := &Binding{Scope: TypeParameter, First: id}
		r.env.bindings[id.Name] = b
		r.module.bindings[id] = b
	}
}

// function resolves a function or lambda. The body function is called
// to resolve the body after the parameters have been bound.
func (r *resolver) function(node syntax.Node, name string, pos syntax.Position, typeParams []*syntax.Ident, params []*syntax.Param, returnType syntax.Expr, body func()) {
	// Default values of parameters are evaluated in the enclosing
	// environment.
	for _, param := range params {
		if param.Default != nil {
			r.expr(param.Default)
		}
	}

	// Type annotations may refer to type parameters.
	if r.dialect.ResolveTypeSyntax {
		r.pushTypeParams(typeParams)
		for _, param := range params {
			r.typeExpr(param.Type)
		}
		r.typeExpr(returnType)
	}

	function := &Function{
		Name:   name,
		Pos:    pos,
		Node:   node,
		Params: params,
	}
	r.module.functions[node] = function
	r.push(&block{function: function, isFunction: true})

	// Validate the order of the parameters.
	var star, starStar *syntax.Param
	seenOptional := false
	for _, param := range params {
		switch param.Kind {
		case syntax.ParamNormal:
			if param.Default == nil {
				if starStar != nil {
					r.errorf(param.Name.NamePos, "required parameter %s may not follow **%s", param.Name.Name, starStar.Name.Name)
				} else if star != nil {
					function.NumKeywordOnlyParams++
				} else if seenOptional {
					r.errorf(param.Name.NamePos, "required positional parameter %s may not follow an optional parameter", param.Name.Name)
				}
			} else {
				if starStar != nil {
					r.errorf(param.Name.NamePos, "optional parameter may not follow **%s", starStar.Name.Name)
				} else if star != nil {
					function.NumKeywordOnlyParams++
				}
				seenOptional = true
			}
		case syntax.ParamStar, syntax.ParamArgs:
			if starStar != nil {
				r.errorf(param.StarPos, "* parameter may not follow **%s", starStar.Name.Name)
			} else if star != nil {
				r.errorf(param.StarPos, "multiple * parameters not allowed")
			} else {
				star = param
			}
		case syntax.ParamKwargs:
			if starStar != nil {
				r.errorf(param.StarPos, "multiple ** parameters not allowed")
			} else {
				starStar = param
			}
		}
	}
	if star != nil && star.Kind == syntax.ParamStar && function.NumKeywordOnlyParams == 0 {
		r.errorf(star.StarPos, "bare * must be followed by keyword-only parameters")
	}

	// Bind the parameters in the order in which they are passed at
	// runtime: positional and keyword-only parameters, followed by
	// *args and **kwargs.
	for _, param := range params {
		if param.Kind == syntax.ParamNormal {
			r.bindParam(param.Name)
		}
	}
	if star != nil && star.Kind == syntax.ParamArgs {
		r.bindParam(star.Name)
		function.HasArgs = true
	}
	if starStar != nil {
		r.bindParam(starStar.Name)
		function.HasKwargs = true
	}

	// Loops are not inherited by nested functions.
	loops := r.loops
	r.loops = 0
	body()
	r.loops = loops

	r.pop()
	if r.dialect.ResolveTypeSyntax && len(typeParams) > 0 {
		r.pop()
	}
}

func (r *resolver) bindParam(id *syntax.Ident) {
	if _, ok := r.env.bindings[id.Name]; ok {
		r.errorf(id.NamePos, "duplicate parameter: %s", id.Name)
		return
	}
	b := r.newLocal(id)
	r.env.bindings[id.Name] = b
	r.module.bindings[id] = b
}

func (r *resolver) exprs(exprs []syntax.Expr) {
	for _, x := range exprs {
		r.expr(x)
	}
}

func (r *resolver) expr(x syntax.Expr) {
	switch e := x.(type) {
	case *syntax.BinaryExpr:
		r.expr(e.X)
		r.expr(e.Y)

	case *syntax.CallExpr:
		r.expr(e.Fn)
		r.callArgs(e.Args)
		r.checkCallArgumentCounts(e)

	case *syntax.CastExpr:
		r.typeExpr(e.Type)
		r.expr(e.Value)

	case *syntax.Comprehension:
		r.comprehension(e)

	case *syntax.CondExpr:
		r.expr(e.Cond)
		r.expr(e.True)
		r.expr(e.False)

	case *syntax.DictExpr:
		for _, entry := range e.Entries {
			r.expr(entry.Key)
			r.expr(entry.Value)
		}

	case *syntax.DotExpr:
		r.expr(e.X)

	case *syntax.Ident:
		r.use(e)

	case *syntax.IndexExpr:
		r.expr(e.X)
		r.expr(e.Y)

	case *syntax.IsInstanceExpr:
		r.errorf(e.IsInstance, "isinstance() is not yet supported")
		r.expr(e.Value)
		r.typeExpr(e.Type)

	case *syntax.LambdaExpr:
		if restricted := r.dialect.RestrictedSyntax; restricted != nil {
			r.errorf(e.Lambda, "functions may not be defined in %s.%s", restricted.FileDescription, r.restrictedSuggestion(" You may move the function to a .bzl file and load it.", ""))
			return
		}
		r.function(e, "lambda", e.Lambda, nil, e.Params, nil, func() {
			r.expr(e.Body)
		})

	case *syntax.ListExpr:
		r.exprs(e.List)

	case *syntax.Literal:

	case *syntax.ParenExpr:
		r.expr(e.X)

	case *syntax.SliceExpr:
		r.expr(e.X)
		for _, y := range []syntax.Expr{e.Lo, e.Hi, e.Step} {
			if y != nil {
				r.expr(y)
			}
		}

	case *syntax.TupleExpr:
		r.exprs(e.List)

	case *syntax.UnaryExpr:
		r.expr(e.X)

	default:
		panic(fmt.Sprintf("unexpected expression type %T", x))
	}
}

// callArgs resolves the arguments of a function call, and checks that
// they are provided in the right order: positional arguments, keyword
// arguments, *args and **kwargs.
// checkCallArgumentCounts reports an error if a function call has more
// arguments than can be represented in the bytecode.
func (r *resolver) checkCallArgumentCounts(call *syntax.CallExpr) {
	positional, named := 0, 0
	for _, arg := range call.Args {
		switch arg.Kind {
		case syntax.ArgPositional:
			positional++
		case syntax.ArgNamed:
			named++
		}
	}
	if positional > MaximumCallArguments {
		r.errorf(call.Lparen, "call has %d positional arguments, while at most %d are permitted", positional, MaximumCallArguments)
	}
	if named > MaximumCallArguments {
		r.errorf(call.Lparen, "call has %d named arguments, while at most %d are permitted", named, MaximumCallArguments)
	}
}

func (r *resolver) callArgs(args []*syntax.Arg) {
	restricted := r.dialect.RestrictedSyntax
	var seenNamed, seenArgs, seenKwargs bool
	names := map[string]struct{}{}
	for _, arg := range args {
		switch arg.Kind {
		case syntax.ArgPositional:
			if seenArgs {
				r.errorf(syntax.Start(arg), "positional argument may not follow *args")
			} else if seenKwargs {
				r.errorf(syntax.Start(arg), "positional argument may not follow **kwargs")
			} else if seenNamed {
				r.errorf(syntax.Start(arg), "positional argument may not follow keyword argument")
			}
		case syntax.ArgNamed:
			name := arg.Name.Name
			if seenArgs {
				r.errorf(arg.Name.NamePos, "keyword argument %s may not follow *args", name)
			} else if seenKwargs {
				r.errorf(arg.Name.NamePos, "keyword argument %s may not follow **kwargs", name)
			}
			if _, ok := names[name]; ok {
				r.errorf(arg.Name.NamePos, "duplicate keyword argument: %s", name)
			}
			names[name] = struct{}{}
			seenNamed = true
		case syntax.ArgArgs:
			if restricted != nil {
				r.errorf(arg.StarPos, "*args arguments are not allowed in %s. Pass the arguments in explicitly.", restricted.FileDescription)
			}
			if seenKwargs {
				r.errorf(arg.StarPos, "*args may not follow **kwargs")
			} else if seenArgs {
				r.errorf(arg.StarPos, "multiple *args not allowed")
			}
			seenArgs = true
		case syntax.ArgKwargs:
			if restricted != nil {
				if !restricted.AllowLiteralKwargs {
					r.errorf(arg.StarPos, "**kwargs arguments are not allowed in %s. Pass the arguments in explicitly.", restricted.FileDescription)
				}
				if _, ok := arg.Value.(*syntax.DictExpr); !ok {
					r.errorf(arg.StarPos, "**kwargs arguments must be a literal dict in %s.", restricted.FileDescription)
				}
			}
			if seenKwargs {
				r.errorf(arg.StarPos, "multiple **kwargs not allowed")
			}
			seenKwargs = true
		}
		r.expr(arg.Value)
	}
}

// comprehension resolves a list or dict comprehension. The iterable of
// the first for clause is resolved in the enclosing block. All
// variables bound by the comprehension are created up front, and are
// local to the comprehension.
func (r *resolver) comprehension(c *syntax.Comprehension) {
	r.expr(c.Clauses[0].(*syntax.ForClause).X)

	r.push(&block{function: r.env.function})
	for _, clause := range c.Clauses {
		if forClause, ok := clause.(*syntax.ForClause); ok {
			forEachBoundIdent(forClause.Vars, func(id *syntax.Ident) {
				if _, ok := r.env.bindings[id.Name]; !ok {
					r.env.bindings[id.Name] = r.newLocal(id)
				}
			})
		}
	}
	for i, clause := range c.Clauses {
		switch cl := clause.(type) {
		case *syntax.ForClause:
			if i > 0 {
				r.expr(cl.X)
			}
			r.assign(cl.Vars)
		case *syntax.IfClause:
			r.expr(cl.Cond)
		}
	}
	switch body := c.Body.(type) {
	case *syntax.DictEntry:
		r.expr(body.Key)
		r.expr(body.Value)
	case syntax.Expr:
		r.expr(body)
	}
	r.pop()
}

// typeExpr resolves the names used in a type expression. Nothing is
// done if type syntax is not resolved.
func (r *resolver) typeExpr(x syntax.Expr) {
	if x == nil || !r.dialect.ResolveTypeSyntax {
		return
	}
	switch e := x.(type) {
	case *syntax.BinaryExpr:
		r.typeExpr(e.X)
		r.typeExpr(e.Y)
	case *syntax.DictExpr:
		for _, entry := range e.Entries {
			r.typeExpr(entry.Value)
		}
	case *syntax.DotExpr:
		r.typeExpr(e.X)
	case *syntax.Ident:
		r.use(e)
	case *syntax.ListExpr:
		for _, element := range e.List {
			r.typeExpr(element)
		}
	case *syntax.ParenExpr:
		r.typeExpr(e.X)
	case *syntax.TupleExpr:
		for _, element := range e.List {
			r.typeExpr(element)
		}
	case *syntax.TypeApplication:
		r.typeExpr(e.Type)
		for _, arg := range e.Args {
			r.typeExpr(arg)
		}
	case *syntax.EllipsisExpr, *syntax.Literal:
	default:
		panic(fmt.Sprintf("unexpected type expression type %T", x))
	}
}

// bindingUse resolves an identifier that is the target of a binding,
// such as the left hand side of an assignment. The binding has already
// been created.
func (r *resolver) bindingUse(id *syntax.Ident) {
	r.module.bindings[id] = r.lookupLexical(id, r.env)
}

// use resolves an identifier that refers to a variable.
func (r *resolver) use(id *syntax.Ident) {
	b := r.lookupLexical(id, r.env)
	r.module.bindings[id] = b

	// Record references to globals and imports in all enclosing
	// functions, so that the compiler knows which globals and
	// imports need to be retained.
	switch b.Scope {
	case Global:
		r.forEachEnclosingFunction(func(f *Function) {
			if f.referencedGlobals == nil {
				f.referencedGlobals = map[int]struct{}{}
			}
			f.referencedGlobals[b.Index] = struct{}{}
		})
	case Import:
		r.forEachEnclosingFunction(func(f *Function) {
			if f.referencedImports == nil {
				f.referencedImports = map[int]struct{}{}
			}
			f.referencedImports[b.Index] = struct{}{}
		})
	}
}

func (r *resolver) forEachEnclosingFunction(f func(*Function)) {
	for b := r.env; b != nil; b = b.parent {
		if b.isFunction || b == r.fileBlock {
			f(b.function)
		}
	}
}

// lookupLexical looks up a name in the provided block and its
// enclosing blocks. If the name refers to a local variable of an
// enclosing function, it is captured as a free variable by all
// functions in between.
func (r *resolver) lookupLexical(id *syntax.Ident, env *block) *Binding {
	if env == r.fileBlock {
		return r.lookupToplevel(id)
	}
	if b, ok := env.bindings[id.Name]; ok {
		return b
	}
	b := r.lookupLexical(id, env.parent)
	if env.isFunction && (b.Scope == Local || b.Scope == Cell || b.Scope == Free) {
		if b.Scope == Local {
			b.Scope = Cell
		}
		function := env.function
		function.FreeVars = append(function.FreeVars, b)
		b = &Binding{
			Scope: Free,
			Index: len(function.FreeVars) - 1,
			First: b.First,
		}
		env.bindings[id.Name] = b
	}
	return b
}

// lookupToplevel looks up a name that is not bound by any function or
// comprehension.
func (r *resolver) lookupToplevel(id *syntax.Ident) *Binding {
	if b, ok := r.fileBlock.bindings[id.Name]; ok {
		return b
	}
	if b, ok := r.toplevel[id.Name]; ok {
		return b
	}
	var b *Binding
	switch {
	case r.isPredeclared(id.Name):
		b = &Binding{Scope: Predeclared}
	case r.isUniversal(id.Name):
		b = &Binding{Scope: Universal}
	default:
		var suggestion string
		if nearest := r.nearestName(id.Name); nearest != "" {
			suggestion = fmt.Sprintf(" (did you mean '%s'?)", nearest)
		}
		r.errorf(id.NamePos, "name '%s' is not defined%s", id.Name, suggestion)
		return &Binding{Scope: Undefined}
	}
	r.toplevel[id.Name] = b
	return b
}

// nearestName returns the name of the variable in scope that is most
// similar to the provided name, for use in error messages.
func (r *resolver) nearestName(name string) string {
	var candidates []string
	for b := r.env; b != nil; b = b.parent {
		for candidate := range b.bindings {
			candidates = append(candidates, candidate)
		}
	}
	for candidate := range r.toplevel {
		candidates = append(candidates, candidate)
	}
	return nearest(name, candidates)
}

func isIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c == '_' || i > 0 && '0' <= c && c <= '9') {
			return false
		}
	}
	return true
}

// unparen removes any parentheses surrounding an expression.
func unparen(x syntax.Expr) syntax.Expr {
	for {
		paren, ok := x.(*syntax.ParenExpr)
		if !ok {
			return x
		}
		x = paren.X
	}
}
