package analysis

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/storage/object"
)

// getRuleDefinition looks up the definition of a rule, given its
// Starlark identifier.
func getRuleDefinition[TReference any](e CompiledBzlFileGlobalValueGetter[TReference], ruleIdentifier label.CanonicalStarlarkIdentifier) (model_core.Message[*model_starlark_pb.Rule_Definition, TReference], error) {
	ruleIdentifierStr := ruleIdentifier.String()
	ruleValue := e.GetCompiledBzlFileGlobalValue(model_analysis_pb.CompiledBzlFileGlobal_Key_builder{
		Identifier: ruleIdentifierStr,
	}.Build())
	if !ruleValue.IsSet() {
		return model_core.Message[*model_starlark_pb.Rule_Definition, TReference]{}, evaluation.ErrMissingDependency
	}
	v := ruleValue.Message.GetGlobal().GetRule()
	if v == nil {
		return model_core.Message[*model_starlark_pb.Rule_Definition, TReference]{}, fmt.Errorf("%#v is not a rule", ruleIdentifierStr)
	}
	d := v.GetDefinition()
	if d == nil {
		return model_core.Message[*model_starlark_pb.Rule_Definition, TReference]{}, fmt.Errorf("%#v is not a rule definition", ruleIdentifierStr)
	}
	return model_core.Nested(ruleValue, d), nil
}

// getSubruleDefinition looks up the definition of a subrule, given its
// Starlark identifier.
func getSubruleDefinition[TReference any](e CompiledBzlFileGlobalValueGetter[TReference], subruleIdentifier string) (model_core.Message[*model_starlark_pb.Subrule_Definition, TReference], error) {
	subruleValue := e.GetCompiledBzlFileGlobalValue(model_analysis_pb.CompiledBzlFileGlobal_Key_builder{
		Identifier: subruleIdentifier,
	}.Build())
	if !subruleValue.IsSet() {
		return model_core.Message[*model_starlark_pb.Subrule_Definition, TReference]{}, evaluation.ErrMissingDependency
	}
	v := subruleValue.Message.GetGlobal().GetSubrule()
	if v == nil {
		return model_core.Message[*model_starlark_pb.Subrule_Definition, TReference]{}, fmt.Errorf("%#v is not a subrule", subruleIdentifier)
	}
	d := v.GetDefinition()
	if d == nil {
		return model_core.Message[*model_starlark_pb.Subrule_Definition, TReference]{}, fmt.Errorf("%#v is not a subrule definition", subruleIdentifier)
	}
	return model_core.Nested(subruleValue, d), nil
}

// getLabelAttrOptions returns the label options of an attr if it is of
// type label, label_keyed_string_dict, label_list, or
// string_keyed_label_dict. For other attr types, nil is returned.
func getLabelAttrOptions(attr *model_starlark_pb.Attr) (labelOptions *model_starlark_pb.Attr_LabelOptions, isScalar, isLabelAttr bool) {
	switch attr.WhichType() {
	case model_starlark_pb.Attr_Label_case:
		return attr.GetLabel().GetValueOptions(), true, true
	case model_starlark_pb.Attr_LabelKeyedStringDict_case:
		return attr.GetLabelKeyedStringDict().GetDictKeyOptions(), false, true
	case model_starlark_pb.Attr_LabelList_case:
		return attr.GetLabelList().GetListValueOptions(), false, true
	case model_starlark_pb.Attr_StringKeyedLabelDict_case:
		return attr.GetStringKeyedLabelDict().GetDictValueOptions(), false, true
	default:
		return nil, false, false
	}
}

type configureDefaultLabelAttrEnvironment[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	model_core.ObjectManager[TReference, TMetadata]
	VisibleTargetValueGetter[TReference, TMetadata]
}

