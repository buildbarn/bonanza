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

	"google.golang.org/protobuf/proto"
	xpb "google.golang.org/protobuf/types/known/emptypb"
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
		return model_starlark_pb.Select_Group_builder{
			NoMatchValue: proto.ValueOrDefault(v),
		}.Build()
	}
	str := func(s string) *model_starlark_pb.Value {
		return newStringValue(s)
	}
	labelList := func(labels ...string) *model_starlark_pb.Value {
		elements := make([]*model_starlark_pb.List_Element, 0, len(labels))
		for _, l := range labels {
			elements = append(elements, model_starlark_pb.List_Element_builder{
				Leaf: model_starlark_pb.Value_builder{Label: proto.String(l)}.Build(),
			}.Build())
		}
		return model_starlark_pb.Value_builder{
			List: model_starlark_pb.List_builder{Elements: elements}.Build(),
		}.Build()
	}
	none := model_starlark_pb.Value_builder{None: &xpb.Empty{}}.Build()
	ruleTarget := model_starlark_pb.Target_Definition_builder{
		RuleTarget: model_starlark_pb.RuleTarget_builder{
			RuleIdentifier: "@@rules_cc+//cc:defs.bzl%cc_library",
			PublicAttrValues: []*model_starlark_pb.RuleTarget_PublicAttrValue{
				model_starlark_pb.RuleTarget_PublicAttrValue_builder{ValueParts: []*model_starlark_pb.Select_Group{valueGroup(labelList("@@main+//baz"))}}.Build(),
				model_starlark_pb.RuleTarget_PublicAttrValue_builder{ValueParts: []*model_starlark_pb.Select_Group{valueGroup(none)}}.Build(),
				model_starlark_pb.RuleTarget_PublicAttrValue_builder{ValueParts: []*model_starlark_pb.Select_Group{
					valueGroup(labelList("@@main+//foo:a.cc")),
					model_starlark_pb.Select_Group_builder{
						Conditions: []*model_starlark_pb.Select_Condition{model_starlark_pb.Select_Condition_builder{
							ConditionIdentifier: "@@platforms//os:linux",
							Value:               labelList("@@main+//foo:linux.cc"),
						}.Build()},
						NoMatchValue: proto.ValueOrDefault(labelList()),
					}.Build(),
				}}.Build(),
				model_starlark_pb.RuleTarget_PublicAttrValue_builder{ValueParts: []*model_starlark_pb.Select_Group{valueGroup(str("a \"b\""))}}.Build(),
			},
			Tags: []string{"manual"},
			// Empty lists of constraints should
			// not be displayed.
			TargetCompatibleWith: []*model_starlark_pb.Select_Group{valueGroup(labelList())},
		}.Build(),
	}.Build()

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
				[]*model_starlark_pb.Select_Group{model_starlark_pb.Select_Group_builder{
					Conditions: []*model_starlark_pb.Select_Condition{model_starlark_pb.Select_Condition_builder{
						ConditionIdentifier: "@@platforms//os:linux",
						Value:               labelList(),
					}.Build()},
					NoMatchValue: proto.ValueOrDefault(labelList("@@platforms//:incompatible")),
				}.Build()},
				`select({"@@platforms//os:linux": [], "//conditions:default": [Label("@@platforms//:incompatible")]})`,
			},
		} {
			require.Equal(t, "cc_library(\n    name = \"bar\",\n    target_compatible_with = "+testCase.value+",\n)", renderTarget("@@main+//foo:bar", model_starlark_pb.Target_Definition_builder{
				RuleTarget: model_starlark_pb.RuleTarget_builder{
					RuleIdentifier:       "@@rules_cc+//cc:defs.bzl%cc_library",
					TargetCompatibleWith: testCase.groups,
				}.Build(),
			}.Build(), nil, 0))
		}
	})

	t.Run("Test", func(t *testing.T) {
		// Attributes that are common to all tests should only
		// be displayed if their values differ from the default.
		intValue := func(i int64) *model_starlark_pb.Value {
			return model_starlark_pb.Value_builder{
				Int: proto.ValueOrDefaultBytes(model_starlark.EncodeInt(big.NewInt(i))),
			}.Build()
		}
		strList := func(values ...string) *model_starlark_pb.Value {
			elements := make([]*model_starlark_pb.List_Element, 0, len(values))
			for _, v := range values {
				elements = append(elements, model_starlark_pb.List_Element_builder{
					Leaf: proto.ValueOrDefault(str(v)),
				}.Build())
			}
			return model_starlark_pb.Value_builder{
				List: model_starlark_pb.List_builder{Elements: elements}.Build(),
			}.Build()
		}
		renderTest := func(test *model_starlark_pb.RuleTarget_Test) string {
			return renderTarget("@@main+//foo:bar", model_starlark_pb.Target_Definition_builder{
				RuleTarget: model_starlark_pb.RuleTarget_builder{
					RuleIdentifier: "@@rules_go+//go:def.bzl%go_test",
					Test:           test,
				}.Build(),
			}.Build(), nil, 0)
		}

		require.Equal(t, "go_test(\n    name = \"bar\",\n)", renderTest(model_starlark_pb.RuleTarget_Test_builder{
			Size:       model_starlark_pb.TestSize_MEDIUM,
			Timeout:    model_starlark_pb.TestTimeout_MODERATE,
			ShardCount: []*model_starlark_pb.Select_Group{valueGroup(intValue(1))},
			Args:       []*model_starlark_pb.Select_Group{valueGroup(strList())},
		}.Build()))

		// The timeout should only be displayed if it differs
		// from the one corresponding to the size of the test.
		require.Equal(t, `go_test(
    name = "bar",
    args = ["--foo"],
    flaky = True,
    size = "small",
)`, renderTest(model_starlark_pb.RuleTarget_Test_builder{
			Size:       model_starlark_pb.TestSize_SMALL,
			Timeout:    model_starlark_pb.TestTimeout_SHORT,
			Flaky:      true,
			ShardCount: []*model_starlark_pb.Select_Group{valueGroup(intValue(1))},
			Args:       []*model_starlark_pb.Select_Group{valueGroup(strList("--foo"))},
		}.Build()))

		require.Equal(t, `go_test(
    name = "bar",
    local = True,
    shard_count = 3,
    size = "large",
    timeout = "short",
)`, renderTest(model_starlark_pb.RuleTarget_Test_builder{
			Size:       model_starlark_pb.TestSize_LARGE,
			Timeout:    model_starlark_pb.TestTimeout_SHORT,
			Local:      true,
			ShardCount: []*model_starlark_pb.Select_Group{valueGroup(intValue(3))},
			Args:       []*model_starlark_pb.Select_Group{valueGroup(strList())},
		}.Build()))
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
)`, renderTarget("@@main+//foo:bar", model_starlark_pb.Target_Definition_builder{
			Alias: model_starlark_pb.Alias_builder{
				Actual: valueGroup(model_starlark_pb.Value_builder{
					Label: proto.String("@@main+//foo:baz"),
				}.Build()),
			}.Build(),
		}.Build(), nil, 0))
	})

	t.Run("TestSuite", func(t *testing.T) {
		// Tags should be reconstructed from the positive and
		// negative tags, sizes and the "manual" tag.
		require.Equal(t, `test_suite(
    name = "bar",
    tags = ["+manual", "-enormous", "-flaky", "-medium", "manual"],
    tests = ["@@main+//foo:a", "@@main+//foo:b"],
)`, renderTarget("@@main+//foo:bar", model_starlark_pb.Target_Definition_builder{
			TestSuite: model_starlark_pb.TestSuite_builder{
				Tests: []*model_starlark_pb.TestSuite_Test{
					model_starlark_pb.TestSuite_Test_builder{Leaf: proto.String("@@main+//foo:a")}.Build(),
					model_starlark_pb.TestSuite_Test_builder{Leaf: proto.String("@@main+//foo:b")}.Build(),
				},
				PositiveTags: []string{"manual"},
				NegativeTags: []string{"flaky"},
				Sizes: []model_starlark_pb.TestSize{
					model_starlark_pb.TestSize_SMALL,
					model_starlark_pb.TestSize_LARGE,
				},
				Manual: true,
			}.Build(),
		}.Build(), nil, 0))

		// Test suites without any tags or tests should only
		// have a name.
		require.Equal(t, `test_suite(
    name = "bar",
)`, renderTarget("@@main+//foo:bar", model_starlark_pb.Target_Definition_builder{
			TestSuite: model_starlark_pb.TestSuite_builder{
				Sizes: []model_starlark_pb.TestSize{
					model_starlark_pb.TestSize_SMALL,
					model_starlark_pb.TestSize_MEDIUM,
					model_starlark_pb.TestSize_LARGE,
					model_starlark_pb.TestSize_ENORMOUS,
				},
			}.Build(),
		}.Build(), nil, 0))
	})

	t.Run("SourceFile", func(t *testing.T) {
		// Targets that are not declared by calling a function
		// should be rendered as comments.
		require.Equal(t, "# @@main+//foo:bar.txt (source file)", renderTarget("@@main+//foo:bar.txt", model_starlark_pb.Target_Definition_builder{
			SourceFileTarget: &model_starlark_pb.SourceFileTarget{},
		}.Build(), nil, 0))
	})
}
