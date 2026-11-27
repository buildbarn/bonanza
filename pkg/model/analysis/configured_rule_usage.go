package analysis

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/storage/object"
)

// isPublicAttrExplicitlySet returns whether the value of a public attr
// of a rule target is explicitly set. Attrs whose value is None and
// does not use select() are left at their default value.
func isPublicAttrExplicitlySet(publicAttrValue *model_starlark_pb.RuleTarget_PublicAttrValue) bool {
	selectGroups := publicAttrValue.GetValueParts()
	if len(selectGroups) != 1 {
		return true
	}
	selectGroup := selectGroups[0]
	if len(selectGroup.GetConditions()) > 0 {
		return true
	}
	noMatch := selectGroup.GetNoMatchValue()
	if noMatch == nil {
		return true
	}
	return noMatch.GetNone() == nil
}

// getExplicitPublicAttrIndices returns the indices of the public attrs
// of a rule target that are explicitly set. This is used to construct
// the key of ConfiguredRuleUsage.
func getExplicitPublicAttrIndices(publicAttrValues []*model_starlark_pb.RuleTarget_PublicAttrValue) []uint32 {
	var indices []uint32
	for i, publicAttrValue := range publicAttrValues {
		if isPublicAttrExplicitlySet(publicAttrValue) {
			indices = append(indices, uint32(i))
		}
	}
	return indices
}

// hasDefaultValueToResolve returns whether the default value of a label
// attr may contain labels that need to be resolved. For attrs without a
// default value or whose default value is None, there is nothing to
// resolve.
//
// This is used to determine both which attrs are part of the key of
// ConfiguredRuleUsage, and which attrs have their default value
// resolved by ConfiguredRuleUsage. These two MUST be consistent, as
// ConfiguredRuleUsage would otherwise resolve the default values of
// attrs that are explicitly set, but omitted from its key.
func hasDefaultValueToResolve(namedAttr *model_starlark_pb.NamedAttr) bool {
	defaultValue := namedAttr.GetAttr().GetDefault()
	return defaultValue != nil && defaultValue.GetNone() == nil
}

// configuredRuleUsageResolvesDefaultValue returns whether
// ConfiguredRuleUsage resolves the labels in the default value of a
// label attr whose outgoing edge transition was already applied by
// ConfiguredRule. This is only done for public attrs that are left
// unset.
//
// Attrs whose default value doesn't need to be resolved may be omitted
// from the key of ConfiguredRuleUsage, meaning it can't tell whether
// they are explicitly set. These are left alone, so that the target
// uses its own value.
func configuredRuleUsageResolvesDefaultValue(namedAttr *model_starlark_pb.NamedAttr, explicitAttrs map[string]struct{}) bool {
	if strings.HasPrefix(namedAttr.GetName(), "_") {
		return false
	}
	if _, isExplicit := explicitAttrs[namedAttr.GetName()]; isExplicit {
		return false
	}
	return hasDefaultValueToResolve(namedAttr)
}

// getConfiguredRuleUsageExplicitPublicAttrIndices returns the indices
// of public attrs that need to be provided as part of the key of
// ConfiguredRuleUsage, and whether a target needs to use
// ConfiguredRuleUsage at all.
//
// If none of the rule's label attrs use user-defined transitions,
// ConfiguredRule is able to apply all outgoing edge transitions. In that
// case ConfiguredRuleUsage is only needed to resolve the labels in the
// default values of public label attrs that are left unset. Only the
// explicitness of those attrs affects the value, so other attrs are
// omitted from the key, allowing more targets to share the same value.
func getConfiguredRuleUsageExplicitPublicAttrIndices(
	ruleDefinition *model_starlark_pb.Rule_Definition,
	publicAttrValues []*model_starlark_pb.RuleTarget_PublicAttrValue,
) (indices []uint32, needed bool) {
	hasUserDefinedTransitions := false
	for _, namedAttr := range ruleDefinition.GetAttrs() {
		if labelOptions, _, _ := getLabelAttrOptions(namedAttr.GetAttr()); labelOptions.GetCfg().GetUserDefined() != nil {
			hasUserDefinedTransitions = true
			break
		}
	}

	publicAttrIndex := 0
	for _, namedAttr := range ruleDefinition.GetAttrs() {
		if strings.HasPrefix(namedAttr.GetName(), "_") {
			continue
		}
		if publicAttrIndex >= len(publicAttrValues) {
			// Let getConfigurationFreeAttrValues() report
			// the mismatch.
			break
		}
		isExplicit := isPublicAttrExplicitlySet(publicAttrValues[publicAttrIndex])
		if hasUserDefinedTransitions {
			if isExplicit {
				indices = append(indices, uint32(publicAttrIndex))
			}
		} else if _, _, isLabelAttr := getLabelAttrOptions(namedAttr.GetAttr()); isLabelAttr && hasDefaultValueToResolve(namedAttr) {
			if isExplicit {
				indices = append(indices, uint32(publicAttrIndex))
			} else {
				needed = true
			}
		}
		publicAttrIndex++
	}
	return indices, needed || hasUserDefinedTransitions
}

