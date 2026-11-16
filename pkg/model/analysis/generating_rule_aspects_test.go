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

func TestGeneratingRuleAspects(t *testing.T) {
	ctrl, ctx := gomock.WithContext(t.Context(), t)
	bct := newBaseComputerTester(ctrl)

	expectAspect := func(e *MockGeneratingRuleAspectsEnvironmentForTesting, identifier string, applyToGeneratingRules bool) {
		e.EXPECT().GetCompiledBzlFileGlobalValue(
			testutil.EqProto(t, &model_analysis_pb.CompiledBzlFileGlobal_Key{Identifier: identifier}),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](&model_analysis_pb.CompiledBzlFileGlobal_Value{
			Global: &model_starlark_pb.Value{
				Kind: &model_starlark_pb.Value_Aspect{
					Aspect: &model_starlark_pb.Aspect{
						Kind: &model_starlark_pb.Aspect_Definition_{
							Definition: &model_starlark_pb.Aspect_Definition{
								ApplyToGeneratingRules: applyToGeneratingRules,
							},
						},
					},
				},
			},
		}))
	}
	const (
		aspect1 = "@@rules+//:defs.bzl%aspect1"
		aspect2 = "@@rules+//:defs.bzl%aspect2"
		aspect3 = "@@rules+//:defs.bzl%aspect3"
	)

	t.Run("Success", func(t *testing.T) {
		e := NewMockGeneratingRuleAspectsEnvironmentForTesting(ctrl)
		expectAspect(e, aspect1, true)
		expectAspect(e, aspect2, false)
		expectAspect(e, aspect3, true)

		value, err := bct.computer.ComputeGeneratingRuleAspectsValue(ctx, &model_analysis_pb.GeneratingRuleAspects_Key{
			AspectIdentifiers: []string{aspect1, aspect2, aspect3},
		}, e)
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_analysis_pb.GeneratingRuleAspects_Value{
			AspectIndices: []uint32{0, 2},
		}, value.Message)
	})

	t.Run("MissingDefinition", func(t *testing.T) {
		// All definitions should be requested, even if some of
		// them are missing.
		e := NewMockGeneratingRuleAspectsEnvironmentForTesting(ctrl)
		e.EXPECT().GetCompiledBzlFileGlobalValue(
			testutil.EqProto(t, &model_analysis_pb.CompiledBzlFileGlobal_Key{Identifier: aspect1}),
		).Return(model_core.Message[*model_analysis_pb.CompiledBzlFileGlobal_Value, model_core.CreatedObjectTree]{})
		expectAspect(e, aspect2, false)

		_, err := bct.computer.ComputeGeneratingRuleAspectsValue(ctx, &model_analysis_pb.GeneratingRuleAspects_Key{
			AspectIdentifiers: []string{aspect1, aspect2},
		}, e)
		require.Equal(t, evaluation.ErrMissingDependency, err)
	})

	t.Run("NotAnAspect", func(t *testing.T) {
		e := NewMockGeneratingRuleAspectsEnvironmentForTesting(ctrl)
		e.EXPECT().GetCompiledBzlFileGlobalValue(
			testutil.EqProto(t, &model_analysis_pb.CompiledBzlFileGlobal_Key{Identifier: aspect1}),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](&model_analysis_pb.CompiledBzlFileGlobal_Value{
			Global: &model_starlark_pb.Value{
				Kind: &model_starlark_pb.Value_Bool{Bool: true},
			},
		}))

		_, err := bct.computer.ComputeGeneratingRuleAspectsValue(ctx, &model_analysis_pb.GeneratingRuleAspects_Key{
			AspectIdentifiers: []string{aspect1},
		}, e)
		require.EqualError(t, err, "\"@@rules+//:defs.bzl%aspect1\" is not an aspect")
	})
}
