package unpack

import (
	"fmt"

	"bonanza.build/pkg/label"

	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/syntax"
)

type apparentLabelUnpackerInto struct{}

// ApparentLabel is capable of unpacking string arguments that contain
// an apparent label (e.g., "@rules_go//go/platform:darwin_arm64").
//
// If the label is not prefixed with a repo name (e.g., "//cmd/mytool")
// or relative (e.g., ":go_default_library"), the label is resolved to
// be relative to the package to which the current Starlark file
// belongs.
var ApparentLabel UnpackerInto[label.ApparentLabel] = apparentLabelUnpackerInto{}

func (apparentLabelUnpackerInto) UnpackInto(thread *eval.Thread, v eval.Value, dst *label.ApparentLabel) error {
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
	l, err := canonicalPackage.AppendLabel(s)
	if err != nil {
		return fmt.Errorf("invalid label: %w", err)
	}
	*dst = l
	return nil
}

func (ui apparentLabelUnpackerInto) Canonicalize(thread *eval.Thread, v eval.Value) (eval.Value, error) {
	var l label.ApparentLabel
	if err := ui.UnpackInto(thread, v, &l); err != nil {
		return nil, err
	}
	return eval.String(l.String()), nil
}

func (apparentLabelUnpackerInto) GetConcatenationOperator() syntax.Token {
	return 0
}
