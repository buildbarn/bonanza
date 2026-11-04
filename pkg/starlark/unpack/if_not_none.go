package unpack

import (
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/syntax"
)

type ifNotNoneUnpackerInto[T any] struct {
	base UnpackerInto[T]
}

// IfNotNone is a decorator for UnpackerInto that only calls into the
// underlying unpacker if the value is not None. This can be of use if
// None is used to denote that a value is unset.
func IfNotNone[T any](base UnpackerInto[T]) UnpackerInto[T] {
	return &ifNotNoneUnpackerInto[T]{
		base: base,
	}
}

func (ui *ifNotNoneUnpackerInto[T]) UnpackInto(thread *eval.Thread, v eval.Value, dst *T) error {
	if v == eval.None {
		return nil
	}
	return ui.base.UnpackInto(thread, v, dst)
}

func (ui *ifNotNoneUnpackerInto[T]) Canonicalize(thread *eval.Thread, v eval.Value) (eval.Value, error) {
	if v == eval.None {
		return v, nil
	}
	return ui.base.Canonicalize(thread, v)
}

func (ui *ifNotNoneUnpackerInto[T]) GetConcatenationOperator() syntax.Token {
	return ui.base.GetConcatenationOperator()
}

type ifNotNoneCanonicalizer struct {
	base Canonicalizer
}

// IfNotNoneCanonicalizer is a decorator for Canonicalizer that only calls
// into the underlying unpacker if the value is not None. This can be of
// use if None is used to denote that a value is unset.
func IfNotNoneCanonicalizer(base Canonicalizer) Canonicalizer {
	return &ifNotNoneCanonicalizer{
		base: base,
	}
}

func (ui *ifNotNoneCanonicalizer) Canonicalize(thread *eval.Thread, v eval.Value) (eval.Value, error) {
	if v == eval.None {
		return v, nil
	}
	return ui.base.Canonicalize(thread, v)
}

func (ui *ifNotNoneCanonicalizer) GetConcatenationOperator() syntax.Token {
	return ui.base.GetConcatenationOperator()
}
