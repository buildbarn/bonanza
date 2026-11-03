package eval

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"math/big"
	"slices"
	"sort"
	"strconv"
	"strings"

	"bonanza.build/pkg/starlark/syntax"
)

// Universe returns the names that are provided by the core of the
// language, such as None, len() and the json module. The resulting
// map may be modified by the caller.
func Universe() map[string]Value {
	return maps.Clone(universe)
}

var universe = map[string]Value{
	"None":      None,
	"True":      True,
	"False":     False,
	"abs":       NewBuiltin("abs", builtinAbs),
	"all":       NewBuiltin("all", builtinAll),
	"any":       NewBuiltin("any", builtinAny),
	"bool":      NewBuiltin("bool", builtinBool),
	"dict":      NewBuiltin("dict", builtinDict),
	"dir":       NewBuiltin("dir", builtinDir),
	"enumerate": NewBuiltin("enumerate", builtinEnumerate),
	"fail":      NewBuiltin("fail", builtinFail),
	"float":     NewBuiltin("float", builtinFloat),
	"getattr":   NewBuiltin("getattr", builtinGetattr),
	"hasattr":   NewBuiltin("hasattr", builtinHasattr),
	"hash":      NewBuiltin("hash", builtinHash),
	"int":       NewBuiltin("int", builtinInt),
	"json":      jsonModule,
	"len":       NewBuiltin("len", builtinLen),
	"list":      NewBuiltin("list", builtinList),
	"max":       NewBuiltin("max", builtinMax),
	"min":       NewBuiltin("min", builtinMin),
	"print":     NewBuiltin("print", builtinPrint),
	"range":     NewBuiltin("range", builtinRange),
	"repr":      NewBuiltin("repr", builtinRepr),
	"reversed":  NewBuiltin("reversed", builtinReversed),
	"set":       NewBuiltin("set", builtinSet),
	"sorted":    NewBuiltin("sorted", builtinSorted),
	"str":       NewBuiltin("str", builtinStr),
	"tuple":     NewBuiltin("tuple", builtinTuple),
	"type":      NewBuiltin("type", builtinType),
	"zip":       NewBuiltin("zip", builtinZip),
}

// positionalArgs checks that a builtin is called with only positional
// arguments, and that their number is in range.
func positionalArgs(b *Builtin, args Args, minimum, maximum int) error {
	if len(args.Named) > 0 {
		return fmt.Errorf("%s() got unexpected keyword argument '%s'", b.Name(), args.Named[0].Name)
	}
	if n := len(args.Positional); n < minimum || n > maximum {
		if minimum == maximum {
			return fmt.Errorf("%s() takes exactly %d argument%s (%d given)", b.Name(), minimum, plural(minimum), n)
		}
		return fmt.Errorf("%s() takes between %d and %d arguments (%d given)", b.Name(), minimum, maximum, n)
	}
	return nil
}

func builtinAbs(t *Thread, b *Builtin, args Args) (Value, error) {
	var x Value
	if err := UnpackArgs(t, b.Name(), args, "x", &x); err != nil {
		return nil, err
	}
	switch v := x.(type) {
	case Int:
		if v.Sign() < 0 {
			return v.Neg(), nil
		}
		return v, nil
	case Float:
		return Float(math.Abs(float64(v))), nil
	}
	return nil, fmt.Errorf("abs(): got %s, want int or float", x.Type())
}

func builtinAll(t *Thread, b *Builtin, args Args) (Value, error) {
	var x Value
	if err := UnpackArgs(t, b.Name(), args, "elements", &x); err != nil {
		return nil, err
	}
	elements, err := Elements(t, x)
	if err != nil {
		return nil, err
	}
	for _, element := range elements {
		if truth, err := element.Truth(t); err != nil || !truth {
			return False, err
		}
	}
	return True, nil
}

