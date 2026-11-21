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

	// Aspects don't permit output attrs, so test them separately.
	require.NoError(t, exec(`aspect(implementation = _impl, attrs = {"a": attr.string(configurable = True)})`))
	require.ErrorContains(
		t,
		exec(`aspect(implementation = _impl, attrs = {"a": attr.string(), "b": attr.string(configurable = False)})`),
		"attr \"b\": configurable may only be set to False for attributes of symbolic macros",
	)

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

func TestAttrAllowEmpty(t *testing.T) {
	execGlobals := func(code string) (map[string]eval.Value, error) {
		return execBzlFile(
			newTestThread(t),
			util.Must(label.NewCanonicalLabel("@@rules+//:defs.bzl")),
			code,
			nil,
		)
	}
	encode := func(t *testing.T, value eval.Value) *model_starlark_pb.Attr {
		encodedValue, err := value.(model_starlark.EncodableValue[object.LocalReference, model_core.CreatedObjectTree]).EncodeValue(newTestThread(t), nil, nil, &model_starlark.ValueEncodingOptions[object.LocalReference, model_core.CreatedObjectTree]{})
		require.NoError(t, err)
		return encodedValue.Message.GetAttr()
	}

	for _, function := range []string{
		"int_list",
		"label_keyed_string_dict",
		"label_list",
		"output_list",
		"string_dict",
		"string_keyed_label_dict",
		"string_list",
		"string_list_dict",
	} {
		t.Run(function, func(t *testing.T) {
			globals, err := execGlobals(fmt.Sprintf(
				"DEFAULT = attr.%s()\nALLOWED = attr.%s(allow_empty = True)\nDISALLOWED = attr.%s(allow_empty = False)\n",
				function, function, function,
			))
			require.NoError(t, err)
			repr, err := eval.Repr(newTestThread(t), globals["DEFAULT"])
			require.NoError(t, err)
			require.Equal(t, "<attr."+function+">", repr)

			// Empty values should be permitted by default,
			// like in Bazel.
			for name, expectedAllowEmpty := range map[string]bool{
				"DEFAULT":    true,
				"ALLOWED":    true,
				"DISALLOWED": false,
			} {
				encodedAttr := encode(t, globals[name])
				compositeOptions, ok := model_starlark.GetAttrCompositeOptions(encodedAttr)
				require.True(t, ok)
				require.Equal(t, expectedAllowEmpty, compositeOptions.GetAllowEmpty(), name)

				// The option should be preserved when
				// decoding and reencoding the attr.
				thread := newTestThread(t)
				thread.SetLocal(model_starlark.ValueDecodingOptionsKey, &model_starlark.ValueDecodingOptions[object.LocalReference]{})
				decodedAttr, err := model_starlark.DecodeValue[object.LocalReference, model_core.CreatedObjectTree](
					thread,
					model_core.NewSimpleMessage[object.LocalReference](&model_starlark_pb.Value{
						Kind: &model_starlark_pb.Value_Attr{Attr: encodedAttr},
					}),
					nil,
				)
				require.NoError(t, err)
				testutil.RequireEqualProto(t, encodedAttr, encode(t, decodedAttr))
			}
		})
	}

	t.Run("PositionalArguments", func(t *testing.T) {
		// Like in Bazel, attr.int_list() and attr.string_list()
		// accept mandatory and allow_empty as positional
		// arguments. Other functions only accept allow_empty.
		globals, err := execGlobals(
			"INT_LIST = attr.int_list(True, False)\n" +
				"STRING_LIST = attr.string_list(True, False)\n" +
				"STRING_DICT = attr.string_dict(False)\n",
		)
		require.NoError(t, err)
		for _, name := range []string{"INT_LIST", "STRING_LIST"} {
			encodedAttr := encode(t, globals[name])
			require.Nil(t, encodedAttr.Default, name)
			compositeOptions, _ := model_starlark.GetAttrCompositeOptions(encodedAttr)
			require.False(t, compositeOptions.GetAllowEmpty(), name)
			require.True(t, encodedAttr.Configurable, name)
		}
		compositeOptions, _ := model_starlark.GetAttrCompositeOptions(encode(t, globals["STRING_DICT"]))
		require.False(t, compositeOptions.GetAllowEmpty())
	})

	t.Run("NonComposite", func(t *testing.T) {
		_, ok := model_starlark.GetAttrCompositeOptions(&model_starlark_pb.Attr{
			Type: &model_starlark_pb.Attr_String_{String_: &model_starlark_pb.Attr_StringType{}},
		})
		require.False(t, ok)
	})
}

