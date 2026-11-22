package starlark_test

import (
	"testing"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"google.golang.org/protobuf/types/known/emptypb"
)

func TestBuildSettingScopeUnpackerInto(t *testing.T) {
	ctrl := gomock.NewController(t)
	unpacker := model_starlark.NewBuildSettingScopeUnpackerInto[object.LocalReference, model_core.CreatedObjectTree](
		util.Must(label.NewCanonicalPackage("@@main+//foo")),
	)
	newThread := func() *eval.Thread {
		thread := newTestThread(t)
		thread.SetLocal(model_starlark.LabelResolverKey, NewMockLabelResolver(ctrl))
		return thread
	}

	t.Run("Success", func(t *testing.T) {
		for input, expected := range map[string]*model_starlark_pb.BuildSettingScope{
			"target":                {Kind: &model_starlark_pb.BuildSettingScope_Target{Target: &emptypb.Empty{}}},
			"project":               {Kind: &model_starlark_pb.BuildSettingScope_Project{Project: &emptypb.Empty{}}},
			"universal":             {Kind: &model_starlark_pb.BuildSettingScope_Universal{Universal: &emptypb.Empty{}}},
			"exec:--:host_flag":     {Kind: &model_starlark_pb.BuildSettingScope_Exec{Exec: "@@main+//foo:host_flag"}},
			"exec:--//bar:host":     {Kind: &model_starlark_pb.BuildSettingScope_Exec{Exec: "@@main+//bar:host"}},
			"exec:--@@other+//:baz": {Kind: &model_starlark_pb.BuildSettingScope_Exec{Exec: "@@other+//:baz"}},
		} {
			var scope *model_starlark_pb.BuildSettingScope
			require.NoError(t, unpacker.UnpackInto(newThread(), eval.String(input), &scope), input)
			testutil.RequireEqualProto(t, expected, scope)
		}
	})

	t.Run("Canonicalize", func(t *testing.T) {
		v, err := unpacker.Canonicalize(newThread(), eval.String("exec:--:host_flag"))
		require.NoError(t, err)
		require.Equal(t, eval.String("exec:--@@main+//foo:host_flag"), v)
	})

	t.Run("Failure", func(t *testing.T) {
		for _, input := range []eval.Value{
			eval.String(""),
			eval.String("default"),
			eval.String("Target"),
			eval.String("exec:--"),
			eval.MakeInt(42),
		} {
			var scope *model_starlark_pb.BuildSettingScope
			require.Error(t, unpacker.UnpackInto(newThread(), input, &scope), util.Must(eval.Repr(newThread(), input)))
		}
	})
}
