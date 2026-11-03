package eval

import (
	"errors"
	"iter"

	"bonanza.build/pkg/starlark/syntax"
)

// List is implemented by all Starlark lists, regardless of whether
// they reside in memory or in storage.
type List interface {
	Sequence
	Sliceable
	isList()
	// Append adds an element to the end of the list.
	Append(t *Thread, v Value) error
	// SetIndex replaces the element at the provided index.
	SetIndex(t *Thread, i int, v Value) error
}

// Tuple is implemented by all Starlark tuples, regardless of whether
// they reside in memory or in storage.
type Tuple interface {
	Sequence
	Sliceable
	isTuple()
}

// mutability tracks whether a mutable value can currently be modified.
// Values may be immutable from the moment they are created, and are
// temporarily immutable while being iterated over.
type mutability struct {
	immutable bool
	iterators int
}

func (m *mutability) checkMutable(typeName string) error {
	if m.immutable {
		return errors.New("trying to mutate a frozen " + typeName + " value")
	}
	if m.iterators > 0 {
		return errors.New(typeName + " value is temporarily immutable due to active for-loop iteration")
	}
	return nil
}

// memList is an in-memory list.
type memList struct {
	mutability
	elements []Value
}

var _ List = (*memList)(nil)

// NewList creates a mutable list containing the provided elements. The
// list takes ownership of the slice.
func NewList(elements []Value) List {
	return &memList{elements: elements}
}

// NewImmutableList creates an immutable list containing the provided
// elements, which must also be immutable. The list takes ownership of
// the slice.
func NewImmutableList(elements []Value) List {
	return &memList{
		mutability: mutability{immutable: true},
		elements:   elements,
	}
}

func (*memList) isList() {}

// Type returns "list".
func (*memList) Type() string { return "list" }

// Truth returns whether the list is non-empty.
func (l *memList) Truth(*Thread) (bool, error) { return len(l.elements) > 0, nil }

// Repr appends the representation of the list to the printer.
func (l *memList) Repr(t *Thread, p *Printer) error {
	if !p.Enter(l) {
		p.WriteString("[...]")
		return nil
	}
	defer p.Leave()
	p.WriteString("[")
	for i, element := range l.elements {
		if i > 0 {
			p.WriteString(", ")
		}
		if err := element.Repr(t, p); err != nil {
			return err
		}
	}
	p.WriteString("]")
	return nil
}

// Len returns the number of elements in the list.
func (l *memList) Len(*Thread) (int, error) { return len(l.elements), nil }

// Index returns the element at the provided index.
func (l *memList) Index(_ *Thread, i int) (Value, error) { return l.elements[i], nil }

// Slice returns a new list containing a subsequence of the elements.
func (l *memList) Slice(_ *Thread, start, end, step int) (Value, error) {
	return NewList(sliceValues(l.elements, start, end, step)), nil
}

func sliceValues(elements []Value, start, end, step int) []Value {
	if step == 1 {
		if start < end {
			return append([]Value(nil), elements[start:end]...)
		}
		return nil
	}
	var result []Value
	for i := start; signum(end-i) == signum(step); i += step {
		result = append(result, elements[i])
	}
	return result
}

// Iterate over the elements of the list. The list cannot be modified
// while iteration is in progress.
func (l *memList) Iterate(*Thread, *error) iter.Seq[Value] {
	return func(yield func(Value) bool) {
		l.iterators++
		defer func() { l.iterators-- }()
		for _, element := range l.elements {
			if !yield(element) {
				return
			}
		}
	}
}

// Append adds an element to the end of the list.
func (l *memList) Append(_ *Thread, v Value) error {
	if err := l.checkMutable("list"); err != nil {
		return err
	}
	l.elements = append(l.elements, v)
	return nil
}

// SetIndex replaces the element at the provided index.
func (l *memList) SetIndex(_ *Thread, i int, v Value) error {
	if err := l.checkMutable("list"); err != nil {
		return err
	}
	l.elements[i] = v
	return nil
}

// CompareSameType compares lists lexicographically.
func (l *memList) CompareSameType(t *Thread, op syntax.Token, y Value, depth int) (bool, error) {
	return compareSequences(t, op, l, y.(Indexable), depth)
}

// memTuple is an in-memory tuple.
type memTuple []Value

var _ Tuple = memTuple(nil)

// NewTuple creates a tuple containing the provided elements. The tuple
// takes ownership of the slice.
func NewTuple(elements []Value) Tuple {
	return memTuple(elements)
}

func (memTuple) isTuple() {}

// Type returns "tuple".
func (memTuple) Type() string { return "tuple" }

// Truth returns whether the tuple is non-empty.
func (tu memTuple) Truth(*Thread) (bool, error) { return len(tu) > 0, nil }

// Repr appends the representation of the tuple to the printer.
func (tu memTuple) Repr(t *Thread, p *Printer) error {
	p.WriteString("(")
	for i, element := range tu {
		if i > 0 {
			p.WriteString(", ")
		}
		if err := element.Repr(t, p); err != nil {
			return err
		}
	}
	if len(tu) == 1 {
		p.WriteString(",")
	}
	p.WriteString(")")
	return nil
}

// Len returns the number of elements in the tuple.
func (tu memTuple) Len(*Thread) (int, error) { return len(tu), nil }

// Index returns the element at the provided index.
func (tu memTuple) Index(_ *Thread, i int) (Value, error) { return tu[i], nil }

// Slice returns a new tuple containing a subsequence of the elements.
func (tu memTuple) Slice(_ *Thread, start, end, step int) (Value, error) {
	return memTuple(sliceValues(tu, start, end, step)), nil
}

// Iterate over the elements of the tuple.
func (tu memTuple) Iterate(*Thread, *error) iter.Seq[Value] {
	return func(yield func(Value) bool) {
		for _, element := range tu {
			if !yield(element) {
				return
			}
		}
	}
}

// Hash returns a hash of the tuple, which is computed from the hashes
// of its elements.
func (tu memTuple) Hash(t *Thread) (uint32, error) {
	h := uint32(0x345678)
	for _, element := range tu {
		eh, err := hashValue(t, element)
		if err != nil {
			return 0, err
		}
		h = (h ^ eh) * 1000003
	}
	return h, nil
}

// CompareSameType compares tuples lexicographically.
func (tu memTuple) CompareSameType(t *Thread, op syntax.Token, y Value, depth int) (bool, error) {
	return compareSequences(t, op, tu, y.(Indexable), depth)
}
