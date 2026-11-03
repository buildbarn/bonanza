package eval

import (
	"fmt"
	"iter"

	"bonanza.build/pkg/starlark/compile"
	"bonanza.build/pkg/starlark/syntax"
)

// stepsBetweenCancellationChecks is the number of instructions that
// are executed between checks for cancellation of the context.
const stepsBetweenCancellationChecks = 1 << 10

// frame is an entry of the call stack of a thread. Frames are either
// created for Starlark functions, in which case all fields are set, or
// for builtins, in which case only callable is set.
type frame struct {
	callable Callable

	funcode *compile.Funcode
	module  Module
	// Program counter of the instruction being executed.
	pc        uint32
	stack     []Value
	locals    []Value
	freeVars  []*Cell
	iterators []iterator
}

func (fr *frame) callFrame() CallFrame {
	if fr.funcode == nil {
		return CallFrame{Name: fr.callable.Name()}
	}
	return CallFrame{
		Name:     fr.funcode.Name,
		Filename: fr.module.Filename(),
		Pos:      fr.funcode.Position(fr.pc),
	}
}

// stopIterators stops all iterators of a frame, so that values that
// are being iterated over become mutable again.
func (fr *frame) stopIterators() {
	for _, it := range fr.iterators {
		it.stop()
	}
	fr.iterators = nil
}

// iterator is used to implement for loops and comprehensions.
type iterator interface {
	next() (Value, bool)
	stop()
	err() error
}

// listIterator iterates over the elements of an in-memory list.
type listIterator struct {
	list *memList
	i    int
}

func (it *listIterator) next() (Value, bool) {
	if it.i >= len(it.list.elements) {
		return nil, false
	}
	v := it.list.elements[it.i]
	it.i++
	return v, true
}

func (it *listIterator) stop() {
	if it.list != nil {
		it.list.iterators--
		it.list = nil
	}
}

func (*listIterator) err() error { return nil }

// tupleIterator iterates over the elements of an in-memory tuple.
type tupleIterator struct {
	tuple memTuple
	i     int
}

func (it *tupleIterator) next() (Value, bool) {
	if it.i >= len(it.tuple) {
		return nil, false
	}
	v := it.tuple[it.i]
	it.i++
	return v, true
}

func (*tupleIterator) stop()      {}
func (*tupleIterator) err() error { return nil }

// seqIterator iterates over an arbitrary iterable value.
type seqIterator struct {
	nextFunc func() (Value, bool)
	stopFunc func()
	errIter  error
}

func (it *seqIterator) next() (Value, bool) { return it.nextFunc() }
func (it *seqIterator) stop()               { it.stopFunc() }
func (it *seqIterator) err() error          { return it.errIter }

func newIterator(t *Thread, v Value) (iterator, error) {
	switch vv := v.(type) {
	case *memList:
		vv.iterators++
		return &listIterator{list: vv}, nil
	case memTuple:
		return &tupleIterator{tuple: vv}, nil
	case Iterable:
		it := &seqIterator{}
		it.nextFunc, it.stopFunc = iter.Pull(vv.Iterate(t, &it.errIter))
		return it, nil
	}
	return nil, fmt.Errorf("type '%s' is not iterable", v.Type())
}

// callFunction calls a Starlark function, and runs the virtual machine
// until it returns.
func (t *Thread) callFunction(fn Function, args Args) (Value, error) {
	base := len(t.frames)
	if err := t.pushFunctionFrame(fn, args); err != nil {
		return nil, t.wrapError(err)
	}
	return t.run(base)
}

