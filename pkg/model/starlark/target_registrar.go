package starlark

import (
	"context"
	"fmt"
	"maps"
	"slices"

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
	context            context.Context
	encoder            model_encoding.DeterministicBinaryEncoder
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
func NewTargetRegistrar[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](ctx context.Context, encoder model_encoding.DeterministicBinaryEncoder, inlinedTreeOptions *inlinedtree.Options, objectManager model_core.ObjectManager[TReference, TMetadata], defaultInheritableAttrs model_core.Message[*model_starlark_pb.InheritableAttrs, TReference]) *TargetRegistrar[TReference, TMetadata] {
	return &TargetRegistrar[TReference, TMetadata]{
		context:                 ctx,
		encoder:                 encoder,
		inlinedTreeOptions:      inlinedTreeOptions,
		objectManager:           objectManager,
		defaultInheritableAttrs: defaultInheritableAttrs,
		targets:                 map[string]model_core.PatchedMessage[*model_starlark_pb.Target_Definition, TMetadata]{},
	}
}

// Discard all targets, so that any resources associated with them are
// released.
func (tr *TargetRegistrar[TReference, TMetadata]) Discard() {
	for _, target := range tr.targets {
		target.Discard()
	}
}

// GetTargetNames returns the set of target names in the current package
// that have been registered against this TargetRegistrar.
func (tr *TargetRegistrar[TReference, TMetadata]) GetTargetNames() []string {
	return slices.Sorted(maps.Keys(tr.targets))
}

var sourceFileTarget = &model_starlark_pb.Target_Definition{
	Kind: &model_starlark_pb.Target_Definition_SourceFileTarget{
		SourceFileTarget: &model_starlark_pb.SourceFileTarget{
			Visibility: &model_starlark_pb.PackageGroup{
				Tree: &model_starlark_pb.PackageGroup_Subpackages{},
			},
		},
	},
}

// GetAndRemoveTarget gets the definition of the target from the
// TargetRegistrar and subsequently removes it. The caller then owns the
// message and its associated resources.
func (tr *TargetRegistrar[TReference, TMetadata]) GetAndRemoveTarget(name string) model_core.PatchedMessage[*model_starlark_pb.Target_Definition, TMetadata] {
	target := tr.targets[name]
	delete(tr.targets, name)
	if !target.IsSet() {
		// Target is referenced, but not provided explicitly.
		// Assume it refers to a source file with private
		// visibility.
		target = model_core.NewSimplePatchedMessage[TMetadata](sourceFileTarget)
	}
	return target
}

func (tr *TargetRegistrar[TReference, TMetadata]) getVisibilityPackageGroup(visibility []pg_label.ResolvedLabel) (model_core.PatchedMessage[*model_starlark_pb.PackageGroup, TMetadata], error) {
	if len(visibility) > 0 {
		// Explicit visibility provided. Construct new package group.
		return NewPackageGroupFromVisibility[TMetadata](tr.context, visibility, tr.encoder, tr.inlinedTreeOptions, tr.objectManager)
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

// Finalize the targets that are registered against the
// TargetRegistrar. This needs to be called after all targets in the
// package have been declared, and before the targets are extracted
// using GetAndRemoveTarget().
//
// Test suites that don't list any tests explicitly are filled in with
// the tests in the package that are not tagged "manual" and pass the
// test suite's filters. This makes such test suites equivalent to ones
// listing these tests explicitly.
func (tr *TargetRegistrar[TReference, TMetadata]) Finalize(canonicalPackage pg_label.CanonicalPackage, valueEncodingOptions *ValueEncodingOptions[TReference, TMetadata]) error {
	type packageTest struct {
		label      string
		ruleTarget *model_starlark_pb.RuleTarget
	}
	var packageTests []packageTest
	var testSuiteNames []string
	for _, name := range tr.GetTargetNames() {
		switch kind := tr.targets[name].Message.GetKind().(type) {
		case *model_starlark_pb.Target_Definition_RuleTarget:
			ruleTarget := kind.RuleTarget
			if ruleTarget.Test == nil {
				continue
			}
			if _, isManual := slices.BinarySearch(ruleTarget.Tags, "manual"); isManual {
				continue
			}
			targetName, err := pg_label.NewTargetName(name)
			if err != nil {
				return fmt.Errorf("invalid target name %#v: %w", name, err)
			}
			packageTests = append(packageTests, packageTest{
				label:      canonicalPackage.AppendTargetName(targetName).String(),
				ruleTarget: ruleTarget,
			})
		case *model_starlark_pb.Target_Definition_TestSuite:
			if len(kind.TestSuite.Tests) == 0 {
				testSuiteNames = append(testSuiteNames, name)
			}
		}
	}

	for _, name := range testSuiteNames {
		target := tr.targets[name]
		testSuite := target.Message.GetTestSuite()
		var tests []string
		for _, packageTest := range packageTests {
			if TestSuiteMatchesTest(testSuite, packageTest.ruleTarget.Tags, packageTest.ruleTarget.Test.Size) {
				tests = append(tests, packageTest.label)
			}
		}
		testsList, err := buildTestSuiteTests(tests, valueEncodingOptions)
		if err != nil {
			return fmt.Errorf("failed to build list of tests of test suite %#v: %w", name, err)
		}
		testSuite.Tests = testsList.Message
		target.Patcher.Merge(testsList.Patcher)
	}
	return nil
}

func (tr *TargetRegistrar[TReference, TMetadata]) registerImplicitTarget(name string) {
	if _, ok := tr.targets[name]; !ok {
		tr.targets[name] = model_core.PatchedMessage[*model_starlark_pb.Target_Definition, TMetadata]{}
	}
}
