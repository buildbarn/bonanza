package starlark

import (
	"bonanza.build/pkg/starlark/compile"
	"bonanza.build/pkg/starlark/eval"

	pg_label "bonanza.build/pkg/label"
)

// LateNamedValue can be embedded into Starlark value types, so that
// they track their own identifier. This is necessary, as there are
// certain Starlark objects that can only be instantiated if they are at
// the same time assigned to a global. This includes rules and
// providers. An identifier is assigned to these objects as soon as they
// are assigned to a global (see BzlModule). Objects that are never
// assigned to a global directly (e.g., ones that are only stored in a
// dict) remain unnamed, and cannot be exported.
type LateNamedValue struct {
	Identifier *pg_label.CanonicalStarlarkIdentifier
}

// AssignIdentifier assigns an identifier to a Starlark object that
// needs to be aware of its own name. Because the same value can be
// assigned to multiple globals, only the first assignment is tracked.
func (lnv *LateNamedValue) AssignIdentifier(identifier pg_label.CanonicalStarlarkIdentifier) {
	if lnv.Identifier == nil {
		lnv.Identifier = &identifier
	}
}

func (lnv *LateNamedValue) equals(other *LateNamedValue) bool {
	// First check whether the values have the same memory address.
	// If values are restored from storage, then their addresses may
	// differ. Fall back to comparing their identifiers in that case.
	return lnv == other || (lnv.Identifier != nil && other.Identifier != nil && *lnv.Identifier == *other.Identifier)
}

// NamedGlobal is satisified by all Starlark objects that need to track
// their own identifier, such as rules and providers.
type NamedGlobal interface {
	eval.Value
	AssignIdentifier(identifier pg_label.CanonicalStarlarkIdentifier)
}

// BzlModule is a module that is used to execute .bzl files. It
// assigns identifiers to Starlark objects that need to be aware of
// their own name (e.g., rules and providers) as soon as they are
// assigned to a global. If a value is assigned to multiple globals,
// the first assignment in execution order determines its name, as is
// the case with Bazel:
//
//	x = rule(..)
//	y = x
type BzlModule struct {
	*eval.InMemoryModule
	canonicalLabel pg_label.CanonicalLabel
}

var _ eval.MutableModule = (*BzlModule)(nil)

// NewBzlModule creates a module that can be used to execute a .bzl
// file having a given canonical label.
func NewBzlModule(canonicalLabel pg_label.CanonicalLabel, predeclared map[string]eval.Value, importer func(t *eval.Thread, imp compile.Import) (eval.Value, error)) *BzlModule {
	return &BzlModule{
		InMemoryModule: eval.NewInMemoryModule(canonicalLabel.String(), predeclared, importer),
		canonicalLabel: canonicalLabel,
	}
}

// SetGlobal assigns a global. If the value needs to be aware of its
// own name and hasn't been assigned one yet, it is named after the
// global.
func (m *BzlModule) SetGlobal(t *eval.Thread, name string, v eval.Value) error {
	if namedGlobal, ok := v.(NamedGlobal); ok {
		starlarkIdentifier, err := pg_label.NewStarlarkIdentifier(name)
		if err != nil {
			return err
		}
		namedGlobal.AssignIdentifier(m.canonicalLabel.AppendStarlarkIdentifier(starlarkIdentifier))
	}
	return m.InMemoryModule.SetGlobal(t, name, v)
}