func TestAttrValues(t *testing.T) {
	// The list of allowed values of attr.int() and attr.string()
	// should be preserved when encoding and decoding attrs.
	globals, err := execBzlFile(
		newTestThread(t),
		util.Must(label.NewCanonicalLabel("@@rules+//:defs.bzl")),
		"INT = attr.int(values = [1, 2])\nSTRING = attr.string(mandatory = True, values = [\"a\", \"b\"])\n",
		nil,
	)
	require.NoError(t, err)
	for name, expectedType := range map[string]*model_starlark_pb.Attr{
		"INT": {
			Type: &model_starlark_pb.Attr_Int{Int: &model_starlark_pb.Attr_IntType{Values: []int32{1, 2}}},
		},
		"STRING": {
			Type: &model_starlark_pb.Attr_String_{String_: &model_starlark_pb.Attr_StringType{Values: []string{"a", "b"}}},
		},
	} {
		encodedValue, err := globals[name].(model_starlark.EncodableValue[object.LocalReference, model_core.CreatedObjectTree]).EncodeValue(newTestThread(t), nil, nil, &model_starlark.ValueEncodingOptions[object.LocalReference, model_core.CreatedObjectTree]{})
		require.NoError(t, err)
		encodedAttr := encodedValue.Message.GetAttr()
		testutil.RequireEqualProto(t, expectedType, &model_starlark_pb.Attr{Type: encodedAttr.Type})

		thread := newTestThread(t)
		thread.SetLocal(model_starlark.ValueDecodingOptionsKey, &model_starlark.ValueDecodingOptions[object.LocalReference]{})
		decodedAttr, err := model_starlark.DecodeValue[object.LocalReference, model_core.CreatedObjectTree](
			thread,
			model_core.NewSimpleMessage[object.LocalReference](encodedValue.Message),
			nil,
		)
		require.NoError(t, err)
		reencodedValue, err := decodedAttr.(model_starlark.EncodableValue[object.LocalReference, model_core.CreatedObjectTree]).EncodeValue(thread, nil, nil, &model_starlark.ValueEncodingOptions[object.LocalReference, model_core.CreatedObjectTree]{})
		require.NoError(t, err)
		testutil.RequireEqualProto(t, encodedValue.Message, reencodedValue.Message)
	}
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

func TestAttrSkipAnalysisTimeFileTypeCheck(t *testing.T) {
	encodeAllowFiles := func(t *testing.T, code string) *model_starlark_pb.Attr_AllowFiles {
		globals, err := execBzlFile(
			newTestThread(t),
			util.Must(label.NewCanonicalLabel("@@rules+//:defs.bzl")),
			"ATTR = "+code+"\n",
			nil,
		)
		require.NoError(t, err)
		encodedAttr, err := globals["ATTR"].(model_starlark.EncodableValue[object.LocalReference, model_core.CreatedObjectTree]).EncodeValue(newTestThread(t), nil, nil, &model_starlark.ValueEncodingOptions[object.LocalReference, model_core.CreatedObjectTree]{})
		require.NoError(t, err)
		attr := encodedAttr.Message.GetAttr()
		if labelList := attr.GetLabelList(); labelList != nil {
			return labelList.GetListValueOptions().GetAllowFiles()
		}
		return attr.GetLabel().GetValueOptions().GetAllowFiles()
	}

	t.Run("WithoutFlag", func(t *testing.T) {
		require.NotEmpty(t, encodeAllowFiles(t, `attr.label_list(allow_files = [".cc"])`).GetFilenamePattern())
	})

	t.Run("Extensions", func(t *testing.T) {
		// The flag causes file extensions to be discarded, as
		// if allow_files = True was provided.
		testutil.RequireEqualProto(
			t,
			&model_starlark_pb.Attr_AllowFiles{},
			encodeAllowFiles(t, `attr.label_list(allow_files = [".cc"], flags = ["SKIP_ANALYSIS_TIME_FILETYPE_CHECK"])`),
		)
		testutil.RequireEqualProto(
			t,
			&model_starlark_pb.Attr_AllowFiles{},
			encodeAllowFiles(t, `attr.label(allow_single_file = [".cc"], flags = ["SKIP_ANALYSIS_TIME_FILETYPE_CHECK"])`),
		)
	})

	t.Run("NoFiles", func(t *testing.T) {
		// File targets remain prohibited.
		require.Nil(t, encodeAllowFiles(t, `attr.label_list(flags = ["SKIP_ANALYSIS_TIME_FILETYPE_CHECK"])`))
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
