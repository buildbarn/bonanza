// Package eval contains the core of the Starlark interpreter: the
// interfaces implemented by values, in-memory implementations of the
// core types, and the virtual machine that executes bytecode produced
// by pkg/starlark/compile.
//
// All operations that may need to load data from storage take a
// *Thread and may fail. This allows composite values such as lists and
// dicts to have implementations that are backed by storage, alongside
// the in-memory implementations provided by this package.
package eval

import (
	"iter"
	"strings"

	"bonanza.build/pkg/starlark/syntax"
)

// Value is implemented by all Starlark values.
type Value interface {
	// Type returns the name of the type of the value, as reported
	// by type().
	Type() string
	// Truth returns the truth value of the value, as used by
	// bool(x) and conditionals.
	Truth(t *Thread) (bool, error)
	// Repr appends the representation of the value, as returned by
	// repr(), to a printer.
	Repr(t *Thread, p *Printer) error
}

// Hashable is implemented by values that may be used as dict keys and
// set elements.
type Hashable interface {
	Value
	// Hash returns a hash of the value. Hashes are only used by
	// in-memory hash tables, meaning they only need to be stable
	// within a single process.
	Hash(t *Thread) (uint32, error)
}

// Comparable is implemented by values that can be compared against
// other values of the same type, using the operators ==, !=, <, <=,
// > and >=.
type Comparable interface {
	Value
	// CompareSameType compares the value against another value of
	// the same type. The depth is used to detect cycles.
	CompareSameType(t *Thread, op syntax.Token, y Value, depth int) (bool, error)
}

// HasAttrs is implemented by values that have attributes or methods,
// such as structs.
type HasAttrs interface {
	Value
	// Attr returns the value of an attribute. If the value has no
	// attribute with the provided name, a NoSuchAttrError is
	// returned.
	Attr(t *Thread, name string) (Value, error)
	// AttrNames returns the names of all attributes, as returned by
	// dir().
	AttrNames(t *Thread) ([]string, error)
}

// HasSetField is implemented by values that have attributes that may
// be assigned.
type HasSetField interface {
	HasAttrs
	SetField(t *Thread, name string, v Value) error
}

// Side indicates whether a value is the left or right operand of a
// binary operator.
type Side bool

const (
	// Left indicates that the receiver of Binary() is the left
	// operand.
	Left Side = false
	// Right indicates that the receiver of Binary() is the right
	// operand.
	Right Side = true
)

// HasBinary is implemented by values that support binary operators
// beyond the ones provided by the core types. If the operator is not
// supported for the other operand, Binary returns (nil, nil).
type HasBinary interface {
	Value
	Binary(t *Thread, op syntax.Token, y Value, side Side) (Value, error)
}

// HasUnary is implemented by values that support unary operators
// (+, -, ~). If the operator is not supported, Unary returns (nil,
// nil).
type HasUnary interface {
	Value
	Unary(t *Thread, op syntax.Token) (Value, error)
}

// Iterable is implemented by values that can be iterated over, such as
// lists, tuples and dicts.
type Iterable interface {
	Value
	// Iterate returns a sequence of the elements of the value. If
	// an error occurs, iteration stops and the error is stored in
	// errOut.
	Iterate(t *Thread, errOut *error) iter.Seq[Value]
}

// Sequence is implemented by iterable values that have a length.
type Sequence interface {
	Iterable
	Len(t *Thread) (int, error)
}

// Indexable is implemented by values whose elements can be accessed
// by index, such as lists, tuples and strings.
type Indexable interface {
	Value
	Len(t *Thread) (int, error)
	Index(t *Thread, i int) (Value, error)
}

// HasSetIndex is implemented by indexable values whose elements may be
// replaced.
type HasSetIndex interface {
	Indexable
	SetIndex(t *Thread, i int, v Value) error
}

// Sliceable is implemented by indexable values that support slicing.
type Sliceable interface {
	Indexable
	Slice(t *Thread, start, end, step int) (Value, error)
}

// Mapping is implemented by values that map keys to values, such as
// dicts.
type Mapping interface {
	Value
	// Get returns the value corresponding to a key, and whether the
	// key was present.
	Get(t *Thread, k Value) (Value, bool, error)
}

// HasSetKey is implemented by mappings whose entries may be modified.
type HasSetKey interface {
	Mapping
	SetKey(t *Thread, k, v Value) error
}

// Printer is used to compute the representation of values. It
// detects cycles, which can be created by appending a list to itself.
type Printer struct {
	sb   strings.Builder
	path []Value
}

// WriteString appends a string to the output.
func (p *Printer) WriteString(s string) {
	p.sb.WriteString(s)
}

// WriteByte appends a byte to the output.
func (p *Printer) WriteByte(c byte) error {
	return p.sb.WriteByte(c)
}

// String returns the output.
func (p *Printer) String() string {
	return p.sb.String()
}

// Enter is called by composite values before printing their elements.
// It returns false if the value is already being printed, meaning
// that a cycle is present. Leave must be called if Enter returns true.
func (p *Printer) Enter(v Value) bool {
	for _, w := range p.path {
		if w == v {
			return false
		}
	}
	p.path = append(p.path, v)
	return true
}