func builtinAny(t *Thread, b *Builtin, args Args) (Value, error) {
	var x Value
	if err := UnpackArgs(t, b.Name(), args, "elements", &x); err != nil {
		return nil, err
	}
	elements, err := Elements(t, x)
	if err != nil {
		return nil, err
	}
	for _, element := range elements {
		if truth, err := element.Truth(t); err != nil || truth {
			return Bool(truth), err
		}
	}
	return False, nil
}

func builtinBool(t *Thread, b *Builtin, args Args) (Value, error) {
	var x Value = False
	if err := UnpackArgs(t, b.Name(), args, "x?", &x); err != nil {
		return nil, err
	}
	truth, err := x.Truth(t)
	return Bool(truth), err
}

func builtinDict(t *Thread, b *Builtin, args Args) (Value, error) {
	if len(args.Positional) > 1 {
		return nil, fmt.Errorf("dict() takes at most 1 positional argument (%d given)", len(args.Positional))
	}
	d := NewDict()
	if err := updateDict(t, d, args); err != nil {
		return nil, fmt.Errorf("dict(): %w", err)
	}
	return d, nil
}

// updateDict implements dict() and dict.update(), adding the entries
// of an optional mapping or iterable of pairs, followed by the keyword
// arguments.
func updateDict(t *Thread, d Dict, args Args) error {
	if len(args.Positional) > 0 {
		switch updates := args.Positional[0].(type) {
		case Dict:
			var errIter error
			for k, v := range updates.Items(t, &errIter) {
				if err := d.SetKey(t, k, v); err != nil {
					return err
				}
			}
			if errIter != nil {
				return errIter
			}
		default:
			elements, err := Elements(t, updates)
			if err != nil {
				return fmt.Errorf("got %s, want iterable", updates.Type())
			}
			for i, element := range elements {
				pair, err := Elements(t, element)
				if err != nil {
					return fmt.Errorf("dictionary update sequence element #%d is not iterable (%s)", i, element.Type())
				}
				if len(pair) != 2 {
					return fmt.Errorf("dictionary update sequence element #%d has length %d, want 2", i, len(pair))
				}
				if err := d.SetKey(t, pair[0], pair[1]); err != nil {
					return err
				}
			}
		}
	}
	for _, arg := range args.Named {
		if err := d.SetKey(t, String(arg.Name), arg.Value); err != nil {
			return err
		}
	}
	return nil
}

// AttrNames returns the names of the attributes and methods of a value,
// in sorted order.
func AttrNames(t *Thread, x Value) ([]string, error) {
	var names []string
	if xa, ok := x.(HasAttrs); ok {
		attrNames, err := xa.AttrNames(t)
		if err != nil {
			return nil, err
		}
		names = append(names, attrNames...)
	}
	for name := range methods[x.Type()] {
		names = append(names, name)
	}
	sort.Strings(names)
	return slices.Compact(names), nil
}

func builtinDir(t *Thread, b *Builtin, args Args) (Value, error) {
	var x Value
	if err := UnpackArgs(t, b.Name(), args, "x", &x); err != nil {
		return nil, err
	}
	names, err := AttrNames(t, x)
	if err != nil {
		return nil, err
	}
	elements := make([]Value, 0, len(names))
	for _, name := range names {
		elements = append(elements, String(name))
	}
	return NewList(elements), nil
}

func builtinEnumerate(t *Thread, b *Builtin, args Args) (Value, error) {
	var x Value
	start := 0
	if err := UnpackArgs(t, b.Name(), args, "list", &x, "start?", &start); err != nil {
		return nil, err
	}
	elements, err := Elements(t, x)
	if err != nil {
		return nil, err
	}
	pairs := make([]Value, 0, len(elements))
	for i, element := range elements {
		pairs = append(pairs, NewTuple([]Value{MakeInt(start + i), element}))
	}
	return NewList(pairs), nil
}

