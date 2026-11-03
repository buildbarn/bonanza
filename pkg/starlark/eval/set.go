package eval

import (
	"errors"
	"fmt"
	"iter"

	"bonanza.build/pkg/starlark/syntax"
)

// Set is implemented by all Starlark sets, regardless of whether they
// reside in memory or in storage. Iterating over a set yields its
// elements in insertion order.
type Set interface {
	Sequence
	isSet()
	Has(t *Thread, v Value) (bool, error)
	Add(t *Thread, v Value) error
	// Discard removes an element, returning whether it was present.
	Discard(t *Thread, v Value) (bool, error)
	Clear(t *Thread) error
}

// memSet is an in-memory set. It is a hash table whose values are
// unused.
type memSet struct {
	mutability
	table memDict
}

var _ Set = (*memSet)(nil)

// NewSet creates an empty mutable set.
func NewSet() Set {
	return &memSet{}
}

func (*memSet) isSet()                        {}
func (*memSet) Type() string                  { return "set" }
func (s *memSet) Truth(*Thread) (bool, error) { return s.table.count > 0, nil }
func (s *memSet) Len(*Thread) (int, error)    { return s.table.count, nil }

func (s *memSet) Repr(t *Thread, p *Printer) error {
	if s.table.count == 0 {
		p.WriteString("set()")
		return nil
	}
	if !p.Enter(s) {
		p.WriteString("set(...)")
		return nil
	}
	defer p.Leave()
	p.WriteString("set([")
	first := true
	for _, entry := range s.table.entries {
		if entry.key == nil {
			continue
		}
		if !first {
			p.WriteString(", ")
		}
		first = false
		if err := entry.key.Repr(t, p); err != nil {
			return err
		}
	}
	p.WriteString("])")
	return nil
}

func (s *memSet) Iterate(*Thread, *error) iter.Seq[Value] {
	return func(yield func(Value) bool) {
		s.iterators++
		defer func() { s.iterators-- }()
		for _, entry := range s.table.entries {
			if entry.key != nil && !yield(entry.key) {
				return
			}
		}
	}
}

func (s *memSet) Has(t *Thread, v Value) (bool, error) {
	_, found, err := s.table.Get(t, v)
	return found, err
}

func (s *memSet) Add(t *Thread, v Value) error {
	if err := s.checkMutable("set"); err != nil {
		return err
	}
	return s.table.SetKey(t, v, None)
}

func (s *memSet) Discard(t *Thread, v Value) (bool, error) {
	if err := s.checkMutable("set"); err != nil {
		return false, err
	}
	_, found, err := s.table.Delete(t, v)
	return found, err
}

func (s *memSet) Clear(t *Thread) error {
	if err := s.checkMutable("set"); err != nil {
		return err
	}
	return s.table.Clear(t)
}

// CompareSameType compares sets. == and != test for equality, while
// <=, <, >= and > test for subsets and supersets.
func (s *memSet) CompareSameType(t *Thread, op syntax.Token, y Value, depth int) (bool, error) {
	other := y.(Set)
	switch op {
	case syntax.EQL, syntax.NEQ:
		equal, err := setsEqual(t, s, other)
		return equal == (op == syntax.EQL), err
	case syntax.LE:
		return isSubset(t, s, other)
	case syntax.GE:
		return isSubset(t, other, s)
	case syntax.LT, syntax.GT:
		x, y := Set(s), other
		if op == syntax.GT {
			x, y = y, x
		}
		subset, err := isSubset(t, x, y)
		if err != nil || !subset {
			return false, err
		}
		equal, err := setsEqual(t, x, y)
		return !equal, err
	}
	return false, unsupportedComparison(op, s, y)
}

func setsEqual(t *Thread, x, y Set) (bool, error) {
	xLen, err := x.Len(t)
	if err != nil {
		return false, err
	}
	yLen, err := y.Len(t)
	if err != nil {
		return false, err
	}
	if xLen != yLen {
		return false, nil
	}
	return isSubset(t, x, y)
}

// isSubset returns whether all elements of x are contained in y.
func isSubset(t *Thread, x, y Set) (bool, error) {
	var errIter error
	for element := range x.Iterate(t, &errIter) {
		if found, err := y.Has(t, element); err != nil || !found {
			return false, err
		}
	}
	return true, errIter
}

