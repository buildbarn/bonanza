package starlark

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	pg_label "bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/inlinedtree"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/unpack"
	"bonanza.build/pkg/storage/object"

	"go.starlark.net/starlark"
)

// TargetRegistrar can be called into by functions like alias(),
// exports_files(), label_flag(), label_setting(), package_group() and
// invocations of rules and symbolic macros to register any targets and
// macro instances in the current package, or in the current macro
// instance.
type TargetRegistrar[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	// Immutable fields.
	context            context.Context
	encoder            model_encoding.DeterministicBinaryEncoder
	inlinedTreeOptions *inlinedtree.Options
	objectManager      model_core.ObjectManager[TReference, TMetadata]
	macro              *registrarMacro[TReference]

	// The value of "visibility" arguments that are omitted, which
	// is equivalent to setting them to None.
	omittedVisibility visibilityArgument[TReference, TMetadata]

	// Mutable fields.
	defaultInheritableAttrs    model_core.Message[*model_starlark_pb.InheritableAttrs, TReference]
	setDefaultInheritableAttrs bool
	targets                    map[string]model_core.PatchedMessage[*model_starlark_pb.Target_Definition, TMetadata]
	macroInstances             map[string]model_core.PatchedMessage[*model_starlark_pb.MacroInstance_Definition, TMetadata]
	macroExpansionContexts     map[string]model_core.PatchedMessage[*model_starlark_pb.MacroExpansionContext, TMetadata]
}

// registrarMacro contains the properties of the macro instance whose
// implementation function is being invoked, if targets are registered
// from within a symbolic macro.
type registrarMacro[TReference any] struct {
	name              string
	definitionPackage pg_label.CanonicalPackage

	// The visibility of the macro instance, which is provided to
	// the implementation function as an opaque MacroVisibility
	// value.
	visibility model_core.Message[*model_starlark_pb.PackageGroup, TReference]

	// Names of targets that the macro instance may not declare, as
	// they are declared outside of the macro instance. These are
	// relative to the name of the macro instance, and sorted.
	forbiddenTargetNameSuffixes []string

	// Whether the macro instance is a finalizer.
	finalizer bool
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
		omittedVisibility:       topLevelVisibilityArgument[TReference, TMetadata]{},
		defaultInheritableAttrs: defaultInheritableAttrs,
		targets:                 map[string]model_core.PatchedMessage[*model_starlark_pb.Target_Definition, TMetadata]{},
		macroInstances:          map[string]model_core.PatchedMessage[*model_starlark_pb.MacroInstance_Definition, TMetadata]{},
		macroExpansionContexts:  map[string]model_core.PatchedMessage[*model_starlark_pb.MacroExpansionContext, TMetadata]{},
	}
}

// NewMacroTargetRegistrar creates a TargetRegistrar for registering
// the targets and macro instances declared by the implementation
// function of a symbolic macro.
//
// The names of these need to be equal to the name of the macro
// instance, or be prefixed by it. Targets inherit attributes from the
// macro instance. By default, they are only visible to the package
// containing the macro's definition.
//
// Targets in the namespace of the macro instance that are referenced by
// the caller of the macro, but not declared by it, are registered
// implicitly. If the macro does not declare these targets, they are
// assumed to refer to source files. Targets that are declared outside
// of the macro instance may not be declared.
//
// Macro instances that are declared outside of the macro instance, but
// whose namespace is contained in the namespace of the macro instance,
// are provided by the macro expansion context. These need to be
// registered by calling RegisterNestedMacroInstance().
func NewMacroTargetRegistrar[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](ctx context.Context, encoder model_encoding.DeterministicBinaryEncoder, inlinedTreeOptions *inlinedtree.Options, objectManager model_core.ObjectManager[TReference, TMetadata], macroName string, macroDefinitionPackage pg_label.CanonicalPackage, macroExpansionContextDefinition model_core.Message[*model_starlark_pb.MacroExpansionContext_Definition, TReference]) *TargetRegistrar[TReference, TMetadata] {
	inheritableAttrs := model_core.Nested(macroExpansionContextDefinition, macroExpansionContextDefinition.Message.GetRootMacroInstanceDefinition().GetInheritableAttrs())
	tr := NewTargetRegistrar(ctx, encoder, inlinedTreeOptions, objectManager, inheritableAttrs)
	tr.macro = &registrarMacro[TReference]{
		name:                        macroName,
		definitionPackage:           macroDefinitionPackage,
		visibility:                  model_core.Nested(inheritableAttrs, inheritableAttrs.Message.GetVisibility()),
		forbiddenTargetNameSuffixes: macroExpansionContextDefinition.Message.GetForbiddenTargetNameSuffixes(),
		finalizer:                   macroExpansionContextDefinition.Message.GetRootMacroInstanceDefinition().GetFinalizer(),
	}
	tr.omittedVisibility = macroVisibilityArgument[TReference, TMetadata]{}
	for _, suffix := range macroExpansionContextDefinition.Message.GetReferencedTargetNameSuffixes() {
		tr.targets[macroName+suffix] = model_core.PatchedMessage[*model_starlark_pb.Target_Definition, TMetadata]{}
	}
	return tr
}

