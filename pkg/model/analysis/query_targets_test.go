package analysis_test

import (
	"encoding"
	"testing"

	model_core "bonanza.build/pkg/model/core"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

	"go.uber.org/mock/gomock"
)

func TestQueryTargets(t *testing.T) {
	ctrl, ctx := gomock.WithContext(t.Context(), t)
	bct := newBaseComputerTester(ctrl)

	newTargetLabelsObject := func(build func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) []*model_analysis_pb.TargetLabel) model_core.Decodable[model_core.CreatedObject[model_core.CreatedObjectTree]] {
		return newObject(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) encoding.BinaryMarshaler {
			return model_core.NewProtoListBinaryMarshaler(build(patcher))
		})
	}
	leafTargetLabel := func(l string) *model_analysis_pb.TargetLabel {
		return &model_analysis_pb.TargetLabel{
			Level: &model_analysis_pb.TargetLabel_Leaf{Leaf: l},
		}
	}
	leafQueryTarget := func(l string, definition *model_starlark_pb.Target_Definition) *model_analysis_pb.QueryTarget {
		return &model_analysis_pb.QueryTarget{
			Level: &model_analysis_pb.QueryTarget_Leaf_{
				Leaf: &model_analysis_pb.QueryTarget_Leaf{
					Label:      l,
					Definition: definition,
				},
			},
		}
	}
	ruleTargetDefinition := &model_starlark_pb.Target_Definition{
		Kind: &model_starlark_pb.Target_Definition_RuleTarget{
			RuleTarget: &model_starlark_pb.RuleTarget{
				RuleIdentifier: "@@rules_cc+//cc:defs.bzl%cc_library",
				Tags:           []string{"manual"},
			},
		},
	}
	ruleTargetKindDefinition := &model_starlark_pb.Target_Definition{
		Kind: &model_starlark_pb.Target_Definition_RuleTarget{
			RuleTarget: &model_starlark_pb.RuleTarget{
				RuleIdentifier: "@@rules_cc+//cc:defs.bzl%cc_library",
			},
		},
	}
	expectTarget := func(e *MockQueryTargetsEnvironmentForTesting, l string, definition *model_starlark_pb.Target_Definition) {
		e.EXPECT().GetTargetValue(
			testutil.EqProto(t, &model_analysis_pb.Target_Key{Label: l}),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](&model_analysis_pb.Target_Value{
			Definition: definition,
		}))
	}

	t.Run("NoDefinitions", func(t *testing.T) {
		// If no definitions are requested, there is no need to
		// load any targets.
		e := NewMockQueryTargetsEnvironmentForTesting(ctrl)

		value, err := bct.computer.ComputeQueryTargetsValue(
			ctx,
			newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.QueryTargets_Key {
				return &model_analysis_pb.QueryTargets_Key{
					TargetLabelsReference: attachObject(patcher, newTargetLabelsObject(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) []*model_analysis_pb.TargetLabel {
						return []*model_analysis_pb.TargetLabel{
							leafTargetLabel("@@main+//:a"),
							leafTargetLabel("@@main+//:b"),
						}
					})),
					DefinitionDetail: model_analysis_pb.QueryTarget_NONE,
				}
			}),
			e,
		)
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_analysis_pb.QueryTargets_Value{
			Targets: []*model_analysis_pb.QueryTarget{
				leafQueryTarget("@@main+//:a", nil),
				leafQueryTarget("@@main+//:b", nil),
			},
		}, value.Message)
	})

	t.Run("FullDefinitions", func(t *testing.T) {
		// Full definitions should be returned as is. For rule
		// targets, the names of the public attributes of the
		// rule should be provided, so that clients can display
		// their values.
		e := NewMockQueryTargetsEnvironmentForTesting(ctrl)
		expectTarget(e, "@@main+//:a", ruleTargetDefinition)
		expectTarget(e, "@@main+//:b", newSourceFileTargetValue().Message.Definition)
		e.EXPECT().GetCompiledBzlFileGlobalValue(
			testutil.EqProto(t, &model_analysis_pb.CompiledBzlFileGlobal_Key{
				Identifier: "@@rules_cc+//cc:defs.bzl%cc_library",
			}),
		).Return(newRuleDefinitionValue(
			false,
			&model_starlark_pb.NamedAttr{Name: "_compiler", Attr: &model_starlark_pb.Attr{}},
			&model_starlark_pb.NamedAttr{Name: "deps", Attr: &model_starlark_pb.Attr{}},
			&model_starlark_pb.NamedAttr{Name: "srcs", Attr: &model_starlark_pb.Attr{}},
		))

		value, err := bct.computer.ComputeQueryTargetsValue(
			ctx,
			newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.QueryTargets_Key {
				return &model_analysis_pb.QueryTargets_Key{
					TargetLabelsReference: attachObject(patcher, newTargetLabelsObject(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) []*model_analysis_pb.TargetLabel {
						return []*model_analysis_pb.TargetLabel{
							leafTargetLabel("@@main+//:a"),
							leafTargetLabel("@@main+//:b"),
						}
					})),
					DefinitionDetail: model_analysis_pb.QueryTarget_FULL,
				}
			}),
			e,
		)
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_analysis_pb.QueryTargets_Value{
			Targets: []*model_analysis_pb.QueryTarget{
				{
					Level: &model_analysis_pb.QueryTarget_Leaf_{
						Leaf: &model_analysis_pb.QueryTarget_Leaf{
							Label:           "@@main+//:a",
							Definition:      ruleTargetDefinition,
							PublicAttrNames: []string{"deps", "srcs"},
						},
					},
				},
				leafQueryTarget("@@main+//:b", newSourceFileTargetValue().Message.Definition),
			},
		}, value.Message)
	})

	t.Run("KindsWithChildObject", func(t *testing.T) {
		// If only kinds are requested, definitions should be
		// pruned. Labels stored in child objects should be
		// processed by requesting QueryTargets for them. The top-level nodes
		// of their results should be inserted in place, so that
		// the order of the labels is preserved.
		e := NewMockQueryTargetsEnvironmentForTesting(ctrl)
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
				return &model_analysis_pb.QueryTargets_Key{
					TargetLabelsReference: attachObject(patcher, childObject),
					DefinitionDetail:      model_analysis_pb.QueryTarget_KIND,
				}
			}),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](&model_analysis_pb.QueryTargets_Value{
			Targets: []*model_analysis_pb.QueryTarget{
				leafQueryTarget("@@main+//:b", ruleTargetKindDefinition),
				leafQueryTarget("@@main+//:c", ruleTargetKindDefinition),
			},
		}))
		expectTarget(e, "@@main+//:d", newSourceFileTargetValue().Message.Definition)

		value, err := bct.computer.ComputeQueryTargetsValue(
			ctx,
			newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.QueryTargets_Key {
				return &model_analysis_pb.QueryTargets_Key{
					TargetLabelsReference: attachObject(patcher, newTargetLabelsObject(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) []*model_analysis_pb.TargetLabel {
						return []*model_analysis_pb.TargetLabel{
							leafTargetLabel("@@main+//:a"),
							{
								Level: &model_analysis_pb.TargetLabel_Parent_{
									Parent: &model_analysis_pb.TargetLabel_Parent{
										Reference: attachObject(patcher, childObject),
									},
								},
							},
							leafTargetLabel("@@main+//:d"),
						}
					})),
					DefinitionDetail: model_analysis_pb.QueryTarget_KIND,
				}
			}),
			e,
		)
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_analysis_pb.QueryTargets_Value{
			Targets: []*model_analysis_pb.QueryTarget{
				leafQueryTarget("@@main+//:a", ruleTargetKindDefinition),
				leafQueryTarget("@@main+//:b", ruleTargetKindDefinition),
				leafQueryTarget("@@main+//:c", ruleTargetKindDefinition),
				leafQueryTarget("@@main+//:d", newSourceFileTargetValue().Message.Definition),
			},
		}, value.Message)
	})
}
