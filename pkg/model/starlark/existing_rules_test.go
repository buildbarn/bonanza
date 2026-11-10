package starlark_test

import (
	"math/big"
	"slices"
	"testing"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"
)

// fakeExistingRulesResolver is a simple implementation of
// ExistingRulesResolver that is backed by a map of target definitions.
type fakeExistingRulesResolver map[string]*model_starlark_pb.Target_Definition

func (r fakeExistingRulesResolver) LookupExistingRule(targetName label.TargetName) (model_core.Message[*model_starlark_pb.Target_Definition, object.LocalReference], error) {
	definition, ok := r[targetName.String()]
	if !ok {
		return model_core.Message[*model_starlark_pb.Target_Definition, object.LocalReference]{}, nil
	}
	return model_core.NewSimpleMessage[object.LocalReference](definition), nil
}

func (r fakeExistingRulesResolver) ForEachExistingRule(yield func(targetName label.TargetName, definition model_core.Message[*model_starlark_pb.Target_Definition, object.LocalReference]) error) error {
	for _, name := range slices.Sorted(func(yield func(string) bool) {
		for name := range r {
			if !yield(name) {
				return
			}
		}
	}) {
		if err := yield(util.Must(label.NewTargetName(name)), model_core.NewSimpleMessage[object.LocalReference](r[name])); err != nil {
			return err
		}
	}
	return nil
}