func builtinFail(t *Thread, b *Builtin, args Args) (Value, error) {
	sep := " "
	var attr Value = None
	parts := make([]string, 0, len(args.Positional)+1)
	for _, arg := range args.Named {
		switch arg.Name {
		case "sep":
			s, ok := arg.Value.(String)
			if !ok {
				return nil, fmt.Errorf("fail(): for parameter sep: got %s, want string", arg.Value.Type())
			}
			sep = string(s)
		case "attr":
			attr = arg.Value
		case "msg":
			if arg.Value != Value(None) {
				s, err := Str(t, arg.Value)
				if err != nil {
					return nil, err
				}
				parts = append(parts, s)
			}
		default:
			return nil, fmt.Errorf("fail() got unexpected keyword argument '%s'", arg.Name)
		}
	}
	for _, arg := range args.Positional {
		s, err := Str(t, arg)
		if err != nil {
			return nil, err
		}
		parts = append(parts, s)
	}
	msg := strings.Join(parts, sep)
	if attr != Value(None) {
		s, err := Str(t, attr)
		if err != nil {
			return nil, err
		}
		msg = fmt.Sprintf("attribute %s: %s", s, msg)
	}
	return nil, errors.New(msg)
}

func builtinFloat(t *Thread, b *Builtin, args Args) (Value, error) {
	var x Value = Float(0)
	if err := UnpackArgs(t, b.Name(), args, "x?", &x); err != nil {
		return nil, err
	}
	switch v := x.(type) {
	case Bool:
		if v {
			return Float(1), nil
		}
		return Float(0), nil
	case Int:
		return v.Float()
	case Float:
		return v, nil
	case String:
		return parseFloat(string(v))
	}
	return nil, fmt.Errorf("float(): got %s, want string, int, float or bool", x.Type())
}

func parseFloat(s string) (Value, error) {
	switch strings.ToLower(strings.TrimLeft(s, "+-")) {
	case "inf", "infinity":
		if strings.HasPrefix(s, "-") {
			return Float(math.Inf(-1)), nil
		}
		return Float(math.Inf(1)), nil
	case "nan":
		return Float(math.NaN()), nil
	}
	if strings.ContainsAny(s, "_xXpP") {
		return nil, fmt.Errorf("invalid float literal: %s", s)
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		if numErr, ok := err.(*strconv.NumError); ok && numErr.Err == strconv.ErrRange {
			return nil, fmt.Errorf("floating-point number too large: %s", s)
		}
		return nil, fmt.Errorf("invalid float literal: %s", s)
	}
	return Float(f), nil
}

func builtinGetattr(t *Thread, b *Builtin, args Args) (Value, error) {
	var x, defaultValue Value
	var name String
	if err := UnpackArgs(t, b.Name(), args, "x", &x, "name", &name, "default?", &defaultValue); err != nil {
		return nil, err
	}
	v, err := getAttr(t, x, string(name))
	if err != nil {
		var noSuchAttr NoSuchAttrError
		if defaultValue != nil && errors.As(err, &noSuchAttr) {
			return defaultValue, nil
		}
		return nil, err
	}
	return v, nil
}

func builtinHasattr(t *Thread, b *Builtin, args Args) (Value, error) {
	var x Value
	var name String
	if err := UnpackArgs(t, b.Name(), args, "x", &x, "name", &name); err != nil {
		return nil, err
	}
	if _, err := getAttr(t, x, string(name)); err != nil {
		var noSuchAttr NoSuchAttrError
		if errors.As(err, &noSuchAttr) {
			return False, nil
		}
		return nil, err
	}
	return True, nil
}

// builtinHash implements hash(), which uses the same algorithm as
// Java's String.hashCode(). As in Bazel, strings are treated as
// sequences of bytes. The result is part of what Starlark code can
// observe, so it must never change.
func builtinHash(t *Thread, b *Builtin, args Args) (Value, error) {
	var s String
	if err := UnpackArgs(t, b.Name(), args, "value", &s); err != nil {
		return nil, err
	}
	var h int32
	for i := 0; i < len(s); i++ {
		h = 31*h + int32(s[i])
	}
	return MakeInt64(int64(h)), nil
}

