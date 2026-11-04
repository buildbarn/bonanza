package unpack

import (
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/syntax"
)

type orUnpackerInto[T any] struct {
	unpackers []UnpackerInto[T]
}

// Or performs unpacking of a value by calling into a series of
// unpackers. This can be useful if an argument of a Starlark function
// permits values of multiple types (e.g., either a scalar value or a
// list).
//
// This unpacker can be used in combination with the Decay() function to
// accept multiple types that are incompatible at the Go type level.
func Or[T any](unpackers []UnpackerInto[T]) UnpackerInto[T] {
	return &orUnpackerInto[T]{
		unpackers: unpackers,
	}
}

func (ui *orUnpackerInto[T]) UnpackInto(thread *eval.Thread, v eval.Value, dst *T) error {
	unpackers := ui.unpackers
	for {
		err := unpackers[0].UnpackInto(thread, v, dst)
		unpackers = unpackers[1:]
		if err == nil || len(unpackers) == 0 {
			return err
		}
	}
}

func (ui *orUnpackerInto[T]) Canonicalize(thread *eval.Thread, v eval.Value) (eval.Value, error) {
	unpackers := ui.unpackers
	for {
		canonicalized, err := unpackers[0].Canonicalize(thread, v)
		unpackers = unpackers[1:]
		if err == nil || len(unpackers) == 0 {
			return canonicalized, err
		}
	}
}

func (ui *orUnpackerInto[T]) GetConcatenationOperator() syntax.Token {
	o := ui.unpackers[0].GetConcatenationOperator()
	if o != 0 {
		for _, unpacker := range ui.unpackers[1:] {
			if unpacker.GetConcatenationOperator() != o {
				return 0
			}
		}
	}
	return o
}
