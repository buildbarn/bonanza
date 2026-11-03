package eval

import (
	"fmt"
	"strings"

	"golang.org/x/exp/constraints"
)

// Unpacker is implemented by types that provide custom logic for
// unpacking arguments of builtins. It can be passed to UnpackArgs()
// instead of a pointer to a variable.
type Unpacker interface {
	Unpack(t *Thread, v Value) error
}

// UnpackArgs assigns the arguments of a call to a builtin to Go
// variables. pairs is an alternating list of parameter names and
// pointers to variables, or implementations of Unpacker.
//
// A parameter name ending in "?" indicates an optional parameter. A
// name ending in "??" indicates an optional parameter for which None is
// treated as if the argument was absent. All parameters following an
// optional parameter are optional as well.
//
// The type of each variable determines how the argument is checked:
// *Value accepts any value, while *String, *Int, *int, *bool, *Callable,
// *Iterable, *Dict and *Mapping require a value of that type.
func UnpackArgs(t *Thread, fnName string, args Args, pairs ...any) error {
	numParams := len(pairs) / 2
	names := make([]string, numParams)
	optional := make([]bool, numParams)
	noneIsAbsent := make([]bool, numParams)
	seenOptional := false
	for i := 0; i < numParams; i++ {
		name := pairs[2*i].(string)
		if strings.HasSuffix(name, "??") {
			name = name[:len(name)-2]
			noneIsAbsent[i] = true
			seenOptional = true
		} else if strings.HasSuffix(name, "?") {
			name = name[:len(name)-1]
			seenOptional = true
		}
		names[i] = name
		optional[i] = seenOptional
	}

	if len(args.Positional) > numParams {
		return fmt.Errorf("%s() accepts no more than %d positional argument%s (%d given)", fnName, numParams, plural(numParams), len(args.Positional))
	}
	provided := make([]bool, numParams)
	assign := func(i int, v Value) error {
		if noneIsAbsent[i] && v == Value(None) {
			return nil
		}
		if err := unpackOneArg(t, v, pairs[2*i+1]); err != nil {
			return fmt.Errorf("%s(): for parameter %s: %w", fnName, names[i], err)
		}
		return nil
	}
	for i, v := range args.Positional {
		provided[i] = true
		if err := assign(i, v); err != nil {
			return err
		}
	}

named:
	for _, arg := range args.Named {
		for i, name := range names {
			if name == arg.Name {
				if provided[i] {
					return fmt.Errorf("%s() got multiple values for parameter '%s'", fnName, name)
				}
				provided[i] = true
				if err := assign(i, arg.Value); err != nil {
					return err
				}
				continue named
			}
		}
		suggestion := ""
		if nearest := nearestName(arg.Name, names); nearest != "" {
			suggestion = fmt.Sprintf(" (did you mean '%s'?)", nearest)
		}
		return fmt.Errorf("%s() got unexpected keyword argument '%s'%s", fnName, arg.Name, suggestion)
	}

	for i, name := range names {
		if !provided[i] && !optional[i] {
			return fmt.Errorf("%s() missing required argument '%s'", fnName, name)
		}
	}
	return nil
}

func unpackOneArg(t *Thread, v Value, ptr any) error {
	switch p := ptr.(type) {
	case Unpacker:
		return p.Unpack(t, v)
	case *Value:
		*p = v
	case *String:
		s, ok := v.(String)
		if !ok {
			return fmt.Errorf("got %s, want string", v.Type())
		}
		*p = s
	case *Int:
		i, ok := v.(Int)
		if !ok {
			return fmt.Errorf("got %s, want int", v.Type())
		}
		*p = i
	case *int:
		i, err := AsInt[int](v)
		if err != nil {
			return err
		}
		*p = i
	case *bool:
		b, ok := v.(Bool)
		if !ok {
			return fmt.Errorf("got %s, want bool", v.Type())
		}
		*p = bool(b)
	case *Callable:
		c, ok := v.(Callable)
		if !ok {
			return fmt.Errorf("got %s, want callable", v.Type())
		}
		*p = c
	case *Iterable:
		it, ok := v.(Iterable)
		if !ok {
			return fmt.Errorf("got %s, want iterable", v.Type())
		}
		*p = it
	case *Dict:
		d, ok := v.(Dict)
		if !ok {
			return fmt.Errorf("got %s, want dict", v.Type())
		}
		*p = d
	case *Mapping:
		m, ok := v.(Mapping)
		if !ok {
			return fmt.Errorf("got %s, want dict", v.Type())
		}
		*p = m
	default:
		panic(fmt.Sprintf("unsupported argument type %T", ptr))
	}
	return nil
}

// AsString returns the contents of a Starlark string.
func AsString(v Value) (string, bool) {
	s, ok := v.(String)
	return string(s), ok
}

// AsInt converts a Starlark int to a Go integer type of choice, failing
// if it is out of range.
func AsInt[T constraints.Integer](v Value) (T, error) {
	i, ok := v.(Int)
	if !ok {
		return 0, fmt.Errorf("got %s, want int", v.Type())
	}
	if i64, ok := i.Int64(); ok {
		if converted := T(i64); int64(converted) == i64 && (converted < 0) == (i64 < 0) {
			return converted, nil
		}
	} else if u64, ok := i.Uint64(); ok {
		// Value exceeds the range of int64. Even though the
		// conversion may round trip for signed types, the
		// result would be negative.
		if converted := T(u64); uint64(converted) == u64 && converted >= 0 {
			return converted, nil
		}
	}
	return 0, fmt.Errorf("%s out of range", i)
}

// noArgs checks that a method is called without any arguments.
func noArgs(b *Builtin, args Args) error {
	if len(args.Positional) > 0 || len(args.Named) > 0 {
		return fmt.Errorf("%s() does not accept any arguments", b.Name())
	}
	return nil
}
