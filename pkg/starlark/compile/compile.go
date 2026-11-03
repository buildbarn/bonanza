// Package compile converts resolved Starlark syntax trees to bytecode.
//
// The bytecode targets a stack based virtual machine, similar to the
// one of go.starlark.net. Each function has:
//
//   - a program counter, which is an index into the bytecode,
//   - an operand stack, whose maximum size is computed by the compiler,
//   - a stack of active iterators,
//   - an array of local variables, computed by the resolver, where
//     locals that are captured by nested functions are stored in
//     cells,
//   - an array of free variables, containing cells of enclosing
//     functions.
//
// Unlike go.starlark.net, globals are referred to by name, and symbols
// bound by load() are referred to through IMPORT instructions, so that
// they can be resolved lazily.
//
// Operands are encoded using varint.AppendForward(), except for the
// addresses of jumps, which are little endian integers whose size
// depends on the size of the function. See JumpOperandSizeBytes().
package compile

import (
	"fmt"
	"math/big"

	"bonanza.build/pkg/encoding/varint"
	"bonanza.build/pkg/starlark/resolve"
	"bonanza.build/pkg/starlark/syntax"
)

// File compiles a file that has been resolved successfully.
func File(file *syntax.File, module *resolve.Module) *Program {
	globals := make([]string, 0, len(module.Globals))
	for _, global := range module.Globals {
		globals = append(globals, global.First.Name)
	}
	c := &compiler{module: module}
	return &Program{
		Filename: file.Path,
		// The top level has no declaration. Leave its position
		// unset, so that it differs from that of any function.
		Toplevel: c.function(module.Toplevel, syntax.Position{}, file.Stmts, nil),
		Globals:  globals,
	}
}

// compiler contains the state that is shared by all functions in a
// file.
type compiler struct {
	module *resolve.Module
}

// fcomp contains the state of the compilation of a single function.
type fcomp struct {
	compiler *compiler
	fn       *Funcode
	// Position of the next instruction to be emitted, if it may
	// fail at runtime.
	pos   syntax.Position
	loops []loop
	block *block

	stringConstants map[string]uint32
	// Keyed by the decimal representation of the integer, as
	// pointers to equal values are not equal.
	intConstants   map[string]uint32
	floatConstants map[float64]uint32
	names          map[string]uint32
	imports        map[Import]uint32
}

type loop struct {
	breakBlock, continueBlock *block
}

// block is a basic block of the control flow graph.
type block struct {
	insns []insn

	// If the last instruction is RETURN, jmp and cjmp are nil. If the
	// last instruction is CJMP or ITERJMP, cjmp and jmp are the
	// "true" and "false" successors. Otherwise, jmp is the only
	// successor.
	jmp, cjmp *block

	initialStack int

	// Set when linearizing the control flow graph.
	index int
	addr  uint32
}

type insn struct {
	op  Opcode
	arg uint32
	pos syntax.Position
}

func (c *compiler) function(f *resolve.Function, pos syntax.Position, body []syntax.Stmt, bodyExpr syntax.Expr) *Funcode {
	fn := &Funcode{
		Name:                 f.Name,
		Pos:                  pos,
		NumKeywordOnlyParams: f.NumKeywordOnlyParams,
		HasArgs:              f.HasArgs,
		HasKwargs:            f.HasKwargs,
	}
	for _, param := range f.Params {
		if param.Kind != syntax.ParamStar {
			fn.NumParams++
		}
	}
	for i, local := range f.Locals {
		fn.Locals = append(fn.Locals, local.First.Name)
		if local.Scope == resolve.Cell {
			fn.Cells = append(fn.Cells, i)
		}
	}
	for _, freeVar := range f.FreeVars {
		fn.FreeVars = append(fn.FreeVars, freeVar.First.Name)
	}

	fc := &fcomp{
		compiler:        c,
		fn:              fn,
		stringConstants: map[string]uint32{},
		intConstants:    map[string]uint32{},
		floatConstants:  map[float64]uint32{},
		names:           map[string]uint32{},
		imports:         map[Import]uint32{},
	}
	entry := fc.newBlock()
	fc.block = entry
	if bodyExpr != nil {
		// Body of a lambda.
		fc.expr(bodyExpr)
		fc.emit(RETURN)
		fc.block = nil
	} else {
		fc.stmts(body)
		if fc.block != nil {
			fc.emit(NONE)
			fc.emit(RETURN)
		}
	}
	fc.linearize(entry)
	return fn
}