// configureDefaultLabelAttr resolves all labels contained in the
// default value of a label attr, for each of the configurations
// yielded by the attr's outgoing edge transition.
func configureDefaultLabelAttr[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	ctx context.Context,
	e configureDefaultLabelAttrEnvironment[TReference, TMetadata],
	valueReaders *model_starlark.ValueReaders[TReference],
	configuration *labelAttrConfiguration[TReference],
	defaultValue model_core.Message[*model_starlark_pb.Value, TReference],
	visibilityFromPackage label.CanonicalPackage,
	patcher *model_core.ReferenceMessagePatcher[TMetadata],
) (*model_analysis_pb.ConfiguredLabelAttr, error) {
	// Gather all labels contained in the default value.
	var originalLabels []string
	if len(configuration.entries) > 0 {
		var err error
		originalLabels, err = getDefaultValueLabels(ctx, valueReaders, defaultValue)
		if err != nil {
			return nil, err
		}
	}

	// Resolve the labels in each of the output configurations.
	missingDependencies := false
	entries := make([]*model_analysis_pb.ConfiguredLabelAttr_Entry, 0, len(configuration.entries))
	for _, entry := range configuration.entries {
		resolvedLabels := make([]string, 0, len(originalLabels))
		for _, originalLabel := range originalLabels {
			patchedConfigurationReference := model_core.Patch(e, entry.outputConfigurationReference)
			resolvedLabelValue := e.GetVisibleTargetValue(
				model_core.NewPatchedMessage(
					model_analysis_pb.VisibleTarget_Key_builder{
						FromPackage:            visibilityFromPackage.String(),
						ToLabel:                originalLabel,
						ConfigurationReference: patchedConfigurationReference.Message,
					}.Build(),
					patchedConfigurationReference.Patcher,
				),
			)
			if !resolvedLabelValue.IsSet() {
				missingDependencies = true
				continue
			}
			resolvedLabels = append(resolvedLabels, resolvedLabelValue.Message.GetLabel())
		}
		entries = append(entries, model_analysis_pb.ConfiguredLabelAttr_Entry_builder{
			Key:                          entry.key,
			OutputConfigurationReference: model_core.Patch(e, entry.outputConfigurationReference).Merge(patcher),
			ResolvedLabels:               resolvedLabels,
		}.Build())
	}
	if missingDependencies {
		return nil, evaluation.ErrMissingDependency
	}

	return model_analysis_pb.ConfiguredLabelAttr_builder{
		Entries:                    entries,
		DefaultValueLabelsResolved: true,
	}.Build(), nil
}

// newConfiguredLabelAttrWithoutLabels creates a ConfiguredLabelAttr
// that only contains the configurations yielded by the outgoing edge
// transition, without resolving any labels.
func newConfiguredLabelAttrWithoutLabels[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	e model_core.ObjectManager[TReference, TMetadata],
	configuration *labelAttrConfiguration[TReference],
	patcher *model_core.ReferenceMessagePatcher[TMetadata],
) *model_analysis_pb.ConfiguredLabelAttr {
	entries := make([]*model_analysis_pb.ConfiguredLabelAttr_Entry, 0, len(configuration.entries))
	for _, entry := range configuration.entries {
		entries = append(entries, model_analysis_pb.ConfiguredLabelAttr_Entry_builder{
			Key:                          entry.key,
			OutputConfigurationReference: model_core.Patch(e, entry.outputConfigurationReference).Merge(patcher),
		}.Build())
	}
	return model_analysis_pb.ConfiguredLabelAttr_builder{
		Entries: entries,
	}.Build()
}

type configureRuleLabelAttrEnvironment[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] interface {
	performTransitionEnvironment[TReference, TMetadata]
	VisibleTargetValueGetter[TReference, TMetadata]
}

