package starlark_test

import (
	"testing"

	model_core "bonanza.build/pkg/model/core"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/storage/object"

	"github.com/stretchr/testify/require"
)

func TestAllListLeafElements(t *testing.T) {
	strValue := func(s string) *model_starlark_pb.Value {
		return &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Str{Str: s}}
	}
	rootList := model_core.NewSimpleMessage[object.LocalReference]([]*model_starlark_pb.List_Element{
		{Level: &model_starlark_pb.List_Element_Leaf{Leaf: strValue("a")}},
		{Level: &model_starlark_pb.List_Element_Leaf{Leaf: strValue("b")}},
		{},
	})

	for name, allElements := range map[string]func(errOut *error) func(yield func(model_core.Message[*model_starlark_pb.Value, object.LocalReference]) bool){
		"AllListLeafElements": func(errOut *error) func(yield func(model_core.Message[*model_starlark_pb.Value, object.LocalReference]) bool) {
			return model_starlark.AllListLeafElements(t.Context(), nil, rootList, errOut)
		},
		"AllListLeafElementsSkippingDuplicateParents": func(errOut *error) func(yield func(model_core.Message[*model_starlark_pb.Value, object.LocalReference]) bool) {
			return model_starlark.AllListLeafElementsSkippingDuplicateParents(t.Context(), nil, rootList, map[model_core.Decodable[object.LocalReference]]struct{}{}, errOut)
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Run("InvalidElement", func(t *testing.T) {
				// Errors raised while processing
				// elements should be reported, as
				// opposed to the list being truncated.
				var values []string
				var errIter error
				for value := range allElements(&errIter) {
					values = append(values, value.Message.GetStr())
				}
				require.Equal(t, []string{"a", "b"}, values)
				require.EqualError(t, errIter, "not a valid leaf entry")
			})

			t.Run("StopEarly", func(t *testing.T) {
				// Stopping iteration before reaching
				// the invalid element should not cause
				// an error to be reported.
				var errIter error
				for value := range allElements(&errIter) {
					require.Equal(t, "a", value.Message.GetStr())
					break
				}
				require.NoError(t, errIter)
			})
		})
	}
}
