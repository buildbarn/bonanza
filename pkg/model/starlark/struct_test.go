package starlark_test

import (
	"testing"

	model_core "bonanza.build/pkg/model/core"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/storage/object"

	"github.com/stretchr/testify/require"

	"google.golang.org/protobuf/proto"
)

func TestAllStructFields(t *testing.T) {
	strValue := func(s string) *model_starlark_pb.Value {
		return newStringValue(s)
	}
	allFieldNames := func(structFields *model_starlark_pb.Struct_Fields, errOut *error) []string {
		var names []string
		for name := range model_starlark.AllStructFields(
			t.Context(),
			nil,
			model_core.NewSimpleMessage[object.LocalReference](structFields),
			errOut,
		) {
			names = append(names, name)
		}
		return names
	}

	t.Run("Success", func(t *testing.T) {
		var errIter error
		require.Equal(t, []string{"a", "b"}, allFieldNames(model_starlark_pb.Struct_Fields_builder{
			Keys: []string{"a", "b"},
			Values: []*model_starlark_pb.List_Element{
				model_starlark_pb.List_Element_builder{Leaf: proto.ValueOrDefault(strValue("1"))}.Build(),
				model_starlark_pb.List_Element_builder{Leaf: proto.ValueOrDefault(strValue("2"))}.Build(),
			},
		}.Build(), &errIter))
		require.NoError(t, errIter)
	})

	t.Run("InvalidValue", func(t *testing.T) {
		// Errors raised while processing values should be
		// reported, as opposed to the struct being truncated.
		var errIter error
		require.Equal(t, []string{"a"}, allFieldNames(model_starlark_pb.Struct_Fields_builder{
			Keys: []string{"a", "b"},
			Values: []*model_starlark_pb.List_Element{
				model_starlark_pb.List_Element_builder{Leaf: proto.ValueOrDefault(strValue("1"))}.Build(),
				{},
			},
		}.Build(), &errIter))
		require.EqualError(t, errIter, "not a valid leaf entry")
	})

	t.Run("FewerKeysThanValues", func(t *testing.T) {
		var errIter error
		require.Equal(t, []string{"a"}, allFieldNames(model_starlark_pb.Struct_Fields_builder{
			Keys: []string{"a"},
			Values: []*model_starlark_pb.List_Element{
				model_starlark_pb.List_Element_builder{Leaf: proto.ValueOrDefault(strValue("1"))}.Build(),
				model_starlark_pb.List_Element_builder{Leaf: proto.ValueOrDefault(strValue("2"))}.Build(),
			},
		}.Build(), &errIter))
		require.EqualError(t, errIter, "struct has fewer keys than values")
	})
}
