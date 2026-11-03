package eval

import (
	"fmt"
	"iter"

	"bonanza.build/pkg/starlark/syntax"
)

// rangeValue is the type of values returned by range(). It represents
// an arithmetic progression without storing its elements.
type rangeValue struct {
	start, stop, step int
}

var (
	_ Sequence  = rangeValue{}
	_ Sliceable = rangeValue{}
)

func (rangeValue) Type() string { return "range" }

func (r rangeValue) length() int {
	if r.step > 0 && r.start < r.stop {
		return (r.stop - r.start + r.step - 1) / r.step
	}
	if r.step < 0 && r.start > r.stop {
		return (r.start - r.stop - r.step - 1) / -r.step
	}
	return 0
}

func (r rangeValue) Truth(*Thread) (bool, error) { return r.length() > 0, nil }

func (r rangeValue) Repr(_ *Thread, p *Printer) error {
	if r.step == 1 {
		p.WriteString(fmt.Sprintf("range(%d, %d)", r.start, r.stop))
	} else {
		p.WriteString(fmt.Sprintf("range(%d, %d, %d)", r.start, r.stop, r.step))
	}
	return nil
}

func (r rangeValue) Len(*Thread) (int, error) { return r.length(), nil }

func (r rangeValue) Index(_ *Thread, i int) (Value, error) {
	return MakeInt(r.start + i*r.step), nil
}

func (r rangeValue) Slice(_ *Thread, start, end, step int) (Value, error) {
	return rangeValue{
		start: r.start + start*r.step,
		stop:  r.start + end*r.step,
		step:  r.step * step,
	}, nil
}

func (r rangeValue) Iterate(*Thread, *error) iter.Seq[Value] {
	return func(yield func(Value) bool) {
		n := r.length()
		for i := 0; i < n; i++ {
			if !yield(MakeInt(r.start + i*r.step)) {
				return
			}
		}
	}
}

// CompareSameType compares ranges for equality. Ranges are equal if
// they contain the same elements.
func (r rangeValue) CompareSameType(t *Thread, op syntax.Token, y Value, depth int) (bool, error) {
	if op != syntax.EQL && op != syntax.NEQ {
		return false, unsupportedComparison(op, r, y)
	}
	return compareSequences(t, op, r, y.(Indexable), depth)
}

// contains returns whether a number is an element of the range,
// without iterating over it. Floats with an integral value are treated
// like the equivalent integer.
func (r rangeValue) contains(v Value) (bool, error) {
	var i Int
	switch n := v.(type) {
	case Int:
		i = n
	case Float:
		var ok bool
		if i, ok = n.integral(); !ok {
			return false, nil
		}
	default:
		return false, fmt.Errorf("'in <range>' requires integer as left operand, not %s", v.Type())
	}
	x, ok := i.Int64()
	if !ok || r.length() == 0 {
		return false, nil
	}
	n := int(x)
	if r.step > 0 && (n < r.start || n >= r.stop) || r.step < 0 && (n > r.start || n <= r.stop) {
		return false, nil
	}
	return (n-r.start)%r.step == 0, nil
}
