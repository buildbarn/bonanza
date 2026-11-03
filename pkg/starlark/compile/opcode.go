package compile

import (
	"fmt"
)

// Opcode of an instruction of the bytecode.
//
// The comments below contain "stack pictures", describing the state of
// the operand stack before and after execution of the instruction.
// OP<index> indicates an immediate operand that is an index into one
// of the tables of the function: Locals, FreeVars, StringConstants,
// IntConstants, FloatConstants, Names, Imports or Functions.
type Opcode uint8

// List of opcodes.
const (
	// Stack operations.
	DUP  Opcode = iota //   x DUP x x
	DUP2               // x y DUP2 x y x y
	POP                //   x POP -
	EXCH               // x y EXCH y x

	// Binary comparisons. The order must match syntax.Token.
	LT
	GT
	GE
	LE
	EQL
	NEQ

	// Binary arithmetic. The order must match syntax.Token.
	PLUS
	MINUS
	STAR
	SLASH
	SLASHSLASH
	PERCENT
	AMP
	PIPE
	CIRCUMFLEX
	LTLT
	GTGT

	IN

	// Unary operators.
	UPLUS  // x UPLUS x
	UMINUS // x UMINUS -x
	TILDE  // x TILDE ~x

	NONE      // - NONE None
	TRUE      // - TRUE True
	FALSE     // - FALSE False
	MANDATORY // - MANDATORY mandatory   (sentinel for required keyword-only parameters)

	ITERPUSH    //       iterable ITERPUSH    -       (pushes the iterator stack)
	ITERPOP     //              - ITERPOP     -       (pops the iterator stack)
	NOT         //          value NOT         bool
	RETURN      //          value RETURN      -
	SETINDEX    //        a i new SETINDEX    -
	INDEX       //            a i INDEX       elem
	SETDICT     // dict key value SETDICT     -
	SETDICTUNIQ // dict key value SETDICTUNIQ -       (fails if the key is already present)
	APPEND      //      list elem APPEND      -
	SLICE       //   x lo hi step SLICE       slice
	INPLACEADD  //            x y INPLACEADD  z       (z is x+y or x.extend(y))
	INPLACEPIPE //            x y INPLACEPIPE z       (z is x|y, updating x in place if possible)
	MAKEDICT    //              - MAKEDICT    dict

	// Opcodes with an operand must go below this line.

	// Control flow.
	JMP     //    - JMP<addr>     -
	CJMP    // cond CJMP<addr>    -
	ITERJMP //    - ITERJMP<addr> elem  (and fall through, acting on the topmost iterator)
	//            - ITERJMP<addr> -     (and jump, if the iterator is exhausted)

	STR          //                 - STR<string>         string
	INT          //                 - INT<int>            int
	FLOAT        //                 - FLOAT<float>        float
	MAKETUPLE    //         x1 ... xn MAKETUPLE<n>        tuple
	MAKELIST     //         x1 ... xn MAKELIST<n>         list
	MAKEFUNC     // defaults+freevars MAKEFUNC<function>  fn
	SETLOCAL     //             value SETLOCAL<local>     -
	SETLOCALCELL //             value SETLOCALCELL<local> -      (sets the contents of a cell)
	SETGLOBAL    //             value SETGLOBAL<name>     -
	LOCAL        //                 - LOCAL<local>        value
	LOCALCELL    //                 - LOCALCELL<local>    value  (contents of a cell)
	FREE         //                 - FREE<freevar>       cell
	FREECELL     //                 - FREECELL<freevar>   value  (contents of a cell)
	GLOBAL       //                 - GLOBAL<name>        value
	IMPORT       //                 - IMPORT<import>      value  (loads the symbol if needed)
	PREDECLARED  //                 - PREDECLARED<name>   value
	UNIVERSAL    //                 - UNIVERSAL<name>     value
	ATTR         //                 x ATTR<name>          y      (y = x.name)
	SETFIELD     //               x y SETFIELD<name>      -      (x.name = y)
	UNPACK       //          iterable UNPACK<n>           vn ... v1

	// The operand of the call instructions contains the number of
	// positional arguments and the number of named arguments (pairs
	// of names and values). See MakeCallOperand().
	CALL      // fn positional named                CALL<n>      result
	CALLARGS  // fn positional named *args          CALLARGS<n>  result
	CALLKW    // fn positional named       **kwargs CALLKW<n>    result
	CALLARGKW // fn positional named *args **kwargs CALLARGKW<n> result

	// OpcodeArgMin is the first opcode that has an operand.
	OpcodeArgMin = JMP
	// OpcodeMax is the last valid opcode.
	OpcodeMax = CALLARGKW
)

