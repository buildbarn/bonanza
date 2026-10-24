package starlark_test

import (
	"testing"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/storage/object"

	"github.com/stretchr/testify/require"
)

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
