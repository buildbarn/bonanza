package eval

import (
	"fmt"
	"slices"
	"strings"

	"bonanza.build/pkg/starlark/compile"
)

// NamedArg is a keyword argument of a function call.
type NamedArg struct {
	Name  string
	Value Value
}

// Args contains the arguments of a function call.
type Args struct {
	Positional []Value
	Named      []NamedArg
}

// Callable is implemented by values that can be called.
type Callable interface {
	Value
	Name() string
	// CallInternal calls the value. It should not be called
	// directly. Use Call() instead, which records the call in the
	// call stack of the thread.
	CallInternal(t *Thread, args Args) (Value, error)
}

// Function is implemented by Starlark functions, whose code is executed
// by the virtual machine. Calls to functions don't recurse in Go.
// Instead, the virtual machine pushes a frame onto the call stack of
// the thread.
//
// Functions may either reside in memory, or be backed by storage. The
// latter may load their code and the values of their default
// parameters and free variables lazily.
type Function interface {
	Callable
	Funcode(t *Thread) (*compile.Funcode, error)
	// Module against which globals, imports and predeclared names
	// referenced by the function are resolved.
	Module(t *Thread) (Module, error)
	// Defaults returns the default values of the parameters, as
	// described by compile.Funcode.
	Defaults(t *Thread) ([]Value, error)
	FreeVars(t *Thread) ([]*Cell, error)
	// NamePath returns the path of the function within its file.
	// The first element refers to the function at the top level of
	// the file, while subsequent elements refer to functions nested
	// inside it.
	NamePath() []compile.FunctionName
}

// memFunction is a function that is created by executing a def
// statement or lambda expression.
type memFunction struct {
	funcode  *compile.Funcode
	module   Module
	defaults []Value
	freeVars []*Cell

	// The function that executed the def statement or lambda
	// expression and the index of this function in the code of the
	// parent. The parent is nil for the top level of a file.
	parent        Function
	parentFuncode *compile.Funcode
	indexInParent int
}

var _ Function = (*memFunction)(nil)

func (*memFunction) Type() string                { return "function" }
func (*memFunction) Truth(*Thread) (bool, error) { return true, nil }
func (f *memFunction) Name() string              { return f.funcode.Name }

func (f *memFunction) Repr(_ *Thread, p *Printer) error {
	p.WriteString("<function " + f.funcode.Name + ">")
	return nil
}

func (f *memFunction) CallInternal(t *Thread, args Args) (Value, error) {
	return t.callFunction(f, args)
}

func (f *memFunction) Funcode(*Thread) (*compile.Funcode, error) { return f.funcode, nil }
func (f *memFunction) Module(*Thread) (Module, error)            { return f.module, nil }
func (f *memFunction) Defaults(*Thread) ([]Value, error)         { return f.defaults, nil }
func (f *memFunction) FreeVars(*Thread) ([]*Cell, error)         { return f.freeVars, nil }

// NamePath returns the path of the function within its file. It is
// computed on demand, as it's only needed when encoding functions.
func (f *memFunction) NamePath() []compile.FunctionName {
	if f.parent == nil {
		return nil
	}
	return append(slices.Clone(f.parent.NamePath()), f.parentFuncode.GetNestedFunctionName(f.indexInParent))
}

// Builtin is a function implemented in Go.
type Builtin struct {
	name     string
	fn       func(t *Thread, b *Builtin, args Args) (Value, error)
	receiver Value
}

var _ Callable = (*Builtin)(nil)

// NewBuiltin creates a function that is implemented in Go.
func NewBuiltin(name string, fn func(t *Thread, b *Builtin, args Args) (Value, error)) *Builtin {
	return &Builtin{name: name, fn: fn}
}

// BindReceiver returns a copy of the builtin that is bound to a
// receiver, which is used to implement methods such as list.append.
func (b *Builtin) BindReceiver(receiver Value) *Builtin {
	return &Builtin{name: b.name, fn: b.fn, receiver: receiver}
}

// Receiver returns the value to which a method is bound.
func (b *Builtin) Receiver() Value { return b.receiver }

// Name returns the name of the builtin.
func (b *Builtin) Name() string { return b.name }

// Type returns "builtin_function_or_method".
func (*Builtin) Type() string { return "builtin_function_or_method" }

// Truth returns true.
func (*Builtin) Truth(*Thread) (bool, error) { return true, nil }

// Repr appends the representation of the builtin to the printer.
func (b *Builtin) Repr(_ *Thread, p *Printer) error {
	if b.receiver != nil {
		p.WriteString(fmt.Sprintf("<built-in method %s of %s value>", b.name, b.receiver.Type()))
	} else {
		p.WriteString("<built-in function " + b.name + ">")
	}
	return nil
}

