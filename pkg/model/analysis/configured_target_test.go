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

func TestConfiguredTargetWithAspects(t *testing.T) {
	ctrl, ctx := gomock.WithContext(t.Context(), t)
	bct := newBaseComputerTester(ctrl)

	newEnvironment := func() *MockConfiguredTargetEnvironmentForTesting {
		e := NewMockConfiguredTargetEnvironmentForTesting(ctrl)
		e.EXPECT().CaptureExistingObject(gomock.Any()).
			DoAndReturn(func(reference model_core.CreatedObjectTree) model_core.CreatedObjectTree {
				return reference
			}).
			AnyTimes()
		return e
	}
	expectTarget := func(e *MockConfiguredTargetEnvironmentForTesting, label string, definition *model_starlark_pb.Target_Definition) {
		e.EXPECT().GetTargetValue(
			testutil.EqProto(t, &model_analysis_pb.Target_Key{Label: label}),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](&model_analysis_pb.Target_Value{
			Definition: definition,
		}))
	}
	expectGeneratingRuleAspects := func(e *MockConfiguredTargetEnvironmentForTesting, aspectIdentifiers []string, aspectIndices []uint32) {
		e.EXPECT().GetGeneratingRuleAspectsValue(
			testutil.EqProto(t, &model_analysis_pb.GeneratingRuleAspects_Key{AspectIdentifiers: aspectIdentifiers}),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](&model_analysis_pb.GeneratingRuleAspects_Value{
			AspectIndices: aspectIndices,
		}))
	}
	compute := func(e *MockConfiguredTargetEnvironmentForTesting, label string, aspects ...*model_starlark_pb.AppliedAspect) (model_core.PatchedMessage[*model_analysis_pb.ConfiguredTarget_Value, model_core.CreatedObjectTree], error) {
		return bct.computer.ComputeConfiguredTargetValue(
			ctx,
			model_core.NewSimpleMessage[model_core.CreatedObjectTree](&model_analysis_pb.ConfiguredTarget_Key{
				Label:   label,
				Variant: newTestTargetVariant(aspects),
			}),
			e,
		)
	}
	aspect1 := model_starlark_pb.AppliedAspect_builder{Identifier: "@@rules+//:defs.bzl%aspect1"}.Build()
	aspect2 := model_starlark_pb.AppliedAspect_builder{Identifier: "@@rules+//:defs.bzl%aspect2"}.Build()
	aspect3 := model_starlark_pb.AppliedAspect_builder{Identifier: "@@rules+//:defs.bzl%aspect3"}.Build()
	predeclaredOutputFileTarget := model_starlark_pb.Target_Definition_builder{
		PredeclaredOutputFileTarget: model_starlark_pb.PredeclaredOutputFileTarget_builder{
			OwnerTargetName: "foo",
		}.Build(),
	}.Build()

	t.Run("MissingTarget", func(t *testing.T) {
		e := newEnvironment()
		e.EXPECT().GetTargetValue(gomock.Any()).
			Return(model_core.Message[*model_analysis_pb.Target_Value, model_core.CreatedObjectTree]{})

		_, err := compute(e, "@@main+//:foo", aspect1)
		require.Equal(t, evaluation.ErrMissingDependency, err)
	})

	t.Run("SourceFile", func(t *testing.T) {
		// Aspects never apply to source files.
		e := newEnvironment()
		expectTarget(e, "@@main+//:foo.txt", model_starlark_pb.Target_Definition_builder{
			SourceFileTarget: &model_starlark_pb.SourceFileTarget{},
		}.Build())

		value, err := compute(e, "@@main+//:foo.txt", aspect1)
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_analysis_pb.ConfiguredTarget_Value{}, value.Message)
	})

	t.Run("PackageGroup", func(t *testing.T) {
		e := newEnvironment()
		expectTarget(e, "@@main+//:group", model_starlark_pb.Target_Definition_builder{
			PackageGroup: &model_starlark_pb.PackageGroup{},
		}.Build())

		value, err := compute(e, "@@main+//:group", aspect1)
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_analysis_pb.ConfiguredTarget_Value{}, value.Message)
	})

	t.Run("PredeclaredOutputFileNotApplyingToGeneratingRules", func(t *testing.T) {
		// If the last aspect does not set
		// apply_to_generating_rules, it does not apply.
		e := newEnvironment()
		expectTarget(e, "@@main+//:foo.out", predeclaredOutputFileTarget)
		expectGeneratingRuleAspects(e, []string{aspect1.GetIdentifier(), aspect2.GetIdentifier()}, []uint32{0})

		value, err := compute(e, "@@main+//:foo.out", aspect1, aspect2)
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_analysis_pb.ConfiguredTarget_Value{}, value.Message)
	})

	t.Run("PredeclaredOutputFileApplyingToGeneratingRules", func(t *testing.T) {
		// If the last aspect sets apply_to_generating_rules, it
		// is applied to the rule target that generates the
		// output file. Preceding aspects that don't set
		// apply_to_generating_rules are omitted.
		e := newEnvironment()
		expectTarget(e, "@@main+//:foo.out", predeclaredOutputFileTarget)
		expectGeneratingRuleAspects(e, []string{aspect1.GetIdentifier(), aspect2.GetIdentifier(), aspect3.GetIdentifier()}, []uint32{1, 2})
		ownerProviderInstances := []*model_starlark_pb.Struct{model_starlark_pb.Struct_builder{
			Fields: &model_starlark_pb.Struct_Fields{},
			ProviderInstanceProperties: model_starlark_pb.Provider_InstanceProperties_builder{
				ProviderIdentifier: "@@rules+//:defs.bzl%AInfo",
			}.Build(),
		}.Build()}
		e.EXPECT().GetConfiguredTargetValue(
			eqPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.ConfiguredTarget_Key {
				return &model_analysis_pb.ConfiguredTarget_Key{
					Label:   "@@main+//:foo",
					Variant: newTestTargetVariant([]*model_starlark_pb.AppliedAspect{aspect2, aspect3}),
				}
			}),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](&model_analysis_pb.ConfiguredTarget_Value{
			ProviderInstances: ownerProviderInstances,
		}))

		value, err := compute(e, "@@main+//:foo.out", aspect1, aspect2, aspect3)
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_analysis_pb.ConfiguredTarget_Value{
			ProviderInstances: ownerProviderInstances,
		}, value.Message)
	})
}