// skipEmptyBlocks returns the first non-empty block that is reached
// when starting at the provided block. All of the empty blocks that are
// traversed are updated to refer to that block directly. Long chains of
// empty blocks are created by constructs such as elif, and would
// otherwise cause compilation to take quadratic time.
func skipEmptyBlocks(b *block) *block {
	target := b
	for target.insns == nil {
		target = target.jmp
	}
	for b.insns == nil {
		next := b.jmp
		b.jmp = target
		b = next
	}
	return target
}

// linearize places the basic blocks of the control flow graph in
// order, computes the maximum stack depth, and emits the bytecode and
// line table.
func (fc *fcomp) linearize(entry *block) {
	var blocks []*block
	maxStack := 0

	setInitialStack := func(b *block, depth int) {
		if b.initialStack == -1 {
			b.initialStack = depth
		} else if b.initialStack != depth {
			panic(fmt.Sprintf("stack depth mismatch in function %s: %d vs. %d", fc.fn.Name, b.initialStack, depth))
		}
	}

	var visit func(b *block)
	visit = func(b *block) {
		if b.index >= 0 {
			return
		}
		b.index = len(blocks)
		blocks = append(blocks, b)

		stack := b.initialStack
		isIterJmp := 0
		for _, insn := range b.insns {
			if insn.op == ITERJMP {
				isIterJmp = 1
			}
			stack += StackEffect(insn.op, insn.arg)
			if stack < 0 {
				panic(fmt.Sprintf("stack underflow in function %s", fc.fn.Name))
			}
			maxStack = max(maxStack, stack+isIterJmp)
		}

		// Place the "false" successor next, so that no explicit
		// jump is needed.
		if b.jmp != nil {
			// Skip empty blocks.
			b.jmp = skipEmptyBlocks(b.jmp)
			setInitialStack(b.jmp, stack+isIterJmp)
			visit(b.jmp)
		}

		if b.cjmp != nil {
			b.cjmp = skipEmptyBlocks(b.cjmp)
			setInitialStack(b.cjmp, stack)
			visit(b.cjmp)
		}
	}
	setInitialStack(entry, 0)
	visit(entry)
	fc.fn.MaxStack = maxStack

	needsExplicitJump := func(b *block) bool {
		return b.jmp != nil && b.jmp.index != b.index+1
	}

	// assignAddresses computes the address of each block under the
	// assumption that jump operands have a given size, returning
	// the size of the resulting bytecode.
	assignAddresses := func(jumpOperandSizeBytes int) int {
		codeSizeBytes := 0
		for _, b := range blocks {
			b.addr = uint32(codeSizeBytes)
			for _, insn := range b.insns {
				codeSizeBytes++
				if insn.op == CJMP || insn.op == ITERJMP {
					codeSizeBytes += jumpOperandSizeBytes
				} else if insn.op >= OpcodeArgMin {
					codeSizeBytes += varint.SizeBytes(insn.arg)
				}
			}
			if needsExplicitJump(b) {
				codeSizeBytes += 1 + jumpOperandSizeBytes
			}
		}
		return codeSizeBytes
	}

	// Use the smallest operand size for jumps that is capable of
	// addressing all of the bytecode. As the size of the bytecode
	// increases along with the size of the operands, this is
	// guaranteed to be equal to JumpOperandSizeBytes() of the size
	// of the final bytecode.
	jumpOperandSizeBytes := 1
	codeSizeBytes := assignAddresses(jumpOperandSizeBytes)
	for JumpOperandSizeBytes(codeSizeBytes) > jumpOperandSizeBytes {
		jumpOperandSizeBytes++
		codeSizeBytes = assignAddresses(jumpOperandSizeBytes)
	}

	// Emit the bytecode and the line table.
	code := make([]byte, 0, codeSizeBytes)
	var lineTable []byte
	previous := fc.fn.Pos
	for _, b := range blocks {
		for _, insn := range b.insns {
			if fc.fn.hasPosition(insn.op, insn.arg) {
				// Instructions that are generated by the
				// compiler may not have a position of
				// their own.
				current := insn.pos
				if !current.IsValid() {
					current = previous
				}
				lineTable = appendLineTableEntry(lineTable, previous, current)
				previous = current
			}
			code = append(code, byte(insn.op))
			if insn.op == CJMP || insn.op == ITERJMP {
				code = appendJumpOperand(code, b.cjmp.addr, jumpOperandSizeBytes)
			} else if insn.op >= OpcodeArgMin {
				code = varint.AppendForward(code, insn.arg)
			}
		}
		if needsExplicitJump(b) {
			code = append(code, byte(JMP))
			code = appendJumpOperand(code, b.jmp.addr, jumpOperandSizeBytes)
		}
	}
	if len(code) != codeSizeBytes {
		panic("wrong code length")
	}
	fc.fn.Code = code
	fc.fn.LineTable = lineTable
}