// CallInternal calls the Go function.
func (b *Builtin) CallInternal(t *Thread, args Args) (Value, error) {
	return b.fn(t, b, args)
}

// bindArgs assigns the arguments of a call to the local variables of a
// function.
func bindArgs(t *Thread, name string, fn *compile.Funcode, defaults []Value, args Args) ([]Value, error) {
	locals := make([]Value, len(fn.Locals))

	numParams := fn.NumParams
	if fn.HasArgs {
		numParams--
	}
	if fn.HasKwargs {
		numParams--
	}
	numPositionalParams := numParams - fn.NumKeywordOnlyParams

	// Positional arguments.
	positional := args.Positional
	if len(positional) > numPositionalParams {
		if !fn.HasArgs {
			return nil, fmt.Errorf("%s() accepts no more than %d positional argument%s but got %d", name, numPositionalParams, plural(numPositionalParams), len(positional))
		}
		locals[numParams] = NewTuple(append([]Value(nil), positional[numPositionalParams:]...))
		positional = positional[:numPositionalParams]
	} else if fn.HasArgs {
		locals[numParams] = NewTuple(nil)
	}
	copy(locals, positional)

	// Named arguments.
	var kwargs Dict
	if fn.HasKwargs {
		kwargs = NewDict()
		index := numParams
		if fn.HasArgs {
			index++
		}
		locals[index] = kwargs
	}
	for _, arg := range args.Named {
		i := 0
		for i < numParams && fn.Locals[i] != arg.Name {
			i++
		}
		if i == numParams {
			if kwargs == nil {
				suggestion := ""
				if nearest := nearestName(arg.Name, fn.Locals[:numParams]); nearest != "" {
					suggestion = fmt.Sprintf(" (did you mean '%s'?)", nearest)
				}
				return nil, fmt.Errorf("%s() got unexpected keyword argument '%s'%s", name, arg.Name, suggestion)
			}
			if _, found, err := kwargs.Get(t, String(arg.Name)); err != nil {
				return nil, err
			} else if found {
				return nil, fmt.Errorf("%s() got multiple values for keyword argument '%s'", name, arg.Name)
			}
			if err := kwargs.SetKey(t, String(arg.Name), arg.Value); err != nil {
				return nil, err
			}
			continue
		}
		if locals[i] != nil {
			return nil, fmt.Errorf("%s() got multiple values for parameter '%s'", name, arg.Name)
		}
		locals[i] = arg.Value
	}

	// Default values. They apply to the last parameters that are
	// not *args or **kwargs.
	firstDefault := numParams - len(defaults)
	var missingPositional, missingKeywordOnly []string
	for i := 0; i < numParams; i++ {
		if locals[i] != nil {
			continue
		}
		if i >= firstDefault {
			if d := defaults[i-firstDefault]; !isMandatory(d) {
				locals[i] = d
				continue
			}
		}
		if i < numPositionalParams {
			missingPositional = append(missingPositional, fn.Locals[i])
		} else {
			missingKeywordOnly = append(missingKeywordOnly, fn.Locals[i])
		}
	}
	if len(missingPositional) > 0 {
		return nil, fmt.Errorf("%s() missing %d required positional argument%s: %s", name, len(missingPositional), plural(len(missingPositional)), strings.Join(missingPositional, ", "))
	}
	if len(missingKeywordOnly) > 0 {
		return nil, fmt.Errorf("%s() missing %d required keyword-only argument%s: %s", name, len(missingKeywordOnly), plural(len(missingKeywordOnly)), strings.Join(missingKeywordOnly, ", "))
	}

	// Parameters that are captured by nested functions are stored in
	// cells.
	for _, i := range fn.Cells {
		locals[i] = &Cell{v: locals[i]}
	}
	return locals, nil
}

func isMandatory(v Value) bool {
	_, ok := v.(mandatory)
	return ok
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// nearestName returns the candidate that is most similar to the
// provided name, for use in error messages.
func nearestName(name string, candidates []string) string {
	maximumDistance := max(1, len(name)/3)
	best, bestDistance := "", maximumDistance+1
	for _, candidate := range candidates {
		if d := levenshtein(strings.ToLower(name), strings.ToLower(candidate)); d < bestDistance {
			best, bestDistance = candidate, d
		}
	}
	return best
}

func levenshtein(a, b string) int {
	previous := make([]int, len(b)+1)
	current := make([]int, len(b)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(a); i++ {
		current[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			current[j] = min(previous[j]+1, current[j-1]+1, previous[j-1]+cost)
		}
		previous, current = current, previous
	}
	return previous[len(b)]
}
