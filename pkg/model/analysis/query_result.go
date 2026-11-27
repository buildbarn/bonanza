package analysis

import (
	"context"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
)

func (c *baseComputer[TReference, TMetadata]) ComputeQueryResultValue(ctx context.Context, key *model_analysis_pb.QueryResult_Key, e QueryResultEnvironment[TReference, TMetadata]) (PatchedQueryResultValue[TMetadata], error) {
	analysisEncoders, gotAnalysisEncoders := getAnalysisEncoders(e)

	canonicalQuery, err := parseRootModuleQuery(e, key.GetQuery())
	if err != nil {
		return PatchedQueryResultValue[TMetadata]{}, err
	}

	queryExpansion := e.GetQueryExpansionValue(newQueryExpansionKey(canonicalQuery, key.GetIncludeImplicitDeps()))
	if !queryExpansion.IsSet() || !gotAnalysisEncoders {
		return PatchedQueryResultValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	targets, err := c.buildQueryTargets(
		ctx,
		e,
		analysisEncoders,
		model_core.Nested(queryExpansion, queryExpansion.Message.GetTargetLabels()),
		key.GetDefinitionDetail(),
	)
	if err != nil {
		return PatchedQueryResultValue[TMetadata]{}, err
	}
	return model_core.NewPatchedMessage(
		model_analysis_pb.QueryResult_Value_builder{
			Targets: targets.Message,
		}.Build(),
		targets.Patcher,
	), nil
}

// QueryResultEnvironmentForTesting is used to generate mocks for
// testing ComputeQueryResultValue().
type QueryResultEnvironmentForTesting QueryResultEnvironment[model_core.CreatedObjectTree, model_core.CreatedObjectTree]
