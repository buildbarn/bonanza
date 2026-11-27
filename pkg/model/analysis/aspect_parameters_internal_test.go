package analysis

import (
	"testing"

	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"

	"github.com/stretchr/testify/require"

	"google.golang.org/protobuf/types/known/emptypb"
)

func TestParseAspectParameterValue(t *testing.T) {
	boolAttr := model_starlark_pb.NamedAttr_builder{
		Name: "flag",
		Attr: model_starlark_pb.Attr_builder{Bool: &emptypb.Empty{}}.Build(),
	}.Build()
	intAttr := model_starlark_pb.NamedAttr_builder{
		Name: "count",
		Attr: model_starlark_pb.Attr_builder{Int: &model_starlark_pb.Attr_IntType{}}.Build(),
	}.Build()
	stringAttr := model_starlark_pb.NamedAttr_builder{
		Name: "mode",
		Attr: model_starlark_pb.Attr_builder{String: &model_starlark_pb.Attr_StringType{}}.Build(),
	}.Build()

	t.Run("Bool", func(t *testing.T) {
		for _, value := range []string{"true", "True", "1"} {
			v, err := parseAspectParameterValue(boolAttr, value)
			require.NoError(t, err)
			require.Equal(t, eval.True, v)
		}
		for _, value := range []string{"false", "False", "0"} {
			v, err := parseAspectParameterValue(boolAttr, value)
			require.NoError(t, err)
			require.Equal(t, eval.False, v)
		}
		_, err := parseAspectParameterValue(boolAttr, "yes")
		require.EqualError(t, err, "value \"yes\" of parameter \"flag\" is not a valid boolean value")
	})

	t.Run("Int", func(t *testing.T) {
		v, err := parseAspectParameterValue(intAttr, "-42")
		require.NoError(t, err)
		require.Equal(t, eval.MakeInt(-42), v)

		_, err = parseAspectParameterValue(intAttr, "4294967296")
		require.EqualError(t, err, "value \"4294967296\" of parameter \"count\" is not a valid integer value")
	})

	t.Run("String", func(t *testing.T) {
		v, err := parseAspectParameterValue(stringAttr, "fast")
		require.NoError(t, err)
		require.Equal(t, eval.String("fast"), v)
	})
}
