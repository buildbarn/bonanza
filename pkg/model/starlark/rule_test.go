package starlark_test

import (
	"math/big"
	"testing"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/inlinedtree"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"

	"go.uber.org/mock/gomock"
)

func TestRuleCallTest(t *testing.T) {
	ctrl, ctx := gomock.WithContext(t.Context(), t)
	referenceFormat := util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1))
	encoder := model_encoding.NewChainedDeterministicBinaryEncoder(nil)

	// callRule invokes a rule from within a BUILD file, returning
	// the rule target that is registered.
	callRule := func(t *testing.T, ruleType model_starlark_pb.Rule_Definition_Type, kwargs ...starlark.Tuple) (*model_starlark_pb.RuleTarget, error) {
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

		thread := &starlark.Thread{}
		thread.SetLocal(model_starlark.TargetRegistrarKey, targetRegistrar)
		thread.SetLocal(model_starlark.CanonicalPackageKey, util.Must(label.NewCanonicalPackage("@@main+//foo")))
		thread.SetLocal(model_starlark.ValueEncodingOptionsKey, &model_starlark.ValueEncodingOptions[object.LocalReference, model_core.CreatedObjectTree]{
			Context:                ctx,
			ObjectEncoder:          encoder,
			ObjectReferenceFormat:  referenceFormat,
			ObjectCapturer:         objectManager,
			ObjectMinimumSizeBytes: 32 * 1024,
			ObjectMaximumSizeBytes: 128 * 1024,
		})

		identifier := util.Must(label.NewCanonicalStarlarkIdentifier("@@rules+//:defs.bzl%my_rule"))
		rule := model_starlark.NewRule(
			&identifier,
			model_starlark.NewStarlarkRuleDefinition[object.LocalReference, model_core.CreatedObjectTree](
				/* attrs = */ nil,
				/* buildSetting = */ nil,
				/* cfg = */ nil,
				/* execGroups = */ nil,
				/* implementation = */ model_starlark.NamedFunction[object.LocalReference, model_core.CreatedObjectTree]{},
				/* initializer = */ nil,
				/* provides = */ nil,
				ruleType,
				/* subrules = */ nil,
			),
		)
		if _, err := starlark.Call(thread, rule, nil, append([]starlark.Tuple{{starlark.String("name"), starlark.String("t")}}, kwargs...)); err != nil {
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

		_, err = callRule(t, model_starlark_pb.Rule_Definition_NON_EXECUTABLE, starlark.Tuple{starlark.String("size"), starlark.String("small")})
		require.ErrorContains(t, err, "unexpected keyword argument \"size\"")
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
		ruleTarget, err := callRule(t, model_starlark_pb.Rule_Definition_TEST, starlark.Tuple{starlark.String("size"), starlark.String("enormous")})
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
			starlark.Tuple{starlark.String("size"), starlark.String("small")},
			starlark.Tuple{starlark.String("timeout"), starlark.String("long")},
			starlark.Tuple{starlark.String("flaky"), starlark.True},
			starlark.Tuple{starlark.String("local"), starlark.True},
			starlark.Tuple{starlark.String("shard_count"), model_starlark.NewSelect[object.LocalReference, model_core.CreatedObjectTree](
				[]model_starlark.SelectGroup{
					model_starlark.NewSelectGroup(
						map[label.ResolvedLabel]starlark.Value{condition: starlark.MakeInt(4)},
						starlark.MakeInt(2),
						"",
					),
				},
				/* concatenationOperator = */ 0,
			)},
			starlark.Tuple{starlark.String("args"), starlark.NewList([]starlark.Value{starlark.String("--foo")})},
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
						Level: &model_starlark_pb.List_Element_Leaf{Leaf: &model_starlark_pb.Value{
							Kind: &model_starlark_pb.Value_Str{Str: "--foo"},
						}},
					}},
				}},
			})},
		}, ruleTarget.Test)
	})

	t.Run("InvalidValues", func(t *testing.T) {
		for name, testCase := range map[string]struct {
			kwarg         starlark.Tuple
			expectedError string
		}{
			"Size":     {starlark.Tuple{starlark.String("size"), starlark.String("huge")}, "attribute \"size\": invalid test size \"huge\""},
			"SizeCase": {starlark.Tuple{starlark.String("size"), starlark.String("SMALL")}, "attribute \"size\": invalid test size \"SMALL\""},
			"Timeout":  {starlark.Tuple{starlark.String("timeout"), starlark.String("forever")}, "attribute \"timeout\": invalid test timeout \"forever\""},
		} {
			t.Run(name, func(t *testing.T) {
				_, err := callRule(t, model_starlark_pb.Rule_Definition_TEST, testCase.kwarg)
				require.ErrorContains(t, err, testCase.expectedError)
			})
		}
	})
}