func builtinInt(t *Thread, b *Builtin, args Args) (Value, error) {
	var x, base Value
	if err := UnpackArgs(t, b.Name(), args, "x", &x, "base?", &base); err != nil {
		return nil, err
	}
	s, isString := x.(String)
	if base != nil && !isString {
		return nil, errors.New("int() can't convert non-string with explicit base")
	}
	switch v := x.(type) {
	case Bool:
		if v {
			return MakeInt(1), nil
		}
		return MakeInt(0), nil
	case Int:
		return v, nil
	case Float:
		i, ok := truncateFloat(v)
		if !ok {
			return nil, fmt.Errorf("cannot convert float %s to integer", v)
		}
		return i, nil
	case String:
		b := 10
		if base != nil {
			var err error
			if b, err = AsInt[int](base); err != nil {
				return nil, fmt.Errorf("int(): for parameter base: %w", err)
			}
			if b != 0 && (b < 2 || b > 36) {
				return nil, errors.New("int() base must be >= 2 and <= 36")
			}
		}
		return parseInt(string(s), b)
	}
	return nil, fmt.Errorf("int(): got %s, want string, int, float or bool", x.Type())
}

// parseInt parses an integer from a string. If base is zero, it is
// inferred from the prefix of the string.
func parseInt(s string, base int) (Value, error) {
	invalid := fmt.Errorf("invalid literal for int() with base %d: %s", base, syntax.Quote(s))
	digits := s
	negative := false
	if len(digits) > 0 && (digits[0] == '+' || digits[0] == '-') {
		negative = digits[0] == '-'
		digits = digits[1:]
	}
	if len(digits) > 1 && digits[0] == '0' {
		prefixBase := 0
		switch digits[1] | 0x20 {
		case 'x':
			prefixBase = 16
		case 'o':
			prefixBase = 8
		case 'b':
			prefixBase = 2
		}
		if prefixBase != 0 && (base == 0 || base == prefixBase) {
			digits = digits[2:]
			base = prefixBase
		} else if base == 0 {
			// Base 0 does not permit leading zeros.
			if strings.Trim(digits, "0") != "" {
				return nil, invalid
			}
		}
	}
	if base == 0 {
		base = 10
	}
	if digits == "" || strings.ContainsAny(digits, "_+-") {
		return nil, invalid
	}
	i, ok := new(big.Int).SetString(digits, base)
	if !ok {
		return nil, invalid
	}
	if negative {
		i.Neg(i)
	}
	return MakeBigInt(i), nil
}

func builtinLen(t *Thread, b *Builtin, args Args) (Value, error) {
	var x Value
	if err := UnpackArgs(t, b.Name(), args, "x", &x); err != nil {
		return nil, err
	}
	switch v := x.(type) {
	case Indexable:
		n, err := v.Len(t)
		return MakeInt(n), err
	case Sequence:
		n, err := v.Len(t)
		return MakeInt(n), err
	}
	return nil, fmt.Errorf("%s is not iterable", x.Type())
}

func builtinList(t *Thread, b *Builtin, args Args) (Value, error) {
	var x Value
	if err := UnpackArgs(t, b.Name(), args, "x?", &x); err != nil {
		return nil, err
	}
	if x == nil {
		return NewList(nil), nil
	}
	elements, err := Elements(t, x)
	if err != nil {
		return nil, err
	}
	return NewList(elements), nil
}

func builtinMax(t *Thread, b *Builtin, args Args) (Value, error) {
	return minMax(t, b, args, syntax.GT)
}

func builtinMin(t *Thread, b *Builtin, args Args) (Value, error) {
	return minMax(t, b, args, syntax.LT)
}