// ruleUsageTransitionAttr is the value of the "attr" parameter that is
// provided to incoming and outgoing edge transitions when computing
// ConfiguredRuleUsage. It provides the default values of all attrs that
// are not explicitly set. Accessing attrs that are explicitly set
// causes the transition to fail with errTransitionDependsOnAttrs,
// meaning that the transition needs to be computed for every target
// individually.
type ruleUsageTransitionAttr[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	ruleDefinition        model_core.Message[*model_starlark_pb.Rule_Definition, TReference]
	explicitAttrs         map[string]struct{}
	starlarkValueEncoders *StarlarkValueEncoders[TReference]
	decodedAttrs          map[string]eval.Value
}

var _ eval.HasAttrs = (*ruleUsageTransitionAttr[object.GlobalReference, model_core.ReferenceMetadata])(nil)

func (ruleUsageTransitionAttr[TReference, TMetadata]) Repr(_ *eval.Thread, p *eval.Printer) error {
	p.WriteString("<transition_attr>")
	return nil
}

func (ruleUsageTransitionAttr[TReference, TMetadata]) Type() string {
	return "transition_attr"
}

func (ruleUsageTransitionAttr[TReference, TMetadata]) Truth(*eval.Thread) (bool, error) {
	return true, nil
}

func (a *ruleUsageTransitionAttr[TReference, TMetadata]) Attr(thread *eval.Thread, name string) (eval.Value, error) {
	switch name {
	case "name", "tags", "testonly":
		// Common attrs are always specific to the target.
		return nil, errTransitionDependsOnAttrs
	}
	if _, ok := a.explicitAttrs[name]; ok {
		return nil, errTransitionDependsOnAttrs
	}
	if v, ok := a.decodedAttrs[name]; ok {
		return v, nil
	}

	var namedAttr *model_starlark_pb.NamedAttr
	for _, candidate := range a.ruleDefinition.Message.GetAttrs() {
		if candidate.GetName() == name {
			namedAttr = candidate
			break
		}
	}
	if namedAttr == nil {
		return nil, eval.NoSuchAttrError{Value: a, Name: name}
	}
	defaultValue := namedAttr.GetAttr().GetDefault()
	if defaultValue == nil {
		// Mandatory attr that is not set. Let the caller
		// report this as an error.
		return nil, errTransitionDependsOnAttrs
	}

	decodedPart, err := model_starlark.DecodeAttrValuePart[TReference, TMetadata](
		thread,
		model_core.Nested(a.ruleDefinition, defaultValue),
		a.starlarkValueEncoders.newValueDecodingOptions(func(resolvedLabel label.ResolvedLabel) (eval.Value, error) {
			return model_starlark.NewLabel[TReference, TMetadata](resolvedLabel), nil
		}),
	)
	if err != nil {
		return nil, err
	}
	var attrValueBuilder model_starlark.AttrValueBuilder
	if err := attrValueBuilder.Add(thread, decodedPart); err != nil {
		return nil, err
	}
	v, err := attrValueBuilder.Build(thread)
	if err != nil {
		return nil, err
	}
	a.decodedAttrs[name] = v
	return v, nil
}

