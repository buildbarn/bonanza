package eval

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"bonanza.build/pkg/starlark/syntax"
)

// maximumCompareDepth limits the depth of nested values that are
// compared, so that comparing cyclic values terminates.
const maximumCompareDepth = 100

// NoSuchAttrError is returned by HasAttrs.Attr() if a value has no
// attribute with the provided name. getattr() and hasattr() only fall
// back to their default for this error, and propagate all others.
type NoSuchAttrError struct {
	Value Value
	Name  string
	// Name of an existing attribute that is similar to the
	// requested one, if any.
	Suggestion string
}

func (e NoSuchAttrError) Error() string {
	msg := fmt.Sprintf("'%s' value has no field or method '%s'", e.Value.Type(), e.Name)
	if e.Suggestion != "" {
		msg += fmt.Sprintf(" (did you mean '%s'?)", e.Suggestion)
	}
	return msg
}

// Hash returns the hash of a value, as used by dicts and sets. It fails
// if the value is not hashable.
func Hash(t *Thread, v Value) (uint32, error) {
	return hashValue(t, v)
}

func hashValue(t *Thread, v Value) (uint32, error) {
	h, ok := v.(Hashable)
	if !ok {
		return 0, fmt.Errorf("unhashable type: %s", v.Type())
	}
	return h.Hash(t)
}

// Equal returns whether two values are equal.
func Equal(t *Thread, x, y Value) (bool, error) {
	return equalDepth(t, x, y, maximumCompareDepth)
}

// EqualDepth returns whether two values are equal. It is used by
// implementations of Comparable to compare the values contained within,
// so that comparisons of cyclic values terminate. depth is the value
// that was provided to CompareSameType(), minus one.
func EqualDepth(t *Thread, x, y Value, depth int) (bool, error) {
	return equalDepth(t, x, y, depth)
}

func equalDepth(t *Thread, x, y Value, depth int) (bool, error) {
	// Fast paths for common types.
	switch xv := x.(type) {
	case String:
		yv, ok := y.(String)
		return ok && xv == yv, nil
	case Int:
		switch yv := y.(type) {
		case Int:
			return xv.Cmp(yv) == 0, nil
		case Float:
			return compareIntFloat(xv, yv) == 0, nil
		}
		return false, nil
	case Float:
		switch yv := y.(type) {
		case Int:
			return compareIntFloat(yv, xv) == 0, nil
		case Float:
			return compareFloats(float64(xv), float64(yv)) == 0, nil
		}
		return false, nil
	case Bool:
		yv, ok := y.(Bool)
		return ok && xv == yv, nil
	case NoneType:
		_, ok := y.(NoneType)
		return ok, nil
	}
	return compareDepth(t, syntax.EQL, x, y, depth)
}

// Compare compares two values using one of the operators ==, !=, <,
// <=, > and >=.
func Compare(t *Thread, op syntax.Token, x, y Value) (bool, error) {
	if op == syntax.EQL || op == syntax.NEQ {
		equal, err := Equal(t, x, y)
		if err != nil {
			return false, err
		}
		return equal == (op == syntax.EQL), nil
	}
	return compareDepth(t, op, x, y, maximumCompareDepth)
}

func compareDepth(t *Thread, op syntax.Token, x, y Value, depth int) (bool, error) {
	if depth < 1 {
		return false, errors.New("comparison exceeded maximum recursion depth")
	}
	if x.Type() == y.Type() {
		if xc, ok := x.(Comparable); ok {
			return xc.CompareSameType(t, op, y, depth)
		}
		if op == syntax.EQL || op == syntax.NEQ {
			// Values without a notion of equality, such as
			// functions, are only equal to themselves.
			return sameValue(x, y) == (op == syntax.EQL), nil
		}
	} else {
		// Integers and floats can be compared against each other.
		switch xv := x.(type) {
		case Int:
			if yv, ok := y.(Float); ok {
				return threeway(op, compareIntFloat(xv, yv)), nil
			}
		case Float:
			if yv, ok := y.(Int); ok {
				return threeway(op, -compareIntFloat(yv, xv)), nil
			}
		}
		if op == syntax.EQL || op == syntax.NEQ {
			return op == syntax.NEQ, nil
		}
	}
	return false, unsupportedComparison(op, x, y)
}

