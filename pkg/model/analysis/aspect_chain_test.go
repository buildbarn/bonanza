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

func TestAspectChain(t *testing.T) {
	ctrl, ctx := gomock.WithContext(t.Context(), t)
	bct := newBaseComputerTester(ctrl)

	const (
		aspect1 = "@@rules+//:defs.bzl%aspect1"
		aspect2 = "@@rules+//:defs.bzl%aspect2"
		aspect3 = "@@rules+//:defs.bzl%aspect3"
		aspect4 = "@@rules+//:defs.bzl%aspect4"
		aInfo   = "@@rules+//:defs.bzl%AInfo"
		bInfo   = "@@rules+//:defs.bzl%BInfo"
	)
	expectAspect := func(e *MockAspectChainEnvironmentForTesting, identifier string, definition *model_starlark_pb.Aspect_Definition) {
		e.EXPECT().GetCompiledBzlFileGlobalValue(
			testutil.EqProto(t, &model_analysis_pb.CompiledBzlFileGlobal_Key{Identifier: identifier}),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](&model_analysis_pb.CompiledBzlFileGlobal_Value{
			Global: &model_starlark_pb.Value{
				Kind: &model_starlark_pb.Value_Aspect{
					Aspect: &model_starlark_pb.Aspect{
						Kind: &model_starlark_pb.Aspect_Definition_{
							Definition: definition,
						},
					},
				},
			},
		}))
	}
	compute := func(e *MockAspectChainEnvironmentForTesting, aspectIdentifiers ...string) (model_core.PatchedMessage[*model_analysis_pb.AspectChain_Value, model_core.CreatedObjectTree], error) {
		return bct.computer.ComputeAspectChainValue(ctx, &model_analysis_pb.AspectChain_Key{
			AspectIdentifiers: aspectIdentifiers,
		}, e)
	}

	t.Run("Success", func(t *testing.T) {
		e := NewMockAspectChainEnvironmentForTesting(ctrl)
		// Propagates along "deps" and provides AInfo.
		expectAspect(e, aspect1, &model_starlark_pb.Aspect_Definition{
			AttrAspects: []string{"deps"},
			Provides:    []string{aInfo},
		})
		// Doesn't propagate itself, but is required by aspect3,
		// meaning it propagates along the same attrs. Provides
		// AInfo and BInfo.
		expectAspect(e, aspect2, &model_starlark_pb.Aspect_Definition{
			Provides: []string{aInfo, bInfo},
		})
		// Propagates along all attrs.
		expectAspect(e, aspect3, &model_starlark_pb.Aspect_Definition{
			AttrAspects: []string{"*"},
			Requires:    []string{aspect2},
		})
		// The last aspect only sees aspects providing BInfo.
		expectAspect(e, aspect4, &model_starlark_pb.Aspect_Definition{
			AttrAspects:             []string{"deps", "srcs"},
			RequiredAspectProviders: []*model_starlark_pb.ProviderSet{{ProviderIdentifiers: []string{bInfo}}},
		})

		value, err := compute(e, aspect1, aspect2, aspect3, aspect4)
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_analysis_pb.AspectChain_Value{
			VisibleAspectIndices:           []uint32{1},
			AlwaysPropagatingAspectIndices: []uint32{1, 2},
			AttrPropagatingAspects: []*model_analysis_pb.AspectChain_Value_AttrPropagatingAspects{
				{AttrName: "deps", AspectIndices: []uint32{0, 3}},
				{AttrName: "srcs", AspectIndices: []uint32{3}},
			},
		}, value.Message)
	})

	t.Run("MissingDefinition", func(t *testing.T) {
		// All definitions should be requested, even if some of
		// them are missing.
		e := NewMockAspectChainEnvironmentForTesting(ctrl)
		e.EXPECT().GetCompiledBzlFileGlobalValue(
			testutil.EqProto(t, &model_analysis_pb.CompiledBzlFileGlobal_Key{Identifier: aspect1}),
		).Return(model_core.Message[*model_analysis_pb.CompiledBzlFileGlobal_Value, model_core.CreatedObjectTree]{})
		expectAspect(e, aspect2, &model_starlark_pb.Aspect_Definition{})

		_, err := compute(e, aspect1, aspect2)
		require.Equal(t, evaluation.ErrMissingDependency, err)
	})

	t.Run("Duplicate", func(t *testing.T) {
		e := NewMockAspectChainEnvironmentForTesting(ctrl)

		_, err := compute(e, aspect1, aspect2, aspect1)
		require.EqualError(t, err, "aspect \"@@rules+//:defs.bzl%aspect1\" is applied multiple times")
	})

	t.Run("RequiredAspectNotPreceding", func(t *testing.T) {
		e := NewMockAspectChainEnvironmentForTesting(ctrl)
		expectAspect(e, aspect1, &model_starlark_pb.Aspect_Definition{
			Requires: []string{aspect2},
		})
		expectAspect(e, aspect2, &model_starlark_pb.Aspect_Definition{})

		_, err := compute(e, aspect1, aspect2)
		require.EqualError(t, err, "aspect \"@@rules+//:defs.bzl%aspect1\" requires aspect \"@@rules+//:defs.bzl%aspect2\", which is not applied before it")
	})
}
