package analysis_test

import (
	"encoding"
	"testing"

	model_core "bonanza.build/pkg/model/core"
	model_format "bonanza.build/pkg/model/format"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

	"google.golang.org/protobuf/proto"

	"go.uber.org/mock/gomock"
)

func TestQueryTargets(t *testing.T) {
	ctrl, ctx := gomock.WithContext(t.Context(), t)
	bct := newBaseComputerTester(ctrl)

	newTargetLabelsObject := func(build func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) []*model_analysis_pb.TargetLabel) model_core.Decodable[model_core.CreatedObject[model_core.CreatedObjectTree]] {
		return newObject(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) encoding.BinaryMarshaler {
			return model_format.NewProtoList[model_analysis_pb.TargetLabel]().NewMarshaler(build(patcher))
		})
	}
	leafTargetLabel := func(l string) *model_analysis_pb.TargetLabel {
		return model_analysis_pb.TargetLabel_builder{
			Leaf: proto.String(l),
		}.Build()
	}
	leafQueryTarget := func(l string, definition *model_starlark_pb.Target_Definition) *model_analysis_pb.QueryTarget {
		return model_analysis_pb.QueryTarget_builder{
			Leaf: model_analysis_pb.QueryTarget_Leaf_builder{
				Label:      l,
				Definition: definition,
			}.Build(),
		}.Build()
	}
	ruleTargetDefinition := model_starlark_pb.Target_Definition_builder{
		RuleTarget: model_starlark_pb.RuleTarget_builder{
			RuleIdentifier: "@@rules_cc+//cc:defs.bzl%cc_library",
			Tags:           []string{"manual"},
		}.Build(),
	}.Build()
	ruleTargetKindDefinition := model_starlark_pb.Target_Definition_builder{
		RuleTarget: model_starlark_pb.RuleTarget_builder{
			RuleIdentifier: "@@rules_cc+//cc:defs.bzl%cc_library",
		}.Build(),
	}.Build()
	expectTarget := func(e *MockQueryTargetsEnvironmentForTesting, l string, definition *model_starlark_pb.Target_Definition) {
		e.EXPECT().GetTargetValue(
			testutil.EqProto(t, model_analysis_pb.Target_Key_builder{Label: l}.Build()),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_analysis_pb.Target_Value_builder{
			Definition: definition,
		}.Build()))
	}

	t.Run("NoDefinitions", func(t *testing.T) {
		// If no definitions are requested, there is no need to
		// load any targets.
		e := NewMockQueryTargetsEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()

		value, err := bct.computer.ComputeQueryTargetsValue(
			ctx,
			newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.QueryTargets_Key {
				return model_analysis_pb.QueryTargets_Key_builder{
					TargetLabelsReference: attachObject(patcher, newTargetLabelsObject(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) []*model_analysis_pb.TargetLabel {
						return []*model_analysis_pb.TargetLabel{
							leafTargetLabel("@@main+//:a"),
							leafTargetLabel("@@main+//:b"),
						}
					})),
					DefinitionDetail: model_analysis_pb.QueryTarget_NONE,
				}.Build()
			}),
			e,
		)
		require.NoError(t, err)
		testutil.RequireEqualProto(t, model_analysis_pb.QueryTargets_Value_builder{
			Targets: []*model_analysis_pb.QueryTarget{
				leafQueryTarget("@@main+//:a", nil),
				leafQueryTarget("@@main+//:b", nil),
			},
		}.Build(), value.Message)
	})

	t.Run("FullDefinitions", func(t *testing.T) {
		// Full definitions should be returned as is. For rule
		// targets, the names of the public attributes of the
		// rule should be provided, so that clients can display
		// their values.
		e := NewMockQueryTargetsEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		expectTarget(e, "@@main+//:a", ruleTargetDefinition)
		expectTarget(e, "@@main+//:b", newSourceFileTargetValue().Message.GetDefinition())
		e.EXPECT().GetCompiledBzlFileGlobalValue(
			testutil.EqProto(t, model_analysis_pb.CompiledBzlFileGlobal_Key_builder{
				Identifier: "@@rules_cc+//cc:defs.bzl%cc_library",
			}.Build()),
		).Return(newRuleDefinitionValue(
			model_starlark_pb.Rule_Definition_NON_EXECUTABLE,
			model_starlark_pb.NamedAttr_builder{Name: "_compiler", Attr: &model_starlark_pb.Attr{}}.Build(),
			model_starlark_pb.NamedAttr_builder{Name: "deps", Attr: &model_starlark_pb.Attr{}}.Build(),
			model_starlark_pb.NamedAttr_builder{Name: "srcs", Attr: &model_starlark_pb.Attr{}}.Build(),
		))

		value, err := bct.computer.ComputeQueryTargetsValue(
			ctx,
			newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.QueryTargets_Key {
				return model_analysis_pb.QueryTargets_Key_builder{
					TargetLabelsReference: attachObject(patcher, newTargetLabelsObject(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) []*model_analysis_pb.TargetLabel {
						return []*model_analysis_pb.TargetLabel{
							leafTargetLabel("@@main+//:a"),
							leafTargetLabel("@@main+//:b"),
						}
					})),
					DefinitionDetail: model_analysis_pb.QueryTarget_FULL,
				}.Build()
			}),
			e,
		)
		require.NoError(t, err)
		testutil.RequireEqualProto(t, model_analysis_pb.QueryTargets_Value_builder{
			Targets: []*model_analysis_pb.QueryTarget{
				model_analysis_pb.QueryTarget_builder{
					Leaf: model_analysis_pb.QueryTarget_Leaf_builder{
						Label:           "@@main+//:a",
						Definition:      ruleTargetDefinition,
						PublicAttrNames: []string{"deps", "srcs"},
					}.Build(),
				}.Build(),
				leafQueryTarget("@@main+//:b", newSourceFileTargetValue().Message.GetDefinition()),
			},
		}.Build(), value.Message)
	})

	t.Run("DoesNotExist", func(t *testing.T) {
		e := NewMockQueryTargetsEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		expectTarget(e, "@@main+//:missing", nil)

		_, err := bct.computer.ComputeQueryTargetsValue(
			ctx,
			newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.QueryTargets_Key {
				return model_analysis_pb.QueryTargets_Key_builder{
					TargetLabelsReference: attachObject(patcher, newTargetLabelsObject(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) []*model_analysis_pb.TargetLabel {
						return []*model_analysis_pb.TargetLabel{
							leafTargetLabel("@@main+//:missing"),
						}
					})),
					DefinitionDetail: model_analysis_pb.QueryTarget_FULL,
				}.Build()
			}),
			e,
		)
		require.ErrorContains(t, err, "target \"@@main+//:missing\" does not exist")
	})

	t.Run("KindsWithChildObject", func(t *testing.T) {
		// If only kinds are requested, definitions should be
		// pruned. Labels stored in child objects should be
		// processed by requesting QueryTargets for them. The top-level nodes
		// of their results should be inserted in place, so that
		// the order of the labels is preserved.
		e := NewMockQueryTargetsEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		e.EXPECT().CaptureExistingObject(gomock.Any()).
			DoAndReturn(func(reference model_core.CreatedObjectTree) model_core.CreatedObjectTree {
				return reference
			}).
			AnyTimes()
		childObject := newTargetLabelsObject(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) []*model_analysis_pb.TargetLabel {
			return []*model_analysis_pb.TargetLabel{
				leafTargetLabel("@@main+//:b"),
				leafTargetLabel("@@main+//:c"),
			}
		})
		expectTarget(e, "@@main+//:a", ruleTargetDefinition)
		e.EXPECT().GetQueryTargetsValue(
			eqPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.QueryTargets_Key {
				return model_analysis_pb.QueryTargets_Key_builder{
					TargetLabelsReference: attachObject(patcher, childObject),
					DefinitionDetail:      model_analysis_pb.QueryTarget_KIND,
				}.Build()
			}),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_analysis_pb.QueryTargets_Value_builder{
			Targets: []*model_analysis_pb.QueryTarget{
				leafQueryTarget("@@main+//:b", ruleTargetKindDefinition),
				leafQueryTarget("@@main+//:c", ruleTargetKindDefinition),
			},
		}.Build()))
		expectTarget(e, "@@main+//:d", newSourceFileTargetValue().Message.GetDefinition())

		value, err := bct.computer.ComputeQueryTargetsValue(
			ctx,
			newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.QueryTargets_Key {
				return model_analysis_pb.QueryTargets_Key_builder{
					TargetLabelsReference: attachObject(patcher, newTargetLabelsObject(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) []*model_analysis_pb.TargetLabel {
						return []*model_analysis_pb.TargetLabel{
							leafTargetLabel("@@main+//:a"),
							model_analysis_pb.TargetLabel_builder{
								Parent: model_analysis_pb.TargetLabel_Parent_builder{
									Reference: attachObject(patcher, childObject),
								}.Build(),
							}.Build(),
							leafTargetLabel("@@main+//:d"),
						}
					})),
					DefinitionDetail: model_analysis_pb.QueryTarget_KIND,
				}.Build()
			}),
			e,
		)
		require.NoError(t, err)
		testutil.RequireEqualProto(t, model_analysis_pb.QueryTargets_Value_builder{
			Targets: []*model_analysis_pb.QueryTarget{
				leafQueryTarget("@@main+//:a", ruleTargetKindDefinition),
				leafQueryTarget("@@main+//:b", ruleTargetKindDefinition),
				leafQueryTarget("@@main+//:c", ruleTargetKindDefinition),
				leafQueryTarget("@@main+//:d", newSourceFileTargetValue().Message.GetDefinition()),
			},
		}.Build(), value.Message)
	})
}
