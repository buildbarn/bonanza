package unpack

import (
	"fmt"

	"bonanza.build/pkg/label"

	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/syntax"
)

type starlarkIdentifierUnpackerInto struct{}

// StarlarkIdentifier is capable of unpacking a string containing a
// Starlark identifier. Any string that is not a valid name for a
// Starlark identifier is rejected.
var StarlarkIdentifier UnpackerInto[label.StarlarkIdentifier] = starlarkIdentifierUnpackerInto{}

func (starlarkIdentifierUnpackerInto) UnpackInto(thread *eval.Thread, v eval.Value, dst *label.StarlarkIdentifier) error {
	s, ok := eval.AsString(v)
	if !ok {
		return fmt.Errorf("got %s, want string", v.Type())
	}
	i, err := label.NewStarlarkIdentifier(s)
	if err != nil {
		return fmt.Errorf("invalid Starlark identifier: %w", err)
	}
	*dst = i
	return nil
}

func (ui starlarkIdentifierUnpackerInto) Canonicalize(thread *eval.Thread, v eval.Value) (eval.Value, error) {
	var i label.StarlarkIdentifier
	if err := ui.UnpackInto(thread, v, &i); err != nil {
		return nil, err
	}
	return eval.String(i.String()), nil
}

func (starlarkIdentifierUnpackerInto) GetConcatenationOperator() syntax.Token {
	return 0
}
