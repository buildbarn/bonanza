package syntax

import (
	"fmt"
)

// Node of a Starlark syntax tree.
//
// The syntax tree is not modified after parsing. In particular, the
// resolver stores its results separately, so that syntax trees can be
// shared.
type Node interface {
	// Span returns the start and end positions of the node.
	Span() (start, end Position)
}

// Start returns the start position of a node.
func Start(n Node) Position {
	start, _ := n.Span()
	return start
}

// End returns the end position of a node.
func End(n Node) Position {
	_, end := n.Span()
	return end
}

// File containing a sequence of Starlark statements.
type File struct {
	Path  string
	Stmts []Stmt
}

// Span returns the start and end positions of the file's statements.
func (x *File) Span() (start, end Position) {
	if len(x.Stmts) == 0 {
		return start, end
	}
	return Start(x.Stmts[0]), End(x.Stmts[len(x.Stmts)-1])
}

// Stmt is a Starlark statement.
type Stmt interface {
	Node
	stmt()
}

func (*AssignStmt) stmt()    {}
func (*BranchStmt) stmt()    {}
func (*DefStmt) stmt()       {}
func (*ExprStmt) stmt()      {}
func (*ForStmt) stmt()       {}
func (*IfStmt) stmt()        {}
func (*LoadStmt) stmt()      {}
func (*ReturnStmt) stmt()    {}
func (*TypeAliasStmt) stmt() {}
func (*VarStmt) stmt()       {}

// AssignStmt represents an assignment:
//
//	x = 0
//	x, y = y, x
//	x += 1
//	x: int = 0
type AssignStmt struct {
	LHS Expr
	// Type annotation of the variable. Only set if LHS is an *Ident
	// and Op is EQ.
	Type  Expr
	OpPos Position
	// EQ, or one of the augmented assignment operators (PLUSEQ,
	// MINUSEQ, etc.).
	Op  Token
	RHS Expr
}

// Span returns the start and end positions of the assignment.
func (x *AssignStmt) Span() (start, end Position) {
	return Start(x.LHS), End(x.RHS)
}

// VarStmt declares the type of a variable without assigning it:
//
//	x: int
type VarStmt struct {
	Name *Ident
	Type Expr
}

// Span returns the start and end positions of the declaration.
func (x *VarStmt) Span() (start, end Position) {
	return x.Name.NamePos, End(x.Type)
}

// BranchStmt changes the flow of control: break, continue or pass.
type BranchStmt struct {
	// BREAK, CONTINUE or PASS.
	Token    Token
	TokenPos Position
}

// Span returns the start and end positions of the statement.
func (x *BranchStmt) Span() (start, end Position) {
	return x.TokenPos, x.TokenPos.add(x.Token.String())
}

// DefStmt represents a function definition.
type DefStmt struct {
	Def  Position
	Name *Ident
	// Type parameters, e.g. T in "def f[T](x: T) -> T".
	TypeParams []*Ident
	Lparen     Position
	Params     []*Param
	Rparen     Position
	// Type annotation of the return value. May be nil.
	ReturnType Expr
	Body       []Stmt
}

// Span returns the start and end positions of the function definition.
func (x *DefStmt) Span() (start, end Position) {
	return x.Def, End(x.Body[len(x.Body)-1])
}

// ExprStmt is an expression that is evaluated for its side effects.
type ExprStmt struct {
	X Expr
}

// Span returns the start and end positions of the expression.
func (x *ExprStmt) Span() (start, end Position) {
	return x.X.Span()
}

// ForStmt represents a loop: for Vars in X: Body.
type ForStmt struct {
	For Position
	// Loop variable, or a tuple of loop variables.
	Vars Expr
	X    Expr
	Body []Stmt
}

// Span returns the start and end positions of the loop.
func (x *ForStmt) Span() (start, end Position) {
	return x.For, End(x.Body[len(x.Body)-1])
}

// IfStmt is a conditional: if Cond: True else: False. "elif" is
// represented as a chain of IfStmts.
type IfStmt struct {
	// Position of IF or ELIF.
	If   Position
	Cond Expr
	True []Stmt
	// Position of ELSE or ELIF.
	ElsePos Position
	// May be empty.
	False []Stmt
}

// Span returns the start and end positions of the conditional.
func (x *IfStmt) Span() (start, end Position) {
	body := x.False
	if len(body) == 0 {
		body = x.True
	}
	return x.If, End(body[len(body)-1])
}

