package analysis

import (
	"context"
	"errors"
	"fmt"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
)

func (baseComputer[TReference, TMetadata]) ComputeGeneratingRuleAspectsValue(ctx context.Context, key *model_analysis_pb.GeneratingRuleAspects_Key, e GeneratingRuleAspectsEnvironment[TReference, TMetadata]) (PatchedGeneratingRuleAspectsValue[TMetadata], error) {
	missingDependencies := false
	var aspectIndices []uint32
	for i, aspectIdentifierStr := range key.GetAspectIdentifiers() {
		aspectIdentifier, err := label.NewCanonicalStarlarkIdentifier(aspectIdentifierStr)
		if err != nil {
			return PatchedGeneratingRuleAspectsValue[TMetadata]{}, fmt.Errorf("invalid aspect identifier %#v: %w", aspectIdentifierStr, err)
		}
		aspectDefinition, err := getAspectDefinition(e, aspectIdentifier)
		if err != nil {
			if errors.Is(err, evaluation.ErrMissingDependency) {
				missingDependencies = true
				continue
			}
			return PatchedGeneratingRuleAspectsValue[TMetadata]{}, err
		}
		if aspectDefinition.Message.GetApplyToGeneratingRules() {
			aspectIndices = append(aspectIndices, uint32(i))
		}
	}
	if missingDependencies {
		return PatchedGeneratingRuleAspectsValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.GeneratingRuleAspects_Value{
		AspectIndices: aspectIndices,
	}), nil
}

// GeneratingRuleAspectsEnvironmentForTesting is used to generate mocks
// for testing ComputeGeneratingRuleAspectsValue().
type GeneratingRuleAspectsEnvironmentForTesting GeneratingRuleAspectsEnvironment[model_core.CreatedObjectTree, model_core.CreatedObjectTree]
