package analysis

import (
	"testing"

	model_core "bonanza.build/pkg/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/storage/object"

	"github.com/stretchr/testify/require"
)

func TestCheckProvides(t *testing.T) {
	const (
		ccInfo   = "@@rules_cc+//cc:defs.bzl%CcInfo"
		javaInfo = "@@rules_java+//java:defs.bzl%JavaInfo"
		pyInfo   = "@@rules_python+//python:defs.bzl%PyInfo"
	)
	providersSeen := map[string]struct{}{
		ccInfo:   {},
		javaInfo: {},
	}

	t.Run("NoneAdvertised", func(t *testing.T) {
		require.NoError(t, checkProvides(providersSeen, nil))
	})

	t.Run("AllReturned", func(t *testing.T) {
		// Returning providers that are not advertised is
		// permitted.
		require.NoError(t, checkProvides(providersSeen, []string{ccInfo}))
		require.NoError(t, checkProvides(providersSeen, []string{ccInfo, javaInfo}))
	})

	t.Run("Missing", func(t *testing.T) {
		require.EqualError(
			t,
			checkProvides(providersSeen, []string{ccInfo, pyInfo}),
			"implementation function did not return providers @@rules_python+//python:defs.bzl%PyInfo, even though they are listed in provides",
		)
	})
}

func TestRuleTargetAnalysisCheckAttrsNonEmpty(t *testing.T) {
	stringListAttr := func(name string, allowEmpty bool) *model_starlark_pb.NamedAttr {
		return model_starlark_pb.NamedAttr_builder{
			Name: name,
			Attr: model_starlark_pb.Attr_builder{
				StringList: model_starlark_pb.Attr_StringListType_builder{
					ListOptions: model_starlark_pb.Attr_CompositeOptions_builder{AllowEmpty: allowEmpty}.Build(),
				}.Build(),
			}.Build(),
		}.Build()
	}
	newAnalysis := func(attrValues map[string]any) *ruleTargetAnalysis[object.LocalReference, model_core.CreatedObjectTree] {
		return &ruleTargetAnalysis[object.LocalReference, model_core.CreatedObjectTree]{
			thread: eval.NewThread(t.Context(), nil),
			ruleDefinition: model_core.NewSimpleMessage[object.LocalReference](model_starlark_pb.Rule_Definition_builder{
				Attrs: []*model_starlark_pb.NamedAttr{
					stringListAttr("allowed", true),
					stringListAttr("disallowed", false),
					model_starlark_pb.NamedAttr_builder{
						Name: "string_dict",
						Attr: model_starlark_pb.Attr_builder{
							StringDict: &model_starlark_pb.Attr_StringDictType{},
						}.Build(),
					}.Build(),
					model_starlark_pb.NamedAttr_builder{
						Name: "string",
						Attr: model_starlark_pb.Attr_builder{
							String: &model_starlark_pb.Attr_StringType{},
						}.Build(),
					}.Build(),
				},
			}.Build()),
			attrValues: attrValues,
		}
	}
	nonEmptyList := eval.NewList([]eval.Value{eval.String("x")})
	nonEmptyDict := eval.NewDict()
	require.NoError(t, nonEmptyDict.SetKey(eval.NewThread(t.Context(), nil), eval.String("k"), eval.String("v")))

	t.Run("NonEmpty", func(t *testing.T) {
		// Attrs that allow empty values may be empty, and
		// non-composite attrs are not checked.
		require.NoError(t, newAnalysis(map[string]any{
			"allowed":     eval.NewList(nil),
			"disallowed":  nonEmptyList,
			"string_dict": nonEmptyDict,
			"string":      eval.String(""),
		}).checkAttrsNonEmpty())
	})

	t.Run("EmptyList", func(t *testing.T) {
		require.EqualError(t, newAnalysis(map[string]any{
			"allowed":     eval.NewList(nil),
			"disallowed":  eval.NewList(nil),
			"string_dict": nonEmptyDict,
			"string":      eval.String(""),
		}).checkAttrsNonEmpty(), "attr \"disallowed\" must be non empty")
	})

	t.Run("EmptyDict", func(t *testing.T) {
		require.EqualError(t, newAnalysis(map[string]any{
			"allowed":     eval.NewList(nil),
			"disallowed":  nonEmptyList,
			"string_dict": eval.NewDict(),
			"string":      eval.String(""),
		}).checkAttrsNonEmpty(), "attr \"string_dict\" must be non empty")
	})

	t.Run("NotComputed", func(t *testing.T) {
		// Values of attrs that have not been computed due to
		// missing dependencies should be ignored.
		require.NoError(t, newAnalysis(map[string]any{}).checkAttrsNonEmpty())
	})
}
