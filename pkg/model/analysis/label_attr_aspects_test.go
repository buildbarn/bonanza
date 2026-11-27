package analysis_test

import (
	"testing"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"

	"go.uber.org/mock/gomock"
)

func TestLabelAttrAspects(t *testing.T) {
	ctrl, ctx := gomock.WithContext(t.Context(), t)
	bct := newBaseComputerTester(ctrl)

	const (
		aspect1 = "@@rules+//:defs.bzl%aspect1"
		aspect2 = "@@rules+//:defs.bzl%aspect2"
		aspect3 = "@@rules+//:defs.bzl%aspect3"
	)
	newEnvironment := func() *MockLabelAttrAspectsEnvironmentForTesting {
		e := NewMockLabelAttrAspectsEnvironmentForTesting(ctrl)
		e.EXPECT().CaptureExistingObject(gomock.Any()).
			DoAndReturn(func(reference model_core.CreatedObjectTree) model_core.CreatedObjectTree {
				return reference
			}).
			AnyTimes()
		return e
	}
	expectAspect := func(e *MockLabelAttrAspectsEnvironmentForTesting, identifier string, definition *model_starlark_pb.Aspect_Definition) {
		e.EXPECT().GetCompiledBzlFileGlobalValue(
			testutil.EqProto(t, &model_analysis_pb.CompiledBzlFileGlobal_Key{Identifier: identifier}),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](&model_analysis_pb.CompiledBzlFileGlobal_Value{
			Global: model_starlark_pb.Value_builder{
				Aspect: model_starlark_pb.Aspect_builder{
					Definition: proto.ValueOrDefault(definition),
				}.Build(),
			}.Build(),
		})).AnyTimes()
	}
	compute := func(e *MockLabelAttrAspectsEnvironmentForTesting, aspectIdentifiers ...string) (model_core.PatchedMessage[*model_analysis_pb.LabelAttrAspects_Value, model_core.CreatedObjectTree], error) {
		return bct.computer.ComputeLabelAttrAspectsValue(ctx, &model_analysis_pb.LabelAttrAspects_Key{
			AspectIdentifiers: aspectIdentifiers,
		}, e)
	}
	boolAttr := model_starlark_pb.Attr_builder{
		Default: model_starlark_pb.Value_builder{Bool: proto.Bool(false)}.Build(),
		Bool:    &emptypb.Empty{},
	}.Build()

	t.Run("Success", func(t *testing.T) {
		// aspect2 requires aspect1, and aspect3 requires
		// aspect2. Each aspect should be preceded by the aspects
		// it requires, and occur only once. Only public attrs are
		// returned.
		e := newEnvironment()
		expectAspect(e, aspect1, model_starlark_pb.Aspect_Definition_builder{
			Attrs: []*model_starlark_pb.NamedAttr{
				model_starlark_pb.NamedAttr_builder{Name: "_private", Attr: boolAttr}.Build(),
				model_starlark_pb.NamedAttr_builder{Name: "public", Attr: boolAttr}.Build(),
			},
		}.Build())
		expectAspect(e, aspect2, model_starlark_pb.Aspect_Definition_builder{
			Requires: []string{aspect1},
		}.Build())
		expectAspect(e, aspect3, model_starlark_pb.Aspect_Definition_builder{
			Requires: []string{aspect2},
		}.Build())

		value, err := compute(e, aspect3, aspect1)
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_analysis_pb.LabelAttrAspects_Value{
			Aspects: []*model_analysis_pb.LabelAttrAspects_Value_Aspect{
				{
					Identifier:  aspect1,
					PublicAttrs: []*model_starlark_pb.NamedAttr{model_starlark_pb.NamedAttr_builder{Name: "public", Attr: boolAttr}.Build()},
				},
				{Identifier: aspect2},
				{Identifier: aspect3},
			},
		}, value.Message)
	})

	t.Run("MissingDefinition", func(t *testing.T) {
		e := newEnvironment()
		e.EXPECT().GetCompiledBzlFileGlobalValue(
			testutil.EqProto(t, &model_analysis_pb.CompiledBzlFileGlobal_Key{Identifier: aspect1}),
		).Return(model_core.Message[*model_analysis_pb.CompiledBzlFileGlobal_Value, model_core.CreatedObjectTree]{})
		expectAspect(e, aspect2, &model_starlark_pb.Aspect_Definition{})

		_, err := compute(e, aspect1, aspect2)
		require.Equal(t, evaluation.ErrMissingDependency, err)
	})

	t.Run("Cycle", func(t *testing.T) {
		e := newEnvironment()
		expectAspect(e, aspect1, model_starlark_pb.Aspect_Definition_builder{
			Requires: []string{aspect2},
		}.Build())
		expectAspect(e, aspect2, model_starlark_pb.Aspect_Definition_builder{
			Requires: []string{aspect1},
		}.Build())

		_, err := compute(e, aspect1)
		require.EqualError(t, err, "aspect \"@@rules+//:defs.bzl%aspect1\" requires itself, either directly or indirectly")
	})
}
