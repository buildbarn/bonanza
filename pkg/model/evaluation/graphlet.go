package evaluation

import (
	"context"

	model_core "bonanza.build/pkg/model/core"
	model_parser "bonanza.build/pkg/model/parser"
	model_evaluation_pb "bonanza.build/pkg/proto/model/evaluation"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GraphletGetEvaluation is a helper function for obtaining the
// Evaluation message contained in, or referenced by a Graphlet.
func GraphletGetEvaluation[TReference any](
	ctx context.Context,
	reader model_parser.MessageObjectReader[TReference, *model_evaluation_pb.Evaluation],
	graphlet model_core.Message[*model_evaluation_pb.Graphlet, TReference],
) (model_core.Message[*model_evaluation_pb.Evaluation, TReference], error) {
	switch evaluation := graphlet.Message.GetEvaluation().(type) {
	case *model_evaluation_pb.Graphlet_EvaluationExternal:
		return model_parser.Dereference(ctx, reader, model_core.Nested(graphlet, evaluation.EvaluationExternal))
	case *model_evaluation_pb.Graphlet_EvaluationInline:
		return model_core.Nested(graphlet, evaluation.EvaluationInline), nil
	default:
		return model_core.Message[*model_evaluation_pb.Evaluation, TReference]{}, status.Error(codes.InvalidArgument, "Graphlet node has no evaluation")
	}
}
