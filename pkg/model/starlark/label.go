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

type Label[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	value pg_label.ResolvedLabel
}

var (
	_ EncodableValue[object.LocalReference, model_core.ReferenceMetadata] = Label[object.LocalReference, model_core.ReferenceMetadata]{}
	_ HasLabels                                                           = Label[object.LocalReference, model_core.ReferenceMetadata]{}
	_ starlark.HasAttrs                                                   = Label[object.LocalReference, model_core.ReferenceMetadata]{}
	_ starlark.Value                                                      = Label[object.LocalReference, model_core.ReferenceMetadata]{}
)

func NewLabel[TReference any, TMetadata model_core.ReferenceMetadata](value pg_label.ResolvedLabel) starlark.Value {
	return Label[TReference, TMetadata]{
		value: value,
	}
}

func (l Label[TReference, TMetadata]) String() string {
	return l.value.String()
}

func (Label[TReference, TMetadata]) Type() string {
	return "Label"
}

func (Label[TReference, TMetadata]) Freeze() {}

func (Label[TReference, TMetadata]) Truth() starlark.Bool {
	return starlark.True
}

func (l Label[TReference, TMetadata]) Hash(thread *starlark.Thread) (uint32, error) {
	return starlark.String(l.value.String()).Hash(thread)
}

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

func (Label[TReference, TMetadata]) AttrNames() []string {
	return labelAttrNames
}

func (l Label[TReference, TMetadata]) EncodeValue(path map[starlark.Value]struct{}, currentIdentifier *pg_label.CanonicalStarlarkIdentifier, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], bool, error) {
	return model_core.NewSimplePatchedMessage[TMetadata](
		&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_Label{
				Label: l.value.String(),
			},
		},
	), false, nil
}

func (l Label[TReference, TMetadata]) VisitLabels(thread *starlark.Thread, path map[starlark.Value]struct{}, visitor func(pg_label.ResolvedLabel) error) error {
	if err := visitor(l.value); err != nil {
		return fmt.Errorf("label %#v: %w", l.value.String(), err)
	}
	return nil
}

type (
	CanonicalRepoResolver = func(fromCanonicalRepo pg_label.CanonicalRepo, toApparentRepo pg_label.ApparentRepo) (*pg_label.CanonicalRepo, error)
	RootModuleResolver    = func() (pg_label.Module, error)
)

const (
	LabelResolverKey = "label_resolver"
)

type labelOrStringUnpackerInto[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	basePackage pg_label.CanonicalPackage
}

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

type labelUnpackerInto[TReference any, TMetadata model_core.ReferenceMetadata] struct{}

func NewLabelUnpackerInto[TReference any, TMetadata model_core.ReferenceMetadata]() unpack.UnpackerInto[pg_label.ResolvedLabel] {
	return labelUnpackerInto[TReference, TMetadata]{}
}

func (labelUnpackerInto[TReference, TMetadata]) UnpackInto(thread *starlark.Thread, v starlark.Value, dst *pg_label.ResolvedLabel) error {
	l, ok := v.(Label[TReference, TMetadata])
	if !ok {
		return fmt.Errorf("got %s, want Label", v.Type())
	}
	*dst = l.value
	return nil
}

func (ui labelUnpackerInto[TReference, TMetadata]) Canonicalize(thread *starlark.Thread, v starlark.Value) (starlark.Value, error) {
	var l pg_label.ResolvedLabel
	if err := ui.UnpackInto(thread, v, &l); err != nil {
		return nil, err
	}
	return NewLabel[TReference, TMetadata](l), nil
}

func (labelUnpackerInto[TReference, TMetadata]) GetConcatenationOperator() syntax.Token {
	return 0
}

func CurrentFilePackage(thread *starlark.Thread, depth int) pg_label.CanonicalPackage {
	return util.Must(pg_label.NewCanonicalLabel(thread.CallFrame(depth).Pos.Filename())).GetCanonicalPackage()
}