// configureRuleLabelAttr applies the outgoing edge transition of a label
// attr of a rule. If resolveDefaultValue is set, the labels contained
// in the attr's default value are resolved as well.
//
// If the transition reads attrs that are not available through
// transitionAttr, nil is returned.
func (c *baseComputer[TReference, TMetadata]) configureRuleLabelAttr(
	ctx context.Context,
	e configureRuleLabelAttrEnvironment[TReference, TMetadata],
	analysisEncoders *Encoders[TReference],
	starlarkValueEncoders *StarlarkValueEncoders[TReference],
	ruleIdentifier label.CanonicalStarlarkIdentifier,
	ruleDefinition model_core.Message[*model_starlark_pb.Rule_Definition, TReference],
	namedAttr *model_starlark_pb.NamedAttr,
	labelOptions *model_starlark_pb.Attr_LabelOptions,
	configurationReference model_core.Message[*model_core_pb.DecodableReference, TReference],
	transitionAttr *ruleUsageTransitionAttr[TReference, TMetadata],
	execGroupPlatformLabels map[string]string,
	resolveDefaultValue bool,
	patcher *model_core.ReferenceMessagePatcher[TMetadata],
) (*model_analysis_pb.ConfiguredLabelAttr, error) {
	patchedTransition, mayHaveMultipleConfigurations, err := c.performTransition(
		ctx,
		e,
		analysisEncoders,
		starlarkValueEncoders,
		model_core.Nested(ruleDefinition, labelOptions.GetCfg()),
		configurationReference,
		transitionAttr,
		execGroupPlatformLabels,
	)
	if err != nil {
		if errors.Is(err, errTransitionDependsOnAttrs) {
			return nil, nil
		}
		return nil, err
	}
	configuration := newLabelAttrConfigurationFromTransition(
		model_core.Unpatch(e, patchedTransition).Decay(),
		mayHaveMultipleConfigurations,
	)

	defaultValue := namedAttr.GetAttr().GetDefault()
	if !resolveDefaultValue || defaultValue == nil {
		return newConfiguredLabelAttrWithoutLabels(e, &configuration, patcher), nil
	}

	// Labels in default values have their visibility checked
	// relative to the package declaring the rule.
	return configureDefaultLabelAttr(
		ctx,
		e,
		&starlarkValueEncoders.Readers,
		&configuration,
		model_core.Nested(ruleDefinition, defaultValue),
		ruleIdentifier.GetCanonicalLabel().GetCanonicalPackage(),
		patcher,
	)
}

// getAllPublicAttrIndices returns the indices of all public attrs of a
// rule. Providing these to newRuleUsageTransitionAttr yields an "attr"
// parameter for transitions that only provides the values of private
// attrs.
func getAllPublicAttrIndices(ruleDefinition *model_starlark_pb.Rule_Definition) []uint32 {
	var indices []uint32
	for _, namedAttr := range ruleDefinition.GetAttrs() {
		if !strings.HasPrefix(namedAttr.GetName(), "_") {
			indices = append(indices, uint32(len(indices)))
		}
	}
	return indices
}

// getExecGroupPlatformLabels returns a map of exec group names to the
// labels of the execution platforms that were selected for them.
func getExecGroupPlatformLabels[TReference any](ruleDefinition *model_starlark_pb.Rule_Definition, configuredRule model_core.Message[*model_analysis_pb.ConfiguredRule_Value, TReference]) (map[string]string, error) {
	namedExecGroups := ruleDefinition.GetExecGroups()
	execGroups := configuredRule.Message.GetExecGroups()
	if len(execGroups) != len(namedExecGroups) {
		return nil, fmt.Errorf("configured rule has %d exec groups, while the rule definition has %d exec groups", len(execGroups), len(namedExecGroups))
	}
	execGroupPlatformLabels := make(map[string]string, len(execGroups))
	for i, namedExecGroup := range namedExecGroups {
		execGroupPlatformLabels[namedExecGroup.GetName()] = execGroups[i].GetPlatformLabel()
	}
	return execGroupPlatformLabels, nil
}

type configureExecGroupsEnvironment[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	getProviderFromVisibleConfiguredTargetEnvironment[TReference, TMetadata]
	ResolvedToolchainsValueGetter[TReference, TMetadata]
}

