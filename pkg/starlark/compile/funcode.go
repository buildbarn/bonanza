package compile

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"math/big"
	"math/bits"
	"strings"

	"bonanza.build/pkg/encoding/varint"
	"bonanza.build/pkg/starlark/syntax"
)

// Version of the bytecode format. It needs to be incremented whenever
// the format of Funcode changes in an incompatible way, so that
// previously compiled code is not misinterpreted.
const Version = 1

// Program is a compiled Starlark file.
type Program struct {
	Filename string
	// Code of the top level of the file, which initializes its
	// globals.
	Toplevel *Funcode
	// Names of all globals that are assigned at the top level of
	// the file, in the order in which they are first bound.
	Globals []string
}

// FunctionName identifies a function among all functions that are
// declared directly inside the same enclosing function, or at the top
// level of the same file.
type FunctionName struct {
	// The name of the function. Empty for lambdas.
	Name string
	// Zero for the first function having this name, one for the
	// second, etc.
	DisambiguationIndex int
}

// Compare two function names, first ordering them by name and then by
// disambiguation index.
func (n FunctionName) Compare(other FunctionName) int {
	return cmp.Or(strings.Compare(n.Name, other.Name), cmp.Compare(n.DisambiguationIndex, other.DisambiguationIndex))
}

func (fn *Funcode) nestedFunctionName(index int) string {
	if name := fn.Functions[index].Name; name != LambdaName {
		return name
	}
	return ""
}

// GetNestedFunctionName returns the name of a function nested directly
// inside this function, given its index in the Functions field.
func (fn *Funcode) GetNestedFunctionName(index int) FunctionName {
	name := FunctionName{Name: fn.nestedFunctionName(index)}
	for i := range index {
		if fn.nestedFunctionName(i) == name.Name {
			name.DisambiguationIndex++
		}
	}
	return name
}

// FindNestedFunction returns the function nested directly inside this
// function that has a given name, or nil if no such function exists.
func (fn *Funcode) FindNestedFunction(name FunctionName) *Funcode {
	occurrence := 0
	for i := range fn.Functions {
		if fn.nestedFunctionName(i) == name.Name {
			if occurrence == name.DisambiguationIndex {
				return fn.Functions[i]
			}
			occurrence++
		}
	}
	return nil
}

// LambdaName is the value of Funcode.Name for lambda expressions.
const LambdaName = "lambda"

// Import refers to a symbol that is loaded from another file.
type Import struct {
	// Module string, as provided to the load() statement.
	Module string
	// Name of the symbol in the loaded module.
	Name string
}

// Funcode is the compiled code of a function, a lambda or the top level
// of a file.
//
// A Funcode is self-contained: all constants, names and nested
// functions it refers to are stored in its own tables. Globals are
// referred to by name, so that a function can be invoked without
// having access to any of the other functions in the file.
type Funcode struct {
	// Name of the function. "lambda" for lambdas, and "<toplevel>"
	// for the top level of a file.
	Name string
	// Position of the "def" or "lambda" keyword. Together with the
	// name of the file, this uniquely identifies the function. It
	// is unset for the top level of a file.
	Pos syntax.Position

	// The bytecode of the function. Instructions consist of an
	// opcode, optionally followed by an operand. Operands of jump
	// instructions have a fixed size that is determined by
	// JumpOperandSizeBytes(), while all other operands are encoded
	// using varint.AppendForward().
	Code []byte
	// Delta encoded table containing the source positions of all
	// instructions for which hasPosition() returns true, in the
	// order of the bytecode. See Position().
	LineTable []byte

	// Constants referred to by STR, INT and FLOAT, respectively.
	StringConstants []string
	IntConstants    []*big.Int
	FloatConstants  []float64
	// Names referred to by ATTR, SETFIELD, GLOBAL, SETGLOBAL,
	// PREDECLARED and UNIVERSAL.
	Names []string
	// Symbols referred to by IMPORT.
	Imports []Import
	// Nested functions referred to by MAKEFUNC.
	Functions []*Funcode

	// Names of the local variables. Parameters come first.
	Locals []string
	// Indices of local variables that need to be stored in cells,
	// as they are captured by nested functions.
	Cells []int
	// Names of the free variables.
	FreeVars []string

	// Number of parameters, including *args and **kwargs.
	NumParams            int
	NumKeywordOnlyParams int
	HasArgs              bool
	HasKwargs            bool

	// Maximum size of the operand stack.
	MaxStack int
}

// DecodeOperand decodes the variable length operand of an instruction.
// It returns the operand and the program counter of the next
// instruction.
func DecodeOperand(code []byte, pc uint32) (uint32, uint32) {
	arg, n := varint.ConsumeForward[uint32](code[pc:])
	if n <= 0 {
		panic("malformed operand")
	}
	return arg, pc + uint32(n)
}

