package starlark_test

import (
	"fmt"
	"testing"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/storage/object"

	"bonanza.build/pkg/starlark/eval"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"
)

func TestAttrConfigurable(t *testing.T) {
	execGlobals := func(code string) (map[string]eval.Value, error) {
		return execBzlFile(
			newTestThread(t),
			util.Must(label.NewCanonicalLabel("@@rules+//:defs.bzl")),
			"def _impl(ctx):\n    pass\n"+code,
			nil,
		)
	}
	exec := func(code string) error {
		_, err := execGlobals(code)
		return err
	}

	// Only attributes of symbolic macros may be marked as not
	// being configurable. Explicitly marking attributes as being
	// configurable is permitted everywhere.
	for _, call := range []string{
		"aspect(implementation = _impl, attrs = %s)",
		"repository_rule(implementation = _impl, attrs = %s)",
		"rule(implementation = _impl, attrs = %s)",
		"tag_class(attrs = %s)",
	} {
		require.NoError(t, exec(fmt.Sprintf(call, `{"a": attr.string(configurable = True), "b": attr.output()}`)))
		require.ErrorContains(
			t,
			exec(fmt.Sprintf(call, `{"a": attr.string(), "b": attr.string(configurable = False)}`)),
			"attr \"b\": configurable may only be set to False for attributes of symbolic macros",
		)
	}
	require.NoError(t, exec(`macro(implementation = _impl, attrs = {"a": attr.string(), "b": attr.string(configurable = False)})`))

	t.Run("Output", func(t *testing.T) {
		// Outputs are never configurable.
		globals, err := execGlobals("OUTPUT = attr.output()\nOUTPUT_LIST = attr.output_list()\n")
		require.NoError(t, err)
		for _, name := range []string{"OUTPUT", "OUTPUT_LIST"} {
			encodedAttr, err := globals[name].(model_starlark.EncodableValue[object.LocalReference, model_core.CreatedObjectTree]).EncodeValue(newTestThread(t), nil, nil, &model_starlark.ValueEncodingOptions[object.LocalReference, model_core.CreatedObjectTree]{})
			require.NoError(t, err)
			require.False(t, encodedAttr.Message.GetAttr().Configurable)
		}
	})

	t.Run("Encode", func(t *testing.T) {
		// Whether an attribute is configurable should be
		// preserved when encoding and decoding it.
		for _, configurable := range []bool{true, false} {
			encodedAttr, err := model_starlark.NewAttr[object.LocalReference, model_core.CreatedObjectTree](
				model_starlark.NewStringAttrType[object.LocalReference, model_core.CreatedObjectTree](nil),
				/* defaultValue = */ nil,
				configurable,
			).EncodeValue(newTestThread(t), nil, nil, &model_starlark.ValueEncodingOptions[object.LocalReference, model_core.CreatedObjectTree]{})
			require.NoError(t, err)
			require.Equal(t, configurable, encodedAttr.Message.GetAttr().Configurable)

			thread := newTestThread(t)
			thread.SetLocal(model_starlark.ValueDecodingOptionsKey, &model_starlark.ValueDecodingOptions[object.LocalReference]{})
			decodedAttr, err := model_starlark.DecodeValue[object.LocalReference, model_core.CreatedObjectTree](
				thread,
				model_core.NewSimpleMessage[object.LocalReference](encodedAttr.Message),
				nil,
			)
			require.NoError(t, err)
			reencodedAttr, err := decodedAttr.(model_starlark.EncodableValue[object.LocalReference, model_core.CreatedObjectTree]).EncodeValue(thread, nil, nil, &model_starlark.ValueEncodingOptions[object.LocalReference, model_core.CreatedObjectTree]{})
			require.NoError(t, err)
			testutil.RequireEqualProto(t, encodedAttr.Message, reencodedAttr.Message)
		}
	})
}