// configureExecGroups resolves the toolchains and execution platforms
// of the exec groups of a rule or aspect. Labels of constraint values
// are resolved relative to the package declaring the rule or aspect.
//
// In addition to the configured exec groups, a map of exec group names
// to the labels of the execution platforms that were selected for them
// is returned.
func configureExecGroups[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	ctx context.Context,
	e configureExecGroupsEnvironment[TReference, TMetadata],
	valueReaders *model_starlark.ValueReaders[TReference],
	definingPackage label.CanonicalPackage,
	namedExecGroups []*model_starlark_pb.NamedExecGroup,
	configurationReference model_core.Message[*model_core_pb.DecodableReference, TReference],
) ([]*model_analysis_pb.ConfiguredExecGroup, map[string]string, error) {
	missingDependencies := false
	execGroups := make([]*model_analysis_pb.ConfiguredExecGroup, 0, len(namedExecGroups))
	execGroupPlatformLabels := make(map[string]string, len(namedExecGroups))
	for _, namedExecGroup := range namedExecGroups {
		execGroupDefinition := namedExecGroup.GetExecGroup()
		if execGroupDefinition == nil {
			return nil, nil, fmt.Errorf("missing definition of exec group %#v", namedExecGroup.GetName())
		}
		execCompatibleWith, err := constraintValuesToConstraints(
			ctx,
			e,
			valueReaders,
			definingPackage,
			execGroupDefinition.GetExecCompatibleWith(),
		)
		if err != nil {
			if errors.Is(err, evaluation.ErrMissingDependency) {
				missingDependencies = true
				continue
			}
			return nil, nil, fmt.Errorf("invalid constraint values for exec group %#v: %w", namedExecGroup.GetName(), err)
		}
		patchedConfigurationReference := model_core.Patch(e, configurationReference)
		resolvedToolchains := e.GetResolvedToolchainsValue(
			model_core.NewPatchedMessage(
				model_analysis_pb.ResolvedToolchains_Key_builder{
					ExecCompatibleWith:     execCompatibleWith,
					ConfigurationReference: patchedConfigurationReference.Message,
					Toolchains:             execGroupDefinition.GetToolchains(),
				}.Build(),
				patchedConfigurationReference.Patcher,
			),
		)
		if !resolvedToolchains.IsSet() {
			missingDependencies = true
			continue
		}
		toolchainIdentifiers := resolvedToolchains.Message.GetToolchainIdentifiers()
		if actual, expected := len(toolchainIdentifiers), len(execGroupDefinition.GetToolchains()); actual != expected {
			return nil, nil, fmt.Errorf("obtained %d resolved toolchains, while exec group %#v depends on %d toolchains", actual, namedExecGroup.GetName(), expected)
		}
		execGroups = append(execGroups, model_analysis_pb.ConfiguredExecGroup_builder{
			ToolchainIdentifiers:  toolchainIdentifiers,
			PlatformLabel:         resolvedToolchains.Message.GetPlatformLabel(),
			PlatformPkixPublicKey: resolvedToolchains.Message.GetPlatformPkixPublicKey(),
		}.Build())
		execGroupPlatformLabels[namedExecGroup.GetName()] = resolvedToolchains.Message.GetPlatformLabel()
	}
	if missingDependencies {
		return nil, nil, evaluation.ErrMissingDependency
	}
	return execGroups, execGroupPlatformLabels, nil
}

// getSubruleDefinitions obtains the definitions of all subrules that
// may be invoked by a rule or aspect, either directly or through other
// subrules.
func getSubruleDefinitions[TReference any](e CompiledBzlFileGlobalValueGetter[TReference], subruleIdentifiers []string) (map[string]model_core.Message[*model_starlark_pb.Subrule_Definition, TReference], error) {
	subruleDefinitions := map[string]model_core.Message[*model_starlark_pb.Subrule_Definition, TReference]{}
	pendingSubruleIdentifiers := slices.Clone(subruleIdentifiers)
	seenSubruleIdentifiers := map[string]struct{}{}
	missingDependencies := false
	for len(pendingSubruleIdentifiers) > 0 {
		subruleIdentifier := pendingSubruleIdentifiers[len(pendingSubruleIdentifiers)-1]
		pendingSubruleIdentifiers = pendingSubruleIdentifiers[:len(pendingSubruleIdentifiers)-1]
		if _, ok := seenSubruleIdentifiers[subruleIdentifier]; ok {
			continue
		}
		seenSubruleIdentifiers[subruleIdentifier] = struct{}{}

		subruleDefinition, err := getSubruleDefinition(e, subruleIdentifier)
		if err != nil {
			if errors.Is(err, evaluation.ErrMissingDependency) {
				missingDependencies = true
				continue
			}
			return nil, err
		}
		subruleDefinitions[subruleIdentifier] = subruleDefinition
		pendingSubruleIdentifiers = append(pendingSubruleIdentifiers, subruleDefinition.Message.GetSubruleIdentifiers()...)
	}
	if missingDependencies {
		return nil, evaluation.ErrMissingDependency
	}
	return subruleDefinitions, nil
}

