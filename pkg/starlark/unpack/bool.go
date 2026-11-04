package unpack

import (
	"fmt"

	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/syntax"
)

type boolUnpackerInto struct{}

// Bool is capable of unpacking boolean arguments.
var Bool UnpackerInto[bool] = boolUnpackerInto{}

func (boolUnpackerInto) UnpackInto(thread *eval.Thread, v eval.Value, dst *bool) error {
	s, ok := v.(eval.Bool)
	if !ok {
		return fmt.Errorf("got %s, want bool", v.Type())
	}
	*dst = bool(s)
	return nil
}

func (boolUnpackerInto) Canonicalize(thread *eval.Thread, v eval.Value) (eval.Value, error) {
	if _, ok := v.(eval.Bool); !ok {
		return nil, fmt.Errorf("got %s, want bool", v.Type())
	}
	return v, nil
}

func (boolUnpackerInto) GetConcatenationOperator() syntax.Token {
	return 0
}
