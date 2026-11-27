package analysis

import (
	"context"
	"errors"
	"fmt"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
)

func (c *baseComputer[TReference, TMetadata]) ComputeTargetIncomingEdgeTransitionValue(ctx context.Context, key model_core.Message[*model_analysis_pb.TargetIncomingEdgeTransition_Key, TReference], e TargetIncomingEdgeTransitionEnvironment[TReference, TMetadata]) (PatchedTargetIncomingEdgeTransitionValue[TMetadata], error) {
	targetLabel, err := label.NewCanonicalLabel(key.Message.GetLabel())
	if err != nil {
		return PatchedTargetIncomingEdgeTransitionValue[TMetadata]{}, fmt.Errorf("invalid target label: %w", err)
	}
	targetValue := e.GetTargetValue(model_analysis_pb.Target_Key_builder{
		Label: targetLabel.String(),
	}.Build())
	if !targetValue.IsSet() {
		return PatchedTargetIncomingEdgeTransitionValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	identityTransition := func() (PatchedTargetIncomingEdgeTransitionValue[TMetadata], error) {
		return model_core.Patch(e, model_core.Nested(key, model_analysis_pb.TargetIncomingEdgeTransition_Value_builder{
			OutputConfigurationReference: key.Message.GetConfigurationReference(),
		}.Build())), nil
	}
	switch targetValue.Message.GetDefinition().WhichKind() {
	case model_starlark_pb.Target_Definition_PackageGroup_case, model_starlark_pb.Target_Definition_SourceFileTarget_case:
		// The properties of these targets don't depend on the
		// configuration. Transition to the empty configuration,
		// so that they are only analyzed once.
		return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.TargetIncomingEdgeTransition_Value{}), nil
	case model_starlark_pb.Target_Definition_PredeclaredOutputFileTarget_case:
		// Predeclared output files use the incoming edge
		// transition of the rule target that owns them, so that
		// they are analyzed in the same configuration.
		ownerTargetNameStr := targetValue.Message.GetDefinition().GetPredeclaredOutputFileTarget().GetOwnerTargetName()
		ownerTargetName, err := label.NewTargetName(ownerTargetNameStr)
		if err != nil {
			return PatchedTargetIncomingEdgeTransitionValue[TMetadata]{}, fmt.Errorf("invalid owner target name %#v: %w", ownerTargetNameStr, err)
		}
		patchedConfigurationReference := model_core.Patch(e, model_core.Nested(key, key.Message.GetConfigurationReference()))
		ownerIncomingEdgeTransition := e.GetTargetIncomingEdgeTransitionValue(
			model_core.NewPatchedMessage(
				model_analysis_pb.TargetIncomingEdgeTransition_Key_builder{
					Label:                  targetLabel.GetCanonicalPackage().AppendTargetName(ownerTargetName).String(),
					ConfigurationReference: patchedConfigurationReference.Message,
				}.Build(),
				patchedConfigurationReference.Patcher,
			),
		)
		if !ownerIncomingEdgeTransition.IsSet() {
			return PatchedTargetIncomingEdgeTransitionValue[TMetadata]{}, evaluation.ErrMissingDependency
		}
		return model_core.Patch(e, ownerIncomingEdgeTransition), nil
	case model_starlark_pb.Target_Definition_RuleTarget_case:
		ruleTarget := model_core.Nested(targetValue, targetValue.Message.GetDefinition().GetRuleTarget())
		ruleIdentifier, err := label.NewCanonicalStarlarkIdentifier(ruleTarget.Message.GetRuleIdentifier())
		if err != nil {
			return PatchedTargetIncomingEdgeTransitionValue[TMetadata]{}, err
		}
		ruleDefinition, err := getRuleDefinition(e, ruleIdentifier)
		if err != nil {
			return PatchedTargetIncomingEdgeTransitionValue[TMetadata]{}, err
		}

		configurationReference := model_core.Nested(key, key.Message.GetConfigurationReference())
		cfgTransition := ruleDefinition.Message.GetCfgTransition()
		if cfgTransition == nil {
			// Rule does not have an incoming edge transition, so
			// the configuration remains unchanged.
			return identityTransition()
		}

		// Attempt to apply the incoming edge transition in a way that
		// can be shared by all targets of the same rule that leave the
		// same set of public attrs at their default values.
		patchedConfigurationReference := model_core.Patch(e, configurationReference)
		incomingEdgeTransition := e.GetRuleUsageIncomingEdgeTransitionValue(
			model_core.NewPatchedMessage(
				model_analysis_pb.RuleUsageIncomingEdgeTransition_Key_builder{
					Identifier:                ruleIdentifier.String(),
					ConfigurationReference:    patchedConfigurationReference.Message,
					ExplicitPublicAttrIndices: getExplicitPublicAttrIndices(ruleTarget.Message.GetPublicAttrValues()),
				}.Build(),
				patchedConfigurationReference.Patcher,
			),
		)
		if !incomingEdgeTransition.IsSet() {
			return PatchedTargetIncomingEdgeTransitionValue[TMetadata]{}, evaluation.ErrMissingDependency
		}
		switch incomingEdgeTransition.Message.WhichResult() {
		case model_analysis_pb.RuleUsageIncomingEdgeTransition_Value_OutputConfigurationReference_case:
			return model_core.Patch(e, model_core.Nested(incomingEdgeTransition, model_analysis_pb.TargetIncomingEdgeTransition_Value_builder{
				OutputConfigurationReference: incomingEdgeTransition.Message.GetOutputConfigurationReference(),
			}.Build())), nil
		case model_analysis_pb.RuleUsageIncomingEdgeTransition_Value_DependsOnExplicitAttrs_case:
		default:
			return PatchedTargetIncomingEdgeTransitionValue[TMetadata]{}, errors.New("unknown incoming edge transition result type")
		}

		// The incoming edge transition depends on attrs that are
		// explicitly set by this target. Apply it, providing all attr
		// values that don't depend on a configuration.
		analysisEncoders, gotAnalysisEncoders := getAnalysisEncoders(e)
		starlarkValueEncoders, gotStarlarkValueEncoders := getStarlarkValueEncoders(e)
		allBuiltinsModulesNames := e.GetBuiltinsModuleNamesValue(&model_analysis_pb.BuiltinsModuleNames_Key{})
		if !gotAnalysisEncoders || !gotStarlarkValueEncoders || !allBuiltinsModulesNames.IsSet() {
			return PatchedTargetIncomingEdgeTransitionValue[TMetadata]{}, evaluation.ErrMissingDependency
		}
		thread, err := c.newStarlarkThread(ctx, e, starlarkValueEncoders, allBuiltinsModulesNames.Message.GetBuiltinsModuleNames())
		if err != nil {
			return PatchedTargetIncomingEdgeTransitionValue[TMetadata]{}, err
		}
		_, edgeTransitionAttrValues, err := getConfigurationFreeAttrValues[TReference, TMetadata](
			thread,
			starlarkValueEncoders,
			targetLabel,
			ruleTarget,
			ruleDefinition,
		)
		if err != nil {
			return PatchedTargetIncomingEdgeTransitionValue[TMetadata]{}, err
		}

		patchedConfigurationReferences, err := c.performUserDefinedTransitionCached(
			ctx,
			e,
			analysisEncoders,
			starlarkValueEncoders,
			model_core.Nested(ruleDefinition, cfgTransition),
			configurationReference,
			model_starlark.NewStructFromDict[TReference, TMetadata](nil, edgeTransitionAttrValues),
		)
		if err != nil {
			if errors.Is(err, errTransitionDependsOnAttrs) {
				return PatchedTargetIncomingEdgeTransitionValue[TMetadata]{}, fmt.Errorf("incoming edge transition used by rule %#v: %w", ruleIdentifier.String(), err)
			}
			return PatchedTargetIncomingEdgeTransitionValue[TMetadata]{}, err
		}
		entries := patchedConfigurationReferences.Message.GetEntries()
		if l := len(entries); l != 1 {
			return PatchedTargetIncomingEdgeTransitionValue[TMetadata]{}, fmt.Errorf("incoming edge transition used by rule %#v is a 1:%d transition, while a 1:1 transition was expected", ruleIdentifier.String(), l)
		}
		return model_core.NewPatchedMessage(
			model_analysis_pb.TargetIncomingEdgeTransition_Value_builder{
				OutputConfigurationReference: entries[0].GetOutputConfigurationReference(),
			}.Build(),
			patchedConfigurationReferences.Patcher,
		), nil
	default:
		// Other target kinds use an identity transition.
		//
		// TODO: Test suites currently don't depend on the
		// configuration either. Keep the configuration, as
		// building a test suite should eventually cause the
		// tests it contains to be built.
		return identityTransition()
	}
}

// TargetIncomingEdgeTransitionEnvironmentForTesting is used to generate
// mocks for testing ComputeTargetIncomingEdgeTransitionValue().
type TargetIncomingEdgeTransitionEnvironmentForTesting TargetIncomingEdgeTransitionEnvironment[model_core.CreatedObjectTree, model_core.CreatedObjectTree]