// setOperation computes the union, intersection, difference or
// symmetric difference of a set and an iterable value.
func setOperation(t *Thread, op syntax.Token, x Set, y Value) (Set, error) {
	result := NewSet()
	other := NewSet()
	elements, err := Elements(t, y)
	if err != nil {
		return nil, err
	}
	for _, element := range elements {
		if err := other.Add(t, element); err != nil {
			return nil, err
		}
	}
	xElements, err := Elements(t, x)
	if err != nil {
		return nil, err
	}

	add := func(v Value) error { return result.Add(t, v) }
	switch op {
	case syntax.PIPE:
		for _, element := range append(xElements, elements...) {
			if err := add(element); err != nil {
				return nil, err
			}
		}
	case syntax.AMP, syntax.MINUS:
		for _, element := range xElements {
			found, err := other.Has(t, element)
			if err != nil {
				return nil, err
			}
			if found == (op == syntax.AMP) {
				if err := add(element); err != nil {
					return nil, err
				}
			}
		}
	case syntax.CIRCUMFLEX:
		for _, element := range xElements {
			if found, err := other.Has(t, element); err != nil {
				return nil, err
			} else if !found {
				if err := add(element); err != nil {
					return nil, err
				}
			}
		}
		for _, element := range elements {
			if found, err := x.Has(t, element); err != nil {
				return nil, err
			} else if !found {
				if err := add(element); err != nil {
					return nil, err
				}
			}
		}
	default:
		panic(op)
	}
	return result, nil
}

// replaceSetContents replaces the elements of a set with the elements
// of another set.
func replaceSetContents(t *Thread, s, contents Set) error {
	elements, err := Elements(t, contents)
	if err != nil {
		return err
	}
	if err := s.Clear(t); err != nil {
		return err
	}
	for _, element := range elements {
		if err := s.Add(t, element); err != nil {
			return err
		}
	}
	return nil
}

var setMethods = map[string]*Builtin{
	"add":                         NewBuiltin("add", setAdd),
	"clear":                       NewBuiltin("clear", setClear),
	"difference":                  NewBuiltin("difference", setMultiOperation(syntax.MINUS, false)),
	"difference_update":           NewBuiltin("difference_update", setMultiOperation(syntax.MINUS, true)),
	"discard":                     NewBuiltin("discard", setDiscard),
	"intersection":                NewBuiltin("intersection", setMultiOperation(syntax.AMP, false)),
	"intersection_update":         NewBuiltin("intersection_update", setMultiOperation(syntax.AMP, true)),
	"isdisjoint":                  NewBuiltin("isdisjoint", setIsdisjoint),
	"issubset":                    NewBuiltin("issubset", setIssubset),
	"issuperset":                  NewBuiltin("issuperset", setIssuperset),
	"pop":                         NewBuiltin("pop", setPop),
	"remove":                      NewBuiltin("remove", setRemove),
	"symmetric_difference":        NewBuiltin("symmetric_difference", setSingleOperation(syntax.CIRCUMFLEX, false)),
	"symmetric_difference_update": NewBuiltin("symmetric_difference_update", setSingleOperation(syntax.CIRCUMFLEX, true)),
	"union":                       NewBuiltin("union", setMultiOperation(syntax.PIPE, false)),
	"update":                      NewBuiltin("update", setMultiOperation(syntax.PIPE, true)),
}

func setAdd(t *Thread, b *Builtin, args Args) (Value, error) {
	var x Value
	if err := UnpackArgs(t, b.Name(), args, "element", &x); err != nil {
		return nil, err
	}
	return None, b.Receiver().(Set).Add(t, x)
}

func setClear(t *Thread, b *Builtin, args Args) (Value, error) {
	if err := noArgs(b, args); err != nil {
		return nil, err
	}
	return None, b.Receiver().(Set).Clear(t)
}

func setDiscard(t *Thread, b *Builtin, args Args) (Value, error) {
	var x Value
	if err := UnpackArgs(t, b.Name(), args, "element", &x); err != nil {
		return nil, err
	}
	_, err := b.Receiver().(Set).Discard(t, x)
	return None, err
}

