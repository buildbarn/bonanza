package starlark

import (
	"context"
	"fmt"

	pg_label "bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/inlinedtree"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/compile"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/eval/unpack"
	"bonanza.build/pkg/starlark/resolve"
	"bonanza.build/pkg/starlark/syntax"
	"bonanza.build/pkg/storage/object"
)

// DefaultInheritableAttrs contains the default values of the
// inheritable attributes of rule targets. These are used when no
// REPO.bazel file is present, and a BUILD file contains no call to
// package().
var DefaultInheritableAttrs = model_starlark_pb.InheritableAttrs{
	Visibility: &model_starlark_pb.PackageGroup{
		Tree: &model_starlark_pb.PackageGroup_Subpackages{},
	},
}

// ParseRepoDotBazel parses a REPO.bazel file that may be stored at the
// root of a repository.
func ParseRepoDotBazel[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	ctx context.Context,
	contents string,
	filename pg_label.CanonicalLabel,
	encoder model_encoding.DeterministicBinaryEncoder,
	inlinedTreeOptions *inlinedtree.Options,
	objectCapturer model_core.ObjectCapturer[TReference, TMetadata],
	labelResolver pg_label.Resolver,
) (model_core.PatchedMessage[*model_starlark_pb.InheritableAttrs, TMetadata], error) {
	var defaultAttrs model_core.PatchedMessage[*model_starlark_pb.InheritableAttrs, TMetadata]
	predeclared := map[string]eval.Value{
		"repo": eval.NewBuiltin("repo", func(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
			if defaultAttrs.IsSet() {
				return nil, fmt.Errorf("%s: function can only be invoked once", b.Name())
			}
			newDefaultAttrs, err := getDefaultInheritableAttrs[TReference, TMetadata](
				ctx,
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
			defaultAttrs = newDefaultAttrs
			return eval.None, nil
		}),
	}

	// Parse, resolve and compile the file.
	filenameStr := filename.String()
	f, err := syntax.Parse(filenameStr, []byte(contents), syntax.Options{})
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.InheritableAttrs, TMetadata]{}, err
	}
	universe := eval.Universe()
	m, err := resolve.File(
		f,
		&resolve.RepoDotBazelDialect,
		func(name string) bool {
			_, ok := predeclared[name]
			return ok
		},
		func(name string) bool {
			_, ok := universe[name]
			return ok
		},
	)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.InheritableAttrs, TMetadata]{}, err
	}

	// Execute the file. The filename of the module needs to be a
	// canonical label, so that labels can be resolved relative to
	// the root package of the repository.
	thread := eval.NewThread(ctx, universe)
	thread.Print = func(_ *eval.Thread, msg string) error {
		// TODO: Provide logging sink.
		fmt.Println(msg)
		return nil
	}
	thread.SetLocal(LabelResolverKey, labelResolver)
	err = eval.ExecFile(thread, compile.File(f, m), eval.NewInMemoryModule(filenameStr, predeclared, nil))
	if !defaultAttrs.IsSet() {
		defaultAttrs = model_core.NewSimplePatchedMessage[TMetadata](&DefaultInheritableAttrs)
	}
	return defaultAttrs, err
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
	inlinedTreeOptions *inlinedtree.Options,
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
