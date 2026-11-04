package unpack

import (
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/syntax"
)

// Canonicalizer of Starlark values. Whereas implementations of
// UnpackerInto provide an algorithm for converting Starlark values to
// native Go types, instances of Canonicalizer merely convert them to
// Starlark values that are considered canonical. For example,
// converting a string value provided to a rule attribute of type label
// to an instance of Label().
type Canonicalizer interface {
	Canonicalize(thread *eval.Thread, v eval.Value) (eval.Value, error)
	GetConcatenationOperator() syntax.Token
}

type canonicalizeUnpackerInto struct {
	Canonicalizer
}

// Canonicalize a Starlark value, as opposed to unpacking a value to a
// Go native type.
func Canonicalize(base Canonicalizer) UnpackerInto[eval.Value] {
	return &canonicalizeUnpackerInto{
		Canonicalizer: base,
	}
}

func (ui *canonicalizeUnpackerInto) UnpackInto(thread *eval.Thread, v eval.Value, dst *eval.Value) error {
	canonicalized, err := ui.Canonicalizer.Canonicalize(thread, v)
	if err != nil {
		return err
	}
	*dst = canonicalized
	return nil
}