func setRemove(t *Thread, b *Builtin, args Args) (Value, error) {
	var x Value
	if err := UnpackArgs(t, b.Name(), args, "element", &x); err != nil {
		return nil, err
	}
	found, err := b.Receiver().(Set).Discard(t, x)
	if err != nil {
		return nil, err
	}
	if !found {
		s, err := Repr(t, x)
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("remove(): element %s not found", s)
	}
	return None, nil
}

func setPop(t *Thread, b *Builtin, args Args) (Value, error) {
	if err := noArgs(b, args); err != nil {
		return nil, err
	}
	s := b.Receiver().(Set)
	var first Value
	var errIter error
	for element := range s.Iterate(t, &errIter) {
		first = element
		break
	}
	if errIter != nil {
		return nil, errIter
	}
	if first == nil {
		return nil, errors.New("pop(): set is empty")
	}
	if _, err := s.Discard(t, first); err != nil {
		return nil, err
	}
	return first, nil
}

// setMultiOperation returns the implementation of a method that
// applies a set operation to the receiver and any number of iterables.
// If update is set, the receiver is modified in place.
func setMultiOperation(op syntax.Token, update bool) func(*Thread, *Builtin, Args) (Value, error) {
	return func(t *Thread, b *Builtin, args Args) (Value, error) {
		if len(args.Named) > 0 {
			return nil, fmt.Errorf("%s() got unexpected keyword argument '%s'", b.Name(), args.Named[0].Name)
		}
		s := b.Receiver().(Set)
		result := s
		for _, other := range args.Positional {
			var err error
			if result, err = setOperation(t, op, result, other); err != nil {
				return nil, err
			}
		}
		if update {
			return None, replaceSetContents(t, s, result)
		}
		if result == s {
			// Always return a copy.
			var err error
			if result, err = setOperation(t, syntax.PIPE, s, NewSet()); err != nil {
				return nil, err
			}
		}
		return result, nil
	}
}

// setSingleOperation is like setMultiOperation, except that the method
// takes exactly one argument.
func setSingleOperation(op syntax.Token, update bool) func(*Thread, *Builtin, Args) (Value, error) {
	return func(t *Thread, b *Builtin, args Args) (Value, error) {
		var other Value
		if err := UnpackArgs(t, b.Name(), args, "other", &other); err != nil {
			return nil, err
		}
		s := b.Receiver().(Set)
		result, err := setOperation(t, op, s, other)
		if err != nil {
			return nil, err
		}
		if update {
			return None, replaceSetContents(t, s, result)
		}
		return result, nil
	}
}

// setOfIterable converts the argument of methods like issubset() to a
// set.
func setOfIterable(t *Thread, x Value) (Set, error) {
	if s, ok := x.(Set); ok {
		return s, nil
	}
	return setOperation(t, syntax.PIPE, NewSet(), x)
}

func setIsdisjoint(t *Thread, b *Builtin, args Args) (Value, error) {
	var other Value
	if err := UnpackArgs(t, b.Name(), args, "other", &other); err != nil {
		return nil, err
	}
	intersection, err := setOperation(t, syntax.AMP, b.Receiver().(Set), other)
	if err != nil {
		return nil, err
	}
	n, err := intersection.Len(t)
	return Bool(n == 0), err
}

func setIssubset(t *Thread, b *Builtin, args Args) (Value, error) {
	var other Value
	if err := UnpackArgs(t, b.Name(), args, "other", &other); err != nil {
		return nil, err
	}
	otherSet, err := setOfIterable(t, other)
	if err != nil {
		return nil, err
	}
	subset, err := isSubset(t, b.Receiver().(Set), otherSet)
	return Bool(subset), err
}

func setIssuperset(t *Thread, b *Builtin, args Args) (Value, error) {
	var other Value
	if err := UnpackArgs(t, b.Name(), args, "other", &other); err != nil {
		return nil, err
	}
	otherSet, err := setOfIterable(t, other)
	if err != nil {
		return nil, err
	}
	superset, err := isSubset(t, otherSet, b.Receiver().(Set))
	return Bool(superset), err
}
