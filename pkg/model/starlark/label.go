package starlark

import (
	"fmt"
	go_path "path"

	pg_label "bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/unpack"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
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
	_ starlark.HasAttrs                                                   = Label[object.LocalReference, model_core.ReferenceMetadata]{}
	_ starlark.Value                                                      = Label[object.LocalReference, model_core.ReferenceMetadata]{}
)

// NewLabel creates a new Starlark label object, corresponding to a
// given label value.
func NewLabel[TReference any, TMetadata model_core.ReferenceMetadata](value pg_label.ResolvedLabel) starlark.Value {
	return Label[TReference, TMetadata]{
		value: value,
	}
}

func (l Label[TReference, TMetadata]) String() string {
	return l.value.String()
}

// Type returns the type name of Starlark label objects in string form.
func (Label[TReference, TMetadata]) Type() string {
	return "Label"
}

// Freeze a Starlark label object, so that it cannot be mutated. This
// has no effect, as Starlark label objects are immutable.
func (Label[TReference, TMetadata]) Freeze() {}

// Truth returns whether a Starlark label object is "truthy" or "falsy".
// Label values are always "truthy".
func (Label[TReference, TMetadata]) Truth() starlark.Bool {
	return starlark.True
}

// Hash a Starlark label object, so that it can be placed in a set or
// used as a key in a dict.
func (l Label[TReference, TMetadata]) Hash(thread *starlark.Thread) (uint32, error) {
	return starlark.String(l.value.String()).Hash(thread)
}

// Attr computes the value of an attribute of a Starlark label object
// when it is requested.
func (l Label[TReference, TMetadata]) Attr(thread *starlark.Thread, name string) (starlark.Value, error) {
	switch name {
	case "name":
		return starlark.String(l.value.GetTargetName().String()), nil
	case "package":
		return starlark.String(l.value.GetPackagePath()), nil
	case "relative":
		// Even though Bazel documents this function as being
		// deprecated, we provide it regardless. Functions like
		// ctx.expand_location() need to resolve labels relative
		// to ctx.label. native.package_relative_label() would
		// be an obvious fit for this, but it is explicitly
		// documented that this function cannot be called from a
		// rule implementation function.
		return starlark.NewBuiltin(
			"Label.relative",
			func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
				canonicalLabel, err := l.value.AsCanonical()
				if err != nil {
					return nil, err
				}

				if len(args) != 1 {
					return nil, fmt.Errorf("%s: got %d positional arguments, want 1", b.Name(), len(args))
				}
				var relName pg_label.ResolvedLabel
				if err := starlark.UnpackArgs(
					b.Name(), args, kwargs,
					"relName", unpack.Bind(thread, &relName, NewLabelOrStringUnpackerInto[TReference, TMetadata](canonicalLabel.GetCanonicalPackage())),
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
		return starlark.String(canonicalLabel.GetCanonicalPackage().GetCanonicalRepo().String()), nil
	case "same_package_label":
		return starlark.NewBuiltin(
			"Label.same_package_label",
			func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
				var targetName pg_label.TargetName
				if err := starlark.UnpackArgs(
					b.Name(), args, kwargs,
					"target_name", unpack.Bind(thread, &targetName, unpack.TargetName),
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
		return starlark.String(go_path.Join(
			ComponentStrExternal,
			canonicalLabel.GetCanonicalPackage().GetCanonicalRepo().String(),
		)), nil

	default:
		return nil, nil
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
func (Label[TReference, TMetadata]) AttrNames() []string {
	return labelAttrNames
}

// EncodeValue converts a Starlark label object to a Protobuf message,
// so that it can be written to storage.
func (l Label[TReference, TMetadata]) EncodeValue(path map[starlark.Value]struct{}, currentIdentifier *pg_label.CanonicalStarlarkIdentifier, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], bool, error) {
	return model_core.NewSimplePatchedMessage[TMetadata](
		&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_Label{
				Label: l.value.String(),
			},
		},
	), false, nil
}

// VisitLabels reports any labels contained within a Starlark value. As
// this specific value happens to be a Starlark label object, it is
// reported.
//
// Visiting of label values is performed when a rule is invoked, as it
// is needed to register predeclared output and implicit source file
// targets.
func (l Label[TReference, TMetadata]) VisitLabels(thread *starlark.Thread, path map[starlark.Value]struct{}, visitor func(pg_label.ResolvedLabel) error) error {
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

func (ui *labelOrStringUnpackerInto[TReference, TMetadata]) UnpackInto(thread *starlark.Thread, v starlark.Value, dst *pg_label.ResolvedLabel) error {
	switch typedV := v.(type) {
	case starlark.String:
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

func (ui *labelOrStringUnpackerInto[TReference, TMetadata]) Canonicalize(thread *starlark.Thread, v starlark.Value) (starlark.Value, error) {
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
// Starlark source file containing the function at a given stack frame
// belongs.
func CurrentFilePackage(thread *starlark.Thread, depth int) pg_label.CanonicalPackage {
	return util.Must(pg_label.NewCanonicalLabel(thread.CallFrame(depth).Pos.Filename())).GetCanonicalPackage()
}
