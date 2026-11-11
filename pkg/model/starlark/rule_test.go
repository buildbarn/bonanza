package starlark_test

import (
	"math/big"
	"testing"

	"bonanza.build/pkg/label"
	"bonanza.build/pkg/model/chunking"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/inlinedtree"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_chunking_pb "bonanza.build/pkg/proto/model/chunking"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"

	"go.uber.org/mock/gomock"
)

func TestRuleCallTest(t *testing.T) {
	ctrl, ctx := gomock.WithContext(t.Context(), t)
	referenceFormat := util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1))
	encoder := model_encoding.NewChainedDeterministicBinaryEncoder(nil)

	// callRule invokes a rule from within a BUILD file, returning
	// the rule target that is registered.
	callRule := func(t *testing.T, ruleType model_starlark_pb.Rule_Definition_Type, kwargs ...eval.NamedArg) (*model_starlark_pb.RuleTarget, error) {
		objectManager := NewMockObjectManagerForTesting(ctrl)
		targetRegistrar := model_starlark.NewTargetRegistrar[object.LocalReference, model_core.CreatedObjectTree](
			ctx,
			encoder,
			&inlinedtree.Options{
				ReferenceFormat:  referenceFormat,
				MaximumSizeBytes: 32 * 1024,
			},
			objectManager,
			model_core.NewSimpleMessage[object.LocalReference](&model_starlark_pb.InheritableAttrs{}),
		)
		defer targetRegistrar.Discard()

		thread := newTestThread(t)
		thread.SetLocal(model_starlark.TargetRegistrarKey, targetRegistrar)
		thread.SetLocal(model_starlark.CanonicalPackageKey, util.Must(label.NewCanonicalPackage("@@main+//foo")))
		thread.SetLocal(model_starlark.ValueEncodingOptionsKey, &model_starlark.ValueEncodingOptions[object.LocalReference, model_core.CreatedObjectTree]{
			Context:               ctx,
			ObjectEncoder:         encoder,
			ObjectReferenceFormat: referenceFormat,
			ObjectCapturer:        objectManager,
			ObjectChunkingParameters: util.Must(chunking.NewParametersFromProto(&model_chunking_pb.Parameters{
				MinimumSizeBytes: 32 * 1024,
				HorizonSizeBytes: 8 * 32 * 1024,
			}, referenceFormat)),
		})

		identifier := util.Must(label.NewCanonicalStarlarkIdentifier("@@rules+//:defs.bzl%my_rule"))
		rule := model_starlark.NewRule(
			&identifier,
			model_starlark.NewStarlarkRuleDefinition[object.LocalReference, model_core.CreatedObjectTree](
				/* attrs = */ nil,
				/* buildSetting = */ nil,
				/* cfg = */ nil,
				/* execGroups = */ nil,
				/* implementation = */ nil,
				/* initializer = */ nil,
				/* provides = */ nil,
				ruleType,
				/* subrules = */ nil,
			),
		)
		if _, err := eval.Call(thread, rule, eval.Args{Named: append([]eval.NamedArg{{Name: "name", Value: eval.String("t")}}, kwargs...)}); err != nil {
			return nil, err
		}
		return targetRegistrar.GetAndRemoveTarget("t").Message.GetRuleTarget(), nil
	}
	intValue := func(i int64) *model_starlark_pb.Value {
		return &model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_Int{Int: model_starlark.EncodeInt(big.NewInt(i))},
		}
	}
	noMatchGroup := func(v *model_starlark_pb.Value) *model_starlark_pb.Select_Group {
		return &model_starlark_pb.Select_Group{
			NoMatch: &model_starlark_pb.Select_Group_NoMatchValue{NoMatchValue: v},
		}
	}

	t.Run("NotATest", func(t *testing.T) {
		// Attributes specific to tests should not be accepted
		// by other rules.
		ruleTarget, err := callRule(t, model_starlark_pb.Rule_Definition_NON_EXECUTABLE)
		require.NoError(t, err)
		require.Nil(t, ruleTarget.Test)

		_, err = callRule(t, model_starlark_pb.Rule_Definition_NON_EXECUTABLE, eval.NamedArg{Name: "size", Value: eval.String("small")})
		require.ErrorContains(t, err, "unexpected keyword argument 'size'")
	})

	t.Run("Defaults", func(t *testing.T) {
		ruleTarget, err := callRule(t, model_starlark_pb.Rule_Definition_TEST)
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_starlark_pb.RuleTarget_Test{
			Size:       model_starlark_pb.TestSize_MEDIUM,
			Timeout:    model_starlark_pb.TestTimeout_MODERATE,
			ShardCount: []*model_starlark_pb.Select_Group{noMatchGroup(intValue(1))},
			Args: []*model_starlark_pb.Select_Group{noMatchGroup(&model_starlark_pb.Value{
				Kind: &model_starlark_pb.Value_List{List: &model_starlark_pb.List{}},
			})},
		}, ruleTarget.Test)
	})

	t.Run("TimeoutDerivedFromSize", func(t *testing.T) {
		ruleTarget, err := callRule(t, model_starlark_pb.Rule_Definition_TEST, eval.NamedArg{Name: "size", Value: eval.String("enormous")})
		require.NoError(t, err)
		require.Equal(t, model_starlark_pb.TestSize_ENORMOUS, ruleTarget.Test.Size)
		require.Equal(t, model_starlark_pb.TestTimeout_ETERNAL, ruleTarget.Test.Timeout)
	})

	t.Run("Explicit", func(t *testing.T) {
		// shard_count and args are configurable.
		condition := util.Must(label.NewResolvedLabel("@@main+//:cond"))
		ruleTarget, err := callRule(
			t,
			model_starlark_pb.Rule_Definition_TEST,
			eval.NamedArg{Name: "size", Value: eval.String("small")},
			eval.NamedArg{Name: "timeout", Value: eval.String("long")},
			eval.NamedArg{Name: "flaky", Value: eval.True},
			eval.NamedArg{Name: "local", Value: eval.True},
			eval.NamedArg{Name: "shard_count", Value: model_starlark.NewSelect[object.LocalReference, model_core.CreatedObjectTree](
				[]model_starlark.SelectGroup{
					model_starlark.NewSelectGroup(
						map[label.ResolvedLabel]eval.Value{condition: eval.MakeInt(4)},
						eval.MakeInt(2),
						"",
					),
				},
				/* concatenationOperator = */ 0,
			)},
			eval.NamedArg{Name: "args", Value: eval.NewList([]eval.Value{eval.String("--foo")})},
		)
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_starlark_pb.RuleTarget_Test{
			Size:    model_starlark_pb.TestSize_SMALL,
			Timeout: model_starlark_pb.TestTimeout_LONG,
			Flaky:   true,
			Local:   true,
			ShardCount: []*model_starlark_pb.Select_Group{{
				Conditions: []*model_starlark_pb.Select_Condition{{
					ConditionIdentifier: "@@main+//:cond",
					Value:               intValue(4),
				}},
				NoMatch: &model_starlark_pb.Select_Group_NoMatchValue{NoMatchValue: intValue(2)},
			}},
			Args: []*model_starlark_pb.Select_Group{noMatchGroup(&model_starlark_pb.Value{
				Kind: &model_starlark_pb.Value_List{List: &model_starlark_pb.List{
					Elements: []*model_starlark_pb.List_Element{{
						Level: &model_starlark_pb.List_Element_Leaf{Leaf: newStringValue("--foo")},
					}},
				}},
			})},
		}, ruleTarget.Test)
	})

	t.Run("InvalidValues", func(t *testing.T) {
		for name, testCase := range map[string]struct {
			kwarg         eval.NamedArg
			expectedError string
		}{
			"Size":     {eval.NamedArg{Name: "size", Value: eval.String("huge")}, "attribute \"size\": invalid test size \"huge\""},
			"SizeCase": {eval.NamedArg{Name: "size", Value: eval.String("SMALL")}, "attribute \"size\": invalid test size \"SMALL\""},
			"Timeout":  {eval.NamedArg{Name: "timeout", Value: eval.String("forever")}, "attribute \"timeout\": invalid test timeout \"forever\""},
		} {
			t.Run(name, func(t *testing.T) {
				_, err := callRule(t, model_starlark_pb.Rule_Definition_TEST, testCase.kwarg)
				require.ErrorContains(t, err, testCase.expectedError)
			})
		}
	})
}

