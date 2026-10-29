package render_test

import (
	"math/big"
	"testing"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/render"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"
)

func TestStarlarkRendererTarget(t *testing.T) {
	var sr render.StarlarkRenderer
	renderTarget := func(targetLabel string, definition *model_starlark_pb.Target_Definition, publicAttrNames []string, width int) string {
		return render.PlainText(
			render.Layout(
				sr.Target(
					util.Must(label.NewCanonicalLabel(targetLabel)),
					model_core.NewSimpleMessage[object.LocalReference](definition),
					publicAttrNames,
				),
				render.LayoutOptions{Width: width, IndentWidth: 4},
			),
			"    ",
		)
	}
	valueGroup := func(v *model_starlark_pb.Value) *model_starlark_pb.Select_Group {
		return &model_starlark_pb.Select_Group{
			NoMatch: &model_starlark_pb.Select_Group_NoMatchValue{NoMatchValue: v},
		}
	}
	str := func(s string) *model_starlark_pb.Value {
		return &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Str{Str: s}}
	}
	labelList := func(labels ...string) *model_starlark_pb.Value {
		elements := make([]*model_starlark_pb.List_Element, 0, len(labels))
		for _, l := range labels {
			elements = append(elements, &model_starlark_pb.List_Element{
				Level: &model_starlark_pb.List_Element_Leaf{
					Leaf: &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Label{Label: l}},
				},
			})
		}
		return &model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_List{List: &model_starlark_pb.List{Elements: elements}},
		}
	}
	none := &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_None{}}
	ruleTarget := &model_starlark_pb.Target_Definition{
		Kind: &model_starlark_pb.Target_Definition_RuleTarget{
			RuleTarget: &model_starlark_pb.RuleTarget{
				RuleIdentifier: "@@rules_cc+//cc:defs.bzl%cc_library",
				PublicAttrValues: []*model_starlark_pb.RuleTarget_PublicAttrValue{
					{ValueParts: []*model_starlark_pb.Select_Group{valueGroup(labelList("@@main+//baz"))}},
					{ValueParts: []*model_starlark_pb.Select_Group{valueGroup(none)}},
					{ValueParts: []*model_starlark_pb.Select_Group{
						valueGroup(labelList("@@main+//foo:a.cc")),
						{
							Conditions: []*model_starlark_pb.Select_Condition{{
								ConditionIdentifier: "@@platforms//os:linux",
								Value:               labelList("@@main+//foo:linux.cc"),
							}},
							NoMatch: &model_starlark_pb.Select_Group_NoMatchValue{
								NoMatchValue: labelList(),
							},
						},
					}},
					{ValueParts: []*model_starlark_pb.Select_Group{valueGroup(str("a \"b\""))}},
				},
				Tags: []string{"manual"},
				// Empty lists of constraints should
				// not be displayed.
				TargetCompatibleWith: []*model_starlark_pb.Select_Group{valueGroup(labelList())},
			},
		},
	}

	t.Run("RuleTarget", func(t *testing.T) {
		// Attributes that are None use their default value,
		// and should be omitted. Each argument should be placed
		// on a separate line. Other than the name, arguments
		// should be sorted alphabetically.
		require.Equal(t, `cc_library(
    name = "bar",
    copts = "a \"b\"",
    deps = [Label("@@main+//baz")],
    srcs = [Label("@@main+//foo:a.cc")] + select({"@@platforms//os:linux": [Label("@@main+//foo:linux.cc")], "//conditions:default": []}),
    tags = ["manual"],
)`, renderTarget("@@main+//foo:bar", ruleTarget, []string{"deps", "hdrs", "srcs", "copts"}, 0))
	})

	t.Run("RuleTargetNarrow", func(t *testing.T) {
		// Values that don't fit on a single line should be
		// spread across multiple lines.
		require.Equal(t, `cc_library(
    name = "bar",
    copts = "a \"b\"",
    deps = [Label("@@main+//baz")],
    srcs = (
        [Label("@@main+//foo:a.cc")]
        + select(
            {
                "@@platforms//os:linux": [
                    Label("@@main+//foo:linux.cc"),
                ],
                "//conditions:default": [],
            },
        )
    ),
    tags = ["manual"],
)`, renderTarget("@@main+//foo:bar", ruleTarget, []string{"deps", "hdrs", "srcs", "copts"}, 60))
	})

	t.Run("TargetCompatibleWith", func(t *testing.T) {
		// Constraints should be displayed if they are not
		// empty, or are wrapped in a select() expression.
		for _, testCase := range []struct {
			groups []*model_starlark_pb.Select_Group
			value  string
		}{
			{
				[]*model_starlark_pb.Select_Group{valueGroup(labelList("@@platforms//os:linux"))},
				`[Label("@@platforms//os:linux")]`,
			},
			{
				[]*model_starlark_pb.Select_Group{{
					Conditions: []*model_starlark_pb.Select_Condition{{
						ConditionIdentifier: "@@platforms//os:linux",
						Value:               labelList(),
					}},
					NoMatch: &model_starlark_pb.Select_Group_NoMatchValue{
						NoMatchValue: labelList("@@platforms//:incompatible"),
					},
				}},
				`select({"@@platforms//os:linux": [], "//conditions:default": [Label("@@platforms//:incompatible")]})`,
			},
		} {
			require.Equal(t, "cc_library(\n    name = \"bar\",\n    target_compatible_with = "+testCase.value+",\n)", renderTarget("@@main+//foo:bar", &model_starlark_pb.Target_Definition{
				Kind: &model_starlark_pb.Target_Definition_RuleTarget{
					RuleTarget: &model_starlark_pb.RuleTarget{
						RuleIdentifier:       "@@rules_cc+//cc:defs.bzl%cc_library",
						TargetCompatibleWith: testCase.groups,
					},
				},
			}, nil, 0))
		}
	})

	t.Run("Test", func(t *testing.T) {
		// Attributes that are common to all tests should only
		// be displayed if their values differ from the default.
		intValue := func(i int64) *model_starlark_pb.Value {
			return &model_starlark_pb.Value{
				Kind: &model_starlark_pb.Value_Int{Int: model_starlark.EncodeInt(big.NewInt(i))},
			}
		}
		strList := func(values ...string) *model_starlark_pb.Value {
			elements := make([]*model_starlark_pb.List_Element, 0, len(values))
			for _, v := range values {
				elements = append(elements, &model_starlark_pb.List_Element{
					Level: &model_starlark_pb.List_Element_Leaf{Leaf: str(v)},
				})
			}
			return &model_starlark_pb.Value{
				Kind: &model_starlark_pb.Value_List{List: &model_starlark_pb.List{Elements: elements}},
			}
		}
		renderTest := func(test *model_starlark_pb.RuleTarget_Test) string {
			return renderTarget("@@main+//foo:bar", &model_starlark_pb.Target_Definition{
				Kind: &model_starlark_pb.Target_Definition_RuleTarget{
					RuleTarget: &model_starlark_pb.RuleTarget{
						RuleIdentifier: "@@rules_go+//go:def.bzl%go_test",
						Test:           test,
					},
				},
			}, nil, 0)
		}

		require.Equal(t, "go_test(\n    name = \"bar\",\n)", renderTest(&model_starlark_pb.RuleTarget_Test{
			Size:       model_starlark_pb.TestSize_MEDIUM,
			Timeout:    model_starlark_pb.TestTimeout_MODERATE,
			ShardCount: []*model_starlark_pb.Select_Group{valueGroup(intValue(1))},
			Args:       []*model_starlark_pb.Select_Group{valueGroup(strList())},
		}))

		// The timeout should only be displayed if it differs
		// from the one corresponding to the size of the test.
		require.Equal(t, `go_test(
    name = "bar",
    args = ["--foo"],
    flaky = True,
    size = "small",
)`, renderTest(&model_starlark_pb.RuleTarget_Test{
			Size:       model_starlark_pb.TestSize_SMALL,
			Timeout:    model_starlark_pb.TestTimeout_SHORT,
			Flaky:      true,
			ShardCount: []*model_starlark_pb.Select_Group{valueGroup(intValue(1))},
			Args:       []*model_starlark_pb.Select_Group{valueGroup(strList("--foo"))},
		}))

		require.Equal(t, `go_test(
    name = "bar",
    local = True,
    shard_count = 3,
    size = "large",
    timeout = "short",
)`, renderTest(&model_starlark_pb.RuleTarget_Test{
			Size:       model_starlark_pb.TestSize_LARGE,
			Timeout:    model_starlark_pb.TestTimeout_SHORT,
			Local:      true,
			ShardCount: []*model_starlark_pb.Select_Group{valueGroup(intValue(3))},
			Args:       []*model_starlark_pb.Select_Group{valueGroup(strList())},
		}))
	})

	t.Run("MismatchingAttrNames", func(t *testing.T) {
		require.Equal(t, `cc_library(
    name = "bar",
    public_attrs = [ Rule target has 4 public attr values, while 0 public attr names were provided ],
)`, renderTarget("@@main+//foo:bar", ruleTarget, nil, 0))
	})

	t.Run("Alias", func(t *testing.T) {
		require.Equal(t, `alias(
    name = "bar",
    actual = Label("@@main+//foo:baz"),
)`, renderTarget("@@main+//foo:bar", &model_starlark_pb.Target_Definition{
			Kind: &model_starlark_pb.Target_Definition_Alias{
				Alias: &model_starlark_pb.Alias{
					Actual: valueGroup(&model_starlark_pb.Value{
						Kind: &model_starlark_pb.Value_Label{Label: "@@main+//foo:baz"},
					}),
				},
			},
		}, nil, 0))
	})

	t.Run("SourceFile", func(t *testing.T) {
		// Targets that are not declared by calling a function
		// should be rendered as comments.
		require.Equal(t, "# @@main+//foo:bar.txt (source file)", renderTarget("@@main+//foo:bar.txt", &model_starlark_pb.Target_Definition{
			Kind: &model_starlark_pb.Target_Definition_SourceFileTarget{
				SourceFileTarget: &model_starlark_pb.SourceFileTarget{},
			},
		}, nil, 0))
	})
}
