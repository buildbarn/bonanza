package starlark

import (
	"context"
	"fmt"

	pg_label "bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/inlinedtree"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/unpack"
	"bonanza.build/pkg/storage/object"
)

// DefaultInheritableAttrs contains the default values of the
// inheritable attributes of rule targets. These are used when no
// REPO.bazel file is present, REPO.bazel contains no call to repo(),
// and a BUILD file contains no call to package().
var DefaultInheritableAttrs = model_starlark_pb.InheritableAttrs{
	Visibility: &model_starlark_pb.PackageGroup{
		Tree: &model_starlark_pb.PackageGroup_Subpackages{},
	},
}

// NewRepoDotBazelRepoFunction creates an implementation of the repo()
// function that may be invoked from within REPO.bazel files. Upon
// invocation, the default inheritable attributes are extracted from
// the arguments and stored in defaultAttrs. The function may only be
// invoked once.
//
// The resulting function is intended to be provided to
// dotbazel.ParseRepo(). Labels provided as strings are resolved using
// the label resolver that is stored in the thread under
// LabelResolverKey.
func NewRepoDotBazelRepoFunction[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	encoder model_encoding.DeterministicBinaryEncoder,
	inlinedTreeOptions inlinedtree.Options,
	objectCapturer model_core.ObjectCapturer[TReference, TMetadata],
	defaultAttrs *model_core.PatchedMessage[*model_starlark_pb.InheritableAttrs, TMetadata],
) eval.BuiltinFunc {
	return func(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
		if defaultAttrs.IsSet() {
			return nil, fmt.Errorf("%s: function can only be invoked once", b.Name())
		}
		newDefaultAttrs, err := getDefaultInheritableAttrs[TReference, TMetadata](
			thread.Context(),
			thread,
			b,
			args,
			model_core.NewSimpleMessage[TReference](&DefaultInheritableAttrs),
			encoder,
			inlinedTreeOptions,
			objectCapturer,
		)
		if err != nil {
			return nil, err
		}
		*defaultAttrs = newDefaultAttrs
		return eval.None, nil
	}
}

// getDefaultInheritableAttrs parses the arguments provided to
// REPO.bazel's repo() function or BUILD.bazel's package() function.
func getDefaultInheritableAttrs[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	ctx context.Context,
	thread *eval.Thread,
	b *eval.Builtin,
	args eval.Args,
	previousInheritableAttrs model_core.Message[*model_starlark_pb.InheritableAttrs, TReference],
	encoder model_encoding.DeterministicBinaryEncoder,
	inlinedTreeOptions inlinedtree.Options,
	objectCapturer model_core.ObjectCapturer[TReference, TMetadata],
) (model_core.PatchedMessage[*model_starlark_pb.InheritableAttrs, TMetadata], error) {
	if len(args.Positional) > 0 {
		return model_core.PatchedMessage[*model_starlark_pb.InheritableAttrs, TMetadata]{}, fmt.Errorf("%s: got %d positional arguments, want 0", b.Name(), len(args.Positional))
	}

	var applicableLicenses []string
	deprecation := previousInheritableAttrs.Message.Deprecation
	packageMetadata := previousInheritableAttrs.Message.PackageMetadata
	testOnly := previousInheritableAttrs.Message.Testonly
	var visibility []pg_label.ResolvedLabel
	canonicalPackage, err := CurrentFilePackage(thread)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.InheritableAttrs, TMetadata]{}, err
	}
	labelUnpackerInto := NewLabelOrStringUnpackerInto[TReference, TMetadata](canonicalPackage)
	labelStringListUnpackerInto := unpack.List(unpack.Stringer(labelUnpackerInto))
	var features []string
	if err := eval.UnpackArgs(
		thread, b.Name(), args,
		"default_applicable_licenses?", unpack.Bind(&applicableLicenses, labelStringListUnpackerInto),
		"default_deprecation?", unpack.Bind(&deprecation, unpack.String),
		"default_package_metadata?", unpack.Bind(&packageMetadata, labelStringListUnpackerInto),
		"default_testonly?", unpack.Bind(&testOnly, unpack.Bool),
		"default_visibility?", unpack.Bind(&visibility, unpack.List(labelUnpackerInto)),
		"features?", unpack.Bind(&features, unpack.List(unpack.String)),
	); err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.InheritableAttrs, TMetadata]{}, err
	}

	// default_applicable_licenses is an alias for default_package_metadata.
	if len(applicableLicenses) > 0 {
		if len(packageMetadata) > 0 {
			return model_core.PatchedMessage[*model_starlark_pb.InheritableAttrs, TMetadata]{}, fmt.Errorf("%s: default_applicable_licenses and default_package_metadata are mutually exclusive", b.Name())
		}
		packageMetadata = applicableLicenses
	}

	var visibilityPackageGroup model_core.PatchedMessage[*model_starlark_pb.PackageGroup, TMetadata]
	if len(visibility) > 0 {
		// Explicit visibility provided. Construct a new package group.
		var err error
		visibilityPackageGroup, err = NewPackageGroupFromVisibility[TMetadata](ctx, visibility, encoder, inlinedTreeOptions, objectCapturer)
		if err != nil {
			return model_core.PatchedMessage[*model_starlark_pb.InheritableAttrs, TMetadata]{}, err
		}
	} else {
		// Clone the existing visibility.
		visibilityPackageGroup = model_core.Patch(
			objectCapturer,
			model_core.Nested(previousInheritableAttrs, previousInheritableAttrs.Message.Visibility),
		)
	}

	// TODO: Also store features?
	return model_core.NewPatchedMessage(
		&model_starlark_pb.InheritableAttrs{
			Deprecation:     deprecation,
			PackageMetadata: packageMetadata,
			Testonly:        testOnly,
			Visibility:      visibilityPackageGroup.Message,
		},
		visibilityPackageGroup.Patcher,
	), nil
}
