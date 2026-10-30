package analysis_test

import (
	"context"
	"encoding"
	"fmt"
	"testing"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_parser "bonanza.build/pkg/model/parser"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

	"go.uber.org/mock/gomock"
)

func TestPackageTargets(t *testing.T) {
	ctrl, ctx := gomock.WithContext(t.Context(), t)
	bct := newBaseComputerTester(ctrl)

	expectPackage := func(e *MockPackageTargetsEnvironmentForTesting, d declarations, macroInstanceName ...string) {
		e.EXPECT().GetPackageValue(
			testutil.EqProto(t, &model_analysis_pb.Package_Key{Label: "@@main+//foo", MacroInstanceName: macroInstanceName}),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](&model_analysis_pb.Package_Value{
			Targets:                d.targets,
			MacroExpansionContexts: d.macroExpansionContexts,
		}))
	}
	// expectMacroInstanceTargets sets up the expectation for
	// obtaining all targets declared by a macro instance, which
	// PackageTargets computes recursively.
	expectMacroInstanceTargets := func(e *MockPackageTargetsEnvironmentForTesting, macroInstanceName []string, targets ...*model_analysis_pb.Package_Value_Target) {
		e.EXPECT().GetPackageTargetsValue(
			testutil.EqProto(t, &model_analysis_pb.PackageTargets_Key{Package: "@@main+//foo", MacroInstanceName: macroInstanceName}),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](&model_analysis_pb.PackageTargets_Value{
			Targets: targets,
		}))
	}
	expectCaptureObjects := func(e *MockPackageTargetsEnvironmentForTesting) {
		e.EXPECT().CaptureCreatedObject(gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, createdObject model_core.CreatedObject[model_core.CreatedObjectTree]) (model_core.CreatedObjectTree, error) {
				return model_core.CreatedObjectTree(createdObject), nil
			}).
			AnyTimes()
		e.EXPECT().CaptureExistingObject(gomock.Any()).
			DoAndReturn(func(reference model_core.CreatedObjectTree) model_core.CreatedObjectTree {
				return reference
			}).
			AnyTimes()
	}
	targetsReader := model_parser.LookupParsedObjectReader(
		bct.parsedObjectPoolIngester,
		model_parser.NewProtoListObjectParser[model_core.CreatedObjectTree, model_analysis_pb.Package_Value_Target](),
	)
	// newTargetsObject creates an object containing a list of
	// targets, which can be referenced by parent nodes.
	newTargetsObject := func(targets ...*model_analysis_pb.Package_Value_Target) model_core.Decodable[model_core.CreatedObject[model_core.CreatedObjectTree]] {
		return newObject(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) encoding.BinaryMarshaler {
			return model_core.NewProtoListBinaryMarshaler(targets)
		})
	}
	newParent := func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree], createdObject model_core.Decodable[model_core.CreatedObject[model_core.CreatedObjectTree]], firstName, lastName string) *model_analysis_pb.Package_Value_Target {
		return &model_analysis_pb.Package_Value_Target{
			Level: &model_analysis_pb.Package_Value_Target_Parent_{
				Parent: &model_analysis_pb.Package_Value_Target_Parent{
					Reference: attachObject(patcher, createdObject),
					FirstName: firstName,
					LastName:  lastName,
				},
			},
		}
	}
	// getAllLeaves returns the names of all targets contained in a
	// B-tree of targets. It also validates that the first and last
	// names stored in parent nodes are correct.
	var getAllLeaves func(t *testing.T, targets model_core.Message[[]*model_analysis_pb.Package_Value_Target, model_core.CreatedObjectTree]) (names []string, parents int)
	getAllLeaves = func(t *testing.T, targets model_core.Message[[]*model_analysis_pb.Package_Value_Target, model_core.CreatedObjectTree]) ([]string, int) {
		var names []string
		parents := 0
		for _, entry := range targets.Message {
			switch level := entry.Level.(type) {
			case *model_analysis_pb.Package_Value_Target_Leaf:
				names = append(names, level.Leaf.Name)
			case *model_analysis_pb.Package_Value_Target_Parent_:
				children, err := model_parser.Dereference(ctx, targetsReader, model_core.Nested(targets, level.Parent.Reference))
				require.NoError(t, err)
				childNames, childParents := getAllLeaves(t, children)
				require.NotEmpty(t, childNames)
				require.Equal(t, childNames[0], level.Parent.FirstName)
				require.Equal(t, childNames[len(childNames)-1], level.Parent.LastName)
				names = append(names, childNames...)
				parents += 1 + childParents
			default:
				require.Fail(t, "invalid level")
			}
		}
		return names, parents
	}
	// toMessage converts the result of ComputePackageTargetsValue()
	// to a message that can be traversed.
	toMessage := func(value model_core.PatchedMessage[*model_analysis_pb.PackageTargets_Value, model_core.CreatedObjectTree]) model_core.Message[[]*model_analysis_pb.Package_Value_Target, model_core.CreatedObjectTree] {
		m, metadata := value.SortAndSetReferences()
		return model_core.NewMessage(m.Message.Targets, object.OutgoingReferencesList[model_core.CreatedObjectTree](metadata))
	}

	computePackageTargets := func(e *MockPackageTargetsEnvironmentForTesting, macroInstanceName ...string) ([]*model_analysis_pb.Package_Value_Target, error) {
		value, err := bct.computer.ComputePackageTargetsValue(ctx, &model_analysis_pb.PackageTargets_Key{Package: "@@main+//foo", MacroInstanceName: macroInstanceName}, e)
		if err != nil {
			return nil, err
		}
		return value.Message.Targets, nil
	}

	t.Run("NoMacroInstances", func(t *testing.T) {
		// If the package does not contain any macro instances,
		// the targets of the package should be returned as is.
		e := NewMockPackageTargetsEnvironmentForTesting(ctrl)
		expectPackage(e, newDeclarations(
			newRuleTargetDeclaration("a", "@@rules+//:defs.bzl%my_rule"),
			newSourceFileTargetDeclaration("b.txt"),
		))

		targets, err := computePackageTargets(e)
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_analysis_pb.PackageTargets_Value{
			Targets: []*model_analysis_pb.Package_Value_Target{
				newRuleTargetDeclaration("a", "@@rules+//:defs.bzl%my_rule"),
				newSourceFileTargetDeclaration("b.txt"),
			},
		}, &model_analysis_pb.PackageTargets_Value{Targets: targets})
	})

	t.Run("MacroInstances", func(t *testing.T) {
		// The targets of all macro instances declared at the
		// top level of the package should be merged into the
		// list of targets of the package. The resulting list
		// should be sorted by name. Targets declared by callers
		// may be part of the namespace of a macro instance.
		e := NewMockPackageTargetsEnvironmentForTesting(ctrl)
		expectPackage(e, newDeclarations(
			newRuleTargetDeclaration("a_main", "@@rules+//:defs.bzl%top_level"),
			newRuleTargetDeclaration("b", "@@rules+//:defs.bzl%top_level"),
		).withMacroExpansionContexts("a", "c"))
		expectMacroInstanceTargets(e, []string{"a"},
			newRuleTargetDeclaration("a", "@@rules+//:defs.bzl%in_a"),
			newRuleTargetDeclaration("a_nested_x", "@@rules+//:defs.bzl%in_a_nested"),
			newRuleTargetDeclaration("a_out", "@@rules+//:defs.bzl%in_a"),
		)
		expectMacroInstanceTargets(e, []string{"c"})

		targets, err := computePackageTargets(e)
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_analysis_pb.PackageTargets_Value{
			Targets: []*model_analysis_pb.Package_Value_Target{
				newRuleTargetDeclaration("a", "@@rules+//:defs.bzl%in_a"),
				newRuleTargetDeclaration("a_main", "@@rules+//:defs.bzl%top_level"),
				newRuleTargetDeclaration("a_nested_x", "@@rules+//:defs.bzl%in_a_nested"),
				newRuleTargetDeclaration("a_out", "@@rules+//:defs.bzl%in_a"),
				newRuleTargetDeclaration("b", "@@rules+//:defs.bzl%top_level"),
			},
		}, &model_analysis_pb.PackageTargets_Value{Targets: targets})
	})

	t.Run("WithinMacroInstance", func(t *testing.T) {
		// Targets of macro instances should be computed by
		// merging the targets declared by the macro instance
		// with those of the macro instances it declares.
		e := NewMockPackageTargetsEnvironmentForTesting(ctrl)
		expectPackage(e, newDeclarations(
			newRuleTargetDeclaration("a", "@@rules+//:defs.bzl%in_a"),
			newRuleTargetDeclaration("a_out", "@@rules+//:defs.bzl%in_a"),
		).withMacroExpansionContexts("_nested"), "a")
		expectMacroInstanceTargets(e, []string{"a", "_nested"},
			newRuleTargetDeclaration("a_nested_x", "@@rules+//:defs.bzl%in_a_nested"),
		)

		targets, err := computePackageTargets(e, "a")
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_analysis_pb.PackageTargets_Value{
			Targets: []*model_analysis_pb.Package_Value_Target{
				newRuleTargetDeclaration("a", "@@rules+//:defs.bzl%in_a"),
				newRuleTargetDeclaration("a_nested_x", "@@rules+//:defs.bzl%in_a_nested"),
				newRuleTargetDeclaration("a_out", "@@rules+//:defs.bzl%in_a"),
			},
		}, &model_analysis_pb.PackageTargets_Value{Targets: targets})
	})

	t.Run("ParentNodes", func(t *testing.T) {
		// Subtrees that don't overlap with any other lists of
		// targets should be copied without being traversed.
		// Subtrees that do overlap should be traversed.
		copiedObject := newTargetsObject(
			newRuleTargetDeclaration("a", "@@rules+//:defs.bzl%top_level"),
			newRuleTargetDeclaration("b", "@@rules+//:defs.bzl%top_level"),
		)
		e := NewMockPackageTargetsEnvironmentForTesting(ctrl)
		expectCaptureObjects(e)
		e.EXPECT().GetPackageValue(
			testutil.EqProto(t, &model_analysis_pb.Package_Key{Label: "@@main+//foo"}),
		).Return(newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.Package_Value {
			return &model_analysis_pb.Package_Value{
				Targets: []*model_analysis_pb.Package_Value_Target{
					newParent(patcher, copiedObject, "a", "b"),
					newParent(patcher, newTargetsObject(
						newRuleTargetDeclaration("e", "@@rules+//:defs.bzl%top_level"),
						newRuleTargetDeclaration("x", "@@rules+//:defs.bzl%top_level"),
					), "e", "x"),
				},
				MacroExpansionContexts: []*model_starlark_pb.MacroExpansionContext_Entry{
					newMacroExpansionContextDeclaration("m"),
				},
			}
		}))
		expectMacroInstanceTargets(e, []string{"m"},
			newRuleTargetDeclaration("m", "@@rules+//:defs.bzl%in_m"),
		)

		value, err := bct.computer.ComputePackageTargetsValue(ctx, &model_analysis_pb.PackageTargets_Key{Package: "@@main+//foo"}, e)
		require.NoError(t, err)
		targets := toMessage(value)
		require.Len(t, targets.Message, 4)
		testutil.RequireEqualProto(t, &model_analysis_pb.Package_Value_Target_Parent{
			Reference: &model_core_pb.DecodableReference{
				Reference: &model_core_pb.Reference{Index: 1},
			},
			FirstName: "a",
			LastName:  "b",
		}, targets.Message[0].GetParent())
		require.Equal(t, copiedObject.Value.GetLocalReference(), targets.OutgoingReferences.GetOutgoingReference(0).GetLocalReference())
		names, _ := getAllLeaves(t, targets)
		require.Equal(t, []string{"a", "b", "e", "m", "x"}, names)
	})

	t.Run("ThreeWayMerge", func(t *testing.T) {
		// Parent nodes may only be copied if they don't overlap
		// with any of the other lists, not just the one that
		// happens to be stored first in the heap.
		e := NewMockPackageTargetsEnvironmentForTesting(ctrl)
		expectCaptureObjects(e)
		e.EXPECT().GetPackageValue(
			testutil.EqProto(t, &model_analysis_pb.Package_Key{Label: "@@main+//foo"}),
		).Return(newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.Package_Value {
			return &model_analysis_pb.Package_Value{
				Targets: []*model_analysis_pb.Package_Value_Target{
					newRuleTargetDeclaration("a", "@@rules+//:defs.bzl%top_level"),
					newParent(patcher, newTargetsObject(
						newRuleTargetDeclaration("b_a", "@@rules+//:defs.bzl%top_level"),
						newRuleTargetDeclaration("b_y", "@@rules+//:defs.bzl%top_level"),
					), "b_a", "b_y"),
				},
				MacroExpansionContexts: []*model_starlark_pb.MacroExpansionContext_Entry{
					newMacroExpansionContextDeclaration("b"),
					newMacroExpansionContextDeclaration("b_q"),
				},
			}
		}))
		expectMacroInstanceTargets(e, []string{"b"},
			newRuleTargetDeclaration("b", "@@rules+//:defs.bzl%in_b"),
			newRuleTargetDeclaration("b_z", "@@rules+//:defs.bzl%in_b"),
		)
		expectMacroInstanceTargets(e, []string{"b_q"},
			newRuleTargetDeclaration("b_q", "@@rules+//:defs.bzl%in_b_q"),
		)

		value, err := bct.computer.ComputePackageTargetsValue(ctx, &model_analysis_pb.PackageTargets_Key{Package: "@@main+//foo"}, e)
		require.NoError(t, err)
		names, _ := getAllLeaves(t, toMessage(value))
		require.Equal(t, []string{"a", "b", "b_a", "b_q", "b_y", "b_z"}, names)
	})

	t.Run("Large", func(t *testing.T) {
		// Merge lists of targets that are large enough to
		// require the creation of parent nodes. Then merge the
		// resulting list once more, so that parent nodes are
		// partially copied.
		newTargets := func(format string, count int) []*model_analysis_pb.Package_Value_Target {
			targets := make([]*model_analysis_pb.Package_Value_Target, 0, count)
			for i := range count {
				targets = append(targets, newRuleTargetDeclaration(fmt.Sprintf(format, i), "@@rules+//:defs.bzl%a_rule_with_a_reasonably_long_identifier"))
			}
			return targets
		}
		topLevelTargets := append(newTargets("a%05d", 2000), newTargets("n%05d", 2000)...)

		e := NewMockPackageTargetsEnvironmentForTesting(ctrl)
		expectCaptureObjects(e)
		expectPackage(e, newDeclarations(topLevelTargets...).withMacroExpansionContexts("m"))
		expectMacroInstanceTargets(e, []string{"m"}, newTargets("m_%05d", 2000)...)
		value, err := bct.computer.ComputePackageTargetsValue(ctx, &model_analysis_pb.PackageTargets_Key{Package: "@@main+//foo"}, e)
		require.NoError(t, err)
		merged := toMessage(value)
		names, parents := getAllLeaves(t, merged)
		require.Len(t, names, 6000)
		require.IsIncreasing(t, names)
		require.Greater(t, parents, 1)

		e = NewMockPackageTargetsEnvironmentForTesting(ctrl)
		expectCaptureObjects(e)
		e.EXPECT().GetPackageValue(
			testutil.EqProto(t, &model_analysis_pb.Package_Key{Label: "@@main+//foo"}),
		).Return(model_core.NewMessage(
			&model_analysis_pb.Package_Value{
				Targets: merged.Message,
				MacroExpansionContexts: []*model_starlark_pb.MacroExpansionContext_Entry{
					newMacroExpansionContextDeclaration("m"),
				},
			},
			merged.OutgoingReferences,
		))
		expectMacroInstanceTargets(e, []string{"m"},
			newRuleTargetDeclaration("m_01000x", "@@rules+//:defs.bzl%in_m"),
		)
		value, err = bct.computer.ComputePackageTargetsValue(ctx, &model_analysis_pb.PackageTargets_Key{Package: "@@main+//foo"}, e)
		require.NoError(t, err)
		names, _ = getAllLeaves(t, toMessage(value))
		require.Len(t, names, 6001)
		require.IsIncreasing(t, names)
		require.Contains(t, names, "m_01000x")
	})

	t.Run("MissingDependencies", func(t *testing.T) {
		// All macro instances should be requested before
		// returning ErrMissingDependency.
		e := NewMockPackageTargetsEnvironmentForTesting(ctrl)
		expectPackage(e, newDeclarations().withMacroExpansionContexts("a", "b"))
		for _, name := range []string{"a", "b"} {
			e.EXPECT().GetPackageTargetsValue(
				testutil.EqProto(t, &model_analysis_pb.PackageTargets_Key{Package: "@@main+//foo", MacroInstanceName: []string{name}}),
			).Return(model_core.Message[*model_analysis_pb.PackageTargets_Value, model_core.CreatedObjectTree]{})
		}

		_, err := computePackageTargets(e)
		require.Equal(t, evaluation.ErrMissingDependency, err)
	})
}