// pushFunctionFrame pushes a frame for a call to a Starlark function
// onto the call stack.
func (t *Thread) pushFunctionFrame(fn Function, args Args) error {
	funcode, err := fn.Funcode(t)
	if err != nil {
		return err
	}
	module, err := fn.Module(t)
	if err != nil {
		return err
	}
	// Functions are identified by the position at which they are
	// declared, as opposed to comparing the addresses of their
	// code. Code that is backed by storage may be decoded multiple
	// times, and files containing identical functions may share
	// the same code.
	filename := module.Filename()
	for _, fr := range t.frames {
		if fr.funcode != nil && fr.funcode.Pos == funcode.Pos && fr.module.Filename() == filename {
			return fmt.Errorf("function %s called recursively", funcode.Name)
		}
	}
	defaults, err := fn.Defaults(t)
	if err != nil {
		return err
	}
	freeVars, err := fn.FreeVars(t)
	if err != nil {
		return err
	}
	locals, err := bindArgs(t, funcode.Name, funcode, defaults, args)
	if err != nil {
		return err
	}
	t.frames = append(t.frames, &frame{
		callable: fn,
		funcode:  funcode,
		module:   module,
		stack:    make([]Value, 0, funcode.MaxStack),
		locals:   locals,
		freeVars: freeVars,
	})
	return nil
}

// run executes instructions until the frame at index base returns.
// Calls to Starlark functions push frames onto the call stack, instead
// of recursing in Go.
func (t *Thread) run(base int) (Value, error) {
	v, err := t.runFrame(base)
	if err == nil {
		return v, nil
	}

	// Capture the call stack, and unwind all frames that were
	// pushed by this invocation.
	err = t.wrapError(err)
	for len(t.frames) > base {
		t.frames[len(t.frames)-1].stopIterators()
		t.frames = t.frames[:len(t.frames)-1]
	}
	return nil, err
}

