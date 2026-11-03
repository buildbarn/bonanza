package unpack

import (
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/syntax"
)

type anyUnpackerInto struct{}

// Any can unpack any Starlark value argument and store it into the
// provided value. This can be useful for situations where the actual
// parsing of an argument needs to be delayed to a later point in time
// (e.g., after interpreting other arguments that control the type of
// this argument.
var Any UnpackerInto[eval.Value] = anyUnpackerInto{}

func (anyUnpackerInto) UnpackInto(thread *eval.Thread, v eval.Value, dst *eval.Value) error {
	*dst = v
	return nil
}

func (anyUnpackerInto) Canonicalize(thread *eval.Thread, v eval.Value) (eval.Value, error) {
	return v, nil
}

func (anyUnpackerInto) GetConcatenationOperator() syntax.Token {
	return 0
}
