package eval

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"bonanza.build/pkg/starlark/compile"
	"bonanza.build/pkg/starlark/syntax"
)

// Module provides the names that the code of a file can refer to,
// other than its local variables.
type Module interface {
	// Filename of the file, used in tracebacks.
	Filename() string
	// Global returns the value of a global variable of the file.
	Global(t *Thread, name string) (Value, error)
	// Import returns the value of a symbol that is loaded from
	// another file. This is done lazily, when the symbol is first
	// used.
	Import(t *Thread, imp compile.Import) (Value, error)
	// Predeclared returns the value of a name that is predeclared
	// by the application, such as glob() or native.
	Predeclared(t *Thread, name string) (Value, error)
}

// MutableModule is implemented by modules whose globals can be
// assigned. This is needed to execute the top level of a file.
type MutableModule interface {
	Module
	SetGlobal(t *Thread, name string, v Value) error
}

// Thread of execution. A thread is not safe for concurrent use.
// Values created by a thread may only be used by that thread, unless
// they are immutable.
type Thread struct {
	ctx      context.Context
	universe map[string]Value

	// Print is called by print(). If nil, output is discarded.
	Print func(t *Thread, msg string) error
	// MaximumSteps limits the number of instructions that may be
	// executed. Zero means no limit.
	MaximumSteps uint64

	steps  uint64
	frames []*frame
	locals map[any]any
}

// NewThread creates a new thread. The universe contains the names that
// are provided by the core of the language, such as None and len.
func NewThread(ctx context.Context, universe map[string]Value) *Thread {
	return &Thread{
		ctx:      ctx,
		universe: universe,
	}
}

// Context returns the context associated with the thread.
func (t *Thread) Context() context.Context {
	return t.ctx
}

// SetLocal sets a thread-local value, which can be used to pass
// information to builtins.
func (t *Thread) SetLocal(key, value any) {
	if t.locals == nil {
		t.locals = map[any]any{}
	}
	t.locals[key] = value
}

// Local returns a thread-local value.
func (t *Thread) Local(key any) any {
	return t.locals[key]
}

// Steps returns the number of instructions executed by the thread.
func (t *Thread) Steps() uint64 {
	return t.steps
}

// CallFrame is an entry of a traceback.
type CallFrame struct {
	// Name of the function.
	Name string
	// Filename and position of the instruction being executed.
	// Unset for builtins.
	Filename string
	Pos      syntax.Position
}

// CallStack returns the call stack of the thread, starting with the
// outermost frame.
func (t *Thread) CallStack() []CallFrame {
	stack := make([]CallFrame, 0, len(t.frames))
	for _, fr := range t.frames {
		stack = append(stack, fr.callFrame())
	}
	return stack
}

// Error is returned when executing Starlark code fails. It
// contains the call stack at the point of failure. The underlying
// error can be obtained using errors.Unwrap(), allowing errors that
// are returned by builtins to be detected using errors.Is() and
// errors.As().
type Error struct {
	CallStack []CallFrame
	cause     error
}

func (e *Error) Error() string {
	return e.cause.Error()
}

func (e *Error) Unwrap() error {
	return e.cause
}

// Backtrace returns a formatted traceback, in the same format as Bazel.
func (e *Error) Backtrace() string {
	var sb strings.Builder
	sb.WriteString("Traceback (most recent call last):\n")
	for _, fr := range e.CallStack {
		if fr.Filename == "" {
			// Builtins are reported in the error message.
			continue
		}
		fmt.Fprintf(&sb, "\tFile %q, line %d, column %d, in %s\n", fr.Filename, fr.Pos.Line, fr.Pos.Column, fr.Name)
	}
	sb.WriteString("Error")
	if n := len(e.CallStack); n > 0 && e.CallStack[n-1].Filename == "" {
		sb.WriteString(" in " + e.CallStack[n-1].Name)
	}
	sb.WriteString(": " + e.cause.Error())
	return sb.String()
}

// Call calls a callable value.
func Call(t *Thread, fn Value, args Args) (Value, error) {
	if f, ok := fn.(Function); ok {
		return t.callFunction(f, args)
	}
	callable, ok := fn.(Callable)
	if !ok {
		return nil, t.wrapError(fmt.Errorf("invalid call of non-function (%s)", fn.Type()))
	}
	t.frames = append(t.frames, &frame{callable: callable})
	v, err := callable.CallInternal(t, args)
	if err != nil {
		err = t.wrapError(err)
	}
	t.frames = t.frames[:len(t.frames)-1]
	return v, err
}

// wrapError converts an error to an Error, capturing the call
// stack. Errors that are already EvalErrors are returned as is.
func (t *Thread) wrapError(err error) error {
	var evalErr *Error
	if errors.As(err, &evalErr) {
		return err
	}
	return &Error{
		CallStack: t.CallStack(),
		cause:     err,
	}
}

// ExecFile executes the top level of a compiled file, assigning its
// globals in the provided module.
func ExecFile(t *Thread, program *compile.Program, module MutableModule) error {
	toplevel := &memFunction{
		funcode: program.Toplevel,
		module:  module,
	}
	_, err := t.callFunction(toplevel, Args{})
	return err
}

// InMemoryModule is a module whose globals are stored in memory. It is
// used to execute files whose globals don't need to be loaded from
// storage.
type InMemoryModule struct {
	filename    string
	globals     map[string]Value
	predeclared map[string]Value
	importer    func(t *Thread, imp compile.Import) (Value, error)
}

var _ MutableModule = (*InMemoryModule)(nil)

// NewInMemoryModule creates a module whose globals are stored in
// memory. The importer is called to obtain the value of symbols that
// are loaded from other files. It may be nil if the file does not
// contain any load() statements.
func NewInMemoryModule(filename string, predeclared map[string]Value, importer func(t *Thread, imp compile.Import) (Value, error)) *InMemoryModule {
	return &InMemoryModule{
		filename:    filename,
		globals:     map[string]Value{},
		predeclared: predeclared,
		importer:    importer,
	}
}

// Filename returns the filename of the module.
func (m *InMemoryModule) Filename() string {
	return m.filename
}

// Global returns the value of a global.
func (m *InMemoryModule) Global(_ *Thread, name string) (Value, error) {
	if v, ok := m.globals[name]; ok {
		return v, nil
	}
	return nil, fmt.Errorf("global variable %s referenced before assignment", name)
}

// SetGlobal assigns a global.
func (m *InMemoryModule) SetGlobal(_ *Thread, name string, v Value) error {
	m.globals[name] = v
	return nil
}

// Globals returns all globals that have been assigned.
func (m *InMemoryModule) Globals() map[string]Value {
	return m.globals
}

// Import returns the value of a symbol loaded from another file.
func (m *InMemoryModule) Import(t *Thread, imp compile.Import) (Value, error) {
	if m.importer == nil {
		return nil, fmt.Errorf("cannot load %s from %s", imp.Name, imp.Module)
	}
	return m.importer(t, imp)
}

// Predeclared returns the value of a predeclared name.
func (m *InMemoryModule) Predeclared(_ *Thread, name string) (Value, error) {
	if v, ok := m.predeclared[name]; ok {
		return v, nil
	}
	return nil, fmt.Errorf("predeclared name %s is not available", name)
}
