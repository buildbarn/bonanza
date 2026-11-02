package starlark_test

import (
	"fmt"
	"testing"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

func TestAttrConfigurable(t *testing.T) {
	bzlFileBuiltins, _ := model_starlark.GetBuiltins[object.LocalReference, model_core.CreatedObjectTree]()
	execGlobals := func(code string) (starlark.StringDict, error) {
		return starlark.ExecFileOptions(
			&syntax.FileOptions{},
			&starlark.Thread{},
			"@@rules+//:defs.bzl",
			"def _impl(ctx):\n    pass\n"+code,
			bzlFileBuiltins,
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
		"subrule(implementation = _impl, attrs = %s)",
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
			encodedAttr, _, err := globals[name].(model_starlark.EncodableValue[object.LocalReference, model_core.CreatedObjectTree]).EncodeValue(map[starlark.Value]struct{}{}, nil, &model_starlark.ValueEncodingOptions[object.LocalReference, model_core.CreatedObjectTree]{})
			require.NoError(t, err)
			require.False(t, encodedAttr.Message.GetAttr().Configurable)
		}
	})

	t.Run("Encode", func(t *testing.T) {
		// Whether an attribute is configurable should be
		// preserved when encoding and decoding it.
		for _, configurable := range []bool{true, false} {
			encodedAttr, _, err := model_starlark.NewAttr[object.LocalReference, model_core.CreatedObjectTree](
				model_starlark.NewStringAttrType[object.LocalReference, model_core.CreatedObjectTree](nil),
				/* defaultValue = */ nil,
				configurable,
			).EncodeValue(map[starlark.Value]struct{}{}, nil, &model_starlark.ValueEncodingOptions[object.LocalReference, model_core.CreatedObjectTree]{})
			require.NoError(t, err)
			require.Equal(t, configurable, encodedAttr.Message.GetAttr().Configurable)

			decodedAttr, err := model_starlark.DecodeValue[object.LocalReference, model_core.CreatedObjectTree](
				model_core.NewSimpleMessage[object.LocalReference](encodedAttr.Message),
				nil,
				&model_starlark.ValueDecodingOptions[object.LocalReference]{},
			)
			require.NoError(t, err)
			reencodedAttr, _, err := decodedAttr.(model_starlark.EncodableValue[object.LocalReference, model_core.CreatedObjectTree]).EncodeValue(map[starlark.Value]struct{}{}, nil, &model_starlark.ValueEncodingOptions[object.LocalReference, model_core.CreatedObjectTree]{})
			require.NoError(t, err)
			testutil.RequireEqualProto(t, encodedAttr.Message, reencodedAttr.Message)
		}
	})
}

func TestVisitAttrValueLabels(t *testing.T) {
	labelValue := func(l string) *model_starlark_pb.Value {
		return &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Label{Label: l}}
	}
	strValue := func(s string) *model_starlark_pb.Value {
		return &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Str{Str: s}}
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