// JumpOperandSizeBytes returns the size of the operands of jump
// instructions in a function having a given amount of bytecode, which
// is the smallest number of bytes capable of holding any address within
// the bytecode. Using the same size for all jumps in a function allows
// the compiler to compute it without knowing the addresses, while
// deriving it from the size of the bytecode means it doesn't need to be
// stored.
func JumpOperandSizeBytes(codeSizeBytes int) int {
	return max((bits.Len64(uint64(codeSizeBytes))+7)/8, 1)
}

// appendJumpOperand appends the address of a jump instruction to the
// bytecode, encoded as a little endian integer of a given size.
func appendJumpOperand(code []byte, address uint32, sizeBytes int) []byte {
	for i := range sizeBytes {
		code = append(code, byte(address>>(8*i)))
	}
	return code
}

func decodeJumpOperand(code []byte, pc uint32, sizeBytes int) uint32 {
	var address uint32
	for i := range sizeBytes {
		address |= uint32(code[pc+uint32(i)]) << (8 * i)
	}
	return address
}

// DecodeInstruction decodes the instruction at a given program
// counter. It returns the opcode, its operand, and the program counter
// of the next instruction.
func DecodeInstruction(code []byte, pc uint32) (Opcode, uint32, uint32) {
	op := Opcode(code[pc])
	pc++
	if op < OpcodeArgMin {
		return op, 0, pc
	}
	if op == JMP || op == CJMP || op == ITERJMP {
		sizeBytes := JumpOperandSizeBytes(len(code))
		return op, decodeJumpOperand(code, pc, sizeBytes), pc + uint32(sizeBytes)
	}
	arg, pc := DecodeOperand(code, pc)
	return op, arg, pc
}

// hasPosition returns whether an instruction has an entry in the line
// table. This is the case for all instructions that can fail, except
// for accessing parameters, as those are always assigned. As this only
// depends on the instruction, the line table doesn't need to contain
// program counters.
func (fn *Funcode) hasPosition(op Opcode, arg uint32) bool {
	switch op {
	case LT, GT, GE, LE, EQL, NEQ,
		PLUS, MINUS, STAR, SLASH, SLASHSLASH, PERCENT, AMP, PIPE, CIRCUMFLEX, LTLT, GTGT,
		IN, UPLUS, UMINUS, TILDE,
		ITERPUSH, SETINDEX, INDEX, SETDICT, SETDICTUNIQ, SLICE, INPLACEADD, INPLACEPIPE,
		FREECELL, GLOBAL, IMPORT, PREDECLARED, ATTR, SETFIELD, UNPACK,
		CALL, CALLARGS, CALLKW, CALLARGKW:
		return true
	case LOCAL, LOCALCELL:
		return uint64(arg) >= uint64(fn.NumParams)
	default:
		return false
	}
}

// lineTableEntry maps a program counter to a source position.
type lineTableEntry struct {
	pc  uint32
	pos syntax.Position
}

// appendLineTableEntry appends the position of an instruction to a line
// table. The first varint contains a bit indicating whether the line
// number differs from that of the previous entry. If so, the remaining
// bits contain the difference in line number, and it is followed by a
// varint containing the column number. Otherwise, the remaining bits
// contain the difference in column number.
func appendLineTableEntry(table []byte, previous, current syntax.Position) []byte {
	if current.Line == previous.Line {
		return varint.AppendForward(table, int64(current.Column-previous.Column)*2)
	}
	table = varint.AppendForward(table, int64(current.Line-previous.Line)*2+1)
	return varint.AppendForward(table, current.Column)
}

var errInvalidLineTable = errors.New("line table is malformed")

// lineTableEntries decodes the line table, calling a function for each
// of its entries. Iteration stops when the function returns false, or
// when the bytecode or line table turn out to be malformed.
func (fn *Funcode) lineTableEntries(f func(lineTableEntry) bool) error {
	entry := lineTableEntry{pos: fn.Pos}
	table := fn.LineTable
	for pc := uint32(0); pc < uint32(len(fn.Code)); {
		if err := validateInstruction(fn.Code, pc); err != nil {
			return err
		}
		op, arg, next := DecodeInstruction(fn.Code, pc)
		if fn.hasPosition(op, arg) {
			header, n := varint.ConsumeForward[int64](table)
			if n <= 0 {
				return errInvalidLineTable
			}
			table = table[n:]
			if header&1 == 0 {
				entry.pos.Column += int32(header >> 1)
			} else {
				column, n := varint.ConsumeForward[int32](table)
				if n <= 0 {
					return errInvalidLineTable
				}
				table = table[n:]
				entry.pos.Line += int32(header >> 1)
				entry.pos.Column = column
			}

			entry.pc = pc
			if !f(entry) {
				return nil
			}
		}
		pc = next
	}
	if len(table) > 0 {
		return errInvalidLineTable
	}
	return nil
}

