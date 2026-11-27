package analysis_test

import (
	"testing"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"

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
			testutil.EqProto(t, model_analysis_pb.CompiledBzlFileGlobal_Key_builder{Identifier: identifier}.Build()),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_analysis_pb.CompiledBzlFileGlobal_Value_builder{
			Global: model_starlark_pb.Value_builder{
				Aspect: model_starlark_pb.Aspect_builder{
					Definition: proto.ValueOrDefault(definition),
				}.Build(),
			}.Build(),
		}.Build()))
	}
	compute := func(e *MockAspectChainEnvironmentForTesting, aspectIdentifiers ...string) (model_core.PatchedMessage[*model_analysis_pb.AspectChain_Value, model_core.CreatedObjectTree], error) {
		return bct.computer.ComputeAspectChainValue(ctx, model_analysis_pb.AspectChain_Key_builder{
			AspectIdentifiers: aspectIdentifiers,
		}.Build(), e)
	}

	t.Run("Success", func(t *testing.T) {
		e := NewMockAspectChainEnvironmentForTesting(ctrl)
		// Propagates along "deps" and provides AInfo.
		expectAspect(e, aspect1, model_starlark_pb.Aspect_Definition_builder{
			AttrAspects: model_starlark_pb.Aspect_PropagationEdges_builder{Fixed: model_starlark_pb.Aspect_PropagationEdges_Fixed_builder{Names: []string{"deps"}}.Build()}.Build(),
			Provides:    []string{aInfo},
		}.Build())
		// Doesn't propagate itself, but is required by aspect3,
		// meaning it propagates along the same attrs. Provides
		// AInfo and BInfo.
		expectAspect(e, aspect2, model_starlark_pb.Aspect_Definition_builder{
			Provides: []string{aInfo, bInfo},
		}.Build())
		// Propagates along all attrs and to toolchains of a
		// single type.
		expectAspect(e, aspect3, model_starlark_pb.Aspect_Definition_builder{
			AttrAspects:       model_starlark_pb.Aspect_PropagationEdges_builder{All: &emptypb.Empty{}}.Build(),
			Requires:          []string{aspect2},
			ToolchainsAspects: model_starlark_pb.Aspect_PropagationEdges_builder{Fixed: model_starlark_pb.Aspect_PropagationEdges_Fixed_builder{Names: []string{"@@rules+//:toolchain_type"}}.Build()}.Build(),
		}.Build())
		// The last aspect only sees aspects providing BInfo.
		// It propagates to all toolchains.
		expectAspect(e, aspect4, model_starlark_pb.Aspect_Definition_builder{
			AttrAspects:             model_starlark_pb.Aspect_PropagationEdges_builder{Fixed: model_starlark_pb.Aspect_PropagationEdges_Fixed_builder{Names: []string{"deps", "srcs"}}.Build()}.Build(),
			RequiredAspectProviders: []*model_starlark_pb.ProviderSet{model_starlark_pb.ProviderSet_builder{ProviderIdentifiers: []string{bInfo}}.Build()},
			ToolchainsAspects:       model_starlark_pb.Aspect_PropagationEdges_builder{All: &emptypb.Empty{}}.Build(),
		}.Build())

		value, err := compute(e, aspect1, aspect2, aspect3, aspect4)
		require.NoError(t, err)
		testutil.RequireEqualProto(t, model_analysis_pb.AspectChain_Value_builder{
			VisibleAspectIndices: []uint32{1},
			AttrPropagatingAspects: model_analysis_pb.AspectChain_Value_PropagatingAspects_builder{
				AllAspectIndices: []uint32{1, 2},
				Edges: []*model_analysis_pb.AspectChain_Value_PropagatingAspects_Edge{
					model_analysis_pb.AspectChain_Value_PropagatingAspects_Edge_builder{Name: "deps", AspectIndices: []uint32{0, 3}}.Build(),
					model_analysis_pb.AspectChain_Value_PropagatingAspects_Edge_builder{Name: "srcs", AspectIndices: []uint32{3}}.Build(),
				},
			}.Build(),
			ToolchainPropagatingAspects: model_analysis_pb.AspectChain_Value_PropagatingAspects_builder{
				AllAspectIndices: []uint32{3},
				Edges: []*model_analysis_pb.AspectChain_Value_PropagatingAspects_Edge{
					model_analysis_pb.AspectChain_Value_PropagatingAspects_Edge_builder{Name: "@@rules+//:toolchain_type", AspectIndices: []uint32{1, 2}}.Build(),
				},
			}.Build(),
		}.Build(), value.Message)
	})

	t.Run("PropagationFunction", func(t *testing.T) {
		e := NewMockAspectChainEnvironmentForTesting(ctrl)
		expectAspect(e, aspect1, model_starlark_pb.Aspect_Definition_builder{
			ToolchainsAspects: model_starlark_pb.Aspect_PropagationEdges_builder{Function: &model_starlark_pb.Function{}}.Build(),
		}.Build())

		_, err := compute(e, aspect1)
		require.EqualError(t, err, "aspect \"@@rules+//:defs.bzl%aspect1\": computing toolchains_aspects using a function is not supported yet")
	})

	t.Run("MissingDefinition", func(t *testing.T) {
		// All definitions should be requested, even if some of
		// them are missing.
		e := NewMockAspectChainEnvironmentForTesting(ctrl)
		e.EXPECT().GetCompiledBzlFileGlobalValue(
			testutil.EqProto(t, model_analysis_pb.CompiledBzlFileGlobal_Key_builder{Identifier: aspect1}.Build()),
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
		expectAspect(e, aspect1, model_starlark_pb.Aspect_Definition_builder{
			Requires: []string{aspect2},
		}.Build())
		expectAspect(e, aspect2, &model_starlark_pb.Aspect_Definition{})

		_, err := compute(e, aspect1, aspect2)
		require.EqualError(t, err, "aspect \"@@rules+//:defs.bzl%aspect1\" requires aspect \"@@rules+//:defs.bzl%aspect2\", which is not applied before it")
	})
}