var opcodeNames = [...]string{
	DUP:          "dup",
	DUP2:         "dup2",
	POP:          "pop",
	EXCH:         "exch",
	LT:           "lt",
	GT:           "gt",
	GE:           "ge",
	LE:           "le",
	EQL:          "eql",
	NEQ:          "neq",
	PLUS:         "plus",
	MINUS:        "minus",
	STAR:         "star",
	SLASH:        "slash",
	SLASHSLASH:   "slashslash",
	PERCENT:      "percent",
	AMP:          "amp",
	PIPE:         "pipe",
	CIRCUMFLEX:   "circumflex",
	LTLT:         "ltlt",
	GTGT:         "gtgt",
	IN:           "in",
	UPLUS:        "uplus",
	UMINUS:       "uminus",
	TILDE:        "tilde",
	NONE:         "none",
	TRUE:         "true",
	FALSE:        "false",
	MANDATORY:    "mandatory",
	ITERPUSH:     "iterpush",
	ITERPOP:      "iterpop",
	NOT:          "not",
	RETURN:       "return",
	SETINDEX:     "setindex",
	INDEX:        "index",
	SETDICT:      "setdict",
	SETDICTUNIQ:  "setdictuniq",
	APPEND:       "append",
	SLICE:        "slice",
	INPLACEADD:   "inplaceadd",
	INPLACEPIPE:  "inplacepipe",
	MAKEDICT:     "makedict",
	JMP:          "jmp",
	CJMP:         "cjmp",
	ITERJMP:      "iterjmp",
	STR:          "str",
	INT:          "int",
	FLOAT:        "float",
	MAKETUPLE:    "maketuple",
	MAKELIST:     "makelist",
	MAKEFUNC:     "makefunc",
	SETLOCAL:     "setlocal",
	SETLOCALCELL: "setlocalcell",
	SETGLOBAL:    "setglobal",
	LOCAL:        "local",
	LOCALCELL:    "localcell",
	FREE:         "free",
	FREECELL:     "freecell",
	GLOBAL:       "global",
	IMPORT:       "import",
	PREDECLARED:  "predeclared",
	UNIVERSAL:    "universal",
	ATTR:         "attr",
	SETFIELD:     "setfield",
	UNPACK:       "unpack",
	CALL:         "call",
	CALLARGS:     "callargs",
	CALLKW:       "callkw",
	CALLARGKW:    "callargkw",
}

func (op Opcode) String() string {
	if op <= OpcodeMax {
		return opcodeNames[op]
	}
	return fmt.Sprintf("illegal opcode (%d)", op)
}

// variableStackEffect is used for instructions whose effect on the
// size of the operand stack depends on their operand.
const variableStackEffect = 0x7f