func TestExistingRules(t *testing.T) {
	bzlFileBuiltins, _ := model_starlark.GetBuiltins[object.LocalReference, model_core.CreatedObjectTree]()
	native := bzlFileBuiltins["native"].(eval.HasAttrs)
	nativeExistingRule := util.Must(native.Attr(newTestThread(t), "existing_rule"))
	nativeExistingRules := util.Must(native.Attr(newTestThread(t), "existing_rules"))

	value := func(kind any) *model_starlark_pb.Value {
		switch v := kind.(type) {
		case string:
			return newStringValue(v)
		case label.CanonicalLabel:
			return &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Label{Label: v.String()}}
		case []*model_starlark_pb.Value:
			elements := make([]*model_starlark_pb.List_Element, 0, len(v))
			for _, element := range v {
				elements = append(elements, &model_starlark_pb.List_Element{
					Level: &model_starlark_pb.List_Element_Leaf{Leaf: element},
				})
			}
			return &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_List{List: &model_starlark_pb.List{Elements: elements}}}
		case nil:
			return &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_None{}}
		}
		panic("unknown value kind")
	}
	newLabel := func(l string) label.CanonicalLabel {
		return util.Must(label.NewCanonicalLabel(l))
	}
	valueParts := func(groups ...*model_starlark_pb.Select_Group) []*model_starlark_pb.Select_Group {
		return groups
	}
	noMatch := func(v *model_starlark_pb.Value, conditions ...*model_starlark_pb.Select_Condition) *model_starlark_pb.Select_Group {
		return &model_starlark_pb.Select_Group{
			Conditions: conditions,
			NoMatch:    &model_starlark_pb.Select_Group_NoMatchValue{NoMatchValue: v},
		}
	}

	newThread := func(t *testing.T, resolver model_starlark.ExistingRulesResolver[object.LocalReference]) *eval.Thread {
		thread := newTestThread(t)
		thread.SetLocal(model_starlark.CanonicalPackageKey, util.Must(label.NewCanonicalPackage("@@main+//foo")))
		thread.SetLocal(model_starlark.ValueDecodingOptionsKey, &model_starlark.ValueDecodingOptions[object.LocalReference]{
			Readers: &model_starlark.ValueReaders[object.LocalReference]{},
			LabelCreator: func(l label.ResolvedLabel) (eval.Value, error) {
				return model_starlark.NewLabel[object.LocalReference, model_core.CreatedObjectTree](l), nil
			},
		})
		thread.SetLocal(model_starlark.GlobalResolverKey, model_starlark.GlobalResolver[object.LocalReference](func(identifier label.CanonicalStarlarkIdentifier) (model_core.Message[*model_starlark_pb.Value, object.LocalReference], error) {
			require.Equal(t, "@@rules+//:defs.bzl%my_rule", identifier.String())
			return model_core.NewSimpleMessage[object.LocalReference](&model_starlark_pb.Value{
				Kind: &model_starlark_pb.Value_Rule{
					Rule: &model_starlark_pb.Rule{
						Kind: &model_starlark_pb.Rule_Definition_{
							Definition: &model_starlark_pb.Rule_Definition{
								Attrs: []*model_starlark_pb.NamedAttr{
									{
										Name: "_private",
										Attr: &model_starlark_pb.Attr{
											Type:    &model_starlark_pb.Attr_String_{String_: &model_starlark_pb.Attr_StringType{}},
											Default: value("private"),
										},
									},
									{
										Name: "computed",
										Attr: &model_starlark_pb.Attr{
											Type: &model_starlark_pb.Attr_String_{String_: &model_starlark_pb.Attr_StringType{}},
											Default: &model_starlark_pb.Value{
												Kind: &model_starlark_pb.Value_Function{Function: &model_starlark_pb.Function{NamePath: []*model_starlark_pb.Function_Name{{Name: "_computed"}}}},
											},
										},
									},
									{
										Name: "defaulted",
										Attr: &model_starlark_pb.Attr{
											Type:    &model_starlark_pb.Attr_String_{String_: &model_starlark_pb.Attr_StringType{}},
											Default: value("default"),
										},
									},
									{
										Name: "env",
										Attr: &model_starlark_pb.Attr{
											Type: &model_starlark_pb.Attr_StringDict{StringDict: &model_starlark_pb.Attr_StringDictType{}},
										},
									},
									{
										Name: "opts",
										Attr: &model_starlark_pb.Attr{
											Type: &model_starlark_pb.Attr_StringList{StringList: &model_starlark_pb.Attr_StringListType{}},
										},
									},
									{
										Name: "srcs",
										Attr: &model_starlark_pb.Attr{
											Type: &model_starlark_pb.Attr_StringList{StringList: &model_starlark_pb.Attr_StringListType{}},
										},
									},
								},
							},
						},
					},
				},
			}), nil
		}))
		if resolver != nil {
			thread.SetLocal(model_starlark.ExistingRulesResolverKey, resolver)
		}
		return thread
	}

	resolver := fakeExistingRulesResolver{
		"alias": {
			Kind: &model_starlark_pb.Target_Definition_Alias{
				Alias: &model_starlark_pb.Alias{
					Actual: noMatch(value(newLabel("@@main+//foo:actual"))),
				},
			},
		},
		"flag": {
			Kind: &model_starlark_pb.Target_Definition_LabelSetting{
				LabelSetting: &model_starlark_pb.LabelSetting{
					BuildSettingDefault: "@@main+//bar:default",
					Flag:                true,
				},
			},
		},
		"rule": {
			Kind: &model_starlark_pb.Target_Definition_RuleTarget{
				RuleTarget: &model_starlark_pb.RuleTarget{
					RuleIdentifier: "@@rules+//:defs.bzl%my_rule",
					PublicAttrValues: []*model_starlark_pb.RuleTarget_PublicAttrValue{
						{ValueParts: valueParts(noMatch(value(nil)))},
						{ValueParts: valueParts(noMatch(value(nil)))},
						{ValueParts: valueParts(noMatch(&model_starlark_pb.Value{
							Kind: &model_starlark_pb.Value_Dict{
								Dict: &model_starlark_pb.Dict{
									Entries: []*model_starlark_pb.Dict_Entry{{
										Level: &model_starlark_pb.Dict_Entry_Leaf_{
											Leaf: &model_starlark_pb.Dict_Entry_Leaf{
												Key:   value("FILES"),
												Value: value([]*model_starlark_pb.Value{value(newLabel("@@main+//foo:f"))}),
											},
										},
									}},
								},
							},
						}))},
						{ValueParts: valueParts(
							noMatch(value([]*model_starlark_pb.Value{value("-O0")})),
							noMatch(value(nil), &model_starlark_pb.Select_Condition{
								ConditionIdentifier: "@@main+//:opt",
								Value:               value([]*model_starlark_pb.Value{value("-O2"), value(newLabel("@@main+//foo:opt.txt"))}),
							}),
						)},
						{ValueParts: valueParts(noMatch(value([]*model_starlark_pb.Value{
							value(newLabel("@@main+//foo:a.c")),
							value(newLabel("@@main+//bar:b.c")),
							value(newLabel("@@other+//baz:c.c")),
						})))},
					},
					ExecCompatibleWith: []string{"@@main+//foo:exec"},
					Tags:               []string{"manual"},
					InheritableAttrs: &model_starlark_pb.InheritableAttrs{
						Deprecation: "Don't use this",
						Testonly:    true,
					},
				},
			},
		},
		"rule_test": {
			Kind: &model_starlark_pb.Target_Definition_RuleTarget{
				RuleTarget: &model_starlark_pb.RuleTarget{
					RuleIdentifier: "@@rules+//:defs.bzl%my_rule",
					PublicAttrValues: []*model_starlark_pb.RuleTarget_PublicAttrValue{
						{ValueParts: valueParts(noMatch(value("explicit")))},
						{ValueParts: valueParts(noMatch(value("explicit")))},
						{ValueParts: valueParts(noMatch(value(nil)))},
						{ValueParts: valueParts(noMatch(value(nil)))},
						{ValueParts: valueParts(noMatch(value(nil)))},
					},
					TargetCompatibleWith: valueParts(noMatch(value([]*model_starlark_pb.Value{value(newLabel("@@main+//:linux"))}))),
					BuildSettingDefault:  value("setting"),
					Test: &model_starlark_pb.RuleTarget_Test{
						Size:       model_starlark_pb.TestSize_LARGE,
						Timeout:    model_starlark_pb.TestTimeout_LONG,
						Flaky:      true,
						ShardCount: valueParts(noMatch(&model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Int{Int: model_starlark.EncodeInt(big.NewInt(3))}})),
						Args:       valueParts(noMatch(value([]*model_starlark_pb.Value{value("--verbose")}))),
					},
				},
			},
		},
		"setting": {
			Kind: &model_starlark_pb.Target_Definition_LabelSetting{
				LabelSetting: &model_starlark_pb.LabelSetting{},
			},
		},
		"source.txt": {
			Kind: &model_starlark_pb.Target_Definition_SourceFileTarget{
				SourceFileTarget: &model_starlark_pb.SourceFileTarget{},
			},
		},
		"suite": {
			Kind: &model_starlark_pb.Target_Definition_TestSuite{
				TestSuite: &model_starlark_pb.TestSuite{
					Tests: []*model_starlark_pb.TestSuite_Test{{
						Level: &model_starlark_pb.TestSuite_Test_Leaf{Leaf: "@@main+//foo:test"},
					}},
					PositiveTags: []string{"fast"},
					NegativeTags: []string{"flaky"},
					Sizes:        []model_starlark_pb.TestSize{model_starlark_pb.TestSize_SMALL},
					Manual:       true,
				},
			},
		},
	}

	t.Run("ExistingRule", func(t *testing.T) {
		thread := newThread(t, resolver)
		for name, expected := range map[string]string{
			"alias":      `{"name": "alias", "kind": "alias", "actual": ":actual"}`,
			"flag":       `{"name": "flag", "kind": "label_flag", "build_setting_default": "//bar:default"}`,
			"rule":       `{"name": "rule", "kind": "my_rule", "defaulted": "default", "env": {"FILES": (":f",)}, "opts": <select>, "srcs": (":a.c", "//bar:b.c", "@@other+//baz:c.c"), "deprecation": "Don't use this", "exec_compatible_with": (":exec",), "tags": ("manual",), "target_compatible_with": (), "testonly": True}`,
			"rule_test":  `{"name": "rule_test", "kind": "my_rule", "computed": "explicit", "defaulted": "explicit", "env": None, "opts": None, "srcs": None, "deprecation": None, "exec_compatible_with": (), "tags": (), "target_compatible_with": ("//:linux",), "testonly": False, "build_setting_default": "setting", "args": ("--verbose",), "flaky": True, "local": False, "shard_count": 3, "size": "large", "timeout": "long"}`,
			"setting":    `{"name": "setting", "kind": "label_setting", "build_setting_default": None}`,
			"source.txt": `None`,
			"suite":      `{"name": "suite", "kind": "test_suite", "tags": ("fast", "-flaky", "small", "manual"), "tests": (":test",)}`,
			"missing":    `None`,
		} {
			v, err := eval.Call(thread, nativeExistingRule, eval.Args{Positional: []eval.Value{eval.String(name)}})
			require.NoError(t, err, name)
			require.Equal(t, expected, util.Must(eval.Repr(thread, v)), name)
			if d, ok := v.(eval.Dict); ok {
				require.Error(t, d.SetKey(thread, eval.String("x"), eval.None), name)
			}
		}
	})

	t.Run("SelectContents", func(t *testing.T) {
		// Contents of select() expressions should be converted
		// as well.
		thread := newThread(t, resolver)
		existingRule, err := eval.Call(thread, nativeExistingRule, eval.Args{Positional: []eval.Value{eval.String("rule")}})
		require.NoError(t, err)
		opts, _, err := existingRule.(eval.Dict).Get(thread, eval.String("opts"))
		require.NoError(t, err)
		encodedOpts, err := model_starlark.EncodeValue[object.LocalReference, model_core.CreatedObjectTree](
			thread,
			opts,
			nil,
			/* currentIdentifier = */ nil,
			&model_starlark.ValueEncodingOptions[object.LocalReference, model_core.CreatedObjectTree]{
				Context:                t.Context(),
				ObjectEncoder:          model_encoding.NewChainedDeterministicBinaryEncoder(nil),
				ObjectReferenceFormat:  util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1)),
				ObjectMinimumSizeBytes: 32 * 1024,
				ObjectHorizonSizeBytes: 8 * 32 * 1024,
			},
		)
		require.NoError(t, err)
		tuple := func(elements ...*model_starlark_pb.Value) *model_starlark_pb.Value {
			return &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Tuple{Tuple: &model_starlark_pb.Tuple{Elements: elements}}}
		}
		testutil.RequireEqualProto(t, &model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_Select{
				Select: &model_starlark_pb.Select{
					Groups: valueParts(
						noMatch(tuple(value("-O0"))),
						noMatch(value(nil), &model_starlark_pb.Select_Condition{
							ConditionIdentifier: "@@main+//:opt",
							Value:               tuple(value("-O2"), value(":opt.txt")),
						}),
					),
					ConcatenationOperator: model_starlark_pb.Select_PLUS,
				},
			},
		}, encodedOpts.Message)
	})

	t.Run("ExistingRules", func(t *testing.T) {
		// Only rule targets should be returned.
		thread := newThread(t, resolver)
		v, err := eval.Call(thread, nativeExistingRules, eval.Args{})
		require.NoError(t, err)
		existingRules := v.(eval.Dict)
		var names []string
		for key := range existingRules.Iterate(thread, nil) {
			names = append(names, string(key.(eval.String)))
		}
		require.Equal(t, []string{"alias", "flag", "rule", "rule_test", "setting", "suite"}, names)

		// The results should be immutable.
		require.Error(t, existingRules.SetKey(thread, eval.String("x"), eval.None))
		alias, _, err := existingRules.Get(thread, eval.String("alias"))
		require.NoError(t, err)
		require.Error(t, alias.(eval.Dict).SetKey(thread, eval.String("x"), eval.None))
	})

	t.Run("OmitMacroInternals", func(t *testing.T) {
		thread := newThread(t, resolver)
		_, err := eval.Call(thread, nativeExistingRules, eval.Args{Named: []eval.NamedArg{{Name: "omit_macro_internals", Value: eval.True}}})
		require.EqualError(t, err, "omitting targets declared by symbolic macros is not supported")
	})

	t.Run("OutsideFinalizer", func(t *testing.T) {
		// Existing rules can only be obtained from within
		// finalizers.
		thread := newThread(t, nil)
		_, err := eval.Call(thread, nativeExistingRule, eval.Args{Positional: []eval.Value{eval.String("alias")}})
		require.EqualError(t, err, "existing rules can only be obtained from within finalizers")
		_, err = eval.Call(thread, nativeExistingRules, eval.Args{})
		require.EqualError(t, err, "existing rules can only be obtained from within finalizers")
	})
}
