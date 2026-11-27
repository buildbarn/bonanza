package analysis_test

import (
	"testing"

	model_core "bonanza.build/pkg/model/core"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

	"google.golang.org/protobuf/proto"
	xpb "google.golang.org/protobuf/types/known/emptypb"

	"go.uber.org/mock/gomock"
)

func labelValue(l string) *model_starlark_pb.Value {
	return model_starlark_pb.Value_builder{
		Label: proto.String(l),
	}.Build()
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
				testutil.EqProto(t, model_analysis_pb.Target_Key_builder{Label: "@@main+//:t"}.Build()),
			).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_analysis_pb.Target_Value_builder{
				Definition: model_starlark_pb.Target_Definition_builder{
					RuleTarget: model_starlark_pb.RuleTarget_builder{
						RuleIdentifier: "@@rules+//:defs.bzl%my_rule",
						PublicAttrValues: []*model_starlark_pb.RuleTarget_PublicAttrValue{
							model_starlark_pb.RuleTarget_PublicAttrValue_builder{
								// deps.
								ValueParts: []*model_starlark_pb.Select_Group{model_starlark_pb.Select_Group_builder{
									Conditions: []*model_starlark_pb.Select_Condition{model_starlark_pb.Select_Condition_builder{
										ConditionIdentifier: "@@main+//:linux",
										Value:               labelListValue("@@main+//:a"),
									}.Build()},
									NoMatchValue: proto.ValueOrDefault(labelListValue("@@main+//:b")),
								}.Build()},
							}.Build(),
							model_starlark_pb.RuleTarget_PublicAttrValue_builder{
								// out.
								ValueParts: []*model_starlark_pb.Select_Group{
									valueSelectGroup(labelValue("@@main+//:out.txt")),
								},
							}.Build(),
							model_starlark_pb.RuleTarget_PublicAttrValue_builder{
								// srcs.
								ValueParts: []*model_starlark_pb.Select_Group{
									valueSelectGroup(model_starlark_pb.Value_builder{
										None: &xpb.Empty{},
									}.Build()),
								},
							}.Build(),
						},
						ExecCompatibleWith: []string{"@@platforms+//os:linux"},
						InheritableAttrs: model_starlark_pb.InheritableAttrs_builder{
							PackageMetadata: []string{"@@main+//:license"},
						}.Build(),
					}.Build(),
				}.Build(),
			}.Build()))
			e.EXPECT().GetCompiledBzlFileGlobalValue(
				testutil.EqProto(t, model_analysis_pb.CompiledBzlFileGlobal_Key_builder{Identifier: "@@rules+//:defs.bzl%my_rule"}.Build()),
			).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_analysis_pb.CompiledBzlFileGlobal_Value_builder{
				Global: model_starlark_pb.Value_builder{
					Rule: model_starlark_pb.Rule_builder{
						Definition: model_starlark_pb.Rule_Definition_builder{
							Attrs: []*model_starlark_pb.NamedAttr{
								model_starlark_pb.NamedAttr_builder{
									Name: "_tool",
									Attr: model_starlark_pb.Attr_builder{
										Default: labelValue("@@main+//:tool"),
										Label:   &model_starlark_pb.Attr_LabelType{},
									}.Build(),
								}.Build(),
								model_starlark_pb.NamedAttr_builder{
									Name: "deps",
									Attr: model_starlark_pb.Attr_builder{
										Default:   labelListValue(),
										LabelList: &model_starlark_pb.Attr_LabelListType{},
									}.Build(),
								}.Build(),
								model_starlark_pb.NamedAttr_builder{
									Name: "out",
									Attr: model_starlark_pb.Attr_builder{
										Output: &model_starlark_pb.Attr_OutputType{},
									}.Build(),
								}.Build(),
								model_starlark_pb.NamedAttr_builder{
									Name: "srcs",
									Attr: model_starlark_pb.Attr_builder{
										Default:   labelListValue("@@main+//:default_src"),
										LabelList: &model_starlark_pb.Attr_LabelListType{},
									}.Build(),
								}.Build(),
							},
						}.Build(),
					}.Build(),
				}.Build(),
			}.Build()))
		}

		t.Run("ExplicitDepsOnly", func(t *testing.T) {
			// Both branches of select() and the condition
			// are dependencies. Outputs are not.
			e := NewMockTargetDirectDependenciesEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			expectRule(e)
			value, err := bct.computer.ComputeTargetDirectDependenciesValue(ctx, model_analysis_pb.TargetDirectDependencies_Key_builder{
				Label: "@@main+//:t",
			}.Build(), e)
			require.NoError(t, err)
			require.Equal(t, []string{
				"@@main+//:a",
				"@@main+//:b",
				"@@main+//:linux",
				"@@platforms+//os:linux",
			}, getTargetLabels(t, value.Message.GetTargetLabels()))
		})

		t.Run("ImplicitDeps", func(t *testing.T) {
			// Private attributes, default values and package
			// metadata are implicit dependencies.
			e := NewMockTargetDirectDependenciesEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			expectRule(e)
			value, err := bct.computer.ComputeTargetDirectDependenciesValue(ctx, model_analysis_pb.TargetDirectDependencies_Key_builder{
				Label:               "@@main+//:t",
				IncludeImplicitDeps: true,
			}.Build(), e)
			require.NoError(t, err)
			require.Equal(t, []string{
				"@@main+//:a",
				"@@main+//:b",
				"@@main+//:default_src",
				"@@main+//:license",
				"@@main+//:linux",
				"@@main+//:tool",
				"@@platforms+//os:linux",
			}, getTargetLabels(t, value.Message.GetTargetLabels()))
		})
	})

	t.Run("Test", func(t *testing.T) {
		// Conditions of select() expressions in attributes that
		// are specific to tests are dependencies as well.
		e := NewMockTargetDirectDependenciesEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
		e.EXPECT().GetTargetValue(
			testutil.EqProto(t, model_analysis_pb.Target_Key_builder{Label: "@@main+//:t"}.Build()),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_analysis_pb.Target_Value_builder{
			Definition: model_starlark_pb.Target_Definition_builder{
				RuleTarget: model_starlark_pb.RuleTarget_builder{
					RuleIdentifier: "@@rules+//:defs.bzl%my_test",
					Test: model_starlark_pb.RuleTarget_Test_builder{
						ShardCount: []*model_starlark_pb.Select_Group{model_starlark_pb.Select_Group_builder{
							Conditions: []*model_starlark_pb.Select_Condition{model_starlark_pb.Select_Condition_builder{
								ConditionIdentifier: "@@main+//:many_shards",
								Value:               model_starlark_pb.Value_builder{Int: []byte{8}}.Build(),
							}.Build()},
							NoMatchValue: model_starlark_pb.Value_builder{Int: []byte{1}}.Build(),
						}.Build()},
						Args: []*model_starlark_pb.Select_Group{model_starlark_pb.Select_Group_builder{
							Conditions: []*model_starlark_pb.Select_Condition{model_starlark_pb.Select_Condition_builder{
								ConditionIdentifier: "@@main+//:verbose",
								Value:               model_starlark_pb.Value_builder{List: &model_starlark_pb.List{}}.Build(),
							}.Build()},
							NoMatchValue: model_starlark_pb.Value_builder{List: &model_starlark_pb.List{}}.Build(),
						}.Build()},
					}.Build(),
				}.Build(),
			}.Build(),
		}.Build()))
		e.EXPECT().GetCompiledBzlFileGlobalValue(
			testutil.EqProto(t, model_analysis_pb.CompiledBzlFileGlobal_Key_builder{Identifier: "@@rules+//:defs.bzl%my_test"}.Build()),
		).Return(newRuleDefinitionValue(model_starlark_pb.Rule_Definition_TEST))

		value, err := bct.computer.ComputeTargetDirectDependenciesValue(ctx, model_analysis_pb.TargetDirectDependencies_Key_builder{
			Label: "@@main+//:t",
		}.Build(), e)
		require.NoError(t, err)
		require.Equal(t, []string{
			"@@main+//:many_shards",
			"@@main+//:verbose",
		}, getTargetLabels(t, value.Message.GetTargetLabels()))
	})

	t.Run("PredeclaredOutputFileTarget", func(t *testing.T) {
		// Output files depend on the rule that generates them.
		e := NewMockTargetDirectDependenciesEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
		e.EXPECT().GetTargetValue(
			testutil.EqProto(t, model_analysis_pb.Target_Key_builder{Label: "@@main+//foo:out.txt"}.Build()),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_analysis_pb.Target_Value_builder{
			Definition: model_starlark_pb.Target_Definition_builder{
				PredeclaredOutputFileTarget: model_starlark_pb.PredeclaredOutputFileTarget_builder{
					OwnerTargetName: "gen",
				}.Build(),
			}.Build(),
		}.Build()))
		value, err := bct.computer.ComputeTargetDirectDependenciesValue(ctx, model_analysis_pb.TargetDirectDependencies_Key_builder{
			Label: "@@main+//foo:out.txt",
		}.Build(), e)
		require.NoError(t, err)
		require.Equal(t, []string{"@@main+//foo:gen"}, getTargetLabels(t, value.Message.GetTargetLabels()))
	})

	t.Run("Alias", func(t *testing.T) {
		e := NewMockTargetDirectDependenciesEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
		e.EXPECT().GetTargetValue(
			testutil.EqProto(t, model_analysis_pb.Target_Key_builder{Label: "@@main+//:alias"}.Build()),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_analysis_pb.Target_Value_builder{
			Definition: model_starlark_pb.Target_Definition_builder{
				Alias: model_starlark_pb.Alias_builder{
					Actual: valueSelectGroup(labelValue("@@main+//:actual")),
				}.Build(),
			}.Build(),
		}.Build()))
		value, err := bct.computer.ComputeTargetDirectDependenciesValue(ctx, model_analysis_pb.TargetDirectDependencies_Key_builder{
			Label: "@@main+//:alias",
		}.Build(), e)
		require.NoError(t, err)
		require.Equal(t, []string{"@@main+//:actual"}, getTargetLabels(t, value.Message.GetTargetLabels()))
	})

	t.Run("DoesNotExist", func(t *testing.T) {
		e := NewMockTargetDirectDependenciesEnvironmentForTesting(ctrl)
		e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
		e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
		e.EXPECT().GetTargetValue(
			testutil.EqProto(t, model_analysis_pb.Target_Key_builder{Label: "@@main+//:missing"}.Build()),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](&model_analysis_pb.Target_Value{}))
		_, err := bct.computer.ComputeTargetDirectDependenciesValue(ctx, model_analysis_pb.TargetDirectDependencies_Key_builder{
			Label: "@@main+//:missing",
		}.Build(), e)
		require.EqualError(t, err, "target does not exist")
	})

	t.Run("TestSuite", func(t *testing.T) {
		// Tests listed by a test suite are explicit
		// dependencies, regardless of the test suite's filters.
		for _, includeImplicitDeps := range []bool{false, true} {
			e := NewMockTargetDirectDependenciesEnvironmentForTesting(ctrl)
			e.EXPECT().GetAnalysisEncodersObject(gomock.Any()).Return(bct.newAnalysisEncoders(), true).AnyTimes()
			e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
			e.EXPECT().GetTargetValue(
				testutil.EqProto(t, model_analysis_pb.Target_Key_builder{Label: "@@main+//foo:suite"}.Build()),
			).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_analysis_pb.Target_Value_builder{
				Definition: model_starlark_pb.Target_Definition_builder{
					TestSuite: model_starlark_pb.TestSuite_builder{
						Tests:        newTestSuiteTests("@@main+//bar:nested_suite", "@@main+//foo:test"),
						PositiveTags: []string{"integration"},
						Sizes:        []model_starlark_pb.TestSize{model_starlark_pb.TestSize_SMALL},
					}.Build(),
				}.Build(),
			}.Build()))
			value, err := bct.computer.ComputeTargetDirectDependenciesValue(ctx, model_analysis_pb.TargetDirectDependencies_Key_builder{
				Label:               "@@main+//foo:suite",
				IncludeImplicitDeps: includeImplicitDeps,
			}.Build(), e)
			require.NoError(t, err)
			require.Equal(t, []string{"@@main+//bar:nested_suite", "@@main+//foo:test"}, getTargetLabels(t, value.Message.GetTargetLabels()))
		}
	})
}
