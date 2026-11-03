package eval

import (
	"errors"
	"fmt"
	"maps"
	"strings"
)

// The functions below form a minimal universe, which is sufficient to
// test the virtual machine. The remainder of the standard library is
// provided separately.

// CoreUniverse returns the names provided by the core of the language
// that are implemented by this package.
func CoreUniverse() map[string]Value {
	return maps.Clone(coreUniverse)
}

var coreUniverse = map[string]Value{
	"None":  None,
	"True":  True,
	"False": False,
	"bool":  NewBuiltin("bool", builtinBool),
	"dict":  NewBuiltin("dict", builtinDict),
	"fail":  NewBuiltin("fail", builtinFail),
	"len":   NewBuiltin("len", builtinLen),
	"list":  NewBuiltin("list", builtinList),
	"print": NewBuiltin("print", builtinPrint),
	"repr":  NewBuiltin("repr", builtinRepr),
	"str":   NewBuiltin("str", builtinStr),
	"tuple": NewBuiltin("tuple", builtinTuple),
	"type":  NewBuiltin("type", builtinType),
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

func builtinBool(t *Thread, b *Builtin, args Args) (Value, error) {
	if err := positionalArgs(b, args, 0, 1); err != nil {
		return nil, err
	}
	if len(args.Positional) == 0 {
		return False, nil
	}
	truth, err := args.Positional[0].Truth(t)
	return Bool(truth), err
}

func builtinDict(t *Thread, b *Builtin, args Args) (Value, error) {
	if len(args.Positional) > 1 {
		return nil, fmt.Errorf("dict() takes at most 1 positional argument (%d given)", len(args.Positional))
	}
	d := NewDict()
	if len(args.Positional) == 1 {
		if err := updateDict(t, d, args.Positional[0]); err != nil {
			return nil, fmt.Errorf("dict(): %w", err)
		}
	}
	for _, arg := range args.Named {
		if err := d.SetKey(t, String(arg.Name), arg.Value); err != nil {
			return nil, err
		}
	}
	return d, nil
}

// updateDict adds the entries of a mapping, or an iterable of pairs,
// to a dict.
func updateDict(t *Thread, d Dict, updates Value) error {
	if other, ok := updates.(Dict); ok {
		var errIter error
		for k, v := range other.Items(t, &errIter) {
			if err := d.SetKey(t, k, v); err != nil {
				return err
			}
		}
		return errIter
	}
	elements, err := Elements(t, updates)
	if err != nil {
		return err
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
	return nil
}

// joinArgs converts the positional arguments of print() and fail() to
// strings, and joins them using a separator.
func joinArgs(t *Thread, b *Builtin, args Args) (string, error) {
	sep := " "
	for _, arg := range args.Named {
		if arg.Name != "sep" {
			return "", fmt.Errorf("%s() got unexpected keyword argument '%s'", b.Name(), arg.Name)
		}
		s, ok := arg.Value.(String)
		if !ok {
			return "", fmt.Errorf("%s(): for parameter sep: got %s, want string", b.Name(), arg.Value.Type())
		}
		sep = string(s)
	}
	parts := make([]string, 0, len(args.Positional))
	for _, arg := range args.Positional {
		s, err := Str(t, arg)
		if err != nil {
			return "", err
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, sep), nil
}

func builtinFail(t *Thread, b *Builtin, args Args) (Value, error) {
	msg, err := joinArgs(t, b, args)
	if err != nil {
		return nil, err
	}
	return nil, errors.New(msg)
}

func builtinLen(t *Thread, b *Builtin, args Args) (Value, error) {
	if err := positionalArgs(b, args, 1, 1); err != nil {
		return nil, err
	}
	switch x := args.Positional[0].(type) {
	case Indexable:
		n, err := x.Len(t)
		return MakeInt(n), err
	case Sequence:
		n, err := x.Len(t)
		return MakeInt(n), err
	}
	return nil, fmt.Errorf("%s is not iterable", args.Positional[0].Type())
}

func builtinList(t *Thread, b *Builtin, args Args) (Value, error) {
	if err := positionalArgs(b, args, 0, 1); err != nil {
		return nil, err
	}
	if len(args.Positional) == 0 {
		return NewList(nil), nil
	}
	elements, err := Elements(t, args.Positional[0])
	if err != nil {
		return nil, err
	}
	return NewList(elements), nil
}

func builtinPrint(t *Thread, b *Builtin, args Args) (Value, error) {
	msg, err := joinArgs(t, b, args)
	if err != nil {
		return nil, err
	}
	if t.Print != nil {
		if err := t.Print(t, msg); err != nil {
			return nil, err
		}
	}
	return None, nil
}

func builtinRepr(t *Thread, b *Builtin, args Args) (Value, error) {
	if err := positionalArgs(b, args, 1, 1); err != nil {
		return nil, err
	}
	s, err := Repr(t, args.Positional[0])
	return String(s), err
}

func builtinStr(t *Thread, b *Builtin, args Args) (Value, error) {
	if err := positionalArgs(b, args, 1, 1); err != nil {
		return nil, err
	}
	s, err := Str(t, args.Positional[0])
	return String(s), err
}

func builtinTuple(t *Thread, b *Builtin, args Args) (Value, error) {
	if err := positionalArgs(b, args, 0, 1); err != nil {
		return nil, err
	}
	if len(args.Positional) == 0 {
		return NewTuple(nil), nil
	}
	elements, err := Elements(t, args.Positional[0])
	if err != nil {
		return nil, err
	}
	return NewTuple(elements), nil
}

func builtinType(_ *Thread, b *Builtin, args Args) (Value, error) {
	if err := positionalArgs(b, args, 1, 1); err != nil {
		return nil, err
	}
	return String(args.Positional[0].Type()), nil
}

// methods of the core types implemented by this package, keyed by
// type name.
var methods = map[string]map[string]*Builtin{
	"dict": {
		"get":    NewBuiltin("get", dictGet),
		"items":  NewBuiltin("items", dictItems),
		"keys":   NewBuiltin("keys", dictKeys),
		"values": NewBuiltin("values", dictValues),
	},
	"list": {
		"append": NewBuiltin("append", listAppend),
		"extend": NewBuiltin("extend", listExtend),
	},
}

// lookupMethod returns a method of a value of a core type, bound to the
// value.
func lookupMethod(x Value, name string) (Value, bool) {
	if method, ok := methods[x.Type()][name]; ok {
		return method.BindReceiver(x), true
	}
	return nil, false
}

func dictGet(t *Thread, b *Builtin, args Args) (Value, error) {
	var key, defaultValue Value = nil, None
	if len(args.Positional) > 0 {
		key = args.Positional[0]
	}
	if len(args.Positional) > 1 {
		defaultValue = args.Positional[1]
	}
	for _, arg := range args.Named {
		switch arg.Name {
		case "key":
			key = arg.Value
		case "default":
			defaultValue = arg.Value
		default:
			return nil, fmt.Errorf("get() got unexpected keyword argument '%s'", arg.Name)
		}
	}
	if key == nil || len(args.Positional) > 2 {
		return nil, errors.New("get() takes 1 or 2 arguments")
	}
	v, found, err := b.Receiver().(Dict).Get(t, key)
	if err != nil || !found {
		return defaultValue, err
	}
	return v, nil
}

func dictItems(t *Thread, b *Builtin, args Args) (Value, error) {
	if err := positionalArgs(b, args, 0, 0); err != nil {
		return nil, err
	}
	var items []Value
	var errIter error
	for k, v := range b.Receiver().(Dict).Items(t, &errIter) {
		items = append(items, NewTuple([]Value{k, v}))
	}
	return NewList(items), errIter
}

func dictKeys(t *Thread, b *Builtin, args Args) (Value, error) {
	if err := positionalArgs(b, args, 0, 0); err != nil {
		return nil, err
	}
	keys, err := Elements(t, b.Receiver())
	return NewList(keys), err
}

func dictValues(t *Thread, b *Builtin, args Args) (Value, error) {
	if err := positionalArgs(b, args, 0, 0); err != nil {
		return nil, err
	}
	var values []Value
	var errIter error
	for _, v := range b.Receiver().(Dict).Items(t, &errIter) {
		values = append(values, v)
	}
	return NewList(values), errIter
}

func listAppend(t *Thread, b *Builtin, args Args) (Value, error) {
	if err := positionalArgs(b, args, 1, 1); err != nil {
		return nil, err
	}
	return None, b.Receiver().(List).Append(t, args.Positional[0])
}

func listExtend(t *Thread, b *Builtin, args Args) (Value, error) {
	if err := positionalArgs(b, args, 1, 1); err != nil {
		return nil, err
	}
	if _, err := inplaceAdd(t, b.Receiver(), args.Positional[0]); err != nil {
		return nil, err
	}
	return None, nil
}
