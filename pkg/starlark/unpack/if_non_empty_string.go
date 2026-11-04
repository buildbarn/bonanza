package unpack

import (
	"fmt"

	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/syntax"
)

type ifNonEmptyStringUnpackerInto[T any] struct {
	base UnpackerInto[T]
}

// IfNonEmptyString is a decorator for UnpackerInto that only calls into
// the underlying unpacker if the value is a non-empty string. This can
// be of use if the default value of an argument is a string, and the
// empty string is used to denote that a value is unset.
func IfNonEmptyString[T any](base UnpackerInto[T]) UnpackerInto[T] {
	return &ifNonEmptyStringUnpackerInto[T]{
		base: base,
	}
}

func (ui *ifNonEmptyStringUnpackerInto[T]) UnpackInto(thread *eval.Thread, v eval.Value, dst *T) error {
	s, ok, err := eval.AsString(thread, v)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("got %s, want string", v.Type())
	}
	if s == "" {
		return nil
	}
	return ui.base.UnpackInto(thread, v, dst)
}

func (ui *ifNonEmptyStringUnpackerInto[T]) Canonicalize(thread *eval.Thread, v eval.Value) (eval.Value, error) {
	s, ok, err := eval.AsString(thread, v)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("got %s, want string", v.Type())
	}
	if s == "" {
		return eval.String(s), nil
	}
	return ui.base.Canonicalize(thread, v)
}

func (ui *ifNonEmptyStringUnpackerInto[T]) GetConcatenationOperator() syntax.Token {
	return ui.base.GetConcatenationOperator()
}