// sameValue returns whether two values are identical. Unlike ==, it
// does not panic for values of uncomparable types.
func sameValue(x, y Value) (equal bool) {
	defer func() {
		if recover() != nil {
			equal = false
		}
	}()
	return x == y
}

func unsupportedComparison(op syntax.Token, x, y Value) error {
	return fmt.Errorf("unsupported comparison: %s %s %s", x.Type(), op, y.Type())
}

// compareSequences compares two indexable values lexicographically.
func compareSequences(t *Thread, op syntax.Token, x, y Indexable, depth int) (bool, error) {
	xLen, err := x.Len(t)
	if err != nil {
		return false, err
	}
	yLen, err := y.Len(t)
	if err != nil {
		return false, err
	}
	if (op == syntax.EQL || op == syntax.NEQ) && xLen != yLen {
		return op == syntax.NEQ, nil
	}
	for i := 0; i < xLen && i < yLen; i++ {
		xe, err := x.Index(t, i)
		if err != nil {
			return false, err
		}
		ye, err := y.Index(t, i)
		if err != nil {
			return false, err
		}
		equal, err := equalDepth(t, xe, ye, depth-1)
		if err != nil {
			return false, err
		}
		if !equal {
			if op == syntax.EQL || op == syntax.NEQ {
				return op == syntax.NEQ, nil
			}
			return compareDepth(t, op, xe, ye, depth-1)
		}
	}
	return threeway(op, xLen-yLen), nil
}

// Binary applies a binary operator to two values.
func Binary(t *Thread, op syntax.Token, x, y Value) (Value, error) {
	switch op {
	case syntax.PLUS:
		switch xv := x.(type) {
		case String:
			if yv, ok := y.(String); ok {
				return xv + yv, nil
			}
		case Int:
			switch yv := y.(type) {
			case Int:
				return xv.Add(yv), nil
			case Float:
				xf, err := xv.Float()
				if err != nil {
					return nil, err
				}
				return xf + yv, nil
			}
		case Float:
			switch yv := y.(type) {
			case Float:
				return xv + yv, nil
			case Int:
				yf, err := yv.Float()
				if err != nil {
					return nil, err
				}
				return xv + yf, nil
			}
		case List:
			if yv, ok := y.(List); ok {
				elements, err := concatenate(t, xv, yv)
				if err != nil {
					return nil, err
				}
				return NewList(elements), nil
			}
		case Tuple:
			if yv, ok := y.(Tuple); ok {
				elements, err := concatenate(t, xv, yv)
				if err != nil {
					return nil, err
				}
				return NewTuple(elements), nil
			}
		}

	case syntax.MINUS:
		if xs, ys, ok := bothSets(x, y); ok {
			return setOperation(t, op, xs, ys)
		}
		if xi, yi, ok := bothInts(x, y); ok {
			return xi.Sub(yi), nil
		}
		if xf, yf, ok, err := bothNumbers(x, y); ok || err != nil {
			return xf - yf, err
		}

	case syntax.STAR:
		switch xv := x.(type) {
		case Int:
			switch yv := y.(type) {
			case Int:
				return xv.Mul(yv), nil
			case Float:
				xf, err := xv.Float()
				if err != nil {
					return nil, err
				}
				return xf * yv, nil
			case String, List, Tuple:
				return repeat(t, yv, xv)
			}
		case Float:
			if xf, yf, ok, err := bothNumbers(x, y); ok || err != nil {
				return xf * yf, err
			}
		case String, List, Tuple:
			if yv, ok := y.(Int); ok {
				return repeat(t, xv, yv)
			}
		}

	case syntax.SLASH:
		if xf, yf, ok, err := bothNumbers(x, y); err != nil {
			return nil, err
		} else if ok {
			if yf == 0 {
				return nil, errors.New("floating-point division by zero")
			}
			return xf / yf, nil
		}

	case syntax.SLASHSLASH:
		if xi, yi, ok := bothInts(x, y); ok {
			if yi.Sign() == 0 {
				return nil, errors.New("integer division by zero")
			}
			return xi.FloorDiv(yi), nil
		}
		if xf, yf, ok, err := bothNumbers(x, y); err != nil {
			return nil, err
		} else if ok {
			if yf == 0 {
				return nil, errors.New("floating-point division by zero")
			}
			return Float(floorDivFloats(float64(xf), float64(yf))), nil
		}

	case syntax.PERCENT:
		if xi, yi, ok := bothInts(x, y); ok {
			if yi.Sign() == 0 {
				return nil, errors.New("integer modulo by zero")
			}
			return xi.Mod(yi), nil
		}
		if xf, yf, ok, err := bothNumbers(x, y); err != nil {
			return nil, err
		} else if ok {
			if yf == 0 {
				return nil, errors.New("floating-point modulo by zero")
			}
			return Float(modFloats(float64(xf), float64(yf))), nil
		}
		if xs, ok := x.(String); ok {
			return percentFormat(t, string(xs), y)
		}

	case syntax.AMP:
		if xi, yi, ok := bothInts(x, y); ok {
			return xi.And(yi), nil
		}
		if xs, ys, ok := bothSets(x, y); ok {
			return setOperation(t, op, xs, ys)
		}
	case syntax.PIPE:
		if xi, yi, ok := bothInts(x, y); ok {
			return xi.Or(yi), nil
		}
		if xs, ys, ok := bothSets(x, y); ok {
			return setOperation(t, op, xs, ys)
		}
		if xd, ok := x.(Dict); ok {
			if yd, ok := y.(Dict); ok {
				return unionDicts(t, xd, yd)
			}
		}
	case syntax.CIRCUMFLEX:
		if xi, yi, ok := bothInts(x, y); ok {
			return xi.Xor(yi), nil
		}
		if xs, ys, ok := bothSets(x, y); ok {
			return setOperation(t, op, xs, ys)
		}
	case syntax.LTLT, syntax.GTGT:
		if xi, yi, ok := bothInts(x, y); ok {
			if yi.Sign() < 0 {
				return nil, errors.New("negative shift count")
			}
			n, ok := yi.Int64()
			if !ok || n >= 512 {
				if op == syntax.GTGT {
					return xi.Rsh(512), nil
				}
				return nil, errors.New("shift count too large")
			}
			if op == syntax.LTLT {
				return xi.Lsh(uint(n)), nil
			}
			return xi.Rsh(uint(n)), nil
		}

	case syntax.IN, syntax.NOTIN:
		found, err := contains(t, y, x)
		if err != nil {
			return nil, err
		}
		return Bool(found == (op == syntax.IN)), nil

	case syntax.EQL, syntax.NEQ, syntax.LT, syntax.LE, syntax.GT, syntax.GE:
		result, err := Compare(t, op, x, y)
		if err != nil {
			return nil, err
		}
		return Bool(result), nil
	}

	// Let types implemented outside this package handle the operator.
	if xb, ok := x.(HasBinary); ok {
		if z, err := xb.Binary(t, op, y, Left); z != nil || err != nil {
			return z, err
		}
	}
	if yb, ok := y.(HasBinary); ok {
		if z, err := yb.Binary(t, op, x, Right); z != nil || err != nil {
			return z, err
		}
	}
	return nil, fmt.Errorf("unsupported binary operation: %s %s %s", x.Type(), op, y.Type())
}

