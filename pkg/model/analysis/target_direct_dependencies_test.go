package analysis_test

import (
	"testing"

	model_core "bonanza.build/pkg/model/core"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

	"go.uber.org/mock/gomock"
)

func labelValue(l string) *model_starlark_pb.Value {
	return &model_starlark_pb.Value{
		Kind: &model_starlark_pb.Value_Label{Label: l},
	}
}

func TestTargetDirectDependencies(t *testing.T) {
	ctrl, ctx := gomock.WithContext(t.Context(), t)
	bct := newBaseComputerTester(ctrl)

	t.Run("RuleTarget", func(t *testing.T) {
		// A rule with a public attribute using select(), an
		// output attribute, a public attribute that falls back
		// to its default value, and a private attribute.
		expectRule := func(e *MockTargetDirectDependenciesEnvironmentForTesting) {
			e.EXPECT().GetTargetValue(
				testutil.EqProto(t, &model_analysis_pb.Target_Key{Label: "@@main+//:t"}),
			).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](&model_analysis_pb.Target_Value{
				Definition: &model_starlark_pb.Target_Definition{
					Kind: &model_starlark_pb.Target_Definition_RuleTarget{
						RuleTarget: &model_starlark_pb.RuleTarget{
							RuleIdentifier: "@@rules+//:defs.bzl%my_rule",
							PublicAttrValues: []*model_starlark_pb.RuleTarget_PublicAttrValue{
								{
									// deps.
									ValueParts: []*model_starlark_pb.Select_Group{{
										Conditions: []*model_starlark_pb.Select_Condition{{
											ConditionIdentifier: "@@main+//:linux",
											Value:               labelListValue("@@main+//:a"),
										}},
										NoMatch: &model_starlark_pb.Select_Group_NoMatchValue{
											NoMatchValue: labelListValue("@@main+//:b"),
										},
									}},
								},
								{
									// out.
									ValueParts: []*model_starlark_pb.Select_Group{
										valueSelectGroup(labelValue("@@main+//:out.txt")),
									},
								},
								{
									// srcs.
									ValueParts: []*model_starlark_pb.Select_Group{
										valueSelectGroup(&model_starlark_pb.Value{
											Kind: &model_starlark_pb.Value_None{},
										}),
									},
								},
							},
							ExecCompatibleWith: []string{"@@platforms+//os:linux"},
							InheritableAttrs: &model_starlark_pb.InheritableAttrs{
								PackageMetadata: []string{"@@main+//:license"},
							},
						},
					},
				},
			}))
			e.EXPECT().GetCompiledBzlFileGlobalValue(
				testutil.EqProto(t, &model_analysis_pb.CompiledBzlFileGlobal_Key{Identifier: "@@rules+//:defs.bzl%my_rule"}),
			).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](&model_analysis_pb.CompiledBzlFileGlobal_Value{
				Global: &model_starlark_pb.Value{
					Kind: &model_starlark_pb.Value_Rule{
						Rule: &model_starlark_pb.Rule{
							Kind: &model_starlark_pb.Rule_Definition_{
								Definition: &model_starlark_pb.Rule_Definition{
									Attrs: []*model_starlark_pb.NamedAttr{
										{
											Name: "_tool",
											Attr: &model_starlark_pb.Attr{
												Default: labelValue("@@main+//:tool"),
												Type:    &model_starlark_pb.Attr_Label{Label: &model_starlark_pb.Attr_LabelType{}},
											},
										},
										{
											Name: "deps",
											Attr: &model_starlark_pb.Attr{
												Default: labelListValue(),
												Type:    &model_starlark_pb.Attr_LabelList{LabelList: &model_starlark_pb.Attr_LabelListType{}},
											},
										},
										{
											Name: "out",
											Attr: &model_starlark_pb.Attr{
												Type: &model_starlark_pb.Attr_Output{Output: &model_starlark_pb.Attr_OutputType{}},
											},
										},
										{
											Name: "srcs",
											Attr: &model_starlark_pb.Attr{
												Default: labelListValue("@@main+//:default_src"),
												Type:    &model_starlark_pb.Attr_LabelList{LabelList: &model_starlark_pb.Attr_LabelListType{}},
											},
										},
									},
								},
							},
						},
					},
				},
			}))
		}

		t.Run("ExplicitDepsOnly", func(t *testing.T) {
			// Both branches of select() and the condition
			// are dependencies. Outputs are not.
			e := NewMockTargetDirectDependenciesEnvironmentForTesting(ctrl)
			expectRule(e)
			value, err := bct.computer.ComputeTargetDirectDependenciesValue(ctx, &model_analysis_pb.TargetDirectDependencies_Key{
				Label: "@@main+//:t",
			}, e)
			require.NoError(t, err)
			require.Equal(t, []string{
				"@@main+//:a",
				"@@main+//:b",
				"@@main+//:linux",
				"@@platforms+//os:linux",
			}, getTargetLabels(t, value.Message.TargetLabels))
		})

		t.Run("ImplicitDeps", func(t *testing.T) {
			// Private attributes, default values and package
			// metadata are implicit dependencies.
			e := NewMockTargetDirectDependenciesEnvironmentForTesting(ctrl)
			expectRule(e)
			value, err := bct.computer.ComputeTargetDirectDependenciesValue(ctx, &model_analysis_pb.TargetDirectDependencies_Key{
				Label:               "@@main+//:t",
				IncludeImplicitDeps: true,
			}, e)
			require.NoError(t, err)
			require.Equal(t, []string{
				"@@main+//:a",
				"@@main+//:b",
				"@@main+//:default_src",
				"@@main+//:license",
				"@@main+//:linux",
				"@@main+//:tool",
				"@@platforms+//os:linux",
			}, getTargetLabels(t, value.Message.TargetLabels))
		})
	})

	t.Run("Test", func(t *testing.T) {
		// Conditions of select() expressions in attributes that
		// are specific to tests are dependencies as well.
		e := NewMockTargetDirectDependenciesEnvironmentForTesting(ctrl)
		e.EXPECT().GetTargetValue(
			testutil.EqProto(t, &model_analysis_pb.Target_Key{Label: "@@main+//:t"}),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](&model_analysis_pb.Target_Value{
			Definition: &model_starlark_pb.Target_Definition{
				Kind: &model_starlark_pb.Target_Definition_RuleTarget{
					RuleTarget: &model_starlark_pb.RuleTarget{
						RuleIdentifier: "@@rules+//:defs.bzl%my_test",
						Test: &model_starlark_pb.RuleTarget_Test{
							ShardCount: []*model_starlark_pb.Select_Group{{
								Conditions: []*model_starlark_pb.Select_Condition{{
									ConditionIdentifier: "@@main+//:many_shards",
									Value:               &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Int{Int: &model_starlark_pb.Int{AbsoluteValue: []byte{8}}}},
								}},
								NoMatch: &model_starlark_pb.Select_Group_NoMatchValue{
									NoMatchValue: &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Int{Int: &model_starlark_pb.Int{AbsoluteValue: []byte{1}}}},
								},
							}},
							Args: []*model_starlark_pb.Select_Group{{
								Conditions: []*model_starlark_pb.Select_Condition{{
									ConditionIdentifier: "@@main+//:verbose",
									Value:               &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_List{List: &model_starlark_pb.List{}}},
								}},
								NoMatch: &model_starlark_pb.Select_Group_NoMatchValue{
									NoMatchValue: &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_List{List: &model_starlark_pb.List{}}},
								},
							}},
						},
					},
				},
			},
		}))
		e.EXPECT().GetCompiledBzlFileGlobalValue(
			testutil.EqProto(t, &model_analysis_pb.CompiledBzlFileGlobal_Key{Identifier: "@@rules+//:defs.bzl%my_test"}),
		).Return(newRuleDefinitionValue(model_starlark_pb.Rule_Definition_TEST))

		value, err := bct.computer.ComputeTargetDirectDependenciesValue(ctx, &model_analysis_pb.TargetDirectDependencies_Key{
			Label: "@@main+//:t",
		}, e)
		require.NoError(t, err)
		require.Equal(t, []string{
			"@@main+//:many_shards",
			"@@main+//:verbose",
		}, getTargetLabels(t, value.Message.TargetLabels))
	})

	t.Run("PredeclaredOutputFileTarget", func(t *testing.T) {
		// Output files depend on the rule that generates them.
		e := NewMockTargetDirectDependenciesEnvironmentForTesting(ctrl)
		e.EXPECT().GetTargetValue(
			testutil.EqProto(t, &model_analysis_pb.Target_Key{Label: "@@main+//foo:out.txt"}),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](&model_analysis_pb.Target_Value{
			Definition: &model_starlark_pb.Target_Definition{
				Kind: &model_starlark_pb.Target_Definition_PredeclaredOutputFileTarget{
					PredeclaredOutputFileTarget: &model_starlark_pb.PredeclaredOutputFileTarget{
						OwnerTargetName: "gen",
					},
				},
			},
		}))
		value, err := bct.computer.ComputeTargetDirectDependenciesValue(ctx, &model_analysis_pb.TargetDirectDependencies_Key{
			Label: "@@main+//foo:out.txt",
		}, e)
		require.NoError(t, err)
		require.Equal(t, []string{"@@main+//foo:gen"}, getTargetLabels(t, value.Message.TargetLabels))
	})

	t.Run("Alias", func(t *testing.T) {
		e := NewMockTargetDirectDependenciesEnvironmentForTesting(ctrl)
		e.EXPECT().GetTargetValue(
			testutil.EqProto(t, &model_analysis_pb.Target_Key{Label: "@@main+//:alias"}),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](&model_analysis_pb.Target_Value{
			Definition: &model_starlark_pb.Target_Definition{
				Kind: &model_starlark_pb.Target_Definition_Alias{
					Alias: &model_starlark_pb.Alias{
						Actual: valueSelectGroup(labelValue("@@main+//:actual")),
					},
				},
			},
		}))
		value, err := bct.computer.ComputeTargetDirectDependenciesValue(ctx, &model_analysis_pb.TargetDirectDependencies_Key{
			Label: "@@main+//:alias",
		}, e)
		require.NoError(t, err)
		require.Equal(t, []string{"@@main+//:actual"}, getTargetLabels(t, value.Message.TargetLabels))
	})

	t.Run("TestSuite", func(t *testing.T) {
		// Tests listed by a test suite are explicit
		// dependencies, regardless of the test suite's filters.
		for _, includeImplicitDeps := range []bool{false, true} {
			e := NewMockTargetDirectDependenciesEnvironmentForTesting(ctrl)
			e.EXPECT().GetTargetValue(
				testutil.EqProto(t, &model_analysis_pb.Target_Key{Label: "@@main+//foo:suite"}),
			).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](&model_analysis_pb.Target_Value{
				Definition: &model_starlark_pb.Target_Definition{
					Kind: &model_starlark_pb.Target_Definition_TestSuite{
						TestSuite: &model_starlark_pb.TestSuite{
							Tests:        newTestSuiteTests("@@main+//bar:nested_suite", "@@main+//foo:test"),
							PositiveTags: []string{"integration"},
							Sizes:        []model_starlark_pb.TestSize{model_starlark_pb.TestSize_SMALL},
						},
					},
				},
			}))
			value, err := bct.computer.ComputeTargetDirectDependenciesValue(ctx, &model_analysis_pb.TargetDirectDependencies_Key{
				Label:               "@@main+//foo:suite",
				IncludeImplicitDeps: includeImplicitDeps,
			}, e)
			require.NoError(t, err)
			require.Equal(t, []string{"@@main+//bar:nested_suite", "@@main+//foo:test"}, getTargetLabels(t, value.Message.TargetLabels))
		}
	})
}
