package analysis_test

import (
	"encoding"
	"errors"
	"testing"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

	"go.uber.org/mock/gomock"
)

// newRuleTargetDeclaration creates an entry in the list of targets
// and macro instances of a package that corresponds to a rule target.
func newRuleTargetDeclaration(name, ruleIdentifier string) *model_analysis_pb.Package_Value_Target {
	return &model_analysis_pb.Package_Value_Target{
		Level: &model_analysis_pb.Package_Value_Target_Leaf{
			Leaf: &model_starlark_pb.Target{
				Name: name,
				Definition: &model_starlark_pb.Target_Definition{
					Kind: &model_starlark_pb.Target_Definition_RuleTarget{
						RuleTarget: &model_starlark_pb.RuleTarget{
							RuleIdentifier: ruleIdentifier,
						},
					},
				},
			},
		},
	}
}

// newSourceFileTargetDeclaration creates an entry in the list of
// targets and macro instances of a package that corresponds to a
// source file target.
func newSourceFileTargetDeclaration(name string) *model_analysis_pb.Package_Value_Target {
	return &model_analysis_pb.Package_Value_Target{
		Level: &model_analysis_pb.Package_Value_Target_Leaf{
			Leaf: &model_starlark_pb.Target{
				Name: name,
				Definition: &model_starlark_pb.Target_Definition{
					Kind: &model_starlark_pb.Target_Definition_SourceFileTarget{
						SourceFileTarget: &model_starlark_pb.SourceFileTarget{},
					},
				},
			},
		},
	}
}

// newMacroExpansionContextDeclaration creates an entry in the list of
// macro expansion contexts of a package or macro instance. For macro
// instances declared by other macro instances, the name suffix is
// relative to the name of the declaring macro instance.
func newMacroExpansionContextDeclaration(nameSuffix string) *model_starlark_pb.MacroExpansionContext_Entry {
	return &model_starlark_pb.MacroExpansionContext_Entry{
		Level: &model_starlark_pb.MacroExpansionContext_Entry_Leaf{
			Leaf: &model_starlark_pb.MacroExpansionContext{
				NameSuffix: nameSuffix,
				Definition: &model_starlark_pb.MacroExpansionContext_Definition{
					RootMacroInstanceDefinition: &model_starlark_pb.MacroInstance_Definition{
						MacroIdentifier: "@@rules+//:defs.bzl%my_macro",
					},
				},
			},
		},
	}
}

// declarations is a helper type for constructing the targets and macro
// expansion contexts in Package values.
type declarations struct {
	targets                []*model_analysis_pb.Package_Value_Target
	macroExpansionContexts []*model_starlark_pb.MacroExpansionContext_Entry
}

func newDeclarations(targets ...*model_analysis_pb.Package_Value_Target) declarations {
	return declarations{targets: targets}
}

func (d declarations) withMacroExpansionContexts(nameSuffixes ...string) declarations {
	for _, nameSuffix := range nameSuffixes {
		d.macroExpansionContexts = append(d.macroExpansionContexts, newMacroExpansionContextDeclaration(nameSuffix))
	}
	return d
}

