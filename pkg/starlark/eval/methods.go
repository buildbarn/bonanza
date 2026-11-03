package eval

import (
	"errors"
	"fmt"
)

// methods of the core types, keyed by type name.
var methods map[string]map[string]*Builtin

func init() {
	// Initialized in init() to break the cycle between the method
	// tables and the functions that use them.
	methods = map[string]map[string]*Builtin{
		"dict":   dictMethods,
		"list":   listMethods,
		"set":    setMethods,
		"string": stringMethods,
	}
}

// lookupMethod returns a method of a value of a core type, bound to the
// value.
func lookupMethod(x Value, name string) (Value, bool) {
	if method, ok := methods[x.Type()][name]; ok {
		return method.BindReceiver(x), true
	}
	return nil, false
}

var listMethods = map[string]*Builtin{
	"append": NewBuiltin("append", listAppend),
	"clear":  NewBuiltin("clear", listClear),
	"extend": NewBuiltin("extend", listExtend),
	"index":  NewBuiltin("index", listIndex),
	"insert": NewBuiltin("insert", listInsert),
	"pop":    NewBuiltin("pop", listPop),
	"remove": NewBuiltin("remove", listRemove),
}

func listAppend(t *Thread, b *Builtin, args Args) (Value, error) {
	var x Value
	if err := unpackArgs(b.Name(), args, "item", &x); err != nil {
		return nil, err
	}
	return None, b.Receiver().(List).Append(t, x)
}

func listClear(t *Thread, b *Builtin, args Args) (Value, error) {
	if err := noArgs(b, args); err != nil {
		return nil, err
	}
	return None, b.Receiver().(List).Clear(t)
}

func listExtend(t *Thread, b *Builtin, args Args) (Value, error) {
	var x Value
	if err := unpackArgs(b.Name(), args, "items", &x); err != nil {
		return nil, err
	}
	l := b.Receiver().(List)
	elements, err := Elements(t, x)
	if err != nil {
		return nil, err
	}
	for _, element := range elements {
		if err := l.Append(t, element); err != nil {
			return nil, err
		}
	}
	return None, nil
}

func listIndex(t *Thread, b *Builtin, args Args) (Value, error) {
	var x, start, end Value
	if err := unpackArgs(b.Name(), args, "x", &x, "start?", &start, "end?", &end); err != nil {
		return nil, err
	}
	l := b.Receiver().(List)
	n, err := l.Len(t)
	if err != nil {
		return nil, err
	}
	i, j, err := sliceIndices(start, end, n)
	if err != nil {
		return nil, err
	}
	for ; i < j; i++ {
		element, err := l.Index(t, i)
		if err != nil {
			return nil, err
		}
		if equal, err := Equal(t, element, x); err != nil {
			return nil, err
		} else if equal {
			return MakeInt(i), nil
		}
	}
	s, err := Repr(t, x)
	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("index(): value %s not found in list", s)
}

func listInsert(t *Thread, b *Builtin, args Args) (Value, error) {
	var index int
	var x Value
	if err := unpackArgs(b.Name(), args, "index", &index, "item", &x); err != nil {
		return nil, err
	}
	l := b.Receiver().(List)
	n, err := l.Len(t)
	if err != nil {
		return nil, err
	}
	if index < 0 {
		index += n
	}
	return None, l.Insert(t, min(max(index, 0), n), x)
}

func listPop(t *Thread, b *Builtin, args Args) (Value, error) {
	index := -1
	if err := unpackArgs(b.Name(), args, "i?", &index); err != nil {
		return nil, err
	}
	l := b.Receiver().(List)
	n, err := l.Len(t)
	if err != nil {
		return nil, err
	}
	i := index
	if i < 0 {
		i += n
	}
	if i < 0 || i >= n {
		return nil, fmt.Errorf("pop(): index out of range (index is %d, but sequence has %d elements)", index, n)
	}
	index = i
	x, err := l.Index(t, index)
	if err != nil {
		return nil, err
	}
	return x, l.RemoveIndex(t, index)
}

