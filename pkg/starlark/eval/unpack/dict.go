package unpack

import (
	"errors"
	"fmt"

	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/syntax"
)

type dictUnpackerInto[TKey comparable, TValue any] struct {
	keyUnpacker   UnpackerInto[TKey]
	valueUnpacker UnpackerInto[TValue]
}

// Dict is capable of unpacking dicts declared in Starlark code to a Go
// native map.
func Dict[TKey comparable, TValue any](keyUnpacker UnpackerInto[TKey], valueUnpacker UnpackerInto[TValue]) UnpackerInto[map[TKey]TValue] {
	return &dictUnpackerInto[TKey, TValue]{
		keyUnpacker:   keyUnpacker,
		valueUnpacker: valueUnpacker,
	}
}

func (ui *dictUnpackerInto[TKey, TValue]) UnpackInto(thread *eval.Thread, v eval.Value, dst *map[TKey]TValue) error {
	dict, ok := v.(eval.Dict)
	if !ok {
		return fmt.Errorf("got %s, want dict", v.Type())
	}

	d := map[TKey]TValue{}
	var errIter error
	for key, value := range dict.Items(thread, &errIter) {
		var unpackedKey TKey
		if err := ui.keyUnpacker.UnpackInto(thread, key, &unpackedKey); err != nil {
			return err
		}
		var unpackedValue TValue
		if err := ui.valueUnpacker.UnpackInto(thread, value, &unpackedValue); err != nil {
			return err
		}
		if _, ok := d[unpackedKey]; ok {
			return errors.New("dict contains duplicate keys")
		}
		d[unpackedKey] = unpackedValue
	}
	if errIter != nil {
		return errIter
	}
	*dst = d
	return nil
}

func (ui *dictUnpackerInto[TKey, TValue]) Canonicalize(thread *eval.Thread, v eval.Value) (eval.Value, error) {
	dict, ok := v.(eval.Dict)
	if !ok {
		return nil, fmt.Errorf("got %s, want dict", v.Type())
	}

	d := eval.NewDict()
	var errIter error
	for key, value := range dict.Items(thread, &errIter) {
		canonicalizedKey, err := ui.keyUnpacker.Canonicalize(thread, key)
		if err != nil {
			return nil, err
		}
		canonicalizedValue, err := ui.valueUnpacker.Canonicalize(thread, value)
		if err != nil {
			return nil, err
		}
		if err := d.SetKey(thread, canonicalizedKey, canonicalizedValue); err != nil {
			return nil, err
		}
	}
	if errIter != nil {
		return nil, errIter
	}
	return d, nil
}

func (dictUnpackerInto[TKey, TValue]) GetConcatenationOperator() syntax.Token {
	return syntax.PIPE
}