type configureBuiltinTransitionLabelAttrEnvironment[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] interface {
	configureDefaultLabelAttrEnvironment[TReference, TMetadata]
	performBuiltinTransitionEnvironment[TReference, TMetadata]
}

// configureBuiltinTransitionLabelAttr configures a label attr that
// does not use a user-defined transition, and always uses its default
// value. This is the case for attrs of subrules, and private attrs of
// aspects. The labels contained in the default value are resolved,
// having their visibility checked relative to the package declaring
// the subrule or aspect.
func configureBuiltinTransitionLabelAttr[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	ctx context.Context,
	e configureBuiltinTransitionLabelAttrEnvironment[TReference, TMetadata],
	valueReaders *model_starlark.ValueReaders[TReference],
	cfg model_core.Message[*model_starlark_pb.Transition, TReference],
	defaultValue model_core.Message[*model_starlark_pb.Value, TReference],
	visibilityFromPackage label.CanonicalPackage,
	configurationReference model_core.Message[*model_core_pb.DecodableReference, TReference],
	execGroupPlatformLabels map[string]string,
	patcher *model_core.ReferenceMessagePatcher[TMetadata],
) (*model_analysis_pb.ConfiguredLabelAttr, error) {
	patchedTransition, err := performBuiltinTransition(e, cfg, configurationReference, execGroupPlatformLabels)
	if err != nil {
		return nil, err
	}
	configuration := newLabelAttrConfigurationFromTransition(
		model_core.Unpatch(e, patchedTransition).Decay(),
		/* mayHaveMultipleConfigurations = */ false,
	)
	return configureDefaultLabelAttr(
		ctx,
		e,
		valueReaders,
		&configuration,
		defaultValue,
		visibilityFromPackage,
		patcher,
	)
}

