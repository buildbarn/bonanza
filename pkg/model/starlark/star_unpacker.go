package starlark

import (
	"fmt"

	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/syntax"
	"bonanza.build/pkg/starlark/unpack"
)

type starStringUnpackerInto struct{}

// starUnpackerInto only accepts the string "*". Functions like aspect()
// accept this string as a wildcard in places where lists of names are
// expected. Using this unpacker in combination with unpack.Or() and
// unpack.List() makes it possible to accept ["*"] in addition to
// arbitrary lists of names.
var starUnpackerInto unpack.UnpackerInto[struct{}] = starStringUnpackerInto{}

func (starStringUnpackerInto) UnpackInto(thread *eval.Thread, v eval.Value, dst *struct{}) error {
	s, ok, err := eval.AsString(thread, v)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("got %s, want string", v.Type())
	}
	if s != "*" {
		return fmt.Errorf("got %#v, want \"*\"", s)
	}
	return nil
}

func (ui starStringUnpackerInto) Canonicalize(thread *eval.Thread, v eval.Value) (eval.Value, error) {
	var dst struct{}
	if err := ui.UnpackInto(thread, v, &dst); err != nil {
		return nil, err
	}
	return v, nil
}

func (starStringUnpackerInto) GetConcatenationOperator() syntax.Token {
	return 0
}