func TestTarget(t *testing.T) {
	ctrl, ctx := gomock.WithContext(t.Context(), t)
	bct := newBaseComputerTester(ctrl)

	expectPackage := func(e *MockTargetEnvironmentForTesting, d declarations) {
		e.EXPECT().GetPackageValue(
			testutil.EqProto(t, &model_analysis_pb.Package_Key{Label: "@@main+//foo"}),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](&model_analysis_pb.Package_Value{
			Targets:                d.targets,
			MacroExpansionContexts: d.macroExpansionContexts,
		}))
	}
	expectMacroInstance := func(e *MockTargetEnvironmentForTesting, macroInstanceNameSuffixes []string, d declarations) {
		e.EXPECT().GetPackageValue(
			testutil.EqProto(t, &model_analysis_pb.Package_Key{Label: "@@main+//foo", MacroInstanceName: macroInstanceNameSuffixes}),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](&model_analysis_pb.Package_Value{
			Targets:                d.targets,
			MacroExpansionContexts: d.macroExpansionContexts,
		}))
	}
	// getRuleIdentifier returns the identifier of the rule of a
	// target. Targets that don't exist are reported as an error.
	getRuleIdentifier := func(t *testing.T, label string, e *MockTargetEnvironmentForTesting) (string, error) {
		value, err := bct.computer.ComputeTargetValue(ctx, &model_analysis_pb.Target_Key{Label: label}, e)
		if err != nil {
			return "", err
		}
		if value.Message.Definition == nil {
			return "", errors.New("target does not exist")
		}
		ruleTarget := value.Message.Definition.GetRuleTarget()
		require.NotNil(t, ruleTarget)
		return ruleTarget.RuleIdentifier, nil
	}

	t.Run("TopLevel", func(t *testing.T) {
		// Targets declared at the top level of a package should
		// be returned without expanding any macro instances,
		// even if they are part of the namespace of a macro
		// instance.
		for _, name := range []string{"bar", "bar_baz", "barbaz"} {
			e := NewMockTargetEnvironmentForTesting(ctrl)
			expectPackage(e, newDeclarations(
				newRuleTargetDeclaration(name, "@@rules+//:defs.bzl%top_level"),
			).withMacroExpansionContexts("bar"))

			ruleIdentifier, err := getRuleIdentifier(t, "@@main+//foo:"+name, e)
			require.NoError(t, err, name)
			require.Equal(t, "@@rules+//:defs.bzl%top_level", ruleIdentifier, name)
		}
	})

	t.Run("DeclaredByMacro", func(t *testing.T) {
		// Targets whose name is equal to the name of a macro
		// instance, or prefixed by it followed by a separator,
		// should be looked up in the declarations of the macro
		// instance if not declared at the top level.
		for _, name := range []string{"bar", "bar_baz", "bar-baz", "bar.baz"} {
			e := NewMockTargetEnvironmentForTesting(ctrl)
			expectPackage(e, newDeclarations().withMacroExpansionContexts("bar"))
			expectMacroInstance(e, []string{"bar"}, newDeclarations(
				newRuleTargetDeclaration(name, "@@rules+//:defs.bzl%in_macro"),
			))

			ruleIdentifier, err := getRuleIdentifier(t, "@@main+//foo:"+name, e)
			require.NoError(t, err, name)
			require.Equal(t, "@@rules+//:defs.bzl%in_macro", ruleIdentifier, name)
		}
	})

	t.Run("MacroInstanceNameNotATargetName", func(t *testing.T) {
		// Names of macro instances don't need to be valid
		// target names.
		e := NewMockTargetEnvironmentForTesting(ctrl)
		expectPackage(e, newDeclarations().withMacroExpansionContexts("bar/"))
		expectMacroInstance(e, []string{"bar/"}, newDeclarations(
			newRuleTargetDeclaration("bar/_baz", "@@rules+//:defs.bzl%in_macro"),
		))

		ruleIdentifier, err := getRuleIdentifier(t, "@@main+//foo:bar/_baz", e)
		require.NoError(t, err)
		require.Equal(t, "@@rules+//:defs.bzl%in_macro", ruleIdentifier)
	})

	t.Run("MacroInstancesInOtherObjects", func(t *testing.T) {
		// Lists of macro instances that are too large to store
		// in a single object are stored as B-trees. As "_"
		// sorts before "0", the macro instance preceding
		// "bar_x" is stored in the first object.
		e := NewMockTargetEnvironmentForTesting(ctrl)
		e.EXPECT().GetPackageValue(
			testutil.EqProto(t, &model_analysis_pb.Package_Key{Label: "@@main+//foo"}),
		).Return(newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.Package_Value {
			return &model_analysis_pb.Package_Value{
				MacroExpansionContexts: []*model_starlark_pb.MacroExpansionContext_Entry{
					newMacroExpansionContextsParent(patcher, "a", "bar"),
					newMacroExpansionContextsParent(patcher, "bar0", "qux"),
				},
			}
		}))
		expectMacroInstance(e, []string{"bar"}, newDeclarations(
			newRuleTargetDeclaration("bar_x", "@@rules+//:defs.bzl%in_macro"),
		))

		ruleIdentifier, err := getRuleIdentifier(t, "@@main+//foo:bar_x", e)
		require.NoError(t, err)
		require.Equal(t, "@@rules+//:defs.bzl%in_macro", ruleIdentifier)
	})

	t.Run("PrecedingMacroInstance", func(t *testing.T) {
		// Macro instances are sorted in such a way that the
		// namespace of a macro instance is a contiguous range
		// of names. As the namespaces of macro instances in a
		// list are disjoint, only the macro instance preceding
		// the target's name may own it.
		for targetName, owner := range map[string]string{
			"a":    "a",
			"a_b":  "a",
			"a-b":  "a",
			"a0_x": "a0",
			"a1x":  "a1x",
		} {
			e := NewMockTargetEnvironmentForTesting(ctrl)
			expectPackage(e, newDeclarations().withMacroExpansionContexts("a", "a0", "a1x", "b"))
			expectMacroInstance(e, []string{owner}, newDeclarations(
				newRuleTargetDeclaration(targetName, "@@rules+//:defs.bzl%in_"+owner),
			))

			ruleIdentifier, err := getRuleIdentifier(t, "@@main+//foo:"+targetName, e)
			require.NoError(t, err, targetName)
			require.Equal(t, "@@rules+//:defs.bzl%in_"+owner, ruleIdentifier, targetName)
		}

		// Names whose preceding macro instance does not own
		// them don't belong to any macro instance.
		for _, targetName := range []string{"0", "a1", "a1y", "c"} {
			e := NewMockTargetEnvironmentForTesting(ctrl)
			expectPackage(e, newDeclarations().withMacroExpansionContexts("a", "a0", "a1x", "b"))

			_, err := getRuleIdentifier(t, "@@main+//foo:"+targetName, e)
			require.EqualError(t, err, "target does not exist", targetName)
		}
	})

	t.Run("NestedMacros", func(t *testing.T) {
		// Macro instances may declare other macro instances.
		// These should only be expanded if their namespace
		// contains the target's name.
		e := NewMockTargetEnvironmentForTesting(ctrl)
		expectPackage(e, newDeclarations().withMacroExpansionContexts("a"))
		expectMacroInstance(e, []string{"a"}, newDeclarations().withMacroExpansionContexts("_b", "_c"))
		expectMacroInstance(e, []string{"a", "_b"}, newDeclarations(
			newRuleTargetDeclaration("a_b_c", "@@rules+//:defs.bzl%nested"),
		))

		ruleIdentifier, err := getRuleIdentifier(t, "@@main+//foo:a_b_c", e)
		require.NoError(t, err)
		require.Equal(t, "@@rules+//:defs.bzl%nested", ruleIdentifier)
	})

	t.Run("NestedMacroWithSameName", func(t *testing.T) {
		// Macro instances cannot declare macro instances with
		// the same name. Even if they did, lookups should
		// terminate, as every macro instance that is expanded
		// causes the macro instance name to become longer.
		e := NewMockTargetEnvironmentForTesting(ctrl)
		expectPackage(e, newDeclarations().withMacroExpansionContexts("a"))
		expectMacroInstance(e, []string{"a"}, newDeclarations().withMacroExpansionContexts(""))
		expectMacroInstance(e, []string{"a", ""}, newDeclarations())

		_, err := getRuleIdentifier(t, "@@main+//foo:a", e)
		require.EqualError(t, err, "target does not exist")
	})

	t.Run("MissingDependencies", func(t *testing.T) {
		// Lookups should stop as soon as a macro instance
		// owning the name cannot be expanded.
		e := NewMockTargetEnvironmentForTesting(ctrl)
		expectPackage(e, newDeclarations().withMacroExpansionContexts("a"))
		e.EXPECT().GetPackageValue(
			testutil.EqProto(t, &model_analysis_pb.Package_Key{Label: "@@main+//foo", MacroInstanceName: []string{"a"}}),
		).Return(model_core.Message[*model_analysis_pb.Package_Value, model_core.CreatedObjectTree]{})

		_, err := getRuleIdentifier(t, "@@main+//foo:a_b_c", e)
		require.Equal(t, evaluation.ErrMissingDependency, err)
	})

	t.Run("DoesNotExist", func(t *testing.T) {
		e := NewMockTargetEnvironmentForTesting(ctrl)
		expectPackage(e, newDeclarations().withMacroExpansionContexts("bar"))
		expectMacroInstance(e, []string{"bar"}, newDeclarations(
			newRuleTargetDeclaration("bar_other", "@@rules+//:defs.bzl%in_macro"),
		))

		// Targets that don't exist should yield a value
		// without a definition, as opposed to an error.
		value, err := bct.computer.ComputeTargetValue(ctx, &model_analysis_pb.Target_Key{Label: "@@main+//foo:bar_out"}, e)
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_analysis_pb.Target_Value{}, value.Message)

		// Macro instances themselves are not targets.
		e = NewMockTargetEnvironmentForTesting(ctrl)
		expectPackage(e, newDeclarations().withMacroExpansionContexts("bar"))
		expectMacroInstance(e, []string{"bar"}, newDeclarations().withMacroExpansionContexts("_nested"))
		expectMacroInstance(e, []string{"bar", "_nested"}, newDeclarations())

		_, err = getRuleIdentifier(t, "@@main+//foo:bar_nested", e)
		require.EqualError(t, err, "target does not exist")
	})
}