// LoadStmt loads another module and binds names from it:
//
//	load(Module, "x", y = "z")
type LoadStmt struct {
	Load     Position
	Module   *Literal
	Bindings []*LoadBinding
	Rparen   Position
}

// Span returns the start and end positions of the statement.
func (x *LoadStmt) Span() (start, end Position) {
	return x.Load, x.Rparen.add(")")
}

// LoadBinding is a single symbol that is bound by a load statement.
type LoadBinding struct {
	// Name under which the symbol is bound in the loading module.
	// For load(Module, "x"), this is an identifier synthesized from
	// the string literal.
	Local *Ident
	// Name of the symbol in the loaded module.
	Name *Literal
}

// Span returns the start and end positions of the binding.
func (x *LoadBinding) Span() (start, end Position) {
	start, end = x.Name.Span()
	if x.Local.NamePos.Compare(start) < 0 {
		start = x.Local.NamePos
	}
	return start, end
}

// ReturnStmt returns from a function.
type ReturnStmt struct {
	Return Position
	// May be nil.
	Result Expr
}

// Span returns the start and end positions of the statement.
func (x *ReturnStmt) Span() (start, end Position) {
	if x.Result == nil {
		return x.Return, x.Return.add("return")
	}
	return x.Return, End(x.Result)
}

// TypeAliasStmt declares an alias for a type:
//
//	type Name[TypeParams] = Value
type TypeAliasStmt struct {
	TypePos    Position
	Name       *Ident
	TypeParams []*Ident
	Eq         Position
	Value      Expr
}

// Span returns the start and end positions of the statement.
func (x *TypeAliasStmt) Span() (start, end Position) {
	return x.TypePos, End(x.Value)
}

// ParamKind indicates the kind of a function parameter.
type ParamKind uint8

const (
	// ParamNormal is a parameter that can be provided either
	// positionally or by name (or by name only, if it is preceded
	// by ParamStar or ParamArgs). It is optional if a default value
	// is provided.
	ParamNormal ParamKind = iota
	// ParamStar is the "*" separator that is placed before
	// keyword-only parameters.
	ParamStar
	// ParamArgs is a parameter that collects additional positional
	// arguments: *args.
	ParamArgs
	// ParamKwargs is a parameter that collects additional keyword
	// arguments: **kwargs.
	ParamKwargs
)

// String returns the name of a parameter kind.
func (k ParamKind) String() string {
	switch k {
	case ParamNormal:
		return "normal"
	case ParamStar:
		return "*"
	case ParamArgs:
		return "*args"
	case ParamKwargs:
		return "**kwargs"
	default:
		return fmt.Sprintf("ParamKind(%d)", k)
	}
}

// Param is a parameter of a function or lambda.
type Param struct {
	Kind ParamKind
	// Position of "*" or "**" for ParamStar, ParamArgs and
	// ParamKwargs.
	StarPos Position
	// Name of the parameter. Nil for ParamStar.
	Name *Ident
	// Type annotation of the parameter. May be nil.
	Type Expr
	// Default value of the parameter. Only set for ParamNormal.
	Eq      Position
	Default Expr
}

// Span returns the start and end positions of the parameter.
func (x *Param) Span() (start, end Position) {
	if x.Kind == ParamNormal {
		start = x.Name.NamePos
	} else {
		start = x.StarPos
	}
	switch {
	case x.Default != nil:
		end = End(x.Default)
	case x.Type != nil:
		end = End(x.Type)
	case x.Name != nil:
		end = End(x.Name)
	default:
		end = x.StarPos.add("*")
	}
	return start, end
}

// Expr is a Starlark expression.
type Expr interface {
	Node
	expr()
}

func (*BinaryExpr) expr()      {}
func (*CallExpr) expr()        {}
func (*CastExpr) expr()        {}
func (*Comprehension) expr()   {}
func (*CondExpr) expr()        {}
func (*DictExpr) expr()        {}
func (*DotExpr) expr()         {}
func (*EllipsisExpr) expr()    {}
func (*Ident) expr()           {}
func (*IndexExpr) expr()       {}
func (*IsInstanceExpr) expr()  {}
func (*LambdaExpr) expr()      {}
func (*ListExpr) expr()        {}
func (*Literal) expr()         {}
func (*ParenExpr) expr()       {}
func (*SliceExpr) expr()       {}
func (*TupleExpr) expr()       {}
func (*TypeApplication) expr() {}
func (*UnaryExpr) expr()       {}

// BinaryExpr represents a binary expression: X Op Y.
type BinaryExpr struct {
	X     Expr
	OpPos Position
	Op    Token
	Y     Expr
}

