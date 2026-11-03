package starlark

import (
	"errors"
	"fmt"
	go_path "path"

	pg_label "bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/eval/unpack"
	"bonanza.build/pkg/starlark/syntax"
	"bonanza.build/pkg/storage/object"
)

// Label value in the form of a Starlark object.
//
// Starlark label objects are typically either constructed explicitly
// using the Label() constructor function, or they are created by
// providing string values to arguments expecting a label, causing them
// to be promoted implicitly.
type Label[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	value pg_label.ResolvedLabel
}

var (
	_ EncodableValue[object.LocalReference, model_core.ReferenceMetadata] = Label[object.LocalReference, model_core.ReferenceMetadata]{}
	_ HasLabels                                                           = Label[object.LocalReference, model_core.ReferenceMetadata]{}
	_ eval.Comparable                                                     = Label[object.LocalReference, model_core.ReferenceMetadata]{}
	_ eval.HasAttrs                                                       = Label[object.LocalReference, model_core.ReferenceMetadata]{}
	_ eval.HasStr                                                         = Label[object.LocalReference, model_core.ReferenceMetadata]{}
	_ eval.Hashable                                                       = Label[object.LocalReference, model_core.ReferenceMetadata]{}
)

// NewLabel creates a new Starlark label object, corresponding to a
// given label value.
func NewLabel[TReference any, TMetadata model_core.ReferenceMetadata](value pg_label.ResolvedLabel) eval.Value {
	return Label[TReference, TMetadata]{
		value: value,
	}
}

// Type returns the type name of Starlark label objects in string form.
func (Label[TReference, TMetadata]) Type() string {
	return "Label"
}

// Truth returns whether a Starlark label object is "truthy" or "falsy".
// Label values are always "truthy".
func (Label[TReference, TMetadata]) Truth(*eval.Thread) (bool, error) {
	return true, nil
}

// Repr writes the representation of a Starlark label object, which
// is the same as the one used by Bazel.
func (l Label[TReference, TMetadata]) Repr(_ *eval.Thread, p *eval.Printer) error {
	p.WriteString("Label(")
	p.WriteString(syntax.Quote(l.value.String()))
	p.WriteString(")")
	return nil
}

// Str writes the string representation of a Starlark label object, as
// returned by str(). This is the label itself.
func (l Label[TReference, TMetadata]) Str(_ *eval.Thread, p *eval.Printer) error {
	p.WriteString(l.value.String())
	return nil
}

// Hash a Starlark label object, so that it can be placed in a set or
// used as a key in a dict.
func (l Label[TReference, TMetadata]) Hash(thread *eval.Thread) (uint32, error) {
	return eval.Hash(thread, eval.String(l.value.String()))
}

// CompareSameType compares two Starlark label objects. Labels are
// ordered by their string representation, as is done by Bazel.
func (l Label[TReference, TMetadata]) CompareSameType(thread *eval.Thread, op syntax.Token, other eval.Value, depth int) (bool, error) {
	return eval.Compare(thread, op, eval.String(l.value.String()), eval.String(other.(Label[TReference, TMetadata]).value.String()))
}

// Attr computes the value of an attribute of a Starlark label object
// when it is requested.
func (l Label[TReference, TMetadata]) Attr(thread *eval.Thread, name string) (eval.Value, error) {
	switch name {
	case "name":
		return eval.String(l.value.GetTargetName().String()), nil
	case "package":
		return eval.String(l.value.GetPackagePath()), nil
	case "relative":
		// Even though Bazel documents this function as being
		// deprecated, we provide it regardless. Functions like
		// ctx.expand_location() need to resolve labels relative
		// to ctx.label. native.package_relative_label() would
		// be an obvious fit for this, but it is explicitly
		// documented that this function cannot be called from a
		// rule implementation function.
		return eval.NewBuiltin(
			"Label.relative",
			func(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
				canonicalLabel, err := l.value.AsCanonical()
				if err != nil {
					return nil, err
				}

				var relName pg_label.ResolvedLabel
				if err := eval.UnpackArgs(
					thread, b.Name(), args,
					"relName", unpack.Bind(&relName, NewLabelOrStringUnpackerInto[TReference, TMetadata](canonicalLabel.GetCanonicalPackage())),
				); err != nil {
					return nil, err
				}
				return NewLabel[TReference, TMetadata](relName), nil
			},
		), nil
	case "repo_name":
		canonicalLabel, err := l.value.AsCanonical()
		if err != nil {
			return nil, err
		}
		return eval.String(canonicalLabel.GetCanonicalPackage().GetCanonicalRepo().String()), nil
	case "same_package_label":
		return eval.NewBuiltin(
			"Label.same_package_label",
			func(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
				var targetName pg_label.TargetName
				if err := eval.UnpackArgs(
					thread, b.Name(), args,
					"target_name", unpack.Bind(&targetName, unpack.TargetName),
				); err != nil {
					return nil, err
				}
				return NewLabel[TReference, TMetadata](l.value.AppendTargetName(targetName)), nil
			},
		), nil
	case "workspace_root":
		canonicalLabel, err := l.value.AsCanonical()
		if err != nil {
			return nil, err
		}
		return eval.String(go_path.Join(
			ComponentStrExternal,
			canonicalLabel.GetCanonicalPackage().GetCanonicalRepo().String(),
		)), nil
	default:
		return nil, eval.NoSuchAttrError{Value: l, Name: name}
	}
}

