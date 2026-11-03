package unpack

import (
	"bonanza.build/pkg/starlark/eval"
)

type pointerUnpackerInto[T any] struct {
	UnpackerInto[T]
}

// Pointer is a decorator for an UnpackerInto that assumes that the
// destination for unpacking is a pointer type. Upon successful
// unpacking, the pointer is assigned to allocated memory holding the
// unpacked value.
func Pointer[T any](base UnpackerInto[T]) UnpackerInto[*T] {
	return &pointerUnpackerInto[T]{
		UnpackerInto: base,
	}
}

func (ui *pointerUnpackerInto[T]) UnpackInto(thread *eval.Thread, v eval.Value, dst **T) error {
	var instance T
	if err := ui.UnpackerInto.UnpackInto(thread, v, &instance); err != nil {
		return err
	}
	*dst = &instance
	return nil
}
