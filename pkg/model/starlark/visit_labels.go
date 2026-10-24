package starlark

import (
	"fmt"

	pg_label "bonanza.build/pkg/label"

	"go.starlark.net/starlark"
)

// LabelVisitor is called for each label contained in a value, as
// done by VisitLabels() for Starlark values and VisitAttrValueLabels()
// for encoded attribute values.
type LabelVisitor func(pg_label.ResolvedLabel) error

// HasLabels can be implemented by Starlark values that potentially
// contain label values. This allows the labels contained within to be
// visited by calling VisitLabels().
type HasLabels interface {
	VisitLabels(thread *starlark.Thread, path map[starlark.Value]struct{}, visitor LabelVisitor) error
}

// VisitLabels recursively visits a Starlark value and reports any
// labels that are contained within. This method can, for example, be
// used to walk all labels contained in rule attributes to determine
// their dependencies.
func VisitLabels(thread *starlark.Thread, v starlark.Value, path map[starlark.Value]struct{}, visitor LabelVisitor) error {
	if _, ok := path[v]; !ok {
		path[v] = struct{}{}
		switch typedV := v.(type) {
		case HasLabels:
			// Type has its own logic for reporting labels.
			if err := typedV.VisitLabels(thread, path, visitor); err != nil {
				return err
			}

		// Composite types that require special handling.
		case *starlark.Dict:
			for key, value := range starlark.Entries(thread, typedV) {
				if err := VisitLabels(thread, key, path, visitor); err != nil {
					return err
				}
				if err := VisitLabels(thread, value, path, visitor); err != nil {
					return err
				}
			}
		case *starlark.List:
			for value := range starlark.Elements(typedV) {
				if err := VisitLabels(thread, value, path, visitor); err != nil {
					return err
				}
			}

		// Non-label scalars.
		case starlark.Bool:
		case starlark.Int:
		case starlark.String:
		case starlark.NoneType:

		default:
			return fmt.Errorf("cannot visit labels contained in value of type %s", v.Type())
		}
		delete(path, v)
	}
	return nil
}