// minMax implements min() and max(). op is the operator that is used
// to determine whether an element is better than the current one.
func minMax(t *Thread, b *Builtin, args Args, op syntax.Token) (Value, error) {
	var key, defaultValue Value
	for _, arg := range args.Named {
		switch arg.Name {
		case "key":
			if arg.Value != Value(None) {
				key = arg.Value
			}
		case "default":
			defaultValue = arg.Value
		default:
			suggestion := ""
			if nearest := nearestName(arg.Name, []string{"key", "default"}); nearest != "" {
				suggestion = fmt.Sprintf(" (did you mean '%s'?)", nearest)
			}
			return nil, fmt.Errorf("%s() got unexpected keyword argument '%s'%s", b.Name(), arg.Name, suggestion)
		}
	}

	var elements []Value
	switch len(args.Positional) {
	case 0:
		return nil, fmt.Errorf("%s() requires at least one positional argument", b.Name())
	case 1:
		var err error
		if elements, err = Elements(t, args.Positional[0]); err != nil {
			return nil, err
		}
	default:
		if defaultValue != nil {
			return nil, fmt.Errorf("%s() cannot use a default value with multiple positional arguments", b.Name())
		}
		elements = args.Positional
	}
	if len(elements) == 0 {
		if defaultValue != nil {
			return defaultValue, nil
		}
		return nil, fmt.Errorf("%s() arg is an empty sequence", b.Name())
	}

	keyOf := func(v Value) (Value, error) {
		if key == nil {
			return v, nil
		}
		return Call(t, key, Args{Positional: []Value{v}})
	}
	best := elements[0]
	bestKey, err := keyOf(best)
	if err != nil {
		return nil, err
	}
	for _, element := range elements[1:] {
		elementKey, err := keyOf(element)
		if err != nil {
			return nil, err
		}
		better, err := Compare(t, op, elementKey, bestKey)
		if err != nil {
			return nil, err
		}
		if better {
			best, bestKey = element, elementKey
		}
	}
	return best, nil
}

func builtinPrint(t *Thread, b *Builtin, args Args) (Value, error) {
	sep := " "
	for _, arg := range args.Named {
		if arg.Name != "sep" {
			return nil, fmt.Errorf("print() got unexpected keyword argument '%s'", arg.Name)
		}
		s, ok := arg.Value.(String)
		if !ok {
			return nil, fmt.Errorf("print(): for parameter sep: got %s, want string", arg.Value.Type())
		}
		sep = string(s)
	}
	parts := make([]string, 0, len(args.Positional))
	for _, arg := range args.Positional {
		s, err := Str(t, arg)
		if err != nil {
			return nil, err
		}
		parts = append(parts, s)
	}
	if t.Print != nil {
		if err := t.Print(t, strings.Join(parts, sep)); err != nil {
			return nil, err
		}
	}
	return None, nil
}

func builtinRange(t *Thread, b *Builtin, args Args) (Value, error) {
	if err := positionalArgs(b, args, 1, 3); err != nil {
		return nil, err
	}
	values := make([]int, len(args.Positional))
	for i, arg := range args.Positional {
		var err error
		if values[i], err = AsInt[int](arg); err != nil {
			return nil, fmt.Errorf("range(): %w", err)
		}
	}
	r := rangeValue{step: 1}
	switch len(values) {
	case 1:
		r.stop = values[0]
	case 2:
		r.start, r.stop = values[0], values[1]
	case 3:
		r.start, r.stop, r.step = values[0], values[1], values[2]
		if r.step == 0 {
			return nil, errors.New("range() step argument must not be zero")
		}
	}
	return r, nil
}

func builtinRepr(t *Thread, b *Builtin, args Args) (Value, error) {
	var x Value
	if err := UnpackArgs(t, b.Name(), args, "x", &x); err != nil {
		return nil, err
	}
	s, err := Repr(t, x)
	return String(s), err
}

func builtinReversed(t *Thread, b *Builtin, args Args) (Value, error) {
	var x Value
	if err := UnpackArgs(t, b.Name(), args, "sequence", &x); err != nil {
		return nil, err
	}
	if _, ok := x.(Dict); ok {
		return nil, errors.New("reversed(): got dict, want sequence")
	}
	elements, err := Elements(t, x)
	if err != nil {
		return nil, err
	}
	slices.Reverse(elements)
	return NewList(elements), nil
}