// Position returns the source position of the instruction at a given
// program counter. Instructions that cannot fail don't have a position
// of their own. For these, the position of the closest preceding
// instruction that has a position is returned.
func (fn *Funcode) Position(pc uint32) syntax.Position {
	result := fn.Pos
	// Positions are only used for reporting errors. If the line
	// table is malformed, use the closest position that is known.
	_ = fn.lineTableEntries(func(entry lineTableEntry) bool {
		if entry.pc > pc {
			return false
		}
		result = entry.pos
		return true
	})
	return result
}

// Instruction is a single instruction of the bytecode of a function,
// as returned by Funcode.Disassemble().
type Instruction struct {
	// The program counter of the instruction.
	PC uint32
	// The opcode of the instruction.
	Opcode Opcode
	// The operand of the instruction. Only meaningful if Opcode is
	// greater or equal to OpcodeArgMin.
	Operand uint32
	// Description of the operand, such as the name of the local
	// variable it refers to. Empty if the operand needs no
	// description.
	OperandComment string
	// The source position of the instruction. Not valid for
	// instructions that have no position of their own.
	Position syntax.Position
}

// Disassembly is a representation of the bytecode of a function and the
// functions nested inside it that is suitable for displaying.
type Disassembly struct {
	// The name and parameters of the function, and the size of its
	// operand stack.
	Signature string
	// Additional properties of the function, such as the names of
	// its cells and free variables.
	Details []string
	// The instructions of the function.
	Instructions []Instruction
	// Disassembly of the functions nested directly inside this
	// function.
	Functions []*Disassembly
}

// Disassemble the bytecode of a function and the functions nested
// inside it. As the function may have been loaded from storage, this
// method validates its input, returning an error if it is malformed.
func (fn *Funcode) Disassemble() (*Disassembly, error) {
	if fn.NumParams < 0 || fn.NumParams > len(fn.Locals) {
		return nil, fmt.Errorf("function %s has %d parameters, while only having %d local variables", fn.Name, fn.NumParams, len(fn.Locals))
	}
	var signature strings.Builder
	fmt.Fprintf(&signature, "function %s(%s)", fn.Name, strings.Join(fn.Locals[:fn.NumParams], ", "))
	if fn.NumKeywordOnlyParams > 0 || fn.HasArgs || fn.HasKwargs {
		fmt.Fprintf(&signature, " keyword_only=%d args=%v kwargs=%v", fn.NumKeywordOnlyParams, fn.HasArgs, fn.HasKwargs)
	}
	fmt.Fprintf(&signature, " stack=%d", fn.MaxStack)
	d := &Disassembly{Signature: signature.String()}

	if len(fn.Cells) > 0 {
		cells := make([]string, 0, len(fn.Cells))
		for _, i := range fn.Cells {
			if i < 0 || i >= len(fn.Locals) {
				return nil, fmt.Errorf("function %s has a cell that refers to local variable %d, which does not exist", fn.Name, i)
			}
			cells = append(cells, fn.Locals[i])
		}
		d.Details = append(d.Details, "cells: "+strings.Join(cells, ", "))
	}
	if len(fn.FreeVars) > 0 {
		d.Details = append(d.Details, "freevars: "+strings.Join(fn.FreeVars, ", "))
	}

	if uint64(len(fn.Code)) > math.MaxUint32 {
		return nil, fmt.Errorf("function %s has too much bytecode", fn.Name)
	}
	for pc := uint32(0); pc < uint32(len(fn.Code)); {
		if err := validateInstruction(fn.Code, pc); err != nil {
			return nil, fmt.Errorf("function %s: %w", fn.Name, err)
		}
		op, arg, next := DecodeInstruction(fn.Code, pc)
		instruction := Instruction{
			PC:     pc,
			Opcode: op,
		}
		if op >= OpcodeArgMin {
			instruction.Operand = arg
			comment, err := fn.operandComment(op, arg)
			if err != nil {
				return nil, fmt.Errorf("function %s: instruction at offset %d: %w", fn.Name, pc, err)
			}
			instruction.OperandComment = comment
		}
		d.Instructions = append(d.Instructions, instruction)
		pc = next
	}

	// Attach source positions to the instructions.
	i := 0
	if err := fn.lineTableEntries(func(entry lineTableEntry) bool {
		for d.Instructions[i].PC != entry.pc {
			i++
		}
		d.Instructions[i].Position = entry.pos
		return true
	}); err != nil {
		return nil, fmt.Errorf("function %s: %w", fn.Name, err)
	}

	for _, nested := range fn.Functions {
		nestedDisassembly, err := nested.Disassemble()
		if err != nil {
			return nil, err
		}
		d.Functions = append(d.Functions, nestedDisassembly)
	}
	return d, nil
}

