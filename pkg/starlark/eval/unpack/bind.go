// Package unpack provides strategies for converting arguments of
// builtins written in Go from Starlark values to native Go types.
//
// It was originally written for go.starlark.net, and ported to
// pkg/starlark/eval.
package unpack

import (
	"bonanza.build/pkg/starlark/eval"
)

// UnpackerInto implements a strategy for unpacking a function argument
// of a Starlark function into a Go type.
type UnpackerInto[T any] interface {
	Canonicalizer
	UnpackInto(thread *eval.Thread, v eval.Value, dst *T) error
}

type boundUnpacker[T any] struct {
	dst      *T
	unpacker UnpackerInto[T]
}

// Bind an UnpackerInto to a variable, so that it can unpack a function
// argument and store it at a desired location. The result can be
// passed to eval.UnpackArgs().
func Bind[T any](dst *T, unpacker UnpackerInto[T]) eval.Unpacker {
	return &boundUnpacker[T]{
		dst:      dst,
		unpacker: unpacker,
	}
}

func (u *boundUnpacker[T]) Unpack(thread *eval.Thread, v eval.Value) error {
	return u.unpacker.UnpackInto(thread, v, u.dst)
}
