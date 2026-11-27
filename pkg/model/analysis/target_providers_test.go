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

	"go.uber.org/mock/gomock"
)

func TestTargetProviders(t *testing.T) {
	ctrl, ctx := gomock.WithContext(t.Context(), t)
	bct := newBaseComputerTester(ctrl)

	const (
		defaultInfo     = "@@builtins_core+//:exports.bzl%DefaultInfo"
		outputGroupInfo = "@@builtins_core+//:exports.bzl%OutputGroupInfo"
		aInfo           = "@@rules+//:defs.bzl%AInfo"
		bInfo           = "@@rules+//:defs.bzl%BInfo"
		cInfo           = "@@rules+//:defs.bzl%CInfo"
	)

	newEnvironment := func() *MockTargetProvidersEnvironmentForTesting {
		e := NewMockTargetProvidersEnvironmentForTesting(ctrl)
		e.EXPECT().CaptureExistingObject(gomock.Any()).
			DoAndReturn(func(reference model_core.CreatedObjectTree) model_core.CreatedObjectTree {
				return reference
			}).
			AnyTimes()
		return e
	}
	// Only request the Starlark value encoders when aspects need
	// to be applied.
	newEnvironmentWithEncoders := func() *MockTargetProvidersEnvironmentForTesting {
		e := newEnvironment()
		e.EXPECT().GetStarlarkValueEncodersObject(gomock.Any()).Return(bct.newStarlarkValueEncoders(), true).AnyTimes()
		return e
	}
	newStruct := func(providerIdentifier string, fields map[string]*model_starlark_pb.Value) *model_starlark_pb.Struct {
		s := model_starlark_pb.Struct_builder{
			Fields: &model_starlark_pb.Struct_Fields{},
			ProviderInstanceProperties: model_starlark_pb.Provider_InstanceProperties_builder{
				ProviderIdentifier: providerIdentifier,
			}.Build(),
		}.Build()
		for _, key := range []string{"files", "x", "y", "z"} {
			if value, ok := fields[key]; ok {
				s.GetFields().SetKeys(append(s.GetFields().GetKeys(), key))
				s.GetFields().SetValues(append(s.GetFields().GetValues(), model_starlark_pb.List_Element_builder{
					Leaf: proto.ValueOrDefault(value),
				}.Build()))
			}
		}
		return s
	}
	boolValue := func(b bool) *model_starlark_pb.Value {
		return model_starlark_pb.Value_builder{Bool: proto.Bool(b)}.Build()
	}
	newAspect := func(identifier string) *model_starlark_pb.AppliedAspect {
		return model_starlark_pb.AppliedAspect_builder{
			Identifier: identifier,
		}.Build()
	}
	aspect1 := newAspect("@@rules+//:defs.bzl%aspect1")
	aspect2 := newAspect("@@rules+//:defs.bzl%aspect2")

	expectIncomingEdgeTransition := func(e *MockTargetProvidersEnvironmentForTesting, label string) {
		e.EXPECT().GetTargetIncomingEdgeTransitionValue(
			eqPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.TargetIncomingEdgeTransition_Key {
				return &model_analysis_pb.TargetIncomingEdgeTransition_Key{Label: label}
			}),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](&model_analysis_pb.TargetIncomingEdgeTransition_Value{}))
	}
	expectConfiguredTarget := func(e *MockTargetProvidersEnvironmentForTesting, label string, aspects []*model_starlark_pb.AppliedAspect, value *model_analysis_pb.ConfiguredTarget_Value) {
		e.EXPECT().GetConfiguredTargetValue(
			eqPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.ConfiguredTarget_Key {
				return &model_analysis_pb.ConfiguredTarget_Key{
					Label:   label,
					Variant: newTestTargetVariant(aspects),
				}
			}),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](value))
	}
	compute := func(e *MockTargetProvidersEnvironmentForTesting, label string, aspects ...*model_starlark_pb.AppliedAspect) (model_core.PatchedMessage[*model_analysis_pb.TargetProviders_Value, model_core.CreatedObjectTree], error) {
		return bct.computer.ComputeTargetProvidersValue(
			ctx,
			model_core.NewSimpleMessage[model_core.CreatedObjectTree](&model_analysis_pb.TargetProviders_Key{
				Label:   label,
				Variant: newTestTargetVariant(aspects),
			}),
			e,
		)
	}
	t.Run("MissingIncomingEdgeTransition", func(t *testing.T) {
		e := newEnvironment()
		e.EXPECT().GetTargetIncomingEdgeTransitionValue(gomock.Any()).
			Return(model_core.Message[*model_analysis_pb.TargetIncomingEdgeTransition_Value, model_core.CreatedObjectTree]{})

		_, err := compute(e, "@@main+//:foo")
		require.Equal(t, evaluation.ErrMissingDependency, err)
	})

	t.Run("NoAspects", func(t *testing.T) {
		e := newEnvironment()
		expectIncomingEdgeTransition(e, "@@main+//:foo")
		expectConfiguredTarget(e, "@@main+//:foo", nil, &model_analysis_pb.ConfiguredTarget_Value{
			ProviderInstances: []*model_starlark_pb.Struct{
				newStruct(defaultInfo, nil),
				newStruct(aInfo, nil),
			},
		})

		value, err := compute(e, "@@main+//:foo")
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_analysis_pb.TargetProviders_Value{
			ProviderInstances: []*model_starlark_pb.Struct{
				newStruct(defaultInfo, nil),
				newStruct(aInfo, nil),
			},
		}, value.Message)
	})

	t.Run("MergeAspects", func(t *testing.T) {
		// Aspects are applied to every prefix of the list of
		// aspects. Their provider instances are merged with
		// those of the target, and instances of OutputGroupInfo
		// are combined.
		e := newEnvironmentWithEncoders()
		expectIncomingEdgeTransition(e, "@@main+//:foo")
		expectConfiguredTarget(e, "@@main+//:foo", nil, &model_analysis_pb.ConfiguredTarget_Value{
			ProviderInstances: []*model_starlark_pb.Struct{
				newStruct(defaultInfo, nil),
				newStruct(outputGroupInfo, map[string]*model_starlark_pb.Value{"y": boolValue(true)}),
				newStruct(aInfo, nil),
			},
		})
		expectConfiguredTarget(e, "@@main+//:foo", []*model_starlark_pb.AppliedAspect{aspect1}, &model_analysis_pb.ConfiguredTarget_Value{
			ProviderInstances: []*model_starlark_pb.Struct{
				newStruct(cInfo, nil),
				newStruct(outputGroupInfo, map[string]*model_starlark_pb.Value{"x": boolValue(false), "z": boolValue(true)}),
			},
		})
		expectConfiguredTarget(e, "@@main+//:foo", []*model_starlark_pb.AppliedAspect{aspect1, aspect2}, &model_analysis_pb.ConfiguredTarget_Value{
			ProviderInstances: []*model_starlark_pb.Struct{
				newStruct(bInfo, nil),
			},
		})

		value, err := compute(e, "@@main+//:foo", aspect1, aspect2)
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_analysis_pb.TargetProviders_Value{
			ProviderInstances: []*model_starlark_pb.Struct{
				newStruct(defaultInfo, nil),
				newStruct(outputGroupInfo, map[string]*model_starlark_pb.Value{"x": boolValue(false), "y": boolValue(true), "z": boolValue(true)}),
				newStruct(aInfo, nil),
				newStruct(bInfo, nil),
				newStruct(cInfo, nil),
			},
		}, value.Message)
	})

	t.Run("MergeOutputGroupsInParentNodes", func(t *testing.T) {
		// The values of struct fields are stored in B-trees,
		// meaning they may not be stored inline. Merging
		// instances of OutputGroupInfo should still work.
		valuesObject := newObject(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) encoding.BinaryMarshaler {
			return model_format.NewProtoList[model_starlark_pb.List_Element]().NewMarshaler([]*model_starlark_pb.List_Element{
				model_starlark_pb.List_Element_builder{Leaf: proto.ValueOrDefault(boolValue(false))}.Build(),
				model_starlark_pb.List_Element_builder{Leaf: proto.ValueOrDefault(boolValue(true))}.Build(),
			})
		})
		e := newEnvironmentWithEncoders()
		expectIncomingEdgeTransition(e, "@@main+//:foo")
		expectConfiguredTarget(e, "@@main+//:foo", nil, &model_analysis_pb.ConfiguredTarget_Value{
			ProviderInstances: []*model_starlark_pb.Struct{
				newStruct(outputGroupInfo, map[string]*model_starlark_pb.Value{"y": boolValue(true)}),
			},
		})
		e.EXPECT().GetConfiguredTargetValue(
			eqPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.ConfiguredTarget_Key {
				return &model_analysis_pb.ConfiguredTarget_Key{
					Label:   "@@main+//:foo",
					Variant: newTestTargetVariant([]*model_starlark_pb.AppliedAspect{aspect1}),
				}
			}),
		).Return(newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.ConfiguredTarget_Value {
			return &model_analysis_pb.ConfiguredTarget_Value{
				ProviderInstances: []*model_starlark_pb.Struct{model_starlark_pb.Struct_builder{
					Fields: model_starlark_pb.Struct_Fields_builder{
						Keys: []string{"x", "z"},
						Values: []*model_starlark_pb.List_Element{model_starlark_pb.List_Element_builder{
							Parent: model_starlark_pb.List_Element_Parent_builder{
								Reference: attachObject(patcher, valuesObject),
								Count:     2,
							}.Build(),
						}.Build()},
					}.Build(),
					ProviderInstanceProperties: model_starlark_pb.Provider_InstanceProperties_builder{
						ProviderIdentifier: outputGroupInfo,
					}.Build(),
				}.Build()},
			}
		}))

		value, err := compute(e, "@@main+//:foo", aspect1)
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_analysis_pb.TargetProviders_Value{
			ProviderInstances: []*model_starlark_pb.Struct{
				newStruct(outputGroupInfo, map[string]*model_starlark_pb.Value{"x": boolValue(false), "y": boolValue(true), "z": boolValue(true)}),
			},
		}, value.Message)
	})

	t.Run("DuplicateProvider", func(t *testing.T) {
		e := newEnvironmentWithEncoders()
		expectIncomingEdgeTransition(e, "@@main+//:foo")
		expectConfiguredTarget(e, "@@main+//:foo", nil, &model_analysis_pb.ConfiguredTarget_Value{
			ProviderInstances: []*model_starlark_pb.Struct{newStruct(aInfo, nil)},
		})
		expectConfiguredTarget(e, "@@main+//:foo", []*model_starlark_pb.AppliedAspect{aspect1}, &model_analysis_pb.ConfiguredTarget_Value{
			ProviderInstances: []*model_starlark_pb.Struct{newStruct(aInfo, nil)},
		})

		_, err := compute(e, "@@main+//:foo", aspect1)
		require.EqualError(t, err, "both target \"@@main+//:foo\" and aspect \"@@rules+//:defs.bzl%aspect1\" provide an instance of provider \"@@rules+//:defs.bzl%AInfo\"")
	})

	t.Run("DuplicateOutputGroup", func(t *testing.T) {
		e := newEnvironmentWithEncoders()
		expectIncomingEdgeTransition(e, "@@main+//:foo")
		expectConfiguredTarget(e, "@@main+//:foo", nil, &model_analysis_pb.ConfiguredTarget_Value{
			ProviderInstances: []*model_starlark_pb.Struct{newStruct(outputGroupInfo, map[string]*model_starlark_pb.Value{"x": boolValue(true)})},
		})
		expectConfiguredTarget(e, "@@main+//:foo", []*model_starlark_pb.AppliedAspect{aspect1}, &model_analysis_pb.ConfiguredTarget_Value{
			ProviderInstances: []*model_starlark_pb.Struct{newStruct(outputGroupInfo, map[string]*model_starlark_pb.Value{"x": boolValue(false)})},
		})

		_, err := compute(e, "@@main+//:foo", aspect1)
		require.EqualError(t, err, "both target \"@@main+//:foo\" and aspect \"@@rules+//:defs.bzl%aspect1\" provide output group \"x\"")
	})
}