// validateInstruction returns an error if the instruction at a given
// program counter cannot be decoded by DecodeInstruction().
func validateInstruction(code []byte, pc uint32) error {
	op := Opcode(code[pc])
	if op > OpcodeMax {
		return fmt.Errorf("instruction at offset %d has illegal opcode %d", pc, uint8(op))
	}
	if op < OpcodeArgMin {
		return nil
	}
	operand := code[pc+1:]
	if op == JMP || op == CJMP || op == ITERJMP {
		if len(operand) < JumpOperandSizeBytes(len(code)) {
			return fmt.Errorf("instruction at offset %d has a truncated operand", pc)
		}
		return nil
	}
	if _, n := varint.ConsumeForward[uint32](operand); n <= 0 {
		return fmt.Errorf("instruction at offset %d has a malformed operand", pc)
	}
	return nil
}

// String returns a textual representation of the disassembly. It is
// used by the golden files of the compiler's tests.
func (d *Disassembly) String() string {
	var sb strings.Builder
	d.writeTo(&sb)
	return sb.String()
}

func (d *Disassembly) writeTo(sb *strings.Builder) {
	sb.WriteString(d.Signature)
	sb.WriteByte('\n')
	for _, detail := range d.Details {
		fmt.Fprintf(sb, "\t%s\n", detail)
	}

	// Determine which program counters are jump targets, so that
	// they can be labeled.
	targets := map[uint32]struct{}{}
	for _, instruction := range d.Instructions {
		if op := instruction.Opcode; op == JMP || op == CJMP || op == ITERJMP {
			targets[instruction.Operand] = struct{}{}
		}
	}

	for _, instruction := range d.Instructions {
		if _, ok := targets[instruction.PC]; ok {
			fmt.Fprintf(sb, "%d:\n", instruction.PC)
		}
		fmt.Fprintf(sb, "\t%s", instruction.Opcode)
		if instruction.Opcode >= OpcodeArgMin {
			fmt.Fprintf(sb, " %d", instruction.Operand)
			if instruction.OperandComment != "" {
				fmt.Fprintf(sb, " (%s)", instruction.OperandComment)
			}
		}
		if instruction.Position.IsValid() {
			fmt.Fprintf(sb, "\t@%s", instruction.Position)
		}
		sb.WriteByte('\n')
	}

	for _, nested := range d.Functions {
		sb.WriteByte('\n')
		nested.writeTo(sb)
	}
}

// operandTableSize returns the size of the table that is indexed by the
// operand of an instruction, or -1 if the operand is not an index.
func (fn *Funcode) operandTableSize(op Opcode) int {
	switch op {
	case STR:
		return len(fn.StringConstants)
	case INT:
		return len(fn.IntConstants)
	case FLOAT:
		return len(fn.FloatConstants)
	case MAKEFUNC:
		return len(fn.Functions)
	case LOCAL, LOCALCELL, SETLOCAL, SETLOCALCELL:
		return len(fn.Locals)
	case FREE, FREECELL:
		return len(fn.FreeVars)
	case ATTR, SETFIELD, GLOBAL, SETGLOBAL, PREDECLARED, UNIVERSAL:
		return len(fn.Names)
	case IMPORT:
		return len(fn.Imports)
	default:
		return -1
	}
}

func (fn *Funcode) operandComment(op Opcode, arg uint32) (string, error) {
	if size := fn.operandTableSize(op); size >= 0 && uint64(arg) >= uint64(size) {
		return "", fmt.Errorf("operand %d exceeds the table of size %d", arg, size)
	}
	switch op {
	case STR:
		return syntax.Quote(fn.StringConstants[arg]), nil
	case INT:
		return fn.IntConstants[arg].String(), nil
	case FLOAT:
		return fmt.Sprint(fn.FloatConstants[arg]), nil
	case MAKEFUNC:
		return fn.Functions[arg].Name, nil
	case LOCAL, LOCALCELL, SETLOCAL, SETLOCALCELL:
		return fn.Locals[arg], nil
	case FREE, FREECELL:
		return fn.FreeVars[arg], nil
	case ATTR, SETFIELD, GLOBAL, SETGLOBAL, PREDECLARED, UNIVERSAL:
		return fn.Names[arg], nil
	case IMPORT:
		return fmt.Sprintf("%s %s", syntax.Quote(fn.Imports[arg].Module), fn.Imports[arg].Name), nil
	case CALL, CALLARGS, CALLKW, CALLARGKW:
		positional, named := SplitCallOperand(arg)
		return fmt.Sprintf("%d positional, %d named", positional, named), nil
	}
	return "", nil
}
