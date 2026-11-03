package unpack

import (
	"fmt"

	"bonanza.build/pkg/label"

	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/syntax"
)

type targetNameUnpackerInto struct{}

// TargetName is capable of unpacking Starlark strings containing Bazel
// target names (e.g. "go_default_library"). Any string value that is
// not a valid Bazel target name is rejected.
var TargetName UnpackerInto[label.TargetName] = targetNameUnpackerInto{}

func (targetNameUnpackerInto) UnpackInto(thread *eval.Thread, v eval.Value, dst *label.TargetName) error {
	s, ok := eval.AsString(v)
	if !ok {
		return fmt.Errorf("got %s, want string", v.Type())
	}
	tn, err := label.NewTargetName(s)
	if err != nil {
		return fmt.Errorf("invalid target name: %w", err)
	}
	*dst = tn
	return nil
}

func (ui targetNameUnpackerInto) Canonicalize(thread *eval.Thread, v eval.Value) (eval.Value, error) {
	var tn label.TargetName
	if err := ui.UnpackInto(thread, v, &tn); err != nil {
		return nil, err
	}
	return eval.String(tn.String()), nil
}

func (targetNameUnpackerInto) GetConcatenationOperator() syntax.Token {
	return 0
}