func bothSets(x, y Value) (Set, Set, bool) {
	xs, ok := x.(Set)
	if !ok {
		return nil, nil, false
	}
	ys, ok := y.(Set)
	return xs, ys, ok
}

func bothInts(x, y Value) (Int, Int, bool) {
	xi, ok := x.(Int)
	if !ok {
		return Int{}, Int{}, false
	}
	yi, ok := y.(Int)
	return xi, yi, ok
}

// bothNumbers converts two numeric values to floats. It returns false
// if either of the values is not a number.
func bothNumbers(x, y Value) (Float, Float, bool, error) {
	toFloat := func(v Value) (Float, bool, error) {
		switch vv := v.(type) {
		case Int:
			f, err := vv.Float()
			return f, true, err
		case Float:
			return vv, true, nil
		}
		return 0, false, nil
	}
	xf, ok, err := toFloat(x)
	if !ok || err != nil {
		return 0, 0, false, err
	}
	yf, ok, err := toFloat(y)
	if !ok || err != nil {
		return 0, 0, false, err
	}
	return xf, yf, true, nil
}

// Elements returns all elements of an iterable value.
func Elements(t *Thread, v Value) ([]Value, error) {
	switch vv := v.(type) {
	case *memList:
		return append([]Value(nil), vv.elements...), nil
	case memTuple:
		return append([]Value(nil), vv...), nil
	}
	iterable, ok := v.(Iterable)
	if !ok {
		return nil, fmt.Errorf("type '%s' is not iterable", v.Type())
	}
	var elements []Value
	var errIter error
	for element := range iterable.Iterate(t, &errIter) {
		elements = append(elements, element)
	}
	return elements, errIter
}

