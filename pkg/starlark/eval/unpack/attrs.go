package unpack

import (
	"errors"
	"fmt"

	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/syntax"
)

// AttrUnpacker specifies how individual named attributes of a
// struct-like object need to be unpacked.
type AttrUnpacker struct {
	Name     string
	Unpacker eval.Unpacker
}

type attrsUnpackerInto[T any] struct {
	nestedUnpackersCreator func(thread *eval.Thread, dst *T) []AttrUnpacker
}

// Attrs can be used to unpack struct-like objects that have named
// attributes into native structs. Whenever unpacking is performed, a
// callback is invoked that is responsible for yielding new unpackers
// for each of the named attributes that is requested.
func Attrs[T any](nestedUnpackersCreator func(thread *eval.Thread, dst *T) []AttrUnpacker) UnpackerInto[T] {
	return &attrsUnpackerInto[T]{
		nestedUnpackersCreator: nestedUnpackersCreator,
	}
}

func (ui *attrsUnpackerInto[T]) UnpackInto(thread *eval.Thread, v eval.Value, dst *T) error {
	hasAttrs, ok := v.(eval.HasAttrs)
	if !ok {
		return errors.New("value does not have attributes")
	}

	nestedUnpackers := ui.nestedUnpackersCreator(thread, dst)
	for _, nestedUnpacker := range nestedUnpackers {
		nestedV, err := hasAttrs.Attr(thread, nestedUnpacker.Name)
		if err != nil {
			var noSuchAttr eval.NoSuchAttrError
			if errors.As(err, &noSuchAttr) {
				return fmt.Errorf("attribute %#v does not exist", nestedUnpacker.Name)
			}
			return fmt.Errorf("attribute %#v: %w", nestedUnpacker.Name, err)
		}
		if err := nestedUnpacker.Unpacker.Unpack(thread, nestedV); err != nil {
			return fmt.Errorf("attribute %#v: %w", nestedUnpacker.Name, err)
		}
	}
	return nil
}

func (attrsUnpackerInto[T]) Canonicalize(thread *eval.Thread, v eval.Value) (eval.Value, error) {
	// The best we could do is create some replacement struct(), but
	// because this unpacker also works on non-struct objects this
	// may not be desirable.
	return v, nil
}

func (attrsUnpackerInto[T]) GetConcatenationOperator() syntax.Token {
	return 0
}