func (*fcomp) newBlock() *block {
	return &block{index: -1, initialStack: -1}
}

func (fc *fcomp) emit(op Opcode) {
	if op >= OpcodeArgMin {
		panic("missing operand: " + op.String())
	}
	fc.block.insns = append(fc.block.insns, insn{op: op, pos: fc.pos})
	fc.pos = syntax.Position{}
}

func (fc *fcomp) emit1(op Opcode, arg uint32) {
	if op < OpcodeArgMin {
		panic("unexpected operand: " + op.String())
	}
	fc.block.insns = append(fc.block.insns, insn{op: op, arg: arg, pos: fc.pos})
	fc.pos = syntax.Position{}
}

// jump terminates the current block with a jump to another block.
func (fc *fcomp) jump(b *block) {
	if b == fc.block {
		panic("self-jump")
	}
	fc.block.jmp = b
	fc.block = nil
}

// condjump terminates the current block with a conditional jump. For
// ITERJMP, t is the block to jump to when the iterator is exhausted.
func (fc *fcomp) condjump(op Opcode, t, f *block) {
	fc.emit1(op, 0)
	fc.block.cjmp = t
	fc.jump(f)
}

// setPos sets the source position of the next instruction. It needs to
// be called before emitting instructions that can fail.
func (fc *fcomp) setPos(pos syntax.Position) {
	fc.pos = pos
}

// internConstant returns the index of a constant in the table of
// constants of its type, adding it if it is not present yet.
func internConstant[TKey comparable, TValue any](indices map[TKey]uint32, table *[]TValue, key TKey, value TValue) uint32 {
	index, ok := indices[key]
	if !ok {
		index = uint32(len(*table))
		indices[key] = index
		*table = append(*table, value)
	}
	return index
}

func (fc *fcomp) int(i *big.Int) {
	fc.emit1(INT, internConstant(fc.intConstants, &fc.fn.IntConstants, i.String(), i))
}

func (fc *fcomp) nameIndex(name string) uint32 {
	index, ok := fc.names[name]
	if !ok {
		index = uint32(len(fc.fn.Names))
		fc.names[name] = index
		fc.fn.Names = append(fc.fn.Names, name)
	}
	return index
}

func (fc *fcomp) importIndex(index int) uint32 {
	loadedSymbol := fc.compiler.module.Imports[index]
	key := Import{Module: loadedSymbol.Module, Name: loadedSymbol.Name}
	i, ok := fc.imports[key]
	if !ok {
		i = uint32(len(fc.fn.Imports))
		fc.imports[key] = i
		fc.fn.Imports = append(fc.fn.Imports, key)
	}
	return i
}

func (fc *fcomp) string(s string) {
	fc.emit1(STR, internConstant(fc.stringConstants, &fc.fn.StringConstants, s, s))
}

// set emits code to store the value at the top of the stack in a
// variable.
func (fc *fcomp) set(id *syntax.Ident) {
	b := fc.compiler.module.Binding(id)
	switch b.Scope {
	case resolve.Local:
		fc.emit1(SETLOCAL, uint32(b.Index))
	case resolve.Cell:
		fc.emit1(SETLOCALCELL, uint32(b.Index))
	case resolve.Global:
		fc.emit1(SETGLOBAL, fc.nameIndex(id.Name))
	default:
		panic(fmt.Sprintf("%s: cannot assign to %s with scope %s", id.NamePos, id.Name, b.Scope))
	}
}

