package unpack

import (
	"fmt"

	"bonanza.build/pkg/label"

	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/syntax"
)

type apparentTargetPatternUnpackerInto struct{}

// ApparentTargetPattern is capable of unpacking string arguments that
// contain an apparent target pattern (e.g., "@rules_swift//...").
//
// If the target pattern is not prefixed with a repo name (e.g.,
// "//...") or relative (e.g., ":all"), the target pattern is resolved
// to be relative to the package to which the current Starlark file
// belongs.
var ApparentTargetPattern UnpackerInto[label.ApparentTargetPattern] = apparentTargetPatternUnpackerInto{}

func (apparentTargetPatternUnpackerInto) UnpackInto(thread *eval.Thread, v eval.Value, dst *label.ApparentTargetPattern) error {
	s, ok, err := eval.AsString(thread, v)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("got %s, want string", v.Type())
	}
	canonicalPackage, err := currentCanonicalPackage(thread)
	if err != nil {
		return err
	}
	tp, err := canonicalPackage.AppendTargetPattern(s)
	if err != nil {
		return fmt.Errorf("invalid target pattern: %w", err)
	}
	*dst = tp
	return nil
}

func (ui apparentTargetPatternUnpackerInto) Canonicalize(thread *eval.Thread, v eval.Value) (eval.Value, error) {
	var tp label.ApparentTargetPattern
	if err := ui.UnpackInto(thread, v, &tp); err != nil {
		return nil, err
	}
	return eval.String(tp.String()), nil
}

func (apparentTargetPatternUnpackerInto) GetConcatenationOperator() syntax.Token {
	return 0
}