func TestAttrProviders(t *testing.T) {
	encodeProviders := func(t *testing.T, providers string) []*model_starlark_pb.ProviderSet {
		globals, err := execBzlFile(
			newTestThread(t),
			util.Must(label.NewCanonicalLabel("@@rules+//:defs.bzl")),
			"A = provider()\nB = provider()\nC = provider()\nATTR = attr.label_list(providers = "+providers+")\n",
			nil,
		)
		require.NoError(t, err)
		encodedAttr, err := globals["ATTR"].(model_starlark.EncodableValue[object.LocalReference, model_core.CreatedObjectTree]).EncodeValue(newTestThread(t), nil, nil, &model_starlark.ValueEncodingOptions[object.LocalReference, model_core.CreatedObjectTree]{})
		require.NoError(t, err)
		return encodedAttr.Message.GetAttr().GetLabelList().GetListValueOptions().GetProviders()
	}
	newProviderSet := func(providerIdentifiers ...string) *model_starlark_pb.ProviderSet {
		return &model_starlark_pb.ProviderSet{ProviderIdentifiers: providerIdentifiers}
	}

	t.Run("Default", func(t *testing.T) {
		// Not specifying any providers yields a single empty
		// set, which is satisfied by any target.
		providerSets := encodeProviders(t, "[]")
		require.Len(t, providerSets, 1)
		testutil.RequireEqualProto(t, newProviderSet(), providerSets[0])
	})

	t.Run("FlatList", func(t *testing.T) {
		// A flat list of providers corresponds to a single set.
		// Providers in a set are sorted and deduplicated.
		testutil.RequireEqualProto(
			t,
			newProviderSet("@@rules+//:defs.bzl%A", "@@rules+//:defs.bzl%B"),
			encodeProviders(t, "[B, A, B]")[0],
		)
	})

	t.Run("NestedLists", func(t *testing.T) {
		// Sets are sorted and deduplicated as well.
		providerSets := encodeProviders(t, "[[C], [B, A], [A, B]]")
		require.Len(t, providerSets, 2)
		testutil.RequireEqualProto(t, newProviderSet("@@rules+//:defs.bzl%A", "@@rules+//:defs.bzl%B"), providerSets[0])
		testutil.RequireEqualProto(t, newProviderSet("@@rules+//:defs.bzl%C"), providerSets[1])
	})

	t.Run("Supersets", func(t *testing.T) {
		// Sets that are a superset of another set are
		// redundant, and should be removed.
		providerSets := encodeProviders(t, "[[A, B, C], [B], [A, C], [C, B]]")
		require.Len(t, providerSets, 2)
		testutil.RequireEqualProto(t, newProviderSet("@@rules+//:defs.bzl%A", "@@rules+//:defs.bzl%C"), providerSets[0])
		testutil.RequireEqualProto(t, newProviderSet("@@rules+//:defs.bzl%B"), providerSets[1])
	})

	t.Run("EmptySet", func(t *testing.T) {
		// An empty set is satisfied by any target, meaning that
		// it subsumes all other sets.
		providerSets := encodeProviders(t, "[[A], []]")
		require.Len(t, providerSets, 1)
		testutil.RequireEqualProto(t, newProviderSet(), providerSets[0])
	})

	t.Run("UnnamedProvider", func(t *testing.T) {
		// Providers are named when they are assigned to a
		// global. Providers that are not assigned to a global
		// can't be referenced by identifier.
		_, err := execBzlFile(
			newTestThread(t),
			util.Must(label.NewCanonicalLabel("@@rules+//:defs.bzl")),
			"ATTR = attr.label_list(providers = [provider()])\n",
			nil,
		)
		require.ErrorContains(t, err, "attr.label_list: for parameter providers: provider does not have a name")
	})
}