func concatenate(t *Thread, x, y Value) ([]Value, error) {
	xe, err := Elements(t, x)
	if err != nil {
		return nil, err
	}
	ye, err := Elements(t, y)
	if err != nil {
		return nil, err
	}
	return append(xe, ye...), nil
}

// maximumRepeatSize limits the size of values created by repeating
// strings, lists and tuples.
const maximumRepeatSize = 1 << 30

func repeat(t *Thread, x Value, n Int) (Value, error) {
	count, ok := n.Int64()
	if !ok || count > maximumRepeatSize {
		return nil, errors.New("repeat count too large")
	}
	if count < 0 {
		count = 0
	}
	if s, ok := x.(String); ok {
		if int64(len(s))*count > maximumRepeatSize {
			return nil, errors.New("repeated string too large")
		}
		return String(strings.Repeat(string(s), int(count))), nil
	}
	elements, err := Elements(t, x)
	if err != nil {
		return nil, err
	}
	if int64(len(elements))*count > maximumRepeatSize {
		return nil, fmt.Errorf("repeated %s too large", x.Type())
	}
	result := make([]Value, 0, len(elements)*int(count))
	for i := int64(0); i < count; i++ {
		result = append(result, elements...)
	}
	if _, ok := x.(Tuple); ok {
		return NewTuple(result), nil
	}
	return NewList(result), nil
}

func unionDicts(t *Thread, x, y Dict) (Value, error) {
	result := NewDict()
	for _, d := range []Dict{x, y} {
		var errIter error
		for k, v := range d.Items(t, &errIter) {
			if err := result.SetKey(t, k, v); err != nil {
				return nil, err
			}
		}
		if errIter != nil {
			return nil, errIter
		}
	}
	return result, nil
}

// contains implements "needle in haystack".
func contains(t *Thread, haystack, needle Value) (bool, error) {
	switch h := haystack.(type) {
	case String:
		n, ok := needle.(String)
		if !ok {
			return false, fmt.Errorf("'in <string>' requires string as left operand, not %s", needle.Type())
		}
		return strings.Contains(string(h), string(n)), nil
	case Mapping:
		_, found, err := h.Get(t, needle)
		return found, err
	case Set:
		return h.Has(t, needle)
	case rangeValue:
		return h.contains(needle)
	case Iterable:
		var errIter error
		for element := range h.Iterate(t, &errIter) {
			if equal, err := Equal(t, element, needle); err != nil || equal {
				return equal, err
			}
		}
		return false, errIter
	}
	return false, fmt.Errorf("unsupported binary operation: %s in %s", needle.Type(), haystack.Type())
}

// Unary applies a unary operator (+, - or ~) to a value.
func Unary(t *Thread, op syntax.Token, x Value) (Value, error) {
	switch xv := x.(type) {
	case Int:
		switch op {
		case syntax.PLUS:
			return xv, nil
		case syntax.MINUS:
			return xv.Neg(), nil
		case syntax.TILDE:
			return xv.Not(), nil
		}
	case Float:
		switch op {
		case syntax.PLUS:
			return xv, nil
		case syntax.MINUS:
			return -xv, nil
		}
	case HasUnary:
		if z, err := xv.Unary(t, op); z != nil || err != nil {
			return z, err
		}
	}
	return nil, fmt.Errorf("unsupported unary operation: %s%s", op, x.Type())
}

// getIndex implements x[y].
func getIndex(t *Thread, x, y Value) (Value, error) {
	switch xv := x.(type) {
	case Mapping:
		v, found, err := xv.Get(t, y)
		if err != nil {
			return nil, err
		}
		if !found {
			key, err := Repr(t, y)
			if err != nil {
				return nil, err
			}
			if _, ok := x.(Dict); ok {
				return nil, fmt.Errorf("key %s not found in dictionary", key)
			}
			// Don't print the mapping itself, as it may be
			// large or backed by storage.
			return nil, fmt.Errorf("key %s not found in %s", key, x.Type())
		}
		return v, nil
	case Indexable:
		n, err := xv.Len(t)
		if err != nil {
			return nil, err
		}
		i, err := checkIndex(y, n)
		if err != nil {
			return nil, err
		}
		return xv.Index(t, i)
	}
	return nil, fmt.Errorf("type '%s' has no operator [](%s)", x.Type(), y.Type())
}

