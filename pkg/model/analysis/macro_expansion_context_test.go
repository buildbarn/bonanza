package analysis_test

import (
	"testing"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

	"go.uber.org/mock/gomock"
)

func TestMacroExpansionContext(t *testing.T) {
	ctrl, ctx := gomock.WithContext(t.Context(), t)
	bct := newBaseComputerTester(ctrl)

	expectPackage := func(e *MockMacroExpansionContextEnvironmentForTesting, packageValue model_core.Message[*model_analysis_pb.Package_Value, model_core.CreatedObjectTree], macroInstanceNameSuffixes ...string) {
		e.EXPECT().GetPackageValue(
			testutil.EqProto(t, &model_analysis_pb.Package_Key{Label: "@@main+//foo", MacroInstanceName: macroInstanceNameSuffixes}),
		).Return(packageValue)
	}
	newPackageValue := func(d declarations) model_core.Message[*model_analysis_pb.Package_Value, model_core.CreatedObjectTree] {
		return model_core.NewSimpleMessage[model_core.CreatedObjectTree](&model_analysis_pb.Package_Value{
			Targets:                d.targets,
			MacroExpansionContexts: d.macroExpansionContexts,
		})
	}
	computeMacroExpansionContext := func(e *MockMacroExpansionContextEnvironmentForTesting, nameSuffixes ...string) (*model_analysis_pb.MacroExpansionContext_Value, error) {
		value, err := bct.computer.ComputeMacroExpansionContextValue(ctx, &model_analysis_pb.MacroExpansionContext_Key{Package: "@@main+//foo", MacroInstanceName: nameSuffixes}, e)
		if err != nil {
			return nil, err
		}
		return value.Message, nil
	}
	newMacroExpansionContextDefinition := func(nameSuffix string) *model_starlark_pb.MacroExpansionContext_Definition {
		return newMacroExpansionContextDeclaration(nameSuffix).GetLeaf().Definition
	}

	t.Run("TopLevel", func(t *testing.T) {
		// Macro instances declared at the top level of a
		// package should be looked up in the package's
		// declarations.
		e := NewMockMacroExpansionContextEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisReaders(), true).AnyTimes()
		expectPackage(e, newPackageValue(newDeclarations().withMacroExpansionContexts("l", "m", "n")))

		value, err := computeMacroExpansionContext(e, "m")
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_analysis_pb.MacroExpansionContext_Value{
			Definition: newMacroExpansionContextDefinition("m"),
		}, value)
	})

	t.Run("MacroInstancesInOtherObjects", func(t *testing.T) {
		// Lists of macro instances that are too large to store
		// in a single object are stored as B-trees. As "_"
		// sorts before "0", "a_c" is stored in the first
		// object.
		e := NewMockMacroExpansionContextEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisReaders(), true).AnyTimes()
		e.EXPECT().CaptureExistingObject(gomock.Any()).
			DoAndReturn(func(reference model_core.CreatedObjectTree) model_core.CreatedObjectTree {
				return reference
			}).
			AnyTimes()
		expectPackage(e, newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.Package_Value {
			return &model_analysis_pb.Package_Value{
				MacroExpansionContexts: []*model_starlark_pb.MacroExpansionContext_Entry{
					newMacroExpansionContextsParent(patcher, "a_b", "a_c"),
					newMacroExpansionContextsParent(patcher, "a0", "b"),
				},
			}
		}))

		value, err := computeMacroExpansionContext(e, "a_c")
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_analysis_pb.MacroExpansionContext_Value{
			Definition: newMacroExpansionContextDefinition("a_c"),
		}, value)
	})

	t.Run("Nested", func(t *testing.T) {
		// Macro instances declared by other macro instances
		// should be looked up in the declarations of the last
		// macro instance in the chain.
		e := NewMockMacroExpansionContextEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisReaders(), true).AnyTimes()
		expectPackage(e, newPackageValue(newDeclarations().withMacroExpansionContexts("_n", "_o")), "m")

		value, err := computeMacroExpansionContext(e, "m", "_n")
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_analysis_pb.MacroExpansionContext_Value{
			Definition: newMacroExpansionContextDefinition("_n"),
		}, value)
	})

	t.Run("DoesNotExist", func(t *testing.T) {
		// Targets and macro instances have separate
		// namespaces, meaning that a target with the same name
		// is not a macro instance.
		e := NewMockMacroExpansionContextEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisReaders(), true).AnyTimes()
		expectPackage(e, newPackageValue(newDeclarations(
			newRuleTargetDeclaration("m", "@@rules+//:defs.bzl%my_rule"),
		)))

		_, err := computeMacroExpansionContext(e, "m")
		require.EqualError(t, err, "macro instance \"m\" does not exist")
	})

	t.Run("InvalidChain", func(t *testing.T) {
		// If the macro instance is not declared by the last
		// macro instance in the chain, it does not exist.
		e := NewMockMacroExpansionContextEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisReaders(), true).AnyTimes()
		expectPackage(e, newPackageValue(newDeclarations()), "m")

		_, err := computeMacroExpansionContext(e, "m", "_x")
		require.EqualError(t, err, "macro instance \"m_x\" does not exist")
	})

	t.Run("MissingDependency", func(t *testing.T) {
		e := NewMockMacroExpansionContextEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisReaders(), true).AnyTimes()
		expectPackage(e, model_core.Message[*model_analysis_pb.Package_Value, model_core.CreatedObjectTree]{}, "m")

		_, err := computeMacroExpansionContext(e, "m", "_n")
		require.Equal(t, evaluation.ErrMissingDependency, err)
	})

	t.Run("NoMacroInstanceName", func(t *testing.T) {
		e := NewMockMacroExpansionContextEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisReaders(), true).AnyTimes()
		_, err := computeMacroExpansionContext(e)
		require.EqualError(t, err, "no macro instance name provided")
	})
}