// Leave is called by composite values after printing their elements.
func (p *Printer) Leave() {
	p.path = p.path[:len(p.path)-1]
}

// Repr returns the representation of a value, as returned by repr().
func Repr(t *Thread, v Value) (string, error) {
	var p Printer
	if err := v.Repr(t, &p); err != nil {
		return "", err
	}
	return p.String(), nil
}

// HasStr is implemented by values whose string representation, as
// returned by str(), differs from the one returned by repr(). For
// example, str() of a label yields the label itself, whereas repr()
// yields Label("...").
type HasStr interface {
	Value
	Str(t *Thread, p *Printer) error
}

// Str returns the string representation of a value, as returned by
// str(). It is equal to Repr(), except for strings, which are
// returned without quotes, and values implementing HasStr.
func Str(t *Thread, v Value) (string, error) {
	switch typedV := v.(type) {
	case String:
		return string(typedV), nil
	case HasStr:
		var p Printer
		if err := typedV.Str(t, &p); err != nil {
			return "", err
		}
		return p.String(), nil
	}
	return Repr(t, v)
}

// NoneType is the type of None.
type NoneType struct{}

// None is the value that is used to represent the absence of a value.
var None = NoneType{}

// Type returns "NoneType".
func (NoneType) Type() string { return "NoneType" }

// Truth returns false, as None is falsy.
func (NoneType) Truth(*Thread) (bool, error) { return false, nil }

// Repr appends "None" to the printer.
func (NoneType) Repr(_ *Thread, p *Printer) error {
	p.WriteString("None")
	return nil
}

// Hash returns a constant hash.
func (NoneType) Hash(*Thread) (uint32, error) { return 0, nil }

// Bool is the type of True and False.
type Bool bool

// Constants for the Boolean values.
const (
	True  = Bool(true)
	False = Bool(false)
)

// Type returns "bool".
func (Bool) Type() string { return "bool" }

// Truth returns the value of the Boolean.
func (b Bool) Truth(*Thread) (bool, error) { return bool(b), nil }

// Repr appends "True" or "False" to the printer.
func (b Bool) Repr(_ *Thread, p *Printer) error {
	if b {
		p.WriteString("True")
	} else {
		p.WriteString("False")
	}
	return nil
}

// Hash returns 1 for True, and 0 for False.
func (b Bool) Hash(*Thread) (uint32, error) {
	if b {
		return 1, nil
	}
	return 0, nil
}

// CompareSameType compares Booleans, where False < True.
func (b Bool) CompareSameType(_ *Thread, op syntax.Token, y Value, _ int) (bool, error) {
	return threeway(op, b2i(bool(b))-b2i(bool(y.(Bool)))), nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// threeway converts the result of a three-way comparison to the result
// of a comparison operator.
func threeway(op syntax.Token, cmp int) bool {
	switch op {
	case syntax.EQL:
		return cmp == 0
	case syntax.NEQ:
		return cmp != 0
	case syntax.LE:
		return cmp <= 0
	case syntax.LT:
		return cmp < 0
	case syntax.GE:
		return cmp >= 0
	case syntax.GT:
		return cmp > 0
	}
	panic(op)
}

// mandatory is a sentinel value that is used in the defaults of
// functions to indicate keyword-only parameters that have no default
// value.
type mandatory struct{}

func (mandatory) Type() string                     { return "mandatory" }
func (mandatory) Truth(*Thread) (bool, error)      { return false, nil }
func (mandatory) Repr(_ *Thread, p *Printer) error { p.WriteString("mandatory"); return nil }

// Cell is a variable that is captured by a nested function.
type Cell struct {
	v Value
}

// NewCell creates a cell containing a value. Storage-backed
// functions use it to provide the values of their free variables.
func NewCell(v Value) *Cell {
	return &Cell{v: v}
}

// Type returns "cell".
func (*Cell) Type() string { return "cell" }

// Truth returns true.
func (*Cell) Truth(*Thread) (bool, error) { return true, nil }

// Repr appends "cell" to the printer.
func (*Cell) Repr(_ *Thread, p *Printer) error { p.WriteString("cell"); return nil }

// Value returns the contents of the cell, or nil if the variable has
// not been assigned yet.
func (c *Cell) Value() Value {
	return c.v
}

// ListMarker can be embedded into types declared outside this package,
// to indicate that they are implementations of List.
type ListMarker struct{}

func (ListMarker) isList() {}

// TupleMarker can be embedded into types declared outside this
// package, to indicate that they are implementations of Tuple.
type TupleMarker struct{}

func (TupleMarker) isTuple() {}

// DictMarker can be embedded into types declared outside this package,
// to indicate that they are implementations of Dict.
type DictMarker struct{}

func (DictMarker) isDict() {}

// SetMarker can be embedded into types declared outside this package,
// to indicate that they are implementations of Set.
type SetMarker struct{}

func (SetMarker) isSet() {}