func (ruleUsageTransitionAttr[TReference, TMetadata]) AttrNames(*eval.Thread) ([]string, error) {
	// The set of attrs that is visible to transitions depends on
	// whether attrs use select(). Let the caller compute the
	// transition if the set of attr names is requested.
	return nil, errTransitionDependsOnAttrs
}

// newRuleUsageTransitionAttr creates the value of the "attr" parameter
// that is provided to edge transitions when computing them for all
// targets of a rule that explicitly set the same public attrs.
func newRuleUsageTransitionAttr[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	ruleDefinition model_core.Message[*model_starlark_pb.Rule_Definition, TReference],
	explicitPublicAttrIndices []uint32,
	starlarkValueEncoders *StarlarkValueEncoders[TReference],
) (*ruleUsageTransitionAttr[TReference, TMetadata], error) {
	explicitAttrs := make(map[string]struct{}, len(explicitPublicAttrIndices))
	publicAttrIndex := uint32(0)
	for _, namedAttr := range ruleDefinition.Message.GetAttrs() {
		if strings.HasPrefix(namedAttr.GetName(), "_") {
			continue
		}
		if len(explicitPublicAttrIndices) > 0 && explicitPublicAttrIndices[0] == publicAttrIndex {
			explicitAttrs[namedAttr.GetName()] = struct{}{}
			explicitPublicAttrIndices = explicitPublicAttrIndices[1:]
			if len(explicitPublicAttrIndices) > 0 && explicitPublicAttrIndices[0] <= publicAttrIndex {
				return nil, errors.New("explicit public attr indices are not sorted in strictly increasing order")
			}
		}
		publicAttrIndex++
	}
	if len(explicitPublicAttrIndices) > 0 {
		return nil, fmt.Errorf("explicit public attr index %d exceeds the number of public attrs of the rule, which is %d", explicitPublicAttrIndices[0], publicAttrIndex)
	}
	return &ruleUsageTransitionAttr[TReference, TMetadata]{
		ruleDefinition:        ruleDefinition,
		explicitAttrs:         explicitAttrs,
		starlarkValueEncoders: starlarkValueEncoders,
		decodedAttrs:          map[string]eval.Value{},
	}, nil
}