// lookup emits code to push the value of a variable.
func (fc *fcomp) lookup(id *syntax.Ident) {
	b := fc.compiler.module.Binding(id)
	if b.Scope != resolve.Universal {
		fc.setPos(id.NamePos)
	}
	switch b.Scope {
	case resolve.Local:
		fc.emit1(LOCAL, uint32(b.Index))
	case resolve.Cell:
		fc.emit1(LOCALCELL, uint32(b.Index))
	case resolve.Free:
		fc.emit1(FREECELL, uint32(b.Index))
	case resolve.Global:
		fc.emit1(GLOBAL, fc.nameIndex(id.Name))
	case resolve.Import:
		fc.emit1(IMPORT, fc.importIndex(b.Index))
	case resolve.Predeclared:
		fc.emit1(PREDECLARED, fc.nameIndex(id.Name))
	case resolve.Universal:
		// The constants have instructions of their own, which
		// don't need to perform a lookup by name.
		switch id.Name {
		case "None":
			fc.emit(NONE)
		case "True":
			fc.emit(TRUE)
		case "False":
			fc.emit(FALSE)
		default:
			fc.emit1(UNIVERSAL, fc.nameIndex(id.Name))
		}
	default:
		panic(fmt.Sprintf("%s: cannot look up %s with scope %s", id.NamePos, id.Name, b.Scope))
	}
}

func (fc *fcomp) stmts(stmts []syntax.Stmt) {
	for _, stmt := range stmts {
		fc.stmt(stmt)
	}
}

func (fc *fcomp) stmt(stmt syntax.Stmt) {
	switch s := stmt.(type) {
	case *syntax.ExprStmt:
		if _, ok := s.X.(*syntax.Literal); ok {
			// Don't compile docstrings, only to pop them.
			return
		}
		fc.expr(s.X)
		fc.emit(POP)

	case *syntax.BranchStmt:
		// The resolver ensures that break and continue only
		// appear inside loops.
		switch s.Token {
		case syntax.BREAK:
			fc.jump(fc.loops[len(fc.loops)-1].breakBlock)
			fc.block = fc.newBlock()
		case syntax.CONTINUE:
			fc.jump(fc.loops[len(fc.loops)-1].continueBlock)
			fc.block = fc.newBlock()
		}

	case *syntax.IfStmt:
		t := fc.newBlock()
		f := fc.newBlock()
		done := fc.newBlock()

		fc.ifElse(s.Cond, t, f)

		fc.block = t
		fc.stmts(s.True)
		fc.jump(done)

		fc.block = f
		fc.stmts(s.False)
		fc.jump(done)

		fc.block = done

	case *syntax.AssignStmt:
		if s.Op == syntax.EQ {
			fc.expr(s.RHS)
			fc.assign(s.OpPos, s.LHS)
		} else {
			fc.augmentedAssign(s)
		}

	case *syntax.DefStmt:
		fc.function(s, nil, s.Body)
		fc.set(s.Name)

	case *syntax.ForStmt:
		head := fc.newBlock()
		body := fc.newBlock()
		tail := fc.newBlock()

		fc.expr(s.X)
		fc.setPos(s.For)
		fc.emit(ITERPUSH)
		fc.jump(head)

		fc.block = head
		fc.condjump(ITERJMP, tail, body)

		fc.block = body
		fc.assign(s.For, s.Vars)
		fc.loops = append(fc.loops, loop{breakBlock: tail, continueBlock: head})
		fc.stmts(s.Body)
		fc.loops = fc.loops[:len(fc.loops)-1]
		fc.jump(head)

		fc.block = tail
		fc.emit(ITERPOP)

	case *syntax.ReturnStmt:
		if s.Result != nil {
			fc.expr(s.Result)
		} else {
			fc.emit(NONE)
		}
		fc.emit(RETURN)
		fc.block = fc.newBlock()

	case *syntax.LoadStmt:
		// Symbols bound by load() are loaded lazily, when they
		// are first used.

	case *syntax.TypeAliasStmt, *syntax.VarStmt:
		// Type annotations have no effect at runtime.

	default:
		panic(fmt.Sprintf("%s: unexpected statement type %T", syntax.Start(stmt), stmt))
	}
}

