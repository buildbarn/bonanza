package starlark

import (
	"fmt"

	pg_label "bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/inlinedtree"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/storage/object"
)

// TargetRegistrar can be called into by functions like alias(),
// exports_files(), label_flag(), label_setting(), package_group() and
// invocations of rules to register any targets in the current package.
type TargetRegistrar[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	// Immutable fields.
	encoder            model_encoding.BinaryEncoder
	inlinedTreeOptions *inlinedtree.Options
	objectManager      model_core.ObjectManager[TReference, TMetadata]

	// Mutable fields.
	defaultInheritableAttrs    model_core.Message[*model_starlark_pb.InheritableAttrs, TReference]
	setDefaultInheritableAttrs bool
	targets                    map[string]model_core.PatchedMessage[*model_starlark_pb.Target_Definition, TMetadata]
}

// NewTargetRegistrar creates a TargetRegistrar that at the time of
// creation contains no targets. The caller needs to provide default
// values for attributes that are provided to calls to repo() in
// REPO.bazel, so that they can be inherited by registered targets.
func NewTargetRegistrar[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](encoder model_encoding.BinaryEncoder, inlinedTreeOptions *inlinedtree.Options, objectManager model_core.ObjectManager[TReference, TMetadata], defaultInheritableAttrs model_core.Message[*model_starlark_pb.InheritableAttrs, TReference]) *TargetRegistrar[TReference, TMetadata] {
	return &TargetRegistrar[TReference, TMetadata]{
		encoder:                 encoder,
		inlinedTreeOptions:      inlinedTreeOptions,
		objectManager:           objectManager,
		defaultInheritableAttrs: defaultInheritableAttrs,
		targets:                 map[string]model_core.PatchedMessage[*model_starlark_pb.Target_Definition, TMetadata]{},
	}
}

// GetTargets returns the set of targets in the current package that
// have been registered against this TargetRegistrar.
//
// This method returns a map that is keyed by target name. The value
// denotes the definition of the target. The value may be left unset if
// the target is implicit, meaning that it is referenced by one of its
// siblings, but no explicit declaration is provided. The caller may
// assume that such targets refer to source files that are part of this
// package.
func (tr *TargetRegistrar[TReference, TMetadata]) GetTargets() map[string]model_core.PatchedMessage[*model_starlark_pb.Target_Definition, TMetadata] {
	return tr.targets
}

func (tr *TargetRegistrar[TReference, TMetadata]) getVisibilityPackageGroup(visibility []pg_label.ResolvedLabel) (model_core.PatchedMessage[*model_starlark_pb.PackageGroup, TMetadata], error) {
	if len(visibility) > 0 {
		// Explicit visibility provided. Construct new package group.
		return NewPackageGroupFromVisibility[TMetadata](visibility, tr.encoder, tr.inlinedTreeOptions, tr.objectManager)
	}

	// Inherit visibility from repo() in the REPO.bazel file
	// or package() in the BUILD.bazel file.
	return model_core.Patch(
		tr.objectManager,
		model_core.Nested(tr.defaultInheritableAttrs, tr.defaultInheritableAttrs.Message.Visibility),
	), nil
}

func (tr *TargetRegistrar[TReference, TMetadata]) registerExplicitTarget(name string, target model_core.PatchedMessage[*model_starlark_pb.Target_Definition, TMetadata]) error {
	if tr.targets[name].IsSet() {
		return fmt.Errorf("package contains multiple targets with name %#v", name)
	}
	tr.targets[name] = target
	return nil
}

func (tr *TargetRegistrar[TReference, TMetadata]) registerImplicitTarget(name string) {
	if _, ok := tr.targets[name]; !ok {
		tr.targets[name] = model_core.PatchedMessage[*model_starlark_pb.Target_Definition, TMetadata]{}
	}
}