// checkIndex converts an index to an int, supporting negative indices.
func checkIndex(y Value, n int) (int, error) {
	yi, ok := y.(Int)
	if !ok {
		return 0, fmt.Errorf("indices must be integers, not %s", y.Type())
	}
	i, ok := yi.Int64()
	if !ok || i < -int64(n) || i >= int64(n) {
		return 0, fmt.Errorf("index out of range (index is %s, but sequence has %d elements)", yi, n)
	}
	if i < 0 {
		i += int64(n)
	}
	return int(i), nil
}

// setIndex implements x[y] = z.
func setIndex(t *Thread, x, y, z Value) error {
	switch xv := x.(type) {
	case HasSetKey:
		return xv.SetKey(t, y, z)
	case HasSetIndex:
		n, err := xv.Len(t)
		if err != nil {
			return err
		}
		i, err := checkIndex(y, n)
		if err != nil {
			return err
		}
		return xv.SetIndex(t, i, z)
	}
	return fmt.Errorf("can only assign an element in a dictionary or a list, not in a '%s'", x.Type())
}

// slice implements x[lo:hi:step].
func slice(t *Thread, x, lo, hi, step Value) (Value, error) {
	sliceable, ok := x.(Sliceable)
	if !ok {
		return nil, fmt.Errorf("invalid slice operand %s", x.Type())
	}
	n, err := sliceable.Len(t)
	if err != nil {
		return nil, err
	}

	stepValue := 1
	if step != None {
		s, ok := step.(Int)
		if !ok {
			return nil, fmt.Errorf("invalid slice step: got %s, want int", step.Type())
		}
		s64, ok := s.Int64()
		if !ok || s64 == 0 || s64 < math.MinInt32 || s64 > math.MaxInt32 {
			return nil, errors.New("slice step cannot be zero")
		}
		stepValue = int(s64)
	}

	// Compute the default bounds, and clamp the provided ones.
	var start, end int
	if stepValue > 0 {
		start, end = 0, n
	} else {
		start, end = n-1, -1
	}
	clamp := func(v Value, defaultValue int) (int, error) {
		if v == None {
			return defaultValue, nil
		}
		vi, ok := v.(Int)
		if !ok {
			return 0, fmt.Errorf("invalid slice index: got %s, want int", v.Type())
		}
		i64, ok := vi.Int64()
		if !ok {
			if vi.Sign() < 0 {
				i64 = math.MinInt64
			} else {
				i64 = math.MaxInt64
			}
		}
		if i64 < 0 {
			i64 += int64(n)
		}
		lower, upper := int64(0), int64(n)
		if stepValue < 0 {
			lower, upper = -1, int64(n-1)
		}
		return int(min(max(i64, lower), upper)), nil
	}
	if start, err = clamp(lo, start); err != nil {
		return nil, err
	}
	if end, err = clamp(hi, end); err != nil {
		return nil, err
	}
	return sliceable.Slice(t, start, end, stepValue)
}

// getAttr implements x.name.
func getAttr(t *Thread, x Value, name string) (Value, error) {
	var err error
	if xa, ok := x.(HasAttrs); ok {
		var v Value
		if v, err = xa.Attr(t, name); err == nil {
			return v, nil
		}
	} else {
		if method, ok := lookupMethod(x, name); ok {
			return method, nil
		}
		err = NoSuchAttrError{Value: x, Name: name}
	}

	// Add a suggestion to errors for missing attributes.
	var noSuchAttr NoSuchAttrError
	if errors.As(err, &noSuchAttr) && noSuchAttr.Suggestion == "" {
		if names, namesErr := AttrNames(t, x); namesErr == nil {
			if suggestion := nearestName(name, names); suggestion != "" {
				noSuchAttr.Suggestion = suggestion
				return nil, noSuchAttr
			}
		}
	}
	return nil, err
}

// setField implements x.name = y.
func setField(t *Thread, x Value, name string, y Value) error {
	if xs, ok := x.(HasSetField); ok {
		return xs.SetField(t, name, y)
	}
	return fmt.Errorf("can't assign to .%s field of %s", name, x.Type())
}