func TestSubruleAttrs(t *testing.T) {
	exec := func(code string) error {
		_, err := execBzlFile(
			newTestThread(t),
			util.Must(label.NewCanonicalLabel("@@rules+//:defs.bzl")),
			"def _impl(ctx):\n    pass\n"+code,
			nil,
		)
		return err
	}

	t.Run("Valid", func(t *testing.T) {
		require.NoError(t, exec(`subrule(implementation = _impl, attrs = {
    "_a": attr.label(),
    "_b": attr.label(cfg = "exec", executable = True),
    "_c": attr.label_list(cfg = config.none()),
})`))
	})

	t.Run("Public", func(t *testing.T) {
		require.ErrorContains(
			t,
			exec(`subrule(implementation = _impl, attrs = {"a": attr.label()})`),
			"attr \"a\": subrule attrs must be private",
		)
	})

	t.Run("NonLabel", func(t *testing.T) {
		require.ErrorContains(
			t,
			exec(`subrule(implementation = _impl, attrs = {"_a": attr.string()})`),
			"attr \"_a\": subrule attrs must be of type label or label_list, not string",
		)
	})

	t.Run("Mandatory", func(t *testing.T) {
		require.ErrorContains(
			t,
			exec(`subrule(implementation = _impl, attrs = {"_a": attr.label(mandatory = True)})`),
			"attr \"_a\": subrule attrs must have a default value",
		)
	})

	t.Run("UserDefinedTransition", func(t *testing.T) {
		require.ErrorContains(
			t,
			exec(`
def _transition_impl(settings, attr):
    return {}

_transition = transition(implementation = _transition_impl, inputs = [], outputs = [])

subrule(implementation = _impl, attrs = {"_a": attr.label(cfg = _transition)})`),
			"attr \"_a\": subrule attrs cannot use user-defined transitions",
		)
	})
}

func TestVisitAttrValueLabels(t *testing.T) {
	labelValue := func(l string) *model_starlark_pb.Value {
		return &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Label{Label: l}}
	}
	strValue := func(s string) *model_starlark_pb.Value {
		return newStringValue(s)
	}
	visitLabels := func(value *model_starlark_pb.Value) []string {
		var labels []string
		require.NoError(t, model_starlark.VisitAttrValueLabels(
			t.Context(),
			&model_starlark.ValueReaders[object.LocalReference]{},
			model_core.NewSimpleMessage[object.LocalReference](value),
			func(l label.ResolvedLabel) error {
				labels = append(labels, l.String())
				return nil
			},
		))
		return labels
	}

	t.Run("Label", func(t *testing.T) {
		require.Equal(t, []string{"@@a+//:b"}, visitLabels(labelValue("@@a+//:b")))
	})

	t.Run("List", func(t *testing.T) {
		require.Equal(t, []string{"@@a+//:b", "@@a+//:c"}, visitLabels(&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_List{
				List: &model_starlark_pb.List{
					Elements: []*model_starlark_pb.List_Element{
						{Level: &model_starlark_pb.List_Element_Leaf{Leaf: labelValue("@@a+//:b")}},
						{Level: &model_starlark_pb.List_Element_Leaf{Leaf: labelValue("@@a+//:c")}},
					},
				},
			},
		}))
	})

	t.Run("Dict", func(t *testing.T) {
		// Labels may be contained in both keys (e.g.,
		// attr.label_keyed_string_dict()) and values (e.g.,
		// attr.string_keyed_label_dict()).
		require.Equal(t, []string{"@@a+//:b", "@@a+//:c"}, visitLabels(&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_Dict{
				Dict: &model_starlark_pb.Dict{
					Entries: []*model_starlark_pb.Dict_Entry{
						{
							Level: &model_starlark_pb.Dict_Entry_Leaf_{
								Leaf: &model_starlark_pb.Dict_Entry_Leaf{
									Key:   labelValue("@@a+//:b"),
									Value: strValue("x"),
								},
							},
						},
						{
							Level: &model_starlark_pb.Dict_Entry_Leaf_{
								Leaf: &model_starlark_pb.Dict_Entry_Leaf{
									Key:   strValue("y"),
									Value: labelValue("@@a+//:c"),
								},
							},
						},
					},
				},
			},
		}))
	})

	t.Run("InvalidLabel", func(t *testing.T) {
		require.ErrorContains(t, model_starlark.VisitAttrValueLabels(
			t.Context(),
			&model_starlark.ValueReaders[object.LocalReference]{},
			model_core.NewSimpleMessage[object.LocalReference](labelValue("foo")),
			func(l label.ResolvedLabel) error {
				return nil
			},
		), "invalid label \"foo\": ")
	})

	t.Run("NoLabels", func(t *testing.T) {
		// Values that can't contain labels, and functions
		// that compute default values, should be ignored.
		require.Empty(t, visitLabels(strValue("x")))
		require.Empty(t, visitLabels(&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_Function{Function: &model_starlark_pb.Function{}},
		}))
	})
}