// runFrame executes instructions of the topmost frame, switching
// frames as functions are called and return. It returns when the frame
// at index base returns, or when an error occurs. In the latter case,
// the failing frame is left on the call stack, so that the call stack
// can be captured.
func (t *Thread) runFrame(base int) (Value, error) {
	fr := t.frames[len(t.frames)-1]
	code := fr.funcode.Code
	var pc uint32

	push := func(v Value) {
		fr.stack = append(fr.stack, v)
	}
	pop := func() Value {
		v := fr.stack[len(fr.stack)-1]
		fr.stack = fr.stack[:len(fr.stack)-1]
		return v
	}

	for {
		t.steps++
		if t.MaximumSteps > 0 && t.steps > t.MaximumSteps {
			return nil, fmt.Errorf("Starlark computation cancelled: too many steps")
		}
		if t.steps%stepsBetweenCancellationChecks == 0 {
			if err := t.ctx.Err(); err != nil {
				return nil, fmt.Errorf("Starlark computation cancelled: %w", err)
			}
		}

		fr.pc = pc
		op, arg, next := compile.DecodeInstruction(code, pc)
		pc = next

		switch op {
		case compile.DUP:
			push(fr.stack[len(fr.stack)-1])
		case compile.DUP2:
			n := len(fr.stack)
			push(fr.stack[n-2])
			push(fr.stack[n-1])
		case compile.POP:
			pop()
		case compile.EXCH:
			n := len(fr.stack)
			fr.stack[n-2], fr.stack[n-1] = fr.stack[n-1], fr.stack[n-2]

		case compile.LT, compile.GT, compile.GE, compile.LE, compile.EQL, compile.NEQ:
			y := pop()
			x := pop()
			result, err := Compare(t, comparisonTokens[op], x, y)
			if err != nil {
				return nil, err
			}
			push(Bool(result))

		case compile.PLUS, compile.MINUS, compile.STAR, compile.SLASH, compile.SLASHSLASH,
			compile.PERCENT, compile.AMP, compile.PIPE, compile.CIRCUMFLEX, compile.LTLT, compile.GTGT:
			y := pop()
			x := pop()
			z, err := Binary(t, syntax.PLUS+syntax.Token(op-compile.PLUS), x, y)
			if err != nil {
				return nil, err
			}
			push(z)

		case compile.IN:
			y := pop()
			x := pop()
			found, err := contains(t, y, x)
			if err != nil {
				return nil, err
			}
			push(Bool(found))

		case compile.UPLUS, compile.UMINUS, compile.TILDE:
			tok := map[compile.Opcode]syntax.Token{compile.UPLUS: syntax.PLUS, compile.UMINUS: syntax.MINUS, compile.TILDE: syntax.TILDE}[op]
			z, err := Unary(t, tok, pop())
			if err != nil {
				return nil, err
			}
			push(z)

		case compile.NONE:
			push(None)
		case compile.TRUE:
			push(True)
		case compile.FALSE:
			push(False)
		case compile.MANDATORY:
			push(mandatory{})

		case compile.ITERPUSH:
			it, err := newIterator(t, pop())
			if err != nil {
				return nil, err
			}
			fr.iterators = append(fr.iterators, it)
		case compile.ITERPOP:
			it := fr.iterators[len(fr.iterators)-1]
			fr.iterators = fr.iterators[:len(fr.iterators)-1]
			it.stop()
			if err := it.err(); err != nil {
				return nil, err
			}
		case compile.ITERJMP:
			it := fr.iterators[len(fr.iterators)-1]
			if v, ok := it.next(); ok {
				push(v)
			} else {
				if err := it.err(); err != nil {
					return nil, err
				}
				pc = arg
			}

		case compile.NOT:
			truth, err := pop().Truth(t)
			if err != nil {
				return nil, err
			}
			push(Bool(!truth))

		case compile.RETURN:
			result := pop()
			fr.stopIterators()
			t.frames = t.frames[:len(t.frames)-1]
			if len(t.frames) == base {
				return result, nil
			}
			fr = t.frames[len(t.frames)-1]
			code = fr.funcode.Code
			pc = fr.pc
			_, _, pc = compile.DecodeInstruction(code, pc)
			push(result)

		case compile.SETINDEX:
			z := pop()
			y := pop()
			x := pop()
			if err := setIndex(t, x, y, z); err != nil {
				return nil, err
			}
		case compile.INDEX:
			y := pop()
			x := pop()
			z, err := getIndex(t, x, y)
			if err != nil {
				return nil, err
			}
			push(z)

		case compile.SETDICT, compile.SETDICTUNIQ:
			v := pop()
			k := pop()
			d := pop().(Dict)
			if op == compile.SETDICTUNIQ {
				if _, found, err := d.Get(t, k); err != nil {
					return nil, err
				} else if found {
					key, err := Repr(t, k)
					if err != nil {
						return nil, err
					}
					return nil, fmt.Errorf("dictionary expression has duplicate key: %s", key)
				}
			}
			if err := d.SetKey(t, k, v); err != nil {
				return nil, err
			}

		case compile.APPEND:
			v := pop()
			if err := pop().(List).Append(t, v); err != nil {
				return nil, err
			}

		case compile.SLICE:
			step := pop()
			hi := pop()
			lo := pop()
			x := pop()
			z, err := slice(t, x, lo, hi, step)
			if err != nil {
				return nil, err
			}
			push(z)

		case compile.INPLACEADD:
			y := pop()
			x := pop()
			z, err := inplaceAdd(t, x, y)
			if err != nil {
				return nil, err
			}
			push(z)

		case compile.INPLACEPIPE:
			y := pop()
			x := pop()
			z, err := inplacePipe(t, x, y)
			if err != nil {
				return nil, err
			}
			push(z)

		case compile.MAKEDICT:
			push(NewDict())

		case compile.JMP:
			pc = arg
		case compile.CJMP:
			truth, err := pop().Truth(t)
			if err != nil {
				return nil, err
			}
			if truth {
				pc = arg
			}

		case compile.STR:
			push(String(fr.funcode.StringConstants[arg]))

		case compile.INT:
			push(MakeBigInt(fr.funcode.IntConstants[arg]))

		case compile.FLOAT:
			push(Float(fr.funcode.FloatConstants[arg]))

		case compile.MAKETUPLE:
			n := len(fr.stack) - int(arg)
			tuple := NewTuple(append([]Value(nil), fr.stack[n:]...))
			fr.stack = append(fr.stack[:n], tuple)

		case compile.MAKELIST:
			n := len(fr.stack) - int(arg)
			list := NewList(append([]Value(nil), fr.stack[n:]...))
			fr.stack = append(fr.stack[:n], list)

		case compile.MAKEFUNC:
			funcode := fr.funcode.Functions[arg]
			tuple := pop().(memTuple)
			numDefaults := len(tuple) - len(funcode.FreeVars)
			freeVars := make([]*Cell, 0, len(funcode.FreeVars))
			for _, v := range tuple[numDefaults:] {
				freeVars = append(freeVars, v.(*Cell))
			}
			push(&memFunction{
				funcode:       funcode,
				module:        fr.module,
				defaults:      tuple[:numDefaults],
				freeVars:      freeVars,
				parent:        fr.callable.(Function),
				parentFuncode: fr.funcode,
				indexInParent: int(arg),
			})

		case compile.SETLOCAL:
			fr.locals[arg] = pop()
		case compile.SETLOCALCELL:
			fr.locals[arg].(*Cell).v = pop()
		case compile.SETGLOBAL:
			module, ok := fr.module.(MutableModule)
			if !ok {
				return nil, fmt.Errorf("cannot assign global variable %s", fr.funcode.Names[arg])
			}
			if err := module.SetGlobal(t, fr.funcode.Names[arg], pop()); err != nil {
				return nil, err
			}

		case compile.LOCAL:
			v := fr.locals[arg]
			if v == nil {
				return nil, fmt.Errorf("local variable %s referenced before assignment", fr.funcode.Locals[arg])
			}
			push(v)
		case compile.LOCALCELL:
			v := fr.locals[arg].(*Cell).v
			if v == nil {
				return nil, fmt.Errorf("local variable %s referenced before assignment", fr.funcode.Locals[arg])
			}
			push(v)
		case compile.FREE:
			push(fr.freeVars[arg])
		case compile.FREECELL:
			v := fr.freeVars[arg].v
			if v == nil {
				return nil, fmt.Errorf("local variable %s referenced before assignment", fr.funcode.FreeVars[arg])
			}
			push(v)
		case compile.GLOBAL:
			v, err := fr.module.Global(t, fr.funcode.Names[arg])
			if err != nil {
				return nil, err
			}
			push(v)
		case compile.IMPORT:
			v, err := fr.module.Import(t, fr.funcode.Imports[arg])
			if err != nil {
				return nil, err
			}
			push(v)
		case compile.PREDECLARED:
			v, err := fr.module.Predeclared(t, fr.funcode.Names[arg])
			if err != nil {
				return nil, err
			}
			push(v)
		case compile.UNIVERSAL:
			name := fr.funcode.Names[arg]
			v, ok := t.universe[name]
			if !ok {
				return nil, fmt.Errorf("universal name %s is not available", name)
			}
			push(v)

		case compile.ATTR:
			v, err := getAttr(t, pop(), fr.funcode.Names[arg])
			if err != nil {
				return nil, err
			}
			push(v)
		case compile.SETFIELD:
			y := pop()
			x := pop()
			if err := setField(t, x, fr.funcode.Names[arg], y); err != nil {
				return nil, err
			}

		case compile.UNPACK:
			elements, err := Elements(t, pop())
			if err != nil {
				return nil, err
			}
			if len(elements) != int(arg) {
				if len(elements) > int(arg) {
					return nil, fmt.Errorf("too many values to unpack (got %d, want %d)", len(elements), arg)
				}
				return nil, fmt.Errorf("too few values to unpack (got %d, want %d)", len(elements), arg)
			}
			for i := len(elements) - 1; i >= 0; i-- {
				push(elements[i])
			}

		case compile.CALL, compile.CALLARGS, compile.CALLKW, compile.CALLARGKW:
			args, fn, err := popCallArgs(t, fr, op, arg)
			if err != nil {
				return nil, err
			}
			if f, ok := fn.(Function); ok {
				// Calls to Starlark functions don't recurse in
				// Go. The program counter of the current frame
				// still points to the call instruction, so that
				// it can be resumed after the function returns.
				if err := t.pushFunctionFrame(f, args); err != nil {
					return nil, err
				}
				fr = t.frames[len(t.frames)-1]
				code = fr.funcode.Code
				pc = 0
				continue
			}
			result, err := Call(t, fn, args)
			if err != nil {
				return nil, err
			}
			push(result)

		default:
			return nil, fmt.Errorf("illegal opcode %s", op)
		}
	}
}