// configureSubrules configures the attrs of all subrules that may be
// invoked by a rule or aspect. Subrule attrs are always private, and
// can't use user-defined transitions. This means that their values can
// be computed without having access to any of the target's attrs.
func configureSubrules[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	ctx context.Context,
	e configureBuiltinTransitionLabelAttrEnvironment[TReference, TMetadata],
	valueReaders *model_starlark.ValueReaders[TReference],
	subruleDefinitions map[string]model_core.Message[*model_starlark_pb.Subrule_Definition, TReference],
	configurationReference model_core.Message[*model_core_pb.DecodableReference, TReference],
	execGroupPlatformLabels map[string]string,
	patcher *model_core.ReferenceMessagePatcher[TMetadata],
) ([]*model_analysis_pb.ConfiguredSubrule, error) {
	missingDependencies := false
	sortedSubruleIdentifiers := slices.Sorted(maps.Keys(subruleDefinitions))
	subrules := make([]*model_analysis_pb.ConfiguredSubrule, 0, len(sortedSubruleIdentifiers))
	for _, subruleIdentifierStr := range sortedSubruleIdentifiers {
		subruleIdentifier, err := label.NewCanonicalStarlarkIdentifier(subruleIdentifierStr)
		if err != nil {
			return nil, fmt.Errorf("invalid subrule identifier %#v: %w", subruleIdentifierStr, err)
		}
		subruleDefinition := subruleDefinitions[subruleIdentifierStr]
		subrulePackage := subruleIdentifier.GetCanonicalLabel().GetCanonicalPackage()
		namedAttrs := subruleDefinition.Message.GetAttrs()
		attrs := make([]*model_analysis_pb.ConfiguredLabelAttr, 0, len(namedAttrs))
		for _, namedAttr := range namedAttrs {
			labelOptions, _, isLabelAttr := getLabelAttrOptions(namedAttr.GetAttr())
			if !isLabelAttr || labelOptions == nil {
				return nil, fmt.Errorf("attr %#v of subrule %#v is not a label attr", namedAttr.GetName(), subruleIdentifierStr)
			}
			defaultValue := namedAttr.GetAttr().GetDefault()
			if defaultValue == nil {
				return nil, fmt.Errorf("attr %#v of subrule %#v does not have a default value", namedAttr.GetName(), subruleIdentifierStr)
			}
			configuredAttr, err := configureBuiltinTransitionLabelAttr(
				ctx,
				e,
				valueReaders,
				model_core.Nested(subruleDefinition, labelOptions.GetCfg()),
				model_core.Nested(subruleDefinition, defaultValue),
				subrulePackage,
				configurationReference,
				execGroupPlatformLabels,
				patcher,
			)
			if err != nil {
				if errors.Is(err, evaluation.ErrMissingDependency) {
					missingDependencies = true
					continue
				}
				return nil, fmt.Errorf("attr %#v of subrule %#v: %w", namedAttr.GetName(), subruleIdentifierStr, err)
			}
			attrs = append(attrs, configuredAttr)
		}
		subrules = append(subrules, model_analysis_pb.ConfiguredSubrule_builder{
			Identifier: subruleIdentifierStr,
			Attrs:      attrs,
		}.Build())
	}
	if missingDependencies {
		return nil, evaluation.ErrMissingDependency
	}
	return subrules, nil
}

