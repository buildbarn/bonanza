package unpack

import (
	"fmt"

	"bonanza.build/pkg/starlark/eval"
)

type stringerUnpackerInto[T fmt.Stringer] struct {
	UnpackerInto[T]
}

// Stringer calls String() on the unpacked value and preserves that
// instead. This is useful if labels, URLs, etc. need to be captured in
// string form only.
func Stringer[T fmt.Stringer](base UnpackerInto[T]) UnpackerInto[string] {
	return &stringerUnpackerInto[T]{
		UnpackerInto: base,
	}
}

func (ui *stringerUnpackerInto[T]) UnpackInto(thread *eval.Thread, v eval.Value, dst *string) error {
	var instance T
	if err := ui.UnpackerInto.UnpackInto(thread, v, &instance); err != nil {
		return err
	}
	*dst = instance.String()
	return nil
}