var labelAttrNames = []string{
	"name",
	"package",
	"relative",
	"repo_name",
	"same_package_label",
	"workspace_root",
}

// AttrNames returns the set of attribute names that Starlark label
// objects have.
func (Label[TReference, TMetadata]) AttrNames(*eval.Thread) ([]string, error) {
	return labelAttrNames, nil
}

// GetLabel returns the label value that is wrapped by the Starlark
// label object.
func (l Label[TReference, TMetadata]) GetLabel() pg_label.ResolvedLabel {
	return l.value
}

// EncodeValue converts a Starlark label object to a Protobuf message,
// so that it can be written to storage.
func (l Label[TReference, TMetadata]) EncodeValue(thread *eval.Thread, path []eval.Value, currentIdentifier *pg_label.CanonicalStarlarkIdentifier, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], error) {
	return model_core.NewSimplePatchedMessage[TMetadata](
		&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_Label{
				Label: l.value.String(),
			},
		},
	), nil
}

// VisitLabels reports any labels contained within a Starlark value. As
// this specific value happens to be a Starlark label object, it is
// reported.
//
// Visiting of label values is performed when a rule is invoked, as it
// is needed to register predeclared output and implicit source file
// targets.
func (l Label[TReference, TMetadata]) VisitLabels(thread *eval.Thread, path []eval.Value, visitor LabelVisitor) error {
	if err := visitor(l.value); err != nil {
		return fmt.Errorf("label %#v: %w", l.value.String(), err)
	}
	return nil
}

// LabelResolverKey is the key of a thread local variable in a Starlark
// thread in which a label resolver is stored. This resolver is used by
// the unpacker that is returned by NewLabelOrStringUnpackerInto() to
// convert apparent label strings to resolved labels.
const LabelResolverKey = "label_resolver"

type labelOrStringUnpackerInto[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	basePackage pg_label.CanonicalPackage
}

// NewLabelOrStringUnpackerInto creates a Starlark function argument
// unpacker that is capable of unpacking label values, or string values
// that are subsequently converted to labels. When a string value is
// provided, resolution of the label is relative to a provided base
// package.
func NewLabelOrStringUnpackerInto[TReference any, TMetadata model_core.ReferenceMetadata](basePackage pg_label.CanonicalPackage) unpack.UnpackerInto[pg_label.ResolvedLabel] {
	return &labelOrStringUnpackerInto[TReference, TMetadata]{
		basePackage: basePackage,
	}
}

func (ui *labelOrStringUnpackerInto[TReference, TMetadata]) UnpackInto(thread *eval.Thread, v eval.Value, dst *pg_label.ResolvedLabel) error {
	switch typedV := v.(type) {
	case eval.String:
		// Label value is a bare string. Parse and resolve it.
		labelResolver := thread.Local(LabelResolverKey)
		if labelResolver == nil {
			return fmt.Errorf("label %#v is provided as a string instead of a Label, but such labels cannot be resolved from within this context", string(typedV))
		}

		apparentLabel, err := ui.basePackage.AppendLabel(string(typedV))
		if err != nil {
			return err
		}
		resolvedLabel, err := pg_label.Resolve(labelResolver.(pg_label.Resolver), ui.basePackage.GetCanonicalRepo(), apparentLabel)
		if err != nil {
			return err
		}

		*dst = resolvedLabel
		return nil
	case Label[TReference, TMetadata]:
		// Label value is already wrapped in Label().
		*dst = typedV.value
		return nil
	default:
		return fmt.Errorf("got %s, want Label or str", v.Type())
	}
}

func (ui *labelOrStringUnpackerInto[TReference, TMetadata]) Canonicalize(thread *eval.Thread, v eval.Value) (eval.Value, error) {
	var l pg_label.ResolvedLabel
	if err := ui.UnpackInto(thread, v, &l); err != nil {
		return nil, err
	}
	return NewLabel[TReference, TMetadata](l), nil
}

func (labelOrStringUnpackerInto[TReference, TMetadata]) GetConcatenationOperator() syntax.Token {
	return 0
}

// CurrentFilePackage returns the name of the package to which the
// Starlark source file containing the innermost Starlark function on
// the call stack belongs. When called from within a builtin, this
// corresponds to the file of the code that called the builtin.
func CurrentFilePackage(thread *eval.Thread) (pg_label.CanonicalPackage, error) {
	callStack := thread.CallStack()
	for i := len(callStack) - 1; i >= 0; i-- {
		if filename := callStack[i].Filename; filename != "" {
			l, err := pg_label.NewCanonicalLabel(filename)
			if err != nil {
				return pg_label.CanonicalPackage{}, fmt.Errorf("filename of calling file is not a canonical label: %w", err)
			}
			return l.GetCanonicalPackage(), nil
		}
	}
	return pg_label.CanonicalPackage{}, errors.New("not called from a Starlark file")
}