// augmentedAssign emits code for an augmented assignment, such as
// x += y. The target is evaluated only once, to avoid duplicate side
// effects.
func (fc *fcomp) augmentedAssign(s *syntax.AssignStmt) {
	var set func()
	switch lhs := unparen(s.LHS).(type) {
	case *syntax.Ident:
		fc.lookup(lhs)
		set = func() {
			fc.set(lhs)
		}
	case *syntax.IndexExpr:
		fc.expr(lhs.X)
		fc.expr(lhs.Y)
		fc.emit(DUP2)
		fc.setPos(lhs.Lbrack)
		fc.emit(INDEX)
		set = func() {
			fc.setPos(lhs.Lbrack)
			fc.emit(SETINDEX)
		}
	case *syntax.DotExpr:
		fc.expr(lhs.X)
		fc.emit(DUP)
		name := fc.nameIndex(lhs.Name.Name)
		fc.setPos(lhs.Dot)
		fc.emit1(ATTR, name)
		set = func() {
			fc.setPos(lhs.Dot)
			fc.emit1(SETFIELD, name)
		}
	default:
		panic(fmt.Sprintf("%s: unexpected augmented assignment target %T", syntax.Start(lhs), lhs))
	}

	fc.expr(s.RHS)

	// x += y and x |= y update x in place, if possible.
	fc.setPos(s.OpPos)
	switch s.Op {
	case syntax.PLUSEQ:
		fc.emit(INPLACEADD)
	case syntax.PIPEEQ:
		fc.emit(INPLACEPIPE)
	default:
		fc.binop(s.OpPos, s.Op-syntax.PLUSEQ+syntax.PLUS)
	}
	set()
}

// assign emits code to assign the value at the top of the stack to an
// assignment target.
func (fc *fcomp) assign(pos syntax.Position, lhs syntax.Expr) {
	switch x := lhs.(type) {
	case *syntax.Ident:
		fc.set(x)
	case *syntax.ParenExpr:
		fc.assign(pos, x.X)
	case *syntax.TupleExpr:
		fc.assignSequence(pos, x.List)
	case *syntax.ListExpr:
		fc.assignSequence(pos, x.List)
	case *syntax.IndexExpr:
		fc.expr(x.X)
		fc.emit(EXCH)
		fc.expr(x.Y)
		fc.emit(EXCH)
		fc.setPos(x.Lbrack)
		fc.emit(SETINDEX)
	case *syntax.DotExpr:
		fc.expr(x.X)
		fc.emit(EXCH)
		fc.setPos(x.Dot)
		fc.emit1(SETFIELD, fc.nameIndex(x.Name.Name))
	default:
		panic(fmt.Sprintf("%s: unexpected assignment target %T", syntax.Start(lhs), lhs))
	}
}

func (fc *fcomp) assignSequence(pos syntax.Position, lhs []syntax.Expr) {
	fc.setPos(pos)
	fc.emit1(UNPACK, uint32(len(lhs)))
	for _, x := range lhs {
		fc.assign(pos, x)
	}
}