func builtinSet(t *Thread, b *Builtin, args Args) (Value, error) {
	var x Value
	if err := UnpackArgs(t, b.Name(), args, "elements?", &x); err != nil {
		return nil, err
	}
	s := NewSet()
	if x != nil {
		elements, err := Elements(t, x)
		if err != nil {
			return nil, fmt.Errorf("set(): %w", err)
		}
		for _, element := range elements {
			if err := s.Add(t, element); err != nil {
				return nil, err
			}
		}
	}
	return s, nil
}

// sortValues sorts values in place, optionally using a key function
// and in reverse order. Sorting is stable.
func sortValues(t *Thread, values []Value, key Value, reverse bool) error {
	keys := values
	if key != nil {
		keys = make([]Value, len(values))
		for i, v := range values {
			k, err := Call(t, key, Args{Positional: []Value{v}})
			if err != nil {
				return err
			}
			keys[i] = k
		}
	}
	indices := make([]int, len(values))
	for i := range indices {
		indices[i] = i
	}
	var sortErr error
	sort.SliceStable(indices, func(i, j int) bool {
		if sortErr != nil {
			return false
		}
		x, y := keys[indices[i]], keys[indices[j]]
		if reverse {
			x, y = y, x
		}
		less, err := Compare(t, syntax.LT, x, y)
		if err != nil {
			sortErr = err
		}
		return less
	})
	if sortErr != nil {
		return sortErr
	}
	sorted := make([]Value, len(values))
	for i, index := range indices {
		sorted[i] = values[index]
	}
	copy(values, sorted)
	return nil
}

func builtinSorted(t *Thread, b *Builtin, args Args) (Value, error) {
	var x, key Value
	reverse := false
	if err := UnpackArgs(t, b.Name(), args, "iterable", &x, "key??", &key, "reverse?", &reverse); err != nil {
		return nil, err
	}
	elements, err := Elements(t, x)
	if err != nil {
		return nil, err
	}
	if err := sortValues(t, elements, key, reverse); err != nil {
		return nil, err
	}
	return NewList(elements), nil
}

func builtinStr(t *Thread, b *Builtin, args Args) (Value, error) {
	var x Value
	if err := UnpackArgs(t, b.Name(), args, "x", &x); err != nil {
		return nil, err
	}
	s, err := Str(t, x)
	return String(s), err
}

func builtinTuple(t *Thread, b *Builtin, args Args) (Value, error) {
	var x Value
	if err := UnpackArgs(t, b.Name(), args, "x?", &x); err != nil {
		return nil, err
	}
	if x == nil {
		return NewTuple(nil), nil
	}
	if tuple, ok := x.(Tuple); ok {
		return tuple, nil
	}
	elements, err := Elements(t, x)
	if err != nil {
		return nil, err
	}
	return NewTuple(elements), nil
}

func builtinType(t *Thread, b *Builtin, args Args) (Value, error) {
	var x Value
	if err := UnpackArgs(t, b.Name(), args, "x", &x); err != nil {
		return nil, err
	}
	return String(x.Type()), nil
}

func builtinZip(t *Thread, b *Builtin, args Args) (Value, error) {
	if len(args.Named) > 0 {
		return nil, fmt.Errorf("zip() got unexpected keyword argument '%s'", args.Named[0].Name)
	}
	sequences := make([][]Value, 0, len(args.Positional))
	shortest := math.MaxInt
	for _, arg := range args.Positional {
		elements, err := Elements(t, arg)
		if err != nil {
			return nil, err
		}
		sequences = append(sequences, elements)
		shortest = min(shortest, len(elements))
	}
	if len(sequences) == 0 {
		return NewList(nil), nil
	}
	result := make([]Value, 0, shortest)
	for i := 0; i < shortest; i++ {
		tuple := make([]Value, 0, len(sequences))
		for _, sequence := range sequences {
			tuple = append(tuple, sequence[i])
		}
		result = append(result, NewTuple(tuple))
	}
	return NewList(result), nil
}
