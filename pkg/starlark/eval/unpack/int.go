package unpack

import (
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/syntax"

	"golang.org/x/exp/constraints"
)

type intUnpackerInto[T constraints.Integer] struct{}

// Int is capable of unpacking Starlark integer values to a Go native
// integer type of choice.
func Int[T constraints.Integer]() UnpackerInto[T] {
	return intUnpackerInto[T]{}
}

func (intUnpackerInto[T]) UnpackInto(thread *eval.Thread, v eval.Value, dst *T) error {
	i, err := eval.AsInt[T](v)
	if err != nil {
		return err
	}
	*dst = i
	return nil
}

func (ui intUnpackerInto[T]) Canonicalize(thread *eval.Thread, v eval.Value) (eval.Value, error) {
	var dst T
	return v, ui.UnpackInto(thread, v, &dst)
}

func (intUnpackerInto[T]) GetConcatenationOperator() syntax.Token {
	// Even though addition isn't necessarily the same thing as
	// concatenation, Bazel seems to allow it.
	return syntax.PLUS
}