func (fc *fcomp) expr(x syntax.Expr) {
	switch e := x.(type) {
	case *syntax.ParenExpr:
		fc.expr(e.X)

	case *syntax.Ident:
		fc.lookup(e)

	case *syntax.Literal:
		switch v := e.Value.(type) {
		case string:
			fc.string(v)
		case int64:
			fc.int(big.NewInt(v))
		case *big.Int:
			fc.int(v)
		case float64:
			fc.emit1(FLOAT, internConstant(fc.floatConstants, &fc.fn.FloatConstants, v, v))
		default:
			panic(fmt.Sprintf("%s: unexpected literal of type %T", e.TokenPos, e.Value))
		}

	case *syntax.ListExpr:
		for _, element := range e.List {
			fc.expr(element)
		}
		fc.emit1(MAKELIST, uint32(len(e.List)))

	case *syntax.CondExpr:
		t := fc.newBlock()
		f := fc.newBlock()
		done := fc.newBlock()

		fc.ifElse(e.Cond, t, f)

		fc.block = t
		fc.expr(e.True)
		fc.jump(done)

		fc.block = f
		fc.expr(e.False)
		fc.jump(done)

		fc.block = done

	case *syntax.IndexExpr:
		fc.expr(e.X)
		fc.expr(e.Y)
		fc.setPos(e.Lbrack)
		fc.emit(INDEX)

	case *syntax.SliceExpr:
		fc.expr(e.X)
		for _, y := range []syntax.Expr{e.Lo, e.Hi, e.Step} {
			if y == nil {
				fc.emit(NONE)
			} else {
				fc.expr(y)
			}
		}
		fc.setPos(e.Lbrack)
		fc.emit(SLICE)

	case *syntax.Comprehension:
		if e.Curly {
			fc.emit(MAKEDICT)
		} else {
			fc.emit1(MAKELIST, 0)
		}
		fc.comprehension(e, 0)

	case *syntax.TupleExpr:
		for _, element := range e.List {
			fc.expr(element)
		}
		fc.emit1(MAKETUPLE, uint32(len(e.List)))

	case *syntax.DictExpr:
		fc.emit(MAKEDICT)
		for _, entry := range e.Entries {
			fc.emit(DUP)
			fc.expr(entry.Key)
			fc.expr(entry.Value)
			fc.setPos(entry.Colon)
			fc.emit(SETDICTUNIQ)
		}

	case *syntax.UnaryExpr:
		fc.expr(e.X)
		fc.setPos(e.OpPos)
		switch e.Op {
		case syntax.MINUS:
			fc.emit(UMINUS)
		case syntax.PLUS:
			fc.emit(UPLUS)
		case syntax.NOT:
			fc.emit(NOT)
		case syntax.TILDE:
			fc.emit(TILDE)
		default:
			panic(fmt.Sprintf("%s: unexpected unary operator %s", e.OpPos, e.Op))
		}

	case *syntax.BinaryExpr:
		switch e.Op {
		case syntax.OR:
			// x or y => if x then x else y
			done := fc.newBlock()
			y := fc.newBlock()

			fc.expr(e.X)
			fc.emit(DUP)
			fc.condjump(CJMP, done, y)

			fc.block = y
			fc.emit(POP)
			fc.expr(e.Y)
			fc.jump(done)

			fc.block = done

		case syntax.AND:
			// x and y => if x then y else x
			done := fc.newBlock()
			y := fc.newBlock()

			fc.expr(e.X)
			fc.emit(DUP)
			fc.condjump(CJMP, y, done)

			fc.block = y
			fc.emit(POP)
			fc.expr(e.Y)
			fc.jump(done)

			fc.block = done

		case syntax.PLUS:
			fc.plus(e)

		default:
			fc.expr(e.X)
			fc.expr(e.Y)
			fc.binop(e.OpPos, e.Op)
		}

	case *syntax.DotExpr:
		fc.expr(e.X)
		fc.setPos(e.Dot)
		fc.emit1(ATTR, fc.nameIndex(e.Name.Name))

	case *syntax.CallExpr:
		fc.call(e)

	case *syntax.CastExpr:
		// Casts have no effect at runtime.
		fc.expr(e.Value)

	case *syntax.LambdaExpr:
		fc.function(e, e.Body, nil)

	default:
		panic(fmt.Sprintf("%s: unexpected expression type %T", syntax.Start(x), x))
	}
}

// summand is an operand of an n-ary sum.
type summand struct {
	x       syntax.Expr
	plusPos syntax.Position
}

// plus emits code for ((a+b)+...)+z. Adjacent string literals, list
// literals and tuple literals are folded together.
func (fc *fcomp) plus(e *syntax.BinaryExpr) {
	// Gather all operands of the left-leaning tree of additions.
	var args []summand
	for plus := e; ; {
		args = append(args, summand{x: unparen(plus.Y), plusPos: plus.OpPos})
		left, ok := unparen(plus.X).(*syntax.BinaryExpr)
		if !ok || left.Op != syntax.PLUS {
			args = append(args, summand{x: unparen(plus.X)})
			break
		}
		plus = left
	}
	for i, j := 0, len(args)-1; i < j; i, j = i+1, j-1 {
		args[i], args[j] = args[j], args[i]
	}

	// Fold sums of adjacent literals of the same kind.
	folded := args[:0]
	for i := 0; i < len(args); {
		j := i + 1
		if kind := addableKind(args[i].x); kind != 0 {
			for j < len(args) && addableKind(args[j].x) == kind {
				j++
			}
			if j > i+1 {
				args[i].x = addLiterals(kind, args[i:j])
			}
		}
		folded = append(folded, args[i])
		i = j
	}

	fc.expr(folded[0].x)
	for _, s := range folded[1:] {
		fc.expr(s.x)
		fc.setPos(s.plusPos)
		fc.emit(PLUS)
	}
}