func TestRuleCallOutputAttr(t *testing.T) {
	ctrl, ctx := gomock.WithContext(t.Context(), t)
	referenceFormat := util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1))
	encoder := model_encoding.NewChainedDeterministicBinaryEncoder(nil)
	valueEncodingOptions := &model_starlark.ValueEncodingOptions[object.LocalReference, model_core.CreatedObjectTree]{
		Context:               ctx,
		ObjectEncoder:         encoder,
		ObjectReferenceFormat: referenceFormat,
		ObjectChunkingParameters: util.Must(chunking.NewParametersFromProto(&model_chunking_pb.Parameters{
			MinimumSizeBytes: 32 * 1024,
			HorizonSizeBytes: 8 * 32 * 1024,
		}, referenceFormat)),
	}
	attr := model_starlark.NewAttr[object.LocalReference, model_core.CreatedObjectTree](
		model_starlark.NewOutputAttrType[object.LocalReference, model_core.CreatedObjectTree](""),
		/* defaultValue = */ nil,
		/* configurable = */ false,
	)

	// Outputs are never configurable, meaning they cannot be
	// provided using select().
	callRule := func(out eval.Value) error {
		objectManager := NewMockObjectManagerForTesting(ctrl)
		targetRegistrar := model_starlark.NewTargetRegistrar[object.LocalReference, model_core.CreatedObjectTree](
			ctx,
			encoder,
			&inlinedtree.Options{
				ReferenceFormat:  referenceFormat,
				MaximumSizeBytes: 32 * 1024,
			},
			objectManager,
			model_core.NewSimpleMessage[object.LocalReference](&model_starlark_pb.InheritableAttrs{}),
		)
		defer targetRegistrar.Discard()

		thread := newTestThread(t)
		thread.SetLocal(model_starlark.TargetRegistrarKey, targetRegistrar)
		thread.SetLocal(model_starlark.CanonicalPackageKey, util.Must(label.NewCanonicalPackage("@@main+//foo")))
		thread.SetLocal(model_starlark.ValueEncodingOptionsKey, valueEncodingOptions)

		identifier := util.Must(label.NewCanonicalStarlarkIdentifier("@@rules+//:defs.bzl%my_rule"))
		rule := model_starlark.NewRule(
			&identifier,
			model_starlark.NewStarlarkRuleDefinition[object.LocalReference, model_core.CreatedObjectTree](
				/* attrs = */ map[label.StarlarkIdentifier]*model_starlark.Attr[object.LocalReference, model_core.CreatedObjectTree]{
					util.Must(label.NewStarlarkIdentifier("out")): attr,
				},
				/* buildSetting = */ nil,
				/* cfg = */ nil,
				/* execGroups = */ nil,
				/* implementation = */ nil,
				/* initializer = */ nil,
				/* provides = */ nil,
				model_starlark_pb.Rule_Definition_NON_EXECUTABLE,
				/* subrules = */ nil,
			),
		)
		_, err := eval.Call(thread, rule, eval.Args{Named: []eval.NamedArg{
			{Name: "name", Value: eval.String("t")},
			{Name: "out", Value: out},
		}})
		return err
	}

	outLabel := model_starlark.NewLabel[object.LocalReference, model_core.CreatedObjectTree](util.Must(label.NewResolvedLabel("@@main+//foo:a.txt")))
	require.NoError(t, callRule(outLabel))
	require.EqualError(t, callRule(model_starlark.NewSelect[object.LocalReference, model_core.CreatedObjectTree](
		[]model_starlark.SelectGroup{model_starlark.NewSelectGroup(nil, outLabel, "")},
		0,
	)), "invalid argument \"out\": attribute is not configurable, meaning select() cannot be used")
}
