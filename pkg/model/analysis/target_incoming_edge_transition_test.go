package analysis_test

import (
	"encoding"
	"testing"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_format "bonanza.build/pkg/model/format"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

	"google.golang.org/protobuf/proto"
	xpb "google.golang.org/protobuf/types/known/emptypb"

	"go.uber.org/mock/gomock"
)

func TestTargetIncomingEdgeTransition(t *testing.T) {
	ctrl, ctx := gomock.WithContext(t.Context(), t)
	bct := newBaseComputerTester(ctrl)

	newConfiguration := func(platform string) model_core.Decodable[model_core.CreatedObject[model_core.CreatedObjectTree]] {
		return newObject(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) encoding.BinaryMarshaler {
			return model_format.NewProtoList[model_analysis_pb.BuildSettingOverride]().NewMarshaler([]*model_analysis_pb.BuildSettingOverride{model_analysis_pb.BuildSettingOverride_builder{
				Leaf: model_analysis_pb.BuildSettingOverride_Leaf_builder{
					Label: "@@bazel_tools+//command_line_option:platforms",
					Value: labelValue(platform),
				}.Build(),
			}.Build()})
		})
	}
	requestedConfiguration := newConfiguration("@@platforms+//requested")
	transitionedConfiguration := newConfiguration("@@platforms+//transitioned")

	newEnvironment := func() *MockTargetIncomingEdgeTransitionEnvironmentForTesting {
		e := NewMockTargetIncomingEdgeTransitionEnvironmentForTesting(ctrl)
		e.EXPECT().CaptureExistingObject(gomock.Any()).
			DoAndReturn(func(reference model_core.CreatedObjectTree) model_core.CreatedObjectTree {
				return reference
			}).
			AnyTimes()
		return e
	}
	expectTarget := func(e *MockTargetIncomingEdgeTransitionEnvironmentForTesting, label string, definition *model_starlark_pb.Target_Definition) {
		e.EXPECT().GetTargetValue(
			testutil.EqProto(t, model_analysis_pb.Target_Key_builder{Label: label}.Build()),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_analysis_pb.Target_Value_builder{
			Definition: definition,
		}.Build()))
	}
	expectRule := func(e *MockTargetIncomingEdgeTransitionEnvironmentForTesting, definition *model_starlark_pb.Rule_Definition) {
		e.EXPECT().GetCompiledBzlFileGlobalValue(
			testutil.EqProto(t, model_analysis_pb.CompiledBzlFileGlobal_Key_builder{Identifier: "@@rules+//:defs.bzl%my_rule"}.Build()),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_analysis_pb.CompiledBzlFileGlobal_Value_builder{
			Global: model_starlark_pb.Value_builder{
				Rule: model_starlark_pb.Rule_builder{
					Definition: proto.ValueOrDefault(definition),
				}.Build(),
			}.Build(),
		}.Build()))
	}
	ruleTargetDefinition := model_starlark_pb.Target_Definition_builder{
		RuleTarget: model_starlark_pb.RuleTarget_builder{
			RuleIdentifier: "@@rules+//:defs.bzl%my_rule",
			PublicAttrValues: []*model_starlark_pb.RuleTarget_PublicAttrValue{
				model_starlark_pb.RuleTarget_PublicAttrValue_builder{
					// Left at its default value.
					ValueParts: []*model_starlark_pb.Select_Group{
						valueSelectGroup(model_starlark_pb.Value_builder{
							None: &xpb.Empty{},
						}.Build()),
					},
				}.Build(),
				model_starlark_pb.RuleTarget_PublicAttrValue_builder{
					// Explicitly set.
					ValueParts: []*model_starlark_pb.Select_Group{
						valueSelectGroup(labelValue("@@main+//:dep")),
					},
				}.Build(),
			},
		}.Build(),
	}.Build()
	compute := func(e *MockTargetIncomingEdgeTransitionEnvironmentForTesting, label string) (model_core.PatchedMessage[*model_analysis_pb.TargetIncomingEdgeTransition_Value, model_core.CreatedObjectTree], error) {
		return bct.computer.ComputeTargetIncomingEdgeTransitionValue(
			ctx,
			newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.TargetIncomingEdgeTransition_Key {
				return model_analysis_pb.TargetIncomingEdgeTransition_Key_builder{
					Label:                  label,
					ConfigurationReference: attachObject(patcher, requestedConfiguration),
				}.Build()
			}),
			e,
		)
	}
	requireOutputConfiguration := func(t *testing.T, want model_core.Decodable[model_core.CreatedObject[model_core.CreatedObjectTree]], got model_core.PatchedMessage[*model_analysis_pb.TargetIncomingEdgeTransition_Value, model_core.CreatedObjectTree]) {
		require.True(t, model_core.PatchedMessagesEqual(
			model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.TargetIncomingEdgeTransition_Value {
				return model_analysis_pb.TargetIncomingEdgeTransition_Value_builder{
					OutputConfigurationReference: attachObject(patcher, want),
				}.Build()
			}),
			got,
		))
	}

	t.Run("MissingTarget", func(t *testing.T) {
		e := newEnvironment()
		e.EXPECT().GetTargetValue(gomock.Any()).
			Return(model_core.Message[*model_analysis_pb.Target_Value, model_core.CreatedObjectTree]{})

		_, err := compute(e, "@@main+//:foo")
		require.Equal(t, evaluation.ErrMissingDependency, err)
	})

	t.Run("SourceFile", func(t *testing.T) {
		// The properties of source files don't depend on the
		// configuration, so they should transition to the empty
		// configuration.
		e := newEnvironment()
		expectTarget(e, "@@main+//:foo.txt", model_starlark_pb.Target_Definition_builder{
			SourceFileTarget: &model_starlark_pb.SourceFileTarget{},
		}.Build())

		value, err := compute(e, "@@main+//:foo.txt")
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_analysis_pb.TargetIncomingEdgeTransition_Value{}, value.Message)
	})

	t.Run("PackageGroup", func(t *testing.T) {
		e := newEnvironment()
		expectTarget(e, "@@main+//:group", model_starlark_pb.Target_Definition_builder{
			PackageGroup: &model_starlark_pb.PackageGroup{},
		}.Build())

		value, err := compute(e, "@@main+//:group")
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_analysis_pb.TargetIncomingEdgeTransition_Value{}, value.Message)
	})

	t.Run("TestSuite", func(t *testing.T) {
		// Test suites use an identity transition.
		e := newEnvironment()
		expectTarget(e, "@@main+//:tests", model_starlark_pb.Target_Definition_builder{
			TestSuite: &model_starlark_pb.TestSuite{},
		}.Build())

		value, err := compute(e, "@@main+//:tests")
		require.NoError(t, err)
		requireOutputConfiguration(t, requestedConfiguration, value)
	})

	t.Run("PredeclaredOutputFile", func(t *testing.T) {
		// Predeclared output files should use the incoming edge
		// transition of the rule target that owns them.
		e := newEnvironment()
		expectTarget(e, "@@main+//:foo.out", model_starlark_pb.Target_Definition_builder{
			PredeclaredOutputFileTarget: model_starlark_pb.PredeclaredOutputFileTarget_builder{
				OwnerTargetName: "foo",
			}.Build(),
		}.Build())
		e.EXPECT().GetTargetIncomingEdgeTransitionValue(
			eqPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.TargetIncomingEdgeTransition_Key {
				return model_analysis_pb.TargetIncomingEdgeTransition_Key_builder{
					Label:                  "@@main+//:foo",
					ConfigurationReference: attachObject(patcher, requestedConfiguration),
				}.Build()
			}),
		).Return(newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.TargetIncomingEdgeTransition_Value {
			return model_analysis_pb.TargetIncomingEdgeTransition_Value_builder{
				OutputConfigurationReference: attachObject(patcher, transitionedConfiguration),
			}.Build()
		}))

		value, err := compute(e, "@@main+//:foo.out")
		require.NoError(t, err)
		requireOutputConfiguration(t, transitionedConfiguration, value)
	})

	t.Run("RuleTargetWithoutTransition", func(t *testing.T) {
		// Rules without an incoming edge transition use an
		// identity transition.
		e := newEnvironment()
		expectTarget(e, "@@main+//:foo", ruleTargetDefinition)
		expectRule(e, &model_starlark_pb.Rule_Definition{})

		value, err := compute(e, "@@main+//:foo")
		require.NoError(t, err)
		requireOutputConfiguration(t, requestedConfiguration, value)
	})

	t.Run("RuleTargetWithTransition", func(t *testing.T) {
		// Rules with an incoming edge transition should apply it
		// through RuleUsageIncomingEdgeTransition, providing the
		// indices of the public attrs that are explicitly set.
		e := newEnvironment()
		expectTarget(e, "@@main+//:foo", ruleTargetDefinition)
		expectRule(e, model_starlark_pb.Rule_Definition_builder{
			CfgTransition: model_starlark_pb.Transition_UserDefined_builder{
				Identifier: proto.String("@@rules+//:defs.bzl%my_transition"),
			}.Build(),
		}.Build())
		e.EXPECT().GetRuleUsageIncomingEdgeTransitionValue(
			eqPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.RuleUsageIncomingEdgeTransition_Key {
				return model_analysis_pb.RuleUsageIncomingEdgeTransition_Key_builder{
					Identifier:                "@@rules+//:defs.bzl%my_rule",
					ConfigurationReference:    attachObject(patcher, requestedConfiguration),
					ExplicitPublicAttrIndices: []uint32{1},
				}.Build()
			}),
		).Return(newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.RuleUsageIncomingEdgeTransition_Value {
			return model_analysis_pb.RuleUsageIncomingEdgeTransition_Value_builder{
				OutputConfigurationReference: proto.ValueOrDefault(attachObject(patcher, transitionedConfiguration)),
			}.Build()
		}))

		value, err := compute(e, "@@main+//:foo")
		require.NoError(t, err)
		requireOutputConfiguration(t, transitionedConfiguration, value)
	})

	t.Run("RuleTargetWithTransitionMissingDependency", func(t *testing.T) {
		e := newEnvironment()
		expectTarget(e, "@@main+//:foo", ruleTargetDefinition)
		expectRule(e, model_starlark_pb.Rule_Definition_builder{
			CfgTransition: model_starlark_pb.Transition_UserDefined_builder{
				Identifier: proto.String("@@rules+//:defs.bzl%my_transition"),
			}.Build(),
		}.Build())
		e.EXPECT().GetRuleUsageIncomingEdgeTransitionValue(gomock.Any()).
			Return(model_core.Message[*model_analysis_pb.RuleUsageIncomingEdgeTransition_Value, model_core.CreatedObjectTree]{})

		_, err := compute(e, "@@main+//:foo")
		require.Equal(t, evaluation.ErrMissingDependency, err)
	})
}
