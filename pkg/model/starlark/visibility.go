package starlark

import (
	"errors"
	"fmt"

	pg_label "bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/unpack"
	"bonanza.build/pkg/storage/object"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// MacroVisibility is the value of the "visibility" argument that is
// provided to the implementation function of a symbolic macro. It is
// opaque, as its only purpose is to be forwarded to the targets and
// macro instances declared by the macro. As the implementation function
// only has access to the visibility of the macro instance that is being
// expanded, the actual visibility is tracked by TargetRegistrar.
type MacroVisibility struct{}

var _ starlark.Value = MacroVisibility{}

func (MacroVisibility) String() string {
	return "<visibility>"
}

// Type returns the type name of Starlark macro visibility objects.
func (MacroVisibility) Type() string {
	return "visibility"
}

// Freeze a Starlark macro visibility object, so that it cannot be
// mutated. This has no effect, as these objects are immutable.
func (MacroVisibility) Freeze() {}

// Truth returns whether a Starlark macro visibility object is "truthy"
// or "falsy". These objects are always "truthy".
func (MacroVisibility) Truth() starlark.Bool {
	return starlark.True
}

// Hash a Starlark macro visibility object. This is not supported, as
// these objects can only be forwarded.
func (MacroVisibility) Hash(thread *starlark.Thread) (uint32, error) {
	return 0, errors.New("visibility cannot be hashed")
}

// visibilityArgument contains the value of a "visibility" argument that
// is provided to a function that declares a target or macro instance.
// Separate implementations are used for the top level of a BUILD file
// and for symbolic macros.
type visibilityArgument[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] interface {
	// getTargetVisibility computes the visibility of a target. At
	// the top level of a BUILD file, targets for which no
	// visibility is provided explicitly use the package's default
	// visibility, unless defaultVisibilityOverride is set.
	getTargetVisibility(tr *TargetRegistrar[TReference, TMetadata], thread *starlark.Thread, defaultVisibilityOverride *model_starlark_pb.PackageGroup) (model_core.PatchedMessage[*model_starlark_pb.PackageGroup, TMetadata], error)

	// getMacroInstanceVisibility computes the visibility of a macro
	// instance. In addition to the packages that are provided
	// explicitly, macro instances are always visible to the
	// location from which the macro is called.
	getMacroInstanceVisibility(tr *TargetRegistrar[TReference, TMetadata], thread *starlark.Thread, currentPackage pg_label.CanonicalPackage) (model_core.PatchedMessage[*model_starlark_pb.PackageGroup, TMetadata], error)
}

// topLevelVisibilityArgument contains the value of a "visibility"
// argument that is provided at the top level of a BUILD file. It is
// either None or a list of labels.
type topLevelVisibilityArgument[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	labels []pg_label.ResolvedLabel
}

func (v topLevelVisibilityArgument[TReference, TMetadata]) getTargetVisibility(tr *TargetRegistrar[TReference, TMetadata], thread *starlark.Thread, defaultVisibilityOverride *model_starlark_pb.PackageGroup) (model_core.PatchedMessage[*model_starlark_pb.PackageGroup, TMetadata], error) {
	if len(v.labels) > 0 {
		// Explicit visibility provided. Construct new package group.
		return NewPackageGroupFromVisibility[TMetadata](tr.context, v.labels, tr.encoder, tr.inlinedTreeOptions, tr.objectManager)
	}
	if defaultVisibilityOverride != nil {
		return model_core.NewSimplePatchedMessage[TMetadata](defaultVisibilityOverride), nil
	}

	// Inherit visibility from repo() in the REPO.bazel file or
	// package() in the BUILD.bazel file.
	return model_core.Patch(
		tr.objectManager,
		model_core.Nested(tr.defaultInheritableAttrs, tr.defaultInheritableAttrs.Message.Visibility),
	), nil
}

func (v topLevelVisibilityArgument[TReference, TMetadata]) getMacroInstanceVisibility(tr *TargetRegistrar[TReference, TMetadata], thread *starlark.Thread, currentPackage pg_label.CanonicalPackage) (model_core.PatchedMessage[*model_starlark_pb.PackageGroup, TMetadata], error) {
	if len(v.labels) == 0 {
		// No explicit visibility provided. Just like rule
		// targets, inherit the visibility from repo() in the
		// REPO.bazel file or package() in the BUILD.bazel file.
		// This ensures that rules can be replaced by macros
		// without affecting visibility.
		return tr.addPackageToVisibility(
			thread,
			model_core.Nested(tr.defaultInheritableAttrs, tr.defaultInheritableAttrs.Message.GetVisibility()),
			currentPackage,
		)
	}
	return NewPackageGroupFromVisibility[TMetadata](
		tr.context,
		getTopLevelMacroInstanceVisibility(v.labels, currentPackage),
		tr.encoder,
		tr.inlinedTreeOptions,
		tr.objectManager,
	)
}

type topLevelVisibilityUnpackerInto[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	labels unpack.UnpackerInto[[]pg_label.ResolvedLabel]
}

func (ui topLevelVisibilityUnpackerInto[TReference, TMetadata]) UnpackInto(thread *starlark.Thread, v starlark.Value, dst *visibilityArgument[TReference, TMetadata]) error {
	var labels []pg_label.ResolvedLabel
	if err := ui.labels.UnpackInto(thread, v, &labels); err != nil {
		return err
	}
	*dst = topLevelVisibilityArgument[TReference, TMetadata]{labels: labels}
	return nil
}

func (ui topLevelVisibilityUnpackerInto[TReference, TMetadata]) Canonicalize(thread *starlark.Thread, v starlark.Value) (starlark.Value, error) {
	return ui.labels.Canonicalize(thread, v)
}

func (topLevelVisibilityUnpackerInto[TReference, TMetadata]) GetConcatenationOperator() syntax.Token {
	return 0
}

// macroVisibilityArgument contains the value of a "visibility" argument
// that is provided within a symbolic macro. It is either None or the
// visibility that was provided to the macro's implementation function.
type macroVisibilityArgument[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	forwarded bool
}

func (v macroVisibilityArgument[TReference, TMetadata]) getTargetVisibility(tr *TargetRegistrar[TReference, TMetadata], thread *starlark.Thread, defaultVisibilityOverride *model_starlark_pb.PackageGroup) (model_core.PatchedMessage[*model_starlark_pb.PackageGroup, TMetadata], error) {
	// Targets and macro instances declared by symbolic macros are
	// always visible to the package containing the macro's
	// definition. The package's default visibility does not apply.
	var visibility model_core.Message[*model_starlark_pb.PackageGroup, TReference]
	if v.forwarded {
		visibility = tr.macro.visibility
	}
	return tr.addPackageToVisibility(thread, visibility, tr.macro.definitionPackage)
}

func (v macroVisibilityArgument[TReference, TMetadata]) getMacroInstanceVisibility(tr *TargetRegistrar[TReference, TMetadata], thread *starlark.Thread, currentPackage pg_label.CanonicalPackage) (model_core.PatchedMessage[*model_starlark_pb.PackageGroup, TMetadata], error) {
	// The location from which the macro is called is the package
	// containing the definition of the macro instance that is
	// being expanded.
	return v.getTargetVisibility(tr, thread, nil)
}

type macroVisibilityUnpackerInto[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct{}

func (macroVisibilityUnpackerInto[TReference, TMetadata]) UnpackInto(thread *starlark.Thread, v starlark.Value, dst *visibilityArgument[TReference, TMetadata]) error {
	switch v.(type) {
	case starlark.NoneType:
		*dst = macroVisibilityArgument[TReference, TMetadata]{}
	case MacroVisibility:
		*dst = macroVisibilityArgument[TReference, TMetadata]{forwarded: true}
	default:
		return fmt.Errorf("got %s, while within symbolic macros visibility can only be None or the visibility provided to the macro's implementation function", v.Type())
	}
	return nil
}

func (ui macroVisibilityUnpackerInto[TReference, TMetadata]) Canonicalize(thread *starlark.Thread, v starlark.Value) (starlark.Value, error) {
	var visibility visibilityArgument[TReference, TMetadata]
	if err := ui.UnpackInto(thread, v, &visibility); err != nil {
		return nil, err
	}
	return v, nil
}

func (macroVisibilityUnpackerInto[TReference, TMetadata]) GetConcatenationOperator() syntax.Token {
	return 0
}