func (c *baseComputer[TReference, TMetadata]) ComputeConfiguredRuleUsageValue(ctx context.Context, key model_core.Message[*model_analysis_pb.ConfiguredRuleUsage_Key, TReference], e ConfiguredRuleUsageEnvironment[TReference, TMetadata]) (PatchedConfiguredRuleUsageValue[TMetadata], error) {
	ruleIdentifier, err := label.NewCanonicalStarlarkIdentifier(key.Message.GetIdentifier())
	if err != nil {
		return PatchedConfiguredRuleUsageValue[TMetadata]{}, fmt.Errorf("invalid rule identifier: %w", err)
	}
	analysisEncoders, gotAnalysisEncoders := getAnalysisEncoders(e)
	starlarkValueEncoders, gotStarlarkValueEncoders := getStarlarkValueEncoders(e)
	ruleDefinition, err := getRuleDefinition(e, ruleIdentifier)
	if err != nil {
		return PatchedConfiguredRuleUsageValue[TMetadata]{}, err
	}
	if !gotAnalysisEncoders || !gotStarlarkValueEncoders {
		return PatchedConfiguredRuleUsageValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	transitionAttr, err := newRuleUsageTransitionAttr[TReference, TMetadata](ruleDefinition, key.Message.GetExplicitPublicAttrIndices(), starlarkValueEncoders)
	if err != nil {
		return PatchedConfiguredRuleUsageValue[TMetadata]{}, err
	}
	configurationReference := model_core.Nested(key, key.Message.GetConfigurationReference())

	// Obtain the execution platforms of all exec groups, as these
	// are needed to apply "exec" transitions.
	patchedConfigurationReference := model_core.Patch(e, configurationReference)
	configuredRule := e.GetConfiguredRuleValue(
		model_core.NewPatchedMessage(
			&model_analysis_pb.ConfiguredRule_Key{
				Identifier:             ruleIdentifier.String(),
				ConfigurationReference: patchedConfigurationReference.Message,
			},
			patchedConfigurationReference.Patcher,
		),
	)
	if !configuredRule.IsSet() {
		return PatchedConfiguredRuleUsageValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	execGroupPlatformLabels, err := getExecGroupPlatformLabels(ruleDefinition.Message, configuredRule)
	if err != nil {
		return PatchedConfiguredRuleUsageValue[TMetadata]{}, err
	}

	// Only compute the properties of label attrs that are not
	// provided by ConfiguredRule, because they depend on which public
	// attrs are explicitly set.
	ruleLabelAttrs := newIndexedConfiguredLabelAttrCursor(model_core.Nested(configuredRule, configuredRule.Message.GetLabelAttrs()))
	return model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (*model_analysis_pb.ConfiguredRuleUsage_Value, error) {
		missingDependencies := false
		var labelAttrs []*model_analysis_pb.IndexedConfiguredLabelAttr
		for attrIndex, namedAttr := range ruleDefinition.Message.GetAttrs() {
			labelOptions, _, isLabelAttr := getLabelAttrOptions(namedAttr.GetAttr())
			if !isLabelAttr {
				continue
			}
			if labelOptions == nil {
				return nil, fmt.Errorf("attr %#v does not have label options", namedAttr.GetName())
			}
			ruleLabelAttr, hasRuleLabelAttr, err := ruleLabelAttrs.get(attrIndex)
			if err != nil {
				return nil, err
			}
			appendLabelAttr := func(configuredAttr *model_analysis_pb.ConfiguredLabelAttr) {
				if configuredAttr != nil {
					labelAttrs = append(labelAttrs, &model_analysis_pb.IndexedConfiguredLabelAttr{
						AttrIndex:  uint32(attrIndex),
						Configured: configuredAttr,
					})
				}
			}

			_, isExplicit := transitionAttr.explicitAttrs[namedAttr.GetName()]
			defaultValue := namedAttr.GetAttr().GetDefault()
			if hasRuleLabelAttr {
				// ConfiguredRule was able to apply the
				// outgoing edge transition. Only resolve the
				// labels in the default value if it's a
				// public attr that is left unset.
				if !configuredRuleUsageResolvesDefaultValue(namedAttr, transitionAttr.explicitAttrs) {
					continue
				}
				configuration := newLabelAttrConfigurationFromProto(ruleLabelAttr, labelOptions.GetCfg())
				configuredAttr, err := configureDefaultLabelAttr(
					ctx,
					e,
					&starlarkValueEncoders.Readers,
					&configuration,
					model_core.Nested(ruleDefinition, defaultValue),
					ruleIdentifier.GetCanonicalLabel().GetCanonicalPackage(),
					patcher,
				)
				if err != nil {
					if errors.Is(err, evaluation.ErrMissingDependency) {
						missingDependencies = true
						continue
					}
					return nil, fmt.Errorf("attr %#v: %w", namedAttr.GetName(), err)
				}
				appendLabelAttr(configuredAttr)
				continue
			}

			// The outgoing edge transition reads the values of
			// public attrs. Apply it, providing the default
			// values of attrs that are not explicitly set.
			configuredAttr, err := c.configureRuleLabelAttr(
				ctx,
				e,
				analysisEncoders,
				starlarkValueEncoders,
				ruleIdentifier,
				ruleDefinition,
				namedAttr,
				labelOptions,
				configurationReference,
				transitionAttr,
				execGroupPlatformLabels,
				/* resolveDefaultValue = */ !isExplicit,
				patcher,
			)
			if err != nil {
				if errors.Is(err, evaluation.ErrMissingDependency) {
					missingDependencies = true
					continue
				}
				return nil, fmt.Errorf("attr %#v: %w", namedAttr.GetName(), err)
			}
			appendLabelAttr(configuredAttr)
		}
		if missingDependencies {
			return nil, evaluation.ErrMissingDependency
		}
		if err := ruleLabelAttrs.finish(); err != nil {
			return nil, err
		}

		return &model_analysis_pb.ConfiguredRuleUsage_Value{
			LabelAttrs: labelAttrs,
		}, nil
	})
}