// Discard all targets, so that any resources associated with them are
// released.
func (tr *TargetRegistrar[TReference, TMetadata]) Discard() {
	for _, target := range tr.targets {
		target.Discard()
	}
	for _, macroInstance := range tr.macroInstances {
		macroInstance.Discard()
	}
	for _, macroExpansionContext := range tr.macroExpansionContexts {
		macroExpansionContext.Discard()
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

// GetMacroExpansionContextNames returns the names of the macro
// instances for which Finalize() created macro expansion contexts, in
// the order of CompareMacroInstanceNames().
func (tr *TargetRegistrar[TReference, TMetadata]) GetMacroExpansionContextNames() []string {
	return slices.SortedFunc(maps.Keys(tr.macroExpansionContexts), CompareMacroInstanceNames)
}

// GetAndRemoveMacroExpansionContext gets the context for expanding a
// macro instance from the TargetRegistrar and subsequently removes it.
// The caller then owns the message and its associated resources.
func (tr *TargetRegistrar[TReference, TMetadata]) GetAndRemoveMacroExpansionContext(name string) model_core.PatchedMessage[*model_starlark_pb.MacroExpansionContext, TMetadata] {
	macroExpansionContext := tr.macroExpansionContexts[name]
	delete(tr.macroExpansionContexts, name)
	return macroExpansionContext
}

// newVisibilityUnpackerInto returns an unpacker for "visibility"
// arguments of functions that declare targets and macro instances. At
// the top level of a BUILD file, visibility is provided as a list of
// labels. Within symbolic macros, it can only be set to the visibility
// that was provided to the macro's implementation function.
func (tr *TargetRegistrar[TReference, TMetadata]) newVisibilityUnpackerInto(currentPackage pg_label.CanonicalPackage) unpack.UnpackerInto[visibilityArgument[TReference, TMetadata]] {
	if tr.macro != nil {
		return macroVisibilityUnpackerInto[TReference, TMetadata]{}
	}
	return topLevelVisibilityUnpackerInto[TReference, TMetadata]{
		labels: unpack.IfNotNone(unpack.List(NewLabelOrStringUnpackerInto[TReference, TMetadata](currentPackage))),
	}
}

// getOmittedVisibility returns the value of "visibility" arguments
// that are omitted, which is equivalent to setting them to None. It can
// be used to initialize "visibility" arguments prior to unpacking.
func (tr *TargetRegistrar[TReference, TMetadata]) getOmittedVisibility() visibilityArgument[TReference, TMetadata] {
	return tr.omittedVisibility
}

// addPackageToVisibility adds a package to a package group, or creates
// a new package group containing only the package if none is provided.
func (tr *TargetRegistrar[TReference, TMetadata]) addPackageToVisibility(thread *starlark.Thread, packageGroup model_core.Message[*model_starlark_pb.PackageGroup, TReference], canonicalPackage pg_label.CanonicalPackage) (model_core.PatchedMessage[*model_starlark_pb.PackageGroup, TMetadata], error) {
	if !packageGroup.IsSet() {
		return NewPackageGroupFromVisibility[TMetadata](
			tr.context,
			[]pg_label.ResolvedLabel{canonicalPackage.AppendTargetName(packageTargetName).AsResolved()},
			tr.encoder,
			tr.inlinedTreeOptions,
			tr.objectManager,
		)
	}
	valueDecodingOptions, ok := thread.Local(ValueDecodingOptionsKey).(*ValueDecodingOptions[TReference])
	if !ok {
		return model_core.PatchedMessage[*model_starlark_pb.PackageGroup, TMetadata]{}, errors.New("value decoding options are not available")
	}
	return AddPackageToPackageGroup(
		tr.context,
		valueDecodingOptions.Readers.PackageGroupOverrides,
		packageGroup,
		canonicalPackage,
		tr.encoder,
		tr.inlinedTreeOptions,
		tr.objectManager,
	)
}

// checkDeclarationName checks whether a target or macro instance with
// a given name may be registered. Targets and macro instances have
// separate namespaces, meaning that a target may have the same name as
// a macro instance.
func (tr *TargetRegistrar[TReference, TMetadata]) checkDeclarationName(name string) error {
	if tr.macro != nil && !IsValidMacroDeclarationName(tr.macro.name, name) {
		return fmt.Errorf("name %#v is not equal to the name of macro instance %#v, nor prefixed by it followed by \"_\", \"-\" or \".\"", name, tr.macro.name)
	}
	return nil
}

func (tr *TargetRegistrar[TReference, TMetadata]) registerExplicitTarget(name string, target model_core.PatchedMessage[*model_starlark_pb.Target_Definition, TMetadata]) error {
	if err := tr.checkDeclarationName(name); err != nil {
		return err
	}
	if tr.macro != nil {
		// Lookups of targets that are declared outside of the
		// macro instance don't expand the macro instance,
		// meaning it may not declare them.
		if _, ok := slices.BinarySearch(tr.macro.forbiddenTargetNameSuffixes, name[len(tr.macro.name):]); ok {
			return fmt.Errorf("target %#v is already declared outside of this macro instance", name)
		}
	}
	if tr.targets[name].IsSet() {
		return fmt.Errorf("package contains multiple targets with name %#v", name)
	}
	tr.targets[name] = target
	return nil
}

func (tr *TargetRegistrar[TReference, TMetadata]) registerMacroInstance(name string, definition model_core.PatchedMessage[*model_starlark_pb.MacroInstance_Definition, TMetadata]) error {
	if err := tr.checkDeclarationName(name); err != nil {
		return err
	}
	if tr.macroInstances[name].IsSet() {
		return fmt.Errorf("package contains multiple macro instances with name %#v", name)
	}
	if tr.macro != nil && name == tr.macro.name {
		// Macro instances need to have a name that is distinct
		// from the macro instance that declares them, as there
		// would otherwise be no way to address them.
		return fmt.Errorf("macro instance %#v cannot declare a macro instance with the same name", name)
	}
	tr.macroInstances[name] = definition
	return nil
}

// RegisterNestedMacroInstance registers a macro instance that is
// declared outside of the macro instance whose implementation function
// is being invoked, but whose namespace is contained within the
// namespace of the latter. The TargetRegistrar treats it as if it were
// declared by the macro instance.
func (tr *TargetRegistrar[TReference, TMetadata]) RegisterNestedMacroInstance(macroInstance model_core.PatchedMessage[*model_starlark_pb.MacroInstance, TMetadata]) error {
	return tr.registerMacroInstance(
		tr.macro.name+macroInstance.Message.NameSuffix,
		model_core.NewPatchedMessage(macroInstance.Message.Definition, macroInstance.Patcher),
	)
}

// getNamespaceOwner returns the name of the macro instance whose
// namespace contains a given name. The names of the macro instances
// must be sorted using CompareMacroInstanceNames(), and their namespaces
// must be disjoint. This means that the namespace owner can only be
// the macro instance whose name precedes the provided name.
func getNamespaceOwner(macroInstanceNames []string, name string) (string, bool) {
	i, ok := slices.BinarySearchFunc(macroInstanceNames, name, CompareMacroInstanceNames)
	if ok {
		return name, true
	}
	if i > 0 && IsValidMacroDeclarationName(macroInstanceNames[i-1], name) {
		return macroInstanceNames[i-1], true
	}
	return "", false
}

// getNamesInNamespace returns the names in a sorted list that are part
// of the namespace of a macro instance, relative to its name. Names in
// the namespace are always within the range [name, name+"`"), as "`"
// sorts after "-", "." and "_".
func getNamesInNamespace(macroInstanceName string, sortedNames []string) []string {
	start, _ := slices.BinarySearch(sortedNames, macroInstanceName)
	end, _ := slices.BinarySearch(sortedNames, macroInstanceName+"`")
	var nameSuffixes []string
	for _, name := range sortedNames[start:end] {
		if IsValidMacroDeclarationName(macroInstanceName, name) {
			nameSuffixes = append(nameSuffixes, name[len(macroInstanceName):])
		}
	}
	return nameSuffixes
}

// getForbiddenTargetNameSuffixes computes the names of targets that a
// macro instance declared against this TargetRegistrar may not
// declare. These are the targets that are declared against this
// TargetRegistrar, and the targets that the current macro instance may
// not declare.
func (tr *TargetRegistrar[TReference, TMetadata]) getForbiddenTargetNameSuffixes(macroInstanceName string, targetNames []string) []string {
	forbiddenTargetNameSuffixes := getNamesInNamespace(macroInstanceName, targetNames)
	if tr.macro != nil {
		forbiddenTargetNameSuffixes = append(
			forbiddenTargetNameSuffixes,
			getNamesInNamespace(macroInstanceName[len(tr.macro.name):], tr.macro.forbiddenTargetNameSuffixes)...,
		)
		slices.Sort(forbiddenTargetNameSuffixes)
	}
	return forbiddenTargetNameSuffixes
}

// Finalize the targets that are registered against the
// TargetRegistrar. This needs to be called after all targets in the
// package have been declared, and before the targets are extracted
// using GetAndRemoveTarget().
//
// Targets that are referenced, but not declared explicitly, are
// assumed to refer to source files. However, if their names are in the
// namespace of a macro instance, the macro instance may declare them.
// Such targets are therefore forwarded to the macro instance.
//
// For each macro instance, a context for expanding it is created,
// which can be extracted using GetAndRemoveMacroExpansionContext().
// Macro instances whose namespace is contained within the namespace of
// another macro instance are stored in the context of the latter. This
// ensures that the namespaces of the macro expansion contexts are
// disjoint. Targets that are referenced or declared in the namespace
// of a macro instance are stored in its context, so that they can be
// processed when the macro instance is expanded. Macro instances may
// not declare targets that are declared by their callers. Outside of
// finalizers, the namespace of a finalizer may not contain non-finalizer
// macro instances.
//
// Test suites that don't list any tests explicitly are filled in with
// the tests in the package that are not tagged "manual" and pass the
// test suite's filters. This makes such test suites equivalent to ones
// listing these tests explicitly.
func (tr *TargetRegistrar[TReference, TMetadata]) Finalize(canonicalPackage pg_label.CanonicalPackage, valueEncodingOptions *ValueEncodingOptions[TReference, TMetadata]) error {
	// As the namespace of a macro instance is a contiguous range
	// of names that starts at the name of the macro instance,
	// macro instances whose namespace is contained within the
	// namespace of another macro instance directly follow it.
	// Create macro expansion contexts for all macro instances that
	// are not contained in another one's namespace.
	nameSuffixOffset := 0
	if tr.macro != nil {
		nameSuffixOffset = len(tr.macro.name)
	}
	var outerMacroInstanceNames []string
	nestedMacroInstanceNames := map[string][]string{}
	for _, name := range slices.SortedFunc(maps.Keys(tr.macroInstances), CompareMacroInstanceNames) {
		if n := len(outerMacroInstanceNames); n > 0 && IsValidMacroDeclarationName(outerMacroInstanceNames[n-1], name) {
			// Finalizers are expanded after all targets
			// declared by non-finalizer macro instances,
			// meaning they can't contain such macro
			// instances. This is only a concern if targets
			// are not already declared by a finalizer.
			outerName := outerMacroInstanceNames[n-1]
			if tr.macroExpansionContexts[outerName].Message.Definition.RootMacroInstanceDefinition.Finalizer &&
				!tr.macroInstances[name].Message.Finalizer &&
				(tr.macro == nil || !tr.macro.finalizer) {
				return fmt.Errorf("non-finalizer macro instance %#v cannot be part of the namespace of finalizer %#v", name, outerName)
			}
			nestedMacroInstanceNames[outerName] = append(nestedMacroInstanceNames[outerName], name)
		} else {
			outerMacroInstanceNames = append(outerMacroInstanceNames, name)
			definition := tr.macroInstances[name]
			delete(tr.macroInstances, name)
			tr.macroExpansionContexts[name] = model_core.NewPatchedMessage(
				&model_starlark_pb.MacroExpansionContext{
					NameSuffix: name[nameSuffixOffset:],
					Definition: &model_starlark_pb.MacroExpansionContext_Definition{
						RootMacroInstanceDefinition: definition.Message,
					},
				},
				definition.Patcher,
			)
		}
	}

	for _, name := range tr.GetTargetNames() {
		if !tr.targets[name].IsSet() {
			if owner, ok := getNamespaceOwner(outerMacroInstanceNames, name); ok {
				definition := tr.macroExpansionContexts[owner].Message.Definition
				definition.ReferencedTargetNameSuffixes = append(definition.ReferencedTargetNameSuffixes, name[len(owner):])
				delete(tr.targets, name)
			}
		}
	}

	targetNames := tr.GetTargetNames()
	for _, name := range outerMacroInstanceNames {
		macroExpansionContext := tr.macroExpansionContexts[name]
		macroExpansionContext.Message.Definition.ForbiddenTargetNameSuffixes = tr.getForbiddenTargetNameSuffixes(name, targetNames)

		if nestedNames := nestedMacroInstanceNames[name]; len(nestedNames) > 0 {
			nestedMacroInstances, err := tr.buildNestedMacroInstances(name, nestedNames, valueEncodingOptions)
			if err != nil {
				return fmt.Errorf("failed to build list of nested macro instances of macro instance %#v: %w", name, err)
			}
			macroExpansionContext.Message.Definition.NestedMacroInstances = nestedMacroInstances.Message
			macroExpansionContext.Patcher.Merge(nestedMacroInstances.Patcher)
		}
	}

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

// buildNestedMacroInstances removes macro instances from the
// TargetRegistrar, and stores them in a B-tree, so that they can be
// stored in the context for expanding the macro instance whose
// namespace contains them.
func (tr *TargetRegistrar[TReference, TMetadata]) buildNestedMacroInstances(outerName string, nestedNames []string, valueEncodingOptions *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[[]*model_starlark_pb.MacroInstance_Entry, TMetadata], error) {
	treeBuilder := newMacroInstanceListBuilder(valueEncodingOptions)
	defer treeBuilder.Discard()
	for _, name := range slices.Sorted(slices.Values(nestedNames)) {
		definition := tr.macroInstances[name]
		delete(tr.macroInstances, name)
		if err := treeBuilder.PushChild(model_core.NewPatchedMessage(
			&model_starlark_pb.MacroInstance_Entry{
				Level: &model_starlark_pb.MacroInstance_Entry_Leaf{
					Leaf: &model_starlark_pb.MacroInstance{
						NameSuffix: name[len(outerName):],
						Definition: definition.Message,
					},
				},
			},
			definition.Patcher,
		)); err != nil {
			return model_core.PatchedMessage[[]*model_starlark_pb.MacroInstance_Entry, TMetadata]{}, err
		}
	}
	return treeBuilder.FinalizeList()
}

func (tr *TargetRegistrar[TReference, TMetadata]) registerImplicitTarget(name string) {
	if tr.macro != nil {
		// Symbolic macros cannot declare source file targets.
		// These need to be referenced by the BUILD file.
		return
	}
	if _, ok := tr.targets[name]; !ok {
		tr.targets[name] = model_core.PatchedMessage[*model_starlark_pb.Target_Definition, TMetadata]{}
	}
}

// getTopLevelMacroInstanceVisibility computes the visibility of a macro
// instance that is declared at the top level of a BUILD file, if
// visibility is provided explicitly. Such macro instances are always
// visible to the package itself.
func getTopLevelMacroInstanceVisibility(visibility []pg_label.ResolvedLabel, currentPackage pg_label.CanonicalPackage) []pg_label.ResolvedLabel {
	newVisibility := make([]pg_label.ResolvedLabel, 0, len(visibility)+1)
	for _, l := range visibility {
		if canonicalLabel, err := l.AsCanonical(); err == nil && canonicalLabel.GetCanonicalPackage().GetPackagePath() == "visibility" {
			switch canonicalLabel.GetTargetName().String() {
			case "public":
				// Already visible everywhere.
				return []pg_label.ResolvedLabel{l}
			case "private":
				// Only visible to the package.
				continue
			}
		}
		newVisibility = append(newVisibility, l)
	}
	return append(newVisibility, currentPackage.AppendTargetName(packageTargetName).AsResolved())
}
