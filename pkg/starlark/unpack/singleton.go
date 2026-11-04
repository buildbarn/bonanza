package unpack

import (
	"bonanza.build/pkg/starlark/eval"
)

type singletonUnpackerInto[T any] struct {
	UnpackerInto[T]
}

// Singleton is capable of unpacking a value and placing it in a slice
// containing a single element. This unpacker is typically used in
// combination with Or() in functions having arguments that can either
// be scalar or a list (e.g., for backward compatibility).
func Singleton[T any](base UnpackerInto[T]) UnpackerInto[[]T] {
	return &singletonUnpackerInto[T]{
		UnpackerInto: base,
	}
}

func (ui *singletonUnpackerInto[T]) UnpackInto(thread *eval.Thread, v eval.Value, dst *[]T) error {
	var instance [1]T
	if err := ui.UnpackerInto.UnpackInto(thread, v, &instance[0]); err != nil {
		return err
	}
	*dst = instance[:]
	return nil
}

func (ui *singletonUnpackerInto[T]) Canonicalize(thread *eval.Thread, v eval.Value) (eval.Value, error) {
	element, err := ui.UnpackerInto.Canonicalize(thread, v)
	if err != nil {
		return nil, err
	}
	return eval.NewList([]eval.Value{element}), nil
}