// Span returns the start and end positions of the expression.
func (x *BinaryExpr) Span() (start, end Position) {
	return Start(x.X), End(x.Y)
}

// ArgKind indicates the kind of an argument of a function call.
type ArgKind uint8

const (
	// ArgPositional is a positional argument: f(x).
	ArgPositional ArgKind = iota
	// ArgNamed is a keyword argument: f(name = x).
	ArgNamed
	// ArgArgs provides additional positional arguments: f(*x).
	ArgArgs
	// ArgKwargs provides additional keyword arguments: f(**x).
	ArgKwargs
)

// String returns the name of an argument kind.
func (k ArgKind) String() string {
	switch k {
	case ArgPositional:
		return "positional"
	case ArgNamed:
		return "named"
	case ArgArgs:
		return "*args"
	case ArgKwargs:
		return "**kwargs"
	default:
		return fmt.Sprintf("ArgKind(%d)", k)
	}
}

// Arg is an argument of a function call.
type Arg struct {
	Kind ArgKind
	// Position of "*" or "**" for ArgArgs and ArgKwargs.
	StarPos Position
	// Name of the argument. Only set for ArgNamed.
	Name  *Ident
	Eq    Position
	Value Expr
}

// Span returns the start and end positions of the argument.
func (x *Arg) Span() (start, end Position) {
	switch x.Kind {
	case ArgNamed:
		start = x.Name.NamePos
	case ArgArgs, ArgKwargs:
		start = x.StarPos
	default:
		start = Start(x.Value)
	}
	return start, End(x.Value)
}

// CallExpr represents a function call: Fn(Args).
type CallExpr struct {
	Fn     Expr
	Lparen Position
	Args   []*Arg
	Rparen Position
}

// Span returns the start and end positions of the expression.
func (x *CallExpr) Span() (start, end Position) {
	return Start(x.Fn), x.Rparen.add(")")
}

// CastExpr represents a cast of a value to a type: cast(Type, Value).
// At runtime, it evaluates to Value.
type CastExpr struct {
	Cast   Position
	Lparen Position
	Type   Expr
	Value  Expr
	Rparen Position
}

// Span returns the start and end positions of the expression.
func (x *CastExpr) Span() (start, end Position) {
	return x.Cast, x.Rparen.add(")")
}

// Comprehension represents a list or dict comprehension:
//
//	[Body for ... if ...]
//	{Body for ... if ...}
type Comprehension struct {
	// Whether the comprehension yields a dict. If set, Body is a
	// *DictEntry.
	Curly   bool
	Lbrack  Position
	Body    Node
	Clauses []Node // *ForClause or *IfClause.
	Rbrack  Position
}

// Span returns the start and end positions of the expression.
func (x *Comprehension) Span() (start, end Position) {
	return x.Lbrack, x.Rbrack.add("]")
}

// ForClause is a "for" clause of a comprehension: for Vars in X.
type ForClause struct {
	For  Position
	Vars Expr
	In   Position
	X    Expr
}

// Span returns the start and end positions of the clause.
func (x *ForClause) Span() (start, end Position) {
	return x.For, End(x.X)
}

// IfClause is an "if" clause of a comprehension: if Cond.
type IfClause struct {
	If   Position
	Cond Expr
}

// Span returns the start and end positions of the clause.
func (x *IfClause) Span() (start, end Position) {
	return x.If, End(x.Cond)
}

// CondExpr represents a conditional expression: True if Cond else
// False.
type CondExpr struct {
	True    Expr
	If      Position
	Cond    Expr
	ElsePos Position
	False   Expr
}

// Span returns the start and end positions of the expression.
func (x *CondExpr) Span() (start, end Position) {
	return Start(x.True), End(x.False)
}

// DictExpr represents a dict literal: {Entries}.
type DictExpr struct {
	Lbrace  Position
	Entries []*DictEntry
	Rbrace  Position
}

// Span returns the start and end positions of the expression.
func (x *DictExpr) Span() (start, end Position) {
	return x.Lbrace, x.Rbrace.add("}")
}

// DictEntry is an entry of a dict literal or dict comprehension:
// Key: Value.
type DictEntry struct {
	Key   Expr
	Colon Position
	Value Expr
}

// Span returns the start and end positions of the entry.
func (x *DictEntry) Span() (start, end Position) {
	return Start(x.Key), End(x.Value)
}

// DotExpr represents a field or method selector: X.Name.
type DotExpr struct {
	X    Expr
	Dot  Position
	Name *Ident
}