// stackEffects contains the effect on the size of the operand stack of
// each instruction.
var stackEffects = [...]int8{
	DUP:          +1,
	DUP2:         +2,
	POP:          -1,
	EXCH:         0,
	LT:           -1,
	GT:           -1,
	GE:           -1,
	LE:           -1,
	EQL:          -1,
	NEQ:          -1,
	PLUS:         -1,
	MINUS:        -1,
	STAR:         -1,
	SLASH:        -1,
	SLASHSLASH:   -1,
	PERCENT:      -1,
	AMP:          -1,
	PIPE:         -1,
	CIRCUMFLEX:   -1,
	LTLT:         -1,
	GTGT:         -1,
	IN:           -1,
	UPLUS:        0,
	UMINUS:       0,
	TILDE:        0,
	NONE:         +1,
	TRUE:         +1,
	FALSE:        +1,
	MANDATORY:    +1,
	ITERPUSH:     -1,
	ITERPOP:      0,
	NOT:          0,
	RETURN:       -1,
	SETINDEX:     -3,
	INDEX:        -1,
	SETDICT:      -3,
	SETDICTUNIQ:  -3,
	APPEND:       -2,
	SLICE:        -3,
	INPLACEADD:   -1,
	INPLACEPIPE:  -1,
	MAKEDICT:     +1,
	JMP:          0,
	CJMP:         -1,
	ITERJMP:      variableStackEffect,
	STR:          +1,
	INT:          +1,
	FLOAT:        +1,
	MAKETUPLE:    variableStackEffect,
	MAKELIST:     variableStackEffect,
	MAKEFUNC:     0,
	SETLOCAL:     -1,
	SETLOCALCELL: -1,
	SETGLOBAL:    -1,
	LOCAL:        +1,
	LOCALCELL:    +1,
	FREE:         +1,
	FREECELL:     +1,
	GLOBAL:       +1,
	IMPORT:       +1,
	PREDECLARED:  +1,
	UNIVERSAL:    +1,
	ATTR:         0,
	SETFIELD:     -2,
	UNPACK:       variableStackEffect,
	CALL:         variableStackEffect,
	CALLARGS:     variableStackEffect,
	CALLKW:       variableStackEffect,
	CALLARGKW:    variableStackEffect,
}

// StackEffect returns the effect on the size of the operand stack of
// executing an instruction. For ITERJMP, it returns the effect for the
// case where the iterator is exhausted (zero).
func StackEffect(op Opcode, arg uint32) int {
	se := int(stackEffects[op])
	if se != variableStackEffect {
		return se
	}
	switch op {
	case CALL, CALLARGS, CALLKW, CALLARGKW:
		positional, named := SplitCallOperand(arg)
		se = -2*named - positional
		if op == CALLARGS || op == CALLKW {
			se--
		} else if op == CALLARGKW {
			se -= 2
		}
		return se
	case ITERJMP:
		return 0
	case MAKELIST, MAKETUPLE:
		return 1 - int(arg)
	case UNPACK:
		return int(arg) - 1
	default:
		panic(op)
	}
}

// spreadBits moves the lower 16 bits of a value to the even numbered
// bit positions, and compactBits does the inverse. These operations
// correspond to the PDEP and PEXT instructions on x86 with a mask of
// 0x55555555. Go does not provide access to these instructions, which
// is why these need to be implemented using shifts and masks. See
// https://github.com/golang/go/issues/45455.
func spreadBits(x uint32) uint32 {
	x = (x | x<<8) & 0x00ff00ff
	x = (x | x<<4) & 0x0f0f0f0f
	x = (x | x<<2) & 0x33333333
	return (x | x<<1) & 0x55555555
}

func compactBits(x uint32) uint32 {
	x &= 0x55555555
	x = (x | x>>1) & 0x33333333
	x = (x | x>>2) & 0x0f0f0f0f
	x = (x | x>>4) & 0x00ff00ff
	return (x | x>>8) & 0x0000ffff
}

// MakeCallOperand returns the operand of a call instruction. The bits
// of the number of positional and named arguments are interleaved, so
// that the operand remains small if both of these are small. This
// allows it to be stored in a single byte for most calls.
func MakeCallOperand(positional, named int) uint32 {
	return spreadBits(uint32(positional)) | spreadBits(uint32(named))<<1
}

// SplitCallOperand returns the number of positional and named
// arguments encoded in the operand of a call instruction.
func SplitCallOperand(arg uint32) (positional, named int) {
	return int(compactBits(arg)), int(compactBits(arg >> 1))
}
