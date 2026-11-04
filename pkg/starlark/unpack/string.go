package unpack

import (
	"fmt"

	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/syntax"
)

type stringUnpackerInto struct{}

// String is capable of unpacking any Starlark string value into a
// native string.
var String UnpackerInto[string] = stringUnpackerInto{}

func (stringUnpackerInto) UnpackInto(thread *eval.Thread, v eval.Value, dst *string) error {
	s, ok := eval.AsString(v)
	if !ok {
		return fmt.Errorf("got %s, want string", v.Type())
	}
	*dst = s
	return nil
}

func (ui stringUnpackerInto) Canonicalize(thread *eval.Thread, v eval.Value) (eval.Value, error) {
	var s string
	if err := ui.UnpackInto(thread, v, &s); err != nil {
		return nil, err
	}
	return eval.String(s), nil
}

func (stringUnpackerInto) GetConcatenationOperator() syntax.Token {
	return syntax.PLUS
}
