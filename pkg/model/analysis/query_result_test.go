package analysis_test

import (
	"testing"

	model_core "bonanza.build/pkg/model/core"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

	"go.uber.org/mock/gomock"
)

func TestQueryResult(t *testing.T) {
	ctrl, ctx := gomock.WithContext(t.Context(), t)
	bct := newBaseComputerTester(ctrl)

	// Apparent repo names should be resolved from the perspective
	// of the root module. The resulting canonical query should be
	// requested in normal form.
	e := NewMockQueryResultEnvironmentForTesting(ctrl)
	e.EXPECT().GetRootModuleValue(
		testutil.EqProto(t, &model_analysis_pb.RootModule_Key{}),
	).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](&model_analysis_pb.RootModule_Value{
		RootModuleName: "main",
	})).AnyTimes()
	e.EXPECT().GetCanonicalRepoNameValue(
		testutil.EqProto(t, &model_analysis_pb.CanonicalRepoName_Key{
			FromCanonicalRepo: "main+",
			ToApparentRepo:    "foo",
		}),
	).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](&model_analysis_pb.CanonicalRepoName_Value{
		ToCanonicalRepo: "foo+",
	})).AnyTimes()
	e.EXPECT().GetQueryExpansionValue(
		testutil.EqProto(t, &model_analysis_pb.QueryExpansion_Key{
			Query:               "let v0 = deps(@@foo+//:x) in $v0 - $v0",
			IncludeImplicitDeps: true,
		}),
	).Return(newQueryExpansionValue("@@foo+//:x"))
	e.EXPECT().GetTargetValue(
		testutil.EqProto(t, &model_analysis_pb.Target_Key{Label: "@@foo+//:x"}),
	).Return(newSourceFileTargetValue())

	value, err := bct.computer.ComputeQueryResultValue(ctx, &model_analysis_pb.QueryResult_Key{
		Query:               "deps(@foo//:x) - deps(@@foo+//:x)",
		IncludeImplicitDeps: true,
		DefinitionDetail:    model_analysis_pb.QueryTarget_FULL,
	}, e)
	require.NoError(t, err)
	testutil.RequireEqualProto(t, &model_analysis_pb.QueryResult_Value{
		Targets: []*model_analysis_pb.QueryTarget{{
			Level: &model_analysis_pb.QueryTarget_Leaf_{
				Leaf: &model_analysis_pb.QueryTarget_Leaf{
					Label: "@@foo+//:x",
					Definition: &model_starlark_pb.Target_Definition{
						Kind: &model_starlark_pb.Target_Definition_SourceFileTarget{
							SourceFileTarget: &model_starlark_pb.SourceFileTarget{},
						},
					},
				},
			},
		}},
	}, value.Message)
}
