package unpack

import (
	"fmt"

	"bonanza.build/pkg/label"

	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/syntax"
)

type moduleVersionUnpackerInto struct{}

// ModuleVersion is capable of unpacking a string containing a Bazel
// module version (e.g., "1.7.1", "0.0.0-20241220-5e258e33").
var ModuleVersion UnpackerInto[label.ModuleVersion] = moduleVersionUnpackerInto{}

func (moduleVersionUnpackerInto) UnpackInto(thread *eval.Thread, v eval.Value, dst *label.ModuleVersion) error {
	s, ok, err := eval.AsString(thread, v)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("got %s, want string", v.Type())
	}
	mv, err := label.NewModuleVersion(s)
	if err != nil {
		return fmt.Errorf("invalid module version: %w", err)
	}
	*dst = mv
	return nil
}

func (ui moduleVersionUnpackerInto) Canonicalize(thread *eval.Thread, v eval.Value) (eval.Value, error) {
	var mv label.ModuleVersion
	if err := ui.UnpackInto(thread, v, &mv); err != nil {
		return nil, err
	}
	return eval.String(mv.String()), nil
}

func (moduleVersionUnpackerInto) GetConcatenationOperator() syntax.Token {
	return 0
}
