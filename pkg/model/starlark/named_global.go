package starlark

import (
	"maps"
	"slices"

	pg_label "bonanza.build/pkg/label"

	"github.com/buildbarn/bb-storage/pkg/util"

	"go.starlark.net/starlark"
)

// LateNamedValue can be embedded into Starlark value types, so that
// they track their own identifier. This is necessary, as there are
// certain Starlark objects that can only be instantiated if they are at
// the same time assigned to a global. This includes rules and
// providers. An identifier is only assigned to these objects after the
// .bzl file has been fully parsed.
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
	starlark.Value
	AssignIdentifier(identifier pg_label.CanonicalStarlarkIdentifier)
}

// NameAndExtractGlobals iterates over a set of globals and assigns
// identifiers to them. Iteration is performed in alphabetical order.
// This ensures that if we encounter constructs like these, we still
// assign names deterministically:
//
//	x = rule(..)
//	y = x
func NameAndExtractGlobals(globals starlark.StringDict, canonicalLabel pg_label.CanonicalLabel) {
	for _, name := range slices.Sorted(maps.Keys(globals)) {
		if value, ok := globals[name].(NamedGlobal); ok {
			identifier := canonicalLabel.AppendStarlarkIdentifier(util.Must(pg_label.NewStarlarkIdentifier(name)))
			value.AssignIdentifier(identifier)
		}
	}
}
