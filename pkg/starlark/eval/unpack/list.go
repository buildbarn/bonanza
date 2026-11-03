package unpack

import (
	"fmt"

	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/syntax"
)

type listUnpackerInto[T any] struct {
	base UnpackerInto[T]
}

// List is capable of unpacking a Starlark list or tuple value into a
// Go native slice.
func List[T any](base UnpackerInto[T]) UnpackerInto[[]T] {
	return &listUnpackerInto[T]{
		base: base,
	}
}

// listElements returns the elements of a list or tuple.
func listElements(thread *eval.Thread, v eval.Value) ([]eval.Value, error) {
	switch v.(type) {
	case eval.List, eval.Tuple:
		return eval.Elements(thread, v)
	}
	return nil, fmt.Errorf("got %s, want list", v.Type())
}

func (ui *listUnpackerInto[T]) UnpackInto(thread *eval.Thread, v eval.Value, dst *[]T) error {
	elements, err := listElements(thread, v)
	if err != nil {
		return err
	}
	l := make([]T, len(elements))
	for i, element := range elements {
		if err := ui.base.UnpackInto(thread, element, &l[i]); err != nil {
			return fmt.Errorf("at index %d: %w", i, err)
		}
	}
	*dst = l
	return nil
}

func (ui *listUnpackerInto[T]) Canonicalize(thread *eval.Thread, v eval.Value) (eval.Value, error) {
	elements, err := listElements(thread, v)
	if err != nil {
		return nil, err
	}
	l := make([]eval.Value, 0, len(elements))
	for i, element := range elements {
		canonicalizedElement, err := ui.base.Canonicalize(thread, element)
		if err != nil {
			return nil, fmt.Errorf("at index %d: %w", i, err)
		}
		l = append(l, canonicalizedElement)
	}
	return eval.NewList(l), nil
}

func (listUnpackerInto[T]) GetConcatenationOperator() syntax.Token {
	return syntax.PLUS
}
