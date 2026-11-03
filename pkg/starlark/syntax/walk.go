package syntax

import (
	"fmt"
)

// Walk traverses a syntax tree in depth-first order. It starts by
// calling f(n). If f returns true, Walk calls itself recursively for
// each non-nil child of n.
func Walk(n Node, f func(Node) bool) {
	if !f(n) {
		return
	}
	walkChildren(n, f)
}

func walkStmts(stmts []Stmt, f func(Node) bool) {
	for _, stmt := range stmts {
		Walk(stmt, f)
	}
}

func walkExprs(exprs []Expr, f func(Node) bool) {
	for _, x := range exprs {
		Walk(x, f)
	}
}

func walkIdents(idents []*Ident, f func(Node) bool) {
	for _, id := range idents {
		Walk(id, f)
	}
}

func walkIfNotNil(x Expr, f func(Node) bool) {
	if x != nil {
		Walk(x, f)
	}
}

func walkChildren(n Node, f func(Node) bool) {
	switch n := n.(type) {
	case *File:
		walkStmts(n.Stmts, f)

	case *AssignStmt:
		Walk(n.LHS, f)
		walkIfNotNil(n.Type, f)
		Walk(n.RHS, f)
	case *BranchStmt:
	case *DefStmt:
		Walk(n.Name, f)
		walkIdents(n.TypeParams, f)
		for _, param := range n.Params {
			Walk(param, f)
		}
		walkIfNotNil(n.ReturnType, f)
		walkStmts(n.Body, f)
	case *ExprStmt:
		Walk(n.X, f)
	case *ForStmt:
		Walk(n.Vars, f)
		Walk(n.X, f)
		walkStmts(n.Body, f)
	case *IfStmt:
		Walk(n.Cond, f)
		walkStmts(n.True, f)
		walkStmts(n.False, f)
	case *LoadStmt:
		Walk(n.Module, f)
		for _, binding := range n.Bindings {
			Walk(binding, f)
		}
	case *LoadBinding:
		Walk(n.Local, f)
		Walk(n.Name, f)
	case *ReturnStmt:
		walkIfNotNil(n.Result, f)
	case *TypeAliasStmt:
		Walk(n.Name, f)
		walkIdents(n.TypeParams, f)
		Walk(n.Value, f)
	case *VarStmt:
		Walk(n.Name, f)
		Walk(n.Type, f)

	case *Param:
		if n.Name != nil {
			Walk(n.Name, f)
		}
		walkIfNotNil(n.Type, f)
		walkIfNotNil(n.Default, f)
	case *Arg:
		if n.Name != nil {
			Walk(n.Name, f)
		}
		Walk(n.Value, f)

	case *BinaryExpr:
		Walk(n.X, f)
		Walk(n.Y, f)
	case *CallExpr:
		Walk(n.Fn, f)
		for _, arg := range n.Args {
			Walk(arg, f)
		}
	case *CastExpr:
		Walk(n.Type, f)
		Walk(n.Value, f)
	case *Comprehension:
		Walk(n.Body, f)
		for _, clause := range n.Clauses {
			Walk(clause, f)
		}
	case *ForClause:
		Walk(n.Vars, f)
		Walk(n.X, f)
	case *IfClause:
		Walk(n.Cond, f)
	case *CondExpr:
		Walk(n.True, f)
		Walk(n.Cond, f)
		Walk(n.False, f)
	case *DictExpr:
		for _, entry := range n.Entries {
			Walk(entry, f)
		}
	case *DictEntry:
		Walk(n.Key, f)
		Walk(n.Value, f)
	case *DotExpr:
		Walk(n.X, f)
		Walk(n.Name, f)
	case *EllipsisExpr:
	case *Ident:
	case *IndexExpr:
		Walk(n.X, f)
		Walk(n.Y, f)
	case *IsInstanceExpr:
		Walk(n.Value, f)
		Walk(n.Type, f)
	case *LambdaExpr:
		for _, param := range n.Params {
			Walk(param, f)
		}
		Walk(n.Body, f)
	case *ListExpr:
		walkExprs(n.List, f)
	case *Literal:
	case *ParenExpr:
		Walk(n.X, f)
	case *SliceExpr:
		Walk(n.X, f)
		walkIfNotNil(n.Lo, f)
		walkIfNotNil(n.Hi, f)
		walkIfNotNil(n.Step, f)
	case *TupleExpr:
		walkExprs(n.List, f)
	case *TypeApplication:
		Walk(n.Type, f)
		walkExprs(n.Args, f)
	case *UnaryExpr:
		Walk(n.X, f)

	default:
		panic(fmt.Sprintf("unexpected node type %T", n))
	}
}