// newMacroExpansionContextsParent creates a parent node in a list of
// macro expansion contexts, whose children are stored in a separate
// object.
func newMacroExpansionContextsParent(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree], nameSuffixes ...string) *model_starlark_pb.MacroExpansionContext_Entry {
	children := make([]*model_starlark_pb.MacroExpansionContext_Entry, 0, len(nameSuffixes))
	for _, nameSuffix := range nameSuffixes {
		children = append(children, newMacroExpansionContextDeclaration(nameSuffix))
	}
	return &model_starlark_pb.MacroExpansionContext_Entry{
		Level: &model_starlark_pb.MacroExpansionContext_Entry_Parent_{
			Parent: &model_starlark_pb.MacroExpansionContext_Entry_Parent{
				Reference: attachObject(patcher, newObject(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) encoding.BinaryMarshaler {
					return model_core.NewProtoListBinaryMarshaler(children)
				})),
				FirstNameSuffix: nameSuffixes[0],
			},
		},
	}
}

// newMacroInstanceEntry creates an entry in a list of macro instances
// of macro "@@rules+//:defs.bzl%my_macro", as stored in macro
// expansion contexts.
func newMacroInstanceEntry(nameSuffix string) *model_starlark_pb.MacroInstance_Entry {
	return &model_starlark_pb.MacroInstance_Entry{
		Level: &model_starlark_pb.MacroInstance_Entry_Leaf{
			Leaf: &model_starlark_pb.MacroInstance{
				NameSuffix: nameSuffix,
				Definition: &model_starlark_pb.MacroInstance_Definition{
					MacroIdentifier: "@@rules+//:defs.bzl%my_macro",
				},
			},
		},
	}
}

// newMacroInstancesParent creates a parent node in a list of macro
// instances, whose children are stored in a separate object.
func newMacroInstancesParent(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree], nameSuffixes ...string) *model_starlark_pb.MacroInstance_Entry {
	children := make([]*model_starlark_pb.MacroInstance_Entry, 0, len(nameSuffixes))
	for _, nameSuffix := range nameSuffixes {
		children = append(children, newMacroInstanceEntry(nameSuffix))
	}
	return &model_starlark_pb.MacroInstance_Entry{
		Level: &model_starlark_pb.MacroInstance_Entry_Parent_{
			Parent: &model_starlark_pb.MacroInstance_Entry_Parent{
				Reference: attachObject(patcher, newObject(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) encoding.BinaryMarshaler {
					return model_core.NewProtoListBinaryMarshaler(children)
				})),
			},
		},
	}
}
