package analysis

import (
	"context"
	"errors"
	"fmt"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"

	"google.golang.org/protobuf/types/known/emptypb"
)

func (c *baseComputer[TReference, TMetadata]) ComputeRuleUsageIncomingEdgeTransitionValue(ctx context.Context, key model_core.Message[*model_analysis_pb.RuleUsageIncomingEdgeTransition_Key, TReference], e RuleUsageIncomingEdgeTransitionEnvironment[TReference, TMetadata]) (PatchedRuleUsageIncomingEdgeTransitionValue[TMetadata], error) {
	ruleIdentifier, err := label.NewCanonicalStarlarkIdentifier(key.Message.GetIdentifier())
	if err != nil {
		return PatchedRuleUsageIncomingEdgeTransitionValue[TMetadata]{}, fmt.Errorf("invalid rule identifier: %w", err)
	}
	analysisEncoders, gotAnalysisEncoders := getAnalysisEncoders(e)
	starlarkValueEncoders, gotStarlarkValueEncoders := getStarlarkValueEncoders(e)
	ruleDefinition, err := getRuleDefinition(e, ruleIdentifier)
	if err != nil {
		return PatchedRuleUsageIncomingEdgeTransitionValue[TMetadata]{}, err
	}
	if !gotAnalysisEncoders || !gotStarlarkValueEncoders {
		return PatchedRuleUsageIncomingEdgeTransitionValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	configurationReference := model_core.Nested(key, key.Message.GetConfigurationReference())
	cfgTransition := ruleDefinition.Message.GetCfgTransition()
	if cfgTransition == nil {
		// Rule does not have an incoming edge transition, so
		// the configuration remains unchanged.
		return model_core.Patch(e, model_core.Nested(key, &model_analysis_pb.RuleUsageIncomingEdgeTransition_Value{
			Result: &model_analysis_pb.RuleUsageIncomingEdgeTransition_Value_OutputConfigurationReference{OutputConfigurationReference: key.Message.GetConfigurationReference()},
		})), nil
	}

	transitionAttr, err := newRuleUsageTransitionAttr[TReference, TMetadata](ruleDefinition, key.Message.GetExplicitPublicAttrIndices(), starlarkValueEncoders)
	if err != nil {
		return PatchedRuleUsageIncomingEdgeTransitionValue[TMetadata]{}, err
	}
	patchedConfigurationReferences, err := c.performUserDefinedTransitionCached(
		ctx,
		e,
		analysisEncoders,
		starlarkValueEncoders,
		model_core.Nested(ruleDefinition, cfgTransition),
		configurationReference,
		transitionAttr,
	)
	if err != nil {
		if errors.Is(err, errTransitionDependsOnAttrs) {
			return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.RuleUsageIncomingEdgeTransition_Value{
				Result: &model_analysis_pb.RuleUsageIncomingEdgeTransition_Value_DependsOnExplicitAttrs{DependsOnExplicitAttrs: &emptypb.Empty{}},
			}), nil
		}
		return PatchedRuleUsageIncomingEdgeTransitionValue[TMetadata]{}, err
	}

	entries := patchedConfigurationReferences.Message.GetEntries()
	if l := len(entries); l != 1 {
		return PatchedRuleUsageIncomingEdgeTransitionValue[TMetadata]{}, fmt.Errorf("incoming edge transition used by rule %#v is a 1:%d transition, while a 1:1 transition was expected", ruleIdentifier.String(), l)
	}
	return model_core.NewPatchedMessage(
		&model_analysis_pb.RuleUsageIncomingEdgeTransition_Value{
			Result: &model_analysis_pb.RuleUsageIncomingEdgeTransition_Value_OutputConfigurationReference{OutputConfigurationReference: entries[0].GetOutputConfigurationReference()},
		},
		patchedConfigurationReferences.Patcher,
	), nil
}
