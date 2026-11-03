package unpack

import (
	"errors"
	"fmt"

	"bonanza.build/pkg/label"
	"bonanza.build/pkg/starlark/eval"
)

// currentCanonicalPackage returns the package containing the Starlark
// file whose code is calling into the builtin that is unpacking its
// arguments. Relative labels are resolved against this package. The
// filename of the file's module must be a canonical label.
func currentCanonicalPackage(thread *eval.Thread) (label.CanonicalPackage, error) {
	callStack := thread.CallStack()
	for i := len(callStack) - 1; i >= 0; i-- {
		if filename := callStack[i].Filename; filename != "" {
			l, err := label.NewCanonicalLabel(filename)
			if err != nil {
				return label.CanonicalPackage{}, fmt.Errorf("filename of calling file is not a canonical label: %w", err)
			}
			return l.GetCanonicalPackage(), nil
		}
	}
	return label.CanonicalPackage{}, errors.New("not called from a Starlark file")
}
