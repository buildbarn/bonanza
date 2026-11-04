package unpack

import (
	"fmt"

	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/syntax"
)

type bytesUnpackerInto struct{}

// Bytes is capable of unpacking any Starlark string value into a byte
// slice. Unlike String, it accepts strings containing bytes that are
// not valid UTF-8. It should be used by builtins that process
// arbitrary data, such as the contents of files.
var Bytes UnpackerInto[[]byte] = bytesUnpackerInto{}

func (bytesUnpackerInto) UnpackInto(thread *eval.Thread, v eval.Value, dst *[]byte) error {
	b, ok, err := eval.AsBytes(thread, v)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("got %s, want string", v.Type())
	}
	*dst = b
	return nil
}

func (bytesUnpackerInto) Canonicalize(thread *eval.Thread, v eval.Value) (eval.Value, error) {
	if !eval.IsString(v) {
		return nil, fmt.Errorf("got %s, want string", v.Type())
	}
	return v, nil
}

func (bytesUnpackerInto) GetConcatenationOperator() syntax.Token {
	return syntax.PLUS
}
