package analysis_test

import (
	"testing"

	model_core "bonanza.build/pkg/model/core"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

	"google.golang.org/protobuf/proto"

	"go.uber.org/mock/gomock"
)

func TestResolvedToolchains(t *testing.T) {
	ctrl, ctx := gomock.WithContext(t.Context(), t)
	bct := newBaseComputerTester(ctrl)

	const toolchainType = "@@rules+//toolchains:type"
	toolchainConstraint := []*model_analysis_pb.Constraint{
		model_analysis_pb.Constraint_builder{Setting: "@@platforms//os:os", Value: "@@platforms//os:linux"}.Build(),
	}

	newEnvironment := func(compatibleToolchains []*model_analysis_pb.RegisteredToolchain) *MockResolvedToolchainsEnvironmentForTesting {
		e := NewMockResolvedToolchainsEnvironmentForTesting(ctrl)
		e.EXPECT().GetCompatibleExecutionPlatformsValue(
			testutil.EqProto(t, &model_analysis_pb.CompatibleExecutionPlatforms_Key{}),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_analysis_pb.CompatibleExecutionPlatforms_Value_builder{
			ExecutionPlatforms: []*model_analysis_pb.ExecutionPlatform{
				model_analysis_pb.ExecutionPlatform_builder{Label: "@@platforms//host"}.Build(),
			},
		}.Build()))
		e.EXPECT().GetVisibleTargetValue(
			eqPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.VisibleTarget_Key {
				return model_analysis_pb.VisibleTarget_Key_builder{
					FromPackage: "@@rules+//toolchains",
					ToLabel:     toolchainType,
				}.Build()
			}),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_analysis_pb.VisibleTarget_Value_builder{
			Label: toolchainType,
		}.Build()))
		e.EXPECT().GetCompatibleToolchainsForTypeValue(
			eqPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.CompatibleToolchainsForType_Key {
				return model_analysis_pb.CompatibleToolchainsForType_Key_builder{
					ToolchainType: toolchainType,
				}.Build()
			}),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_analysis_pb.CompatibleToolchainsForType_Value_builder{
			Toolchains: compatibleToolchains,
		}.Build()))
		return e
	}
	expectToolchainTypeInfo := func(e *MockResolvedToolchainsEnvironmentForTesting, noMatchError string) {
		e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		e.EXPECT().GetTargetProviderValue(
			eqPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.TargetProvider_Key {
				return model_analysis_pb.TargetProvider_Key_builder{
					Label:              toolchainType,
					ProviderIdentifier: "@@builtins_core+//:exports.bzl%ToolchainTypeInfo",
				}.Build()
			}),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_analysis_pb.TargetProvider_Value_builder{
			Fields: model_starlark_pb.Struct_Fields_builder{
				Keys: []string{"no_match_error", "type_label"},
				Values: []*model_starlark_pb.List_Element{
					model_starlark_pb.List_Element_builder{Leaf: proto.ValueOrDefault(newStringValue(noMatchError))}.Build(),
					model_starlark_pb.List_Element_builder{Leaf: model_starlark_pb.Value_builder{
						Label: proto.String(toolchainType),
					}.Build()}.Build(),
				},
			}.Build(),
		}.Build()))
	}
	compute := func(e *MockResolvedToolchainsEnvironmentForTesting, mandatory bool) (*model_analysis_pb.ResolvedToolchains_Value, error) {
		value, err := bct.computer.ComputeResolvedToolchainsValue(
			ctx,
			model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_analysis_pb.ResolvedToolchains_Key_builder{
				Toolchains: []*model_starlark_pb.ToolchainType{model_starlark_pb.ToolchainType_builder{
					ToolchainType: toolchainType,
					Mandatory:     mandatory,
				}.Build()},
			}.Build()),
			e,
		)
		return value.Message, err
	}

	t.Run("OptionalWithoutToolchains", func(t *testing.T) {
		// Optional toolchain types without any toolchains
		// should resolve to the empty string. There is no need
		// to load the toolchain type's ToolchainTypeInfo.
		e := newEnvironment(nil)

		value, err := compute(e, false)
		require.NoError(t, err)
		testutil.RequireEqualProto(t, model_analysis_pb.ResolvedToolchains_Value_builder{
			ToolchainIdentifiers: []string{""},
			PlatformLabel:        "@@platforms//host",
		}.Build(), value)
	})

	t.Run("MandatoryWithoutToolchains", func(t *testing.T) {
		e := newEnvironment(nil)
		expectToolchainTypeInfo(e, "")

		_, err := compute(e, true)
		require.EqualError(t, err, "dependency on toolchain type \"@@rules+//toolchains:type\" is mandatory, but no toolchains exist that are compatible with the target")
	})

	t.Run("MandatoryWithoutToolchainsNoMatchError", func(t *testing.T) {
		// If toolchain_type(no_match_error = ...) is provided,
		// its value should be appended to the error message.
		e := newEnvironment(nil)
		expectToolchainTypeInfo(e, "Please install the frobnicator")

		_, err := compute(e, true)
		require.EqualError(t, err, "dependency on toolchain type \"@@rules+//toolchains:type\" is mandatory, but no toolchains exist that are compatible with the target: Please install the frobnicator")
	})

	t.Run("MandatoryWithoutCompatibleExecutionPlatformNoMatchError", func(t *testing.T) {
		// The same holds if toolchains exist, but none of them
		// are compatible with any of the execution platforms.
		e := newEnvironment([]*model_analysis_pb.RegisteredToolchain{model_analysis_pb.RegisteredToolchain_builder{
			CompatibleWith: model_analysis_pb.RegisteredToolchain_CompatibleWith_builder{
				Exec: toolchainConstraint,
			}.Build(),
			Toolchain: "@@rules+//toolchains:linux",
		}.Build()})
		expectToolchainTypeInfo(e, "Please install the frobnicator")

		_, err := compute(e, true)
		require.EqualError(t, err, "dependency on toolchain type \"@@rules+//toolchains:type\" is mandatory, but none of the 1 toolchains that are compatible with the target are also compatible with any of the 1 execution platforms: Please install the frobnicator")
	})
}
