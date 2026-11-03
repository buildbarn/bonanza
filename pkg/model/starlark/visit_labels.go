package starlark

import (
	"fmt"

	pg_label "bonanza.build/pkg/label"
	"bonanza.build/pkg/starlark/eval"
)

// LabelVisitor is called for each label contained in a value, as
// done by VisitLabels() for Starlark values and VisitAttrValueLabels()
// for encoded attribute values.
type LabelVisitor func(pg_label.ResolvedLabel) error

// HasLabels can be implemented by Starlark values that potentially
// contain label values. This allows the labels contained within to be
// visited by calling VisitLabels().
type HasLabels interface {
	VisitLabels(thread *eval.Thread, path []eval.Value, visitor LabelVisitor) error
}

// VisitLabels recursively visits a Starlark value and reports any
// labels that are contained within. This method can, for example, be
// used to walk all labels contained in rule attributes to determine
// their dependencies. path contains the mutable composite values that
// are currently being visited, which is used to prevent infinite
// recursion.
func VisitLabels(thread *eval.Thread, v eval.Value, path []eval.Value, visitor LabelVisitor) error {
	switch v.(type) {
	case eval.List, eval.Dict:
		for _, pathValue := range path {
			if pathValue == v {
				return nil
			}
		}
		path = append(path, v)
	}

	switch typedV := v.(type) {
	case HasLabels:
		// Type has its own logic for reporting labels.
		return typedV.VisitLabels(thread, path, visitor)

	// Composite types that require special handling.
	case eval.Dict:
		var errIter error
		for key, value := range typedV.Items(thread, &errIter) {
			if err := VisitLabels(thread, key, path, visitor); err != nil {
				return err
			}
			if err := VisitLabels(thread, value, path, visitor); err != nil {
				return err
			}
		}
		return errIter
	case eval.List:
		var errIter error
		for value := range typedV.Iterate(thread, &errIter) {
			if err := VisitLabels(thread, value, path, visitor); err != nil {
				return err
			}
		}
		return errIter

	// Non-label scalars.
	case eval.Bool, eval.Int, eval.String, eval.NoneType:
		return nil

	default:
		return fmt.Errorf("cannot visit labels contained in value of type %s", v.Type())
	}
}