// Span returns the start and end positions of the expression.
func (x *DotExpr) Span() (start, end Position) {
	return Start(x.X), End(x.Name)
}

// EllipsisExpr represents "..." in a type expression.
type EllipsisExpr struct {
	Pos Position
}

// Span returns the start and end positions of the expression.
func (x *EllipsisExpr) Span() (start, end Position) {
	return x.Pos, x.Pos.add("...")
}

// Ident is an identifier.
type Ident struct {
	NamePos Position
	Name    string
}

// Span returns the start and end positions of the identifier.
func (x *Ident) Span() (start, end Position) {
	return x.NamePos, x.NamePos.add(x.Name)
}

// IndexExpr represents an index expression: X[Y].
type IndexExpr struct {
	X      Expr
	Lbrack Position
	Y      Expr
	Rbrack Position
}

// Span returns the start and end positions of the expression.
func (x *IndexExpr) Span() (start, end Position) {
	return Start(x.X), x.Rbrack.add("]")
}

// IsInstanceExpr represents a type check: isinstance(Value, Type).
type IsInstanceExpr struct {
	IsInstance Position
	Lparen     Position
	Value      Expr
	Type       Expr
	Rparen     Position
}

// Span returns the start and end positions of the expression.
func (x *IsInstanceExpr) Span() (start, end Position) {
	return x.IsInstance, x.Rparen.add(")")
}

// LambdaExpr represents an anonymous function: lambda Params: Body.
type LambdaExpr struct {
	Lambda Position
	Params []*Param
	Body   Expr
}

// Span returns the start and end positions of the expression.
func (x *LambdaExpr) Span() (start, end Position) {
	return x.Lambda, End(x.Body)
}

// ListExpr represents a list literal: [List].
type ListExpr struct {
	Lbrack Position
	List   []Expr
	Rbrack Position
}

// Span returns the start and end positions of the expression.
func (x *ListExpr) Span() (start, end Position) {
	return x.Lbrack, x.Rbrack.add("]")
}

// Literal represents a string or number literal.
type Literal struct {
	// STRING, INT or FLOAT.
	Token    Token
	TokenPos Position
	// Text of the literal, as it appears in the source file.
	Raw string
	// Value of the literal: string for STRING, int64 or *big.Int
	// for INT, float64 for FLOAT.
	Value any
}

// Span returns the start and end positions of the literal.
func (x *Literal) Span() (start, end Position) {
	return x.TokenPos, x.TokenPos.add(x.Raw)
}

// ParenExpr represents a parenthesized expression: (X).
type ParenExpr struct {
	Lparen Position
	X      Expr
	Rparen Position
}

// Span returns the start and end positions of the expression.
func (x *ParenExpr) Span() (start, end Position) {
	return x.Lparen, x.Rparen.add(")")
}

// SliceExpr represents a slice expression: X[Lo:Hi:Step].
type SliceExpr struct {
	X      Expr
	Lbrack Position
	// All of these are optional.
	Lo, Hi, Step Expr
	Rbrack       Position
}

// Span returns the start and end positions of the expression.
func (x *SliceExpr) Span() (start, end Position) {
	return Start(x.X), x.Rbrack.add("]")
}

// TupleExpr represents a tuple literal: (List) or List.
type TupleExpr struct {
	// Only set if the tuple is empty, as the parentheses of
	// non-empty tuples are represented by ParenExpr.
	Lparen Position
	List   []Expr
	Rparen Position
}

// Span returns the start and end positions of the expression.
func (x *TupleExpr) Span() (start, end Position) {
	if len(x.List) == 0 {
		return x.Lparen, x.Rparen.add(")")
	}
	return Start(x.List[0]), End(x.List[len(x.List)-1])
}

// TypeApplication represents a generic type that is applied to
// arguments: Type[Args], e.g. list[str] or dict[str, int].
type TypeApplication struct {
	// *Ident or *DotExpr.
	Type   Expr
	Lbrack Position
	Args   []Expr
	Rbrack Position
}

// Span returns the start and end positions of the expression.
func (x *TypeApplication) Span() (start, end Position) {
	return Start(x.Type), x.Rbrack.add("]")
}

// UnaryExpr represents a unary expression: Op X.
type UnaryExpr struct {
	OpPos Position
	// MINUS, PLUS, TILDE or NOT.
	Op Token
	X  Expr
}

// Span returns the start and end positions of the expression.
func (x *UnaryExpr) Span() (start, end Position) {
	return x.OpPos, End(x.X)
}
