package unpack

import (
	"fmt"

	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/syntax"
)

type typeUnpackerInto[T eval.Value] struct {
	name string
}

// Type is capable of unpacking values of an exact Go type. This can be
// used to validate that an argument corresponds to a custom Starlark
// type written in Go (e.g., a struct).
func Type[T eval.Value](name string) UnpackerInto[T] {
	return &typeUnpackerInto[T]{
		name: name,
	}
}

func (ui *typeUnpackerInto[T]) UnpackInto(thread *eval.Thread, v eval.Value, dst *T) error {
	if typedV, ok := v.(T); ok {
		*dst = typedV
		return nil
	}
	return fmt.Errorf("got %s, want %s", v.Type(), ui.name)
}

func (ui *typeUnpackerInto[T]) Canonicalize(thread *eval.Thread, v eval.Value) (eval.Value, error) {
	if _, ok := v.(T); ok {
		return v, nil
	}
	return nil, fmt.Errorf("got %s, want %s", v.Type(), ui.name)
}

func (typeUnpackerInto[T]) GetConcatenationOperator() syntax.Token {
	return 0
}