func (c *baseComputer[TReference, TMetadata]) ComputeConfiguredRuleValue(ctx context.Context, key model_core.Message[*model_analysis_pb.ConfiguredRule_Key, TReference], e ConfiguredRuleEnvironment[TReference, TMetadata]) (PatchedConfiguredRuleValue[TMetadata], error) {
	ruleIdentifier, err := label.NewCanonicalStarlarkIdentifier(key.Message.GetIdentifier())
	if err != nil {
		return PatchedConfiguredRuleValue[TMetadata]{}, fmt.Errorf("invalid rule identifier: %w", err)
	}
	analysisEncoders, gotAnalysisEncoders := getAnalysisEncoders(e)
	starlarkValueEncoders, gotStarlarkValueEncoders := getStarlarkValueEncoders(e)
	ruleDefinition, err := getRuleDefinition(e, ruleIdentifier)
	if err != nil {
		return PatchedConfiguredRuleValue[TMetadata]{}, err
	}
	if !gotAnalysisEncoders || !gotStarlarkValueEncoders {
		return PatchedConfiguredRuleValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	valueReaders := &starlarkValueEncoders.Readers
	configurationReference := model_core.Nested(key, key.Message.GetConfigurationReference())

	// Resolve the toolchains and execution platforms of all exec
	// groups. Labels of constraint values are resolved relative to
	// the package declaring the rule.
	execGroups, execGroupPlatformLabels, execGroupsErr := configureExecGroups(
		ctx,
		e,
		valueReaders,
		ruleIdentifier.GetCanonicalLabel().GetCanonicalPackage(),
		ruleDefinition.Message.GetExecGroups(),
		configurationReference,
	)
	subruleDefinitions, subrulesErr := getSubruleDefinitions(e, ruleDefinition.Message.GetSubruleIdentifiers())
	for _, err := range []error{execGroupsErr, subrulesErr} {
		if err != nil && !errors.Is(err, evaluation.ErrMissingDependency) {
			return PatchedConfiguredRuleValue[TMetadata]{}, err
		}
	}
	if execGroupsErr != nil || subrulesErr != nil {
		return PatchedConfiguredRuleValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	return model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (*model_analysis_pb.ConfiguredRule_Value, error) {
		missingDependencies := false
		subrules, err := configureSubrules(
			ctx,
			e,
			valueReaders,
			subruleDefinitions,
			configurationReference,
			execGroupPlatformLabels,
			patcher,
		)
		if err != nil {
			if !errors.Is(err, evaluation.ErrMissingDependency) {
				return nil, err
			}
			missingDependencies = true
		}

		// Apply the outgoing edge transitions of all label attrs,
		// providing only the values of private attrs. Transitions
		// that read the values of public attrs are applied by
		// ConfiguredRuleUsage or the target instead. As private
		// attrs always use their default value, also resolve the
		// labels contained in them.
		transitionAttr, err := newRuleUsageTransitionAttr[TReference, TMetadata](
			ruleDefinition,
			getAllPublicAttrIndices(ruleDefinition.Message),
			starlarkValueEncoders,
		)
		if err != nil {
			return nil, err
		}
		var labelAttrs []*model_analysis_pb.IndexedConfiguredLabelAttr
		for attrIndex, namedAttr := range ruleDefinition.Message.GetAttrs() {
			labelOptions, _, isLabelAttr := getLabelAttrOptions(namedAttr.GetAttr())
			if !isLabelAttr {
				continue
			}
			if labelOptions == nil {
				return nil, fmt.Errorf("attr %#v does not have label options", namedAttr.GetName())
			}
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
				/* resolveDefaultValue = */ strings.HasPrefix(namedAttr.GetName(), "_"),
				patcher,
			)
			if err != nil {
				if errors.Is(err, evaluation.ErrMissingDependency) {
					missingDependencies = true
					continue
				}
				return nil, fmt.Errorf("attr %#v: %w", namedAttr.GetName(), err)
			}
			if configuredAttr != nil {
				labelAttrs = append(labelAttrs, model_analysis_pb.IndexedConfiguredLabelAttr_builder{
					AttrIndex:  uint32(attrIndex),
					Configured: configuredAttr,
				}.Build())
			}
		}

		if missingDependencies {
			return nil, evaluation.ErrMissingDependency
		}

		return model_analysis_pb.ConfiguredRule_Value_builder{
			ExecGroups: execGroups,
			Subrules:   subrules,
			LabelAttrs: labelAttrs,
		}.Build(), nil
	})
}

// getDefaultValueLabels returns the unique labels contained in the
// default value of an attr, in canonical form, sorted alphabetically.
// This is the order in which ConfiguredLabelAttr.Entry.resolved_labels
// stores the resolved labels. Both the computation and the consumption
// of ConfiguredLabelAttr MUST use this function to obtain the labels,
// so that they are consistent.
func getDefaultValueLabels[TReference object.BasicReference](ctx context.Context, valueReaders *model_starlark.ValueReaders[TReference], defaultValue model_core.Message[*model_starlark_pb.Value, TReference]) ([]string, error) {
	seenLabels := map[string]struct{}{}
	var labels []string
	if err := model_starlark.VisitAttrValueLabels(ctx, valueReaders, defaultValue, func(resolvedLabel label.ResolvedLabel) error {
		canonicalLabel, err := resolvedLabel.AsCanonical()
		if err != nil {
			return err
		}
		canonicalLabelStr := canonicalLabel.String()
		if _, ok := seenLabels[canonicalLabelStr]; !ok {
			seenLabels[canonicalLabelStr] = struct{}{}
			labels = append(labels, canonicalLabelStr)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	slices.Sort(labels)
	return labels, nil
}

// lookupResolvedLabel looks up the resolved label of a label contained
// in the default value of an attr, as stored in ConfiguredLabelAttr.
// originalLabels MUST be obtained by calling getDefaultValueLabels().
func lookupResolvedLabel(originalLabels, resolvedLabels []string, originalLabel string) (string, error) {
	i, ok := slices.BinarySearch(originalLabels, originalLabel)
	if !ok {
		return "", fmt.Errorf("label %#v is not contained in the default value", originalLabel)
	}
	return resolvedLabels[i], nil
}