var comparisonTokens = map[compile.Opcode]syntax.Token{
	compile.LT:  syntax.LT,
	compile.GT:  syntax.GT,
	compile.GE:  syntax.GE,
	compile.LE:  syntax.LE,
	compile.EQL: syntax.EQL,
	compile.NEQ: syntax.NEQ,
}

// popCallArgs pops the function and its arguments off the stack of a
// frame.
func popCallArgs(t *Thread, fr *frame, op compile.Opcode, arg uint32) (Args, Value, error) {
	var kwargs, varargs Value
	if op == compile.CALLKW || op == compile.CALLARGKW {
		kwargs = fr.stack[len(fr.stack)-1]
		fr.stack = fr.stack[:len(fr.stack)-1]
	}
	if op == compile.CALLARGS || op == compile.CALLARGKW {
		varargs = fr.stack[len(fr.stack)-1]
		fr.stack = fr.stack[:len(fr.stack)-1]
	}

	numPositional, numNamed := compile.SplitCallOperand(arg)
	n := len(fr.stack) - numPositional - 2*numNamed
	fn := fr.stack[n-1]
	var args Args
	if numPositional > 0 {
		args.Positional = append([]Value(nil), fr.stack[n:n+numPositional]...)
	}
	for i := n + numPositional; i < len(fr.stack); i += 2 {
		args.Named = append(args.Named, NamedArg{
			Name:  string(fr.stack[i].(String)),
			Value: fr.stack[i+1],
		})
	}
	fr.stack = fr.stack[:n-1]

	if varargs != nil {
		elements, err := Elements(t, varargs)
		if err != nil {
			return Args{}, nil, fmt.Errorf("argument after * must be iterable, not %s", varargs.Type())
		}
		args.Positional = append(args.Positional, elements...)
	}
	if kwargs != nil {
		d, ok := kwargs.(Dict)
		if !ok {
			return Args{}, nil, fmt.Errorf("argument after ** must be a dict, not %s", kwargs.Type())
		}
		var errIter error
		for k, v := range d.Items(t, &errIter) {
			name, ok := k.(String)
			if !ok {
				return Args{}, nil, fmt.Errorf("keywords must be strings, not %s", k.Type())
			}
			args.Named = append(args.Named, NamedArg{Name: string(name), Value: v})
		}
		if errIter != nil {
			return Args{}, nil, errIter
		}
	}
	return args, fn, nil
}

// inplaceAdd implements x += y. Lists are extended in place.
func inplaceAdd(t *Thread, x, y Value) (Value, error) {
	if xl, ok := x.(List); ok {
		if _, ok := y.(Iterable); ok {
			elements, err := Elements(t, y)
			if err != nil {
				return nil, err
			}
			for _, element := range elements {
				if err := xl.Append(t, element); err != nil {
					return nil, err
				}
			}
			return xl, nil
		}
	}
	return Binary(t, syntax.PLUS, x, y)
}

// inplacePipe implements x |= y. Dicts are updated in place.
func inplacePipe(t *Thread, x, y Value) (Value, error) {
	if xd, ok := x.(Dict); ok {
		if yd, ok := y.(Dict); ok {
			var errIter error
			for k, v := range yd.Items(t, &errIter) {
				if err := xd.SetKey(t, k, v); err != nil {
					return nil, err
				}
			}
			return xd, errIter
		}
	}
	return Binary(t, syntax.PIPE, x, y)
}
