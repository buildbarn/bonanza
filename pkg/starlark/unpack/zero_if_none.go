package unpack

import (
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/syntax"
)

type zeroIfNoneUnpackerInto[T any] struct {
	base UnpackerInto[T]
}

// ZeroIfNone is a decorator for UnpackerInto that sets the destination
// to its zero value if the value is None, and calls into the underlying
// unpacker otherwise.
//
// Contrary to IfNotNone, which leaves the destination untouched, this
// decorator makes it possible to distinguish None from the argument
// being omitted, by initializing the destination to a non-zero
// value prior to unpacking (e.g., a pointer to a placeholder).
func ZeroIfNone[T any](base UnpackerInto[T]) UnpackerInto[T] {
	return &zeroIfNoneUnpackerInto[T]{
		base: base,
	}
}

func (ui *zeroIfNoneUnpackerInto[T]) UnpackInto(thread *eval.Thread, v eval.Value, dst *T) error {
	if v == eval.None {
		var zero T
		*dst = zero
		return nil
	}
	return ui.base.UnpackInto(thread, v, dst)
}

func (ui *zeroIfNoneUnpackerInto[T]) Canonicalize(thread *eval.Thread, v eval.Value) (eval.Value, error) {
	if v == eval.None {
		return v, nil
	}
	return ui.base.Canonicalize(thread, v)
}

func (ui *zeroIfNoneUnpackerInto[T]) GetConcatenationOperator() syntax.Token {
	return ui.base.GetConcatenationOperator()
}
