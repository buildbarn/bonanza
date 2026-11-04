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

func TestRuleImplementationWrappers(t *testing.T) {
	ctrl, ctx := gomock.WithContext(t.Context(), t)
	bct := newBaseComputerTester(ctrl)

	expectBuildSpecification := func(e *MockRuleImplementationWrappersEnvironmentForTesting) {
		e.EXPECT().GetBuildSpecificationValue(gomock.Any()).
			Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](&model_analysis_pb.BuildSpecification_Value{
				RuleImplementationWrapperIdentifier:    "@@rules+//:wrappers.bzl%rule_wrapper",
				SubruleImplementationWrapperIdentifier: "@@rules+//:wrappers.bzl%subrule_wrapper",
			}))
	}

	newFunction := func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree], name string) *model_starlark_pb.Function {
		return &model_starlark_pb.Function{
			Filename: "@@rules+//:wrappers.bzl",
			NamePath: []*model_starlark_pb.Function_Name{{Name: name}},
		}
	}
	expectGlobal := func(e *MockRuleImplementationWrappersEnvironmentForTesting, identifier, name string) {
		message := model_core.Message[*model_analysis_pb.CompiledBzlFileGlobal_Value, model_core.CreatedObjectTree]{}
		if name != "" {
			message = newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.CompiledBzlFileGlobal_Value {
				return &model_analysis_pb.CompiledBzlFileGlobal_Value{
					Global: &model_starlark_pb.Value{
						Kind: &model_starlark_pb.Value_Function{
							Function: newFunction(patcher, name),
						},
					},
				}
			})
		}
		e.EXPECT().GetCompiledBzlFileGlobalValue(
			testutil.EqProto(t, &model_analysis_pb.CompiledBzlFileGlobal_Key{Identifier: identifier}),
		).Return(message)
	}

	t.Run("MissingDependency", func(t *testing.T) {
		// Both wrappers should be requested, even if one of
		// them is unavailable. As no value is returned, no
		// objects may be captured.
		e := NewMockRuleImplementationWrappersEnvironmentForTesting(ctrl)
		expectBuildSpecification(e)
		expectGlobal(e, "@@rules+//:wrappers.bzl%rule_wrapper", "rule_wrapper")
		expectGlobal(e, "@@rules+//:wrappers.bzl%subrule_wrapper", "")

		_, err := bct.computer.ComputeRuleImplementationWrappersValue(ctx, &model_analysis_pb.RuleImplementationWrappers_Key{}, e)
		require.ErrorIs(t, err, evaluation.ErrMissingDependency)
	})

	t.Run("Success", func(t *testing.T) {
		e := NewMockRuleImplementationWrappersEnvironmentForTesting(ctrl)
		expectBuildSpecification(e)
		expectGlobal(e, "@@rules+//:wrappers.bzl%rule_wrapper", "rule_wrapper")
		expectGlobal(e, "@@rules+//:wrappers.bzl%subrule_wrapper", "subrule_wrapper")

		value, err := bct.computer.ComputeRuleImplementationWrappersValue(ctx, &model_analysis_pb.RuleImplementationWrappers_Key{}, e)
		require.NoError(t, err)
		requireEqualPatchedMessage(t, func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.RuleImplementationWrappers_Value {
			return &model_analysis_pb.RuleImplementationWrappers_Value{
				Rule:    newFunction(patcher, "rule_wrapper"),
				Subrule: newFunction(patcher, "subrule_wrapper"),
			}
		}, value)
	})
}