func listRemove(t *Thread, b *Builtin, args Args) (Value, error) {
	var x Value
	if err := unpackArgs(b.Name(), args, "x", &x); err != nil {
		return nil, err
	}
	l := b.Receiver().(List)
	n, err := l.Len(t)
	if err != nil {
		return nil, err
	}
	for i := 0; i < n; i++ {
		element, err := l.Index(t, i)
		if err != nil {
			return nil, err
		}
		if equal, err := Equal(t, element, x); err != nil {
			return nil, err
		} else if equal {
			return None, l.RemoveIndex(t, i)
		}
	}
	s, err := Repr(t, x)
	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("remove(): element %s not found", s)
}

var dictMethods = map[string]*Builtin{
	"clear":      NewBuiltin("clear", dictClear),
	"get":        NewBuiltin("get", dictGet),
	"items":      NewBuiltin("items", dictItems),
	"keys":       NewBuiltin("keys", dictKeys),
	"pop":        NewBuiltin("pop", dictPop),
	"popitem":    NewBuiltin("popitem", dictPopitem),
	"setdefault": NewBuiltin("setdefault", dictSetdefault),
	"update":     NewBuiltin("update", dictUpdate),
	"values":     NewBuiltin("values", dictValues),
}

func dictClear(t *Thread, b *Builtin, args Args) (Value, error) {
	if err := noArgs(b, args); err != nil {
		return nil, err
	}
	return None, b.Receiver().(Dict).Clear(t)
}

func dictGet(t *Thread, b *Builtin, args Args) (Value, error) {
	var key Value
	var defaultValue Value = None
	if err := unpackArgs(b.Name(), args, "key", &key, "default?", &defaultValue); err != nil {
		return nil, err
	}
	v, found, err := b.Receiver().(Dict).Get(t, key)
	if err != nil || !found {
		return defaultValue, err
	}
	return v, nil
}

func dictItems(t *Thread, b *Builtin, args Args) (Value, error) {
	if err := noArgs(b, args); err != nil {
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
	if err := noArgs(b, args); err != nil {
		return nil, err
	}
	keys, err := Elements(t, b.Receiver())
	return NewList(keys), err
}

func dictPop(t *Thread, b *Builtin, args Args) (Value, error) {
	var key, defaultValue Value
	if err := unpackArgs(b.Name(), args, "key", &key, "default?", &defaultValue); err != nil {
		return nil, err
	}
	v, found, err := b.Receiver().(Dict).Delete(t, key)
	if err != nil {
		return nil, err
	}
	if !found {
		if defaultValue != nil {
			return defaultValue, nil
		}
		s, err := Repr(t, key)
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("pop(): key %s not found", s)
	}
	return v, nil
}

func dictPopitem(t *Thread, b *Builtin, args Args) (Value, error) {
	if err := noArgs(b, args); err != nil {
		return nil, err
	}
	d := b.Receiver().(Dict)
	var key, value Value
	var errIter error
	for k, v := range d.Items(t, &errIter) {
		key, value = k, v
		break
	}
	if errIter != nil {
		return nil, errIter
	}
	if key == nil {
		return nil, errors.New("popitem(): dictionary is empty")
	}
	if _, _, err := d.Delete(t, key); err != nil {
		return nil, err
	}
	return NewTuple([]Value{key, value}), nil
}

func dictSetdefault(t *Thread, b *Builtin, args Args) (Value, error) {
	var key Value
	var defaultValue Value = None
	if err := unpackArgs(b.Name(), args, "key", &key, "default?", &defaultValue); err != nil {
		return nil, err
	}
	d := b.Receiver().(Dict)
	if v, found, err := d.Get(t, key); err != nil {
		return nil, err
	} else if found {
		return v, nil
	}
	return defaultValue, d.SetKey(t, key, defaultValue)
}

func dictUpdate(t *Thread, b *Builtin, args Args) (Value, error) {
	if len(args.Positional) > 1 {
		return nil, fmt.Errorf("update() takes at most 1 positional argument (%d given)", len(args.Positional))
	}
	if err := updateDict(t, b.Receiver().(Dict), args); err != nil {
		return nil, fmt.Errorf("update(): %w", err)
	}
	return None, nil
}

func dictValues(t *Thread, b *Builtin, args Args) (Value, error) {
	if err := noArgs(b, args); err != nil {
		return nil, err
	}
	var values []Value
	var errIter error
	for _, v := range b.Receiver().(Dict).Items(t, &errIter) {
		values = append(values, v)
	}
	return NewList(values), errIter
}