// addableKind returns whether an expression is a literal that can be
// folded when added to an adjacent literal of the same kind: a [s]tring,
// [l]ist or [t]uple.
func addableKind(x syntax.Expr) rune {
	switch e := x.(type) {
	case *syntax.Literal:
		if e.Token == syntax.STRING {
			return 's'
		}
	case *syntax.ListExpr:
		return 'l'
	case *syntax.TupleExpr:
		return 't'
	}
	return 0
}

func addLiterals(kind rune, args []summand) syntax.Expr {
	switch kind {
	case 's':
		var s string
		for _, arg := range args {
			s += arg.x.(*syntax.Literal).Value.(string)
		}
		return &syntax.Literal{Token: syntax.STRING, TokenPos: syntax.Start(args[0].x), Value: s}
	case 'l':
		var elements []syntax.Expr
		for _, arg := range args {
			elements = append(elements, arg.x.(*syntax.ListExpr).List...)
		}
		return &syntax.ListExpr{List: elements}
	case 't':
		var elements []syntax.Expr
		for _, arg := range args {
			elements = append(elements, arg.x.(*syntax.TupleExpr).List...)
		}
		return &syntax.TupleExpr{List: elements}
	}
	panic(kind)
}

func unparen(x syntax.Expr) syntax.Expr {
	for {
		paren, ok := x.(*syntax.ParenExpr)
		if !ok {
			return x
		}
		x = paren.X
	}
}

func (fc *fcomp) binop(pos syntax.Position, op syntax.Token) {
	fc.setPos(pos)
	switch op {
	case syntax.PLUS, syntax.MINUS, syntax.STAR, syntax.SLASH, syntax.SLASHSLASH,
		syntax.PERCENT, syntax.AMP, syntax.PIPE, syntax.CIRCUMFLEX, syntax.LTLT, syntax.GTGT:
		fc.emit(Opcode(op-syntax.PLUS) + PLUS)
	case syntax.EQL, syntax.NEQ, syntax.GT, syntax.LT, syntax.LE, syntax.GE:
		fc.emit(comparisonOpcodes[op])
	case syntax.IN:
		fc.emit(IN)
	case syntax.NOTIN:
		fc.emit(IN)
		fc.emit(NOT)
	default:
		panic(fmt.Sprintf("%s: unexpected binary operator %s", pos, op))
	}
}

var comparisonOpcodes = map[syntax.Token]Opcode{
	syntax.LT:  LT,
	syntax.GT:  GT,
	syntax.GE:  GE,
	syntax.LE:  LE,
	syntax.EQL: EQL,
	syntax.NEQ: NEQ,
}

// call emits code for a function call. The resolver ensures that
// arguments are provided in the order positional, named, *args,
// **kwargs, meaning that they can be evaluated from left to right.
func (fc *fcomp) call(call *syntax.CallExpr) {
	fc.expr(call.Fn)
	var positional, named int
	var hasArgs, hasKwargs bool
	for _, arg := range call.Args {
		switch arg.Kind {
		case syntax.ArgPositional:
			fc.expr(arg.Value)
			positional++
		case syntax.ArgNamed:
			fc.string(arg.Name.Name)
			fc.expr(arg.Value)
			named++
		case syntax.ArgArgs:
			fc.expr(arg.Value)
			hasArgs = true
		case syntax.ArgKwargs:
			fc.expr(arg.Value)
			hasKwargs = true
		}
	}
	if positional > resolve.MaximumCallArguments || named > resolve.MaximumCallArguments {
		// The resolver rejects calls like these.
		panic(fmt.Sprintf("%s: too many arguments", call.Lparen))
	}

	op := CALL
	switch {
	case hasArgs && hasKwargs:
		op = CALLARGKW
	case hasArgs:
		op = CALLARGS
	case hasKwargs:
		op = CALLKW
	}
	fc.setPos(call.Lparen)
	fc.emit1(op, MakeCallOperand(positional, named))
}

func (fc *fcomp) comprehension(c *syntax.Comprehension, clauseIndex int) {
	if clauseIndex == len(c.Clauses) {
		// Duplicate the accumulator.
		fc.emit(DUP)
		if c.Curly {
			entry := c.Body.(*syntax.DictEntry)
			fc.expr(entry.Key)
			fc.expr(entry.Value)
			fc.setPos(entry.Colon)
			fc.emit(SETDICT)
		} else {
			fc.expr(c.Body.(syntax.Expr))
			fc.emit(APPEND)
		}
		return
	}

	switch clause := c.Clauses[clauseIndex].(type) {
	case *syntax.IfClause:
		t := fc.newBlock()
		done := fc.newBlock()
		fc.ifElse(clause.Cond, t, done)

		fc.block = t
		fc.comprehension(c, clauseIndex+1)
		fc.jump(done)

		fc.block = done

	case *syntax.ForClause:
		head := fc.newBlock()
		body := fc.newBlock()
		tail := fc.newBlock()

		fc.expr(clause.X)
		fc.setPos(clause.For)
		fc.emit(ITERPUSH)
		fc.jump(head)

		fc.block = head
		fc.condjump(ITERJMP, tail, body)

		fc.block = body
		fc.assign(clause.For, clause.Vars)
		fc.comprehension(c, clauseIndex+1)
		fc.jump(head)

		fc.block = tail
		fc.emit(ITERPOP)

	default:
		panic(fmt.Sprintf("unexpected comprehension clause type %T", clause))
	}
}

// function emits code to create a function or lambda. Its body is
// compiled to a separate Funcode.
func (fc *fcomp) function(node syntax.Node, bodyExpr syntax.Expr, body []syntax.Stmt) {
	f := fc.compiler.module.Function(node)

	// Emit a tuple containing the default values of the
	// parameters, followed by the free variables. For:
	//
	//	def f(p1, p2=dp2, p3=dp3, *, k1, k2=dk2, k3, **kwargs)
	//
	// the default values are (dp2, dp3, MANDATORY, dk2, MANDATORY).
	defaults := 0
	seenStar := false
	for _, param := range f.Params {
		switch param.Kind {
		case syntax.ParamNormal:
			if param.Default != nil {
				fc.expr(param.Default)
				defaults++
			} else if seenStar {
				fc.emit(MANDATORY)
				defaults++
			}
		case syntax.ParamStar, syntax.ParamArgs:
			seenStar = true
		}
	}

	// Capture the cells of the free variables of the function.
	for _, freeVar := range f.FreeVars {
		switch freeVar.Scope {
		case resolve.Cell:
			fc.emit1(LOCAL, uint32(freeVar.Index))
		case resolve.Free:
			fc.emit1(FREE, uint32(freeVar.Index))
		default:
			panic(fmt.Sprintf("unexpected scope %s of free variable", freeVar.Scope))
		}
	}
	fc.emit1(MAKETUPLE, uint32(defaults+len(f.FreeVars)))

	funcode := fc.compiler.function(f, f.Pos, body, bodyExpr)
	index := uint32(len(fc.fn.Functions))
	fc.fn.Functions = append(fc.fn.Functions, funcode)
	fc.emit1(MAKEFUNC, index)
}

// ifElse emits code for a Boolean control flow decision. On return,
// the current block is unset.
func (fc *fcomp) ifElse(cond syntax.Expr, t, f *block) {
	switch c := cond.(type) {
	case *syntax.UnaryExpr:
		if c.Op == syntax.NOT {
			// if not x then goto t else goto f
			//    =>
			// if x then goto f else goto t
			fc.ifElse(c.X, f, t)
			return
		}

	case *syntax.BinaryExpr:
		switch c.Op {
		case syntax.AND:
			// if x and y then goto t else goto f
			//    =>
			// if x then ifElse(y, t, f) else goto f
			fc.expr(c.X)
			y := fc.newBlock()
			fc.condjump(CJMP, y, f)

			fc.block = y
			fc.ifElse(c.Y, t, f)
			return

		case syntax.OR:
			// if x or y then goto t else goto f
			//    =>
			// if x then goto t else ifElse(y, t, f)
			fc.expr(c.X)
			y := fc.newBlock()
			fc.condjump(CJMP, t, y)

			fc.block = y
			fc.ifElse(c.Y, t, f)
			return

		case syntax.NOTIN:
			// if x not in y then goto t else goto f
			//    =>
			// if x in y then goto f else goto t
			fc.expr(c.X)
			fc.expr(c.Y)
			fc.setPos(c.OpPos)
			fc.emit(IN)
			fc.condjump(CJMP, f, t)
			return
		}
	}

	fc.expr(cond)
	fc.condjump(CJMP, t, f)
}
