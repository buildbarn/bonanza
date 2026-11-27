package analysis

import (
	"context"
	"fmt"
	"strings"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/storage/object"
)

// labelAttrConfiguration describes how the dependencies of a label attr
// need to be configured, after its outgoing edge transition has been
// applied. It can either be obtained from a ConfiguredLabelAttr
// message, or by applying the outgoing edge transition directly.
type labelAttrConfiguration[TReference any] struct {
	// The configurations yielded by the outgoing edge transition. If
	// empty, dependencies should be left unconfigured.
	entries []labelAttrConfigurationEntry[TReference]

	// Whether the outgoing edge transition may yield multiple
	// configurations. If set, scalar values need to be converted to
	// lists, and ctx.split_attr needs to be populated.
	mayHaveMultipleConfigurations bool

	// Whether the attr is left at its default value, and the labels
	// contained in the default value have already been resolved.
	defaultValueLabelsResolved bool

	// If defaultValueLabelsResolved is set, the labels contained in
	// the default value, as returned by getDefaultValueLabels(). These
	// are set by calling setLabelAttrConfigurationDefaultValue().
	originalLabels []string
}

type labelAttrConfigurationEntry[TReference any] struct {
	key                          string
	outputConfigurationReference model_core.Message[*model_core_pb.DecodableReference, TReference]
	resolvedLabels               []string
}

// setLabelAttrConfigurationDefaultValue provides the default value of
// the attr, which is needed to look up resolved labels if
// defaultValueLabelsResolved is set.
func setLabelAttrConfigurationDefaultValue[TReference object.BasicReference](ctx context.Context, c *labelAttrConfiguration[TReference], valueReaders *model_starlark.ValueReaders[TReference], defaultValue model_core.Message[*model_starlark_pb.Value, TReference]) error {
	originalLabels, err := getDefaultValueLabels(ctx, valueReaders, defaultValue)
	if err != nil {
		return err
	}
	for _, entry := range c.entries {
		if len(entry.resolvedLabels) != len(originalLabels) {
			return fmt.Errorf("default value contains %d labels, while %d resolved labels are provided", len(originalLabels), len(entry.resolvedLabels))
		}
	}
	c.originalLabels = originalLabels
	return nil
}

// transitionMayHaveMultipleConfigurations returns whether an outgoing
// edge transition may yield multiple configurations. This is the case
// for user-defined transitions. For these, the values of scalar attrs
// need to be converted to lists, and ctx.split_attr needs to be
// populated, even if the transition happens to yield a single
// configuration.
func transitionMayHaveMultipleConfigurations(cfg *model_starlark_pb.Transition) bool {
	return cfg.GetUserDefined() != nil
}

// newLabelAttrConfigurationFromProto converts a ConfiguredLabelAttr
// message, as computed by ConfiguredRule or ConfiguredRuleUsage, to a
// labelAttrConfiguration. cfg is the outgoing edge transition of the
// attr, as provided in the attr's definition.
func newLabelAttrConfigurationFromProto[TReference any](configured model_core.Message[*model_analysis_pb.ConfiguredLabelAttr, TReference], cfg *model_starlark_pb.Transition) labelAttrConfiguration[TReference] {
	configuredEntries := configured.Message.GetEntries()
	entries := make([]labelAttrConfigurationEntry[TReference], 0, len(configuredEntries))
	for _, entry := range configuredEntries {
		entries = append(entries, labelAttrConfigurationEntry[TReference]{
			key:                          entry.GetKey(),
			outputConfigurationReference: model_core.Nested(configured, entry.GetOutputConfigurationReference()),
			resolvedLabels:               entry.GetResolvedLabels(),
		})
	}
	return labelAttrConfiguration[TReference]{
		entries:                       entries,
		mayHaveMultipleConfigurations: transitionMayHaveMultipleConfigurations(cfg),
		defaultValueLabelsResolved:    configured.Message.GetDefaultValueLabelsResolved(),
	}
}

// newLabelAttrConfigurationFromTransition creates a
// labelAttrConfiguration based on the result of applying an outgoing
// edge transition. Labels contained in the attr value still need to be
// resolved.
func newLabelAttrConfigurationFromTransition[TReference any](transition model_core.Message[*model_analysis_pb.UserDefinedTransition_Value_Success, TReference], mayHaveMultipleConfigurations bool) labelAttrConfiguration[TReference] {
	transitionEntries := transition.Message.GetEntries()
	entries := make([]labelAttrConfigurationEntry[TReference], 0, len(transitionEntries))
	for _, entry := range transitionEntries {
		entries = append(entries, labelAttrConfigurationEntry[TReference]{
			key:                          entry.GetKey(),
			outputConfigurationReference: model_core.Nested(transition, entry.GetOutputConfigurationReference()),
		})
	}
	return labelAttrConfiguration[TReference]{
		entries:                       entries,
		mayHaveMultipleConfigurations: mayHaveMultipleConfigurations,
	}
}

// indexedConfiguredLabelAttrCursor can be used to look up the values of
// label attrs in a sparse list of IndexedConfiguredLabelAttr messages,
// while iterating over the attrs of a rule definition in order.
type indexedConfiguredLabelAttrCursor[TReference any] struct {
	remaining model_core.Message[[]*model_analysis_pb.IndexedConfiguredLabelAttr, TReference]
}

func newIndexedConfiguredLabelAttrCursor[TReference any](labelAttrs model_core.Message[[]*model_analysis_pb.IndexedConfiguredLabelAttr, TReference]) indexedConfiguredLabelAttrCursor[TReference] {
	return indexedConfiguredLabelAttrCursor[TReference]{remaining: labelAttrs}
}

// get returns the value of the label attr at a given index, if present
// in the list. It must be called with strictly increasing indices, and
// only for label attrs.
func (c *indexedConfiguredLabelAttrCursor[TReference]) get(attrIndex int) (model_core.Message[*model_analysis_pb.ConfiguredLabelAttr, TReference], bool, error) {
	if len(c.remaining.Message) == 0 {
		return model_core.Message[*model_analysis_pb.ConfiguredLabelAttr, TReference]{}, false, nil
	}
	first := c.remaining.Message[0]
	if int64(first.GetAttrIndex()) < int64(attrIndex) {
		return model_core.Message[*model_analysis_pb.ConfiguredLabelAttr, TReference]{}, false, fmt.Errorf("label attr entry with attr index %d is not sorted, or does not refer to a label attr", first.GetAttrIndex())
	}
	if int64(first.GetAttrIndex()) > int64(attrIndex) {
		return model_core.Message[*model_analysis_pb.ConfiguredLabelAttr, TReference]{}, false, nil
	}
	firstConfigured := first.GetConfigured()
	if firstConfigured == nil {
		return model_core.Message[*model_analysis_pb.ConfiguredLabelAttr, TReference]{}, false, fmt.Errorf("label attr entry with attr index %d does not have a value", first.GetAttrIndex())
	}
	c.remaining.Message = c.remaining.Message[1:]
	return model_core.Nested(c.remaining, firstConfigured), true, nil
}

// finish checks that all entries in the list have been consumed. Any
// remaining entries refer to attrs that don't exist or are not label
// attrs.
func (c *indexedConfiguredLabelAttrCursor[TReference]) finish() error {
	if len(c.remaining.Message) > 0 {
		return fmt.Errorf("label attr entry with attr index %d does not refer to a label attr", c.remaining.Message[0].GetAttrIndex())
	}
	return nil
}

type configuredTargetReferenceCreatorEnvironment[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	model_core.ObjectManager[TReference, TMetadata]
	TargetProvidersValueGetter[TReference, TMetadata]
	VisibleTargetValueGetter[TReference, TMetadata]
}

// checkRequiredProviders checks whether a target yields all providers
// of at least one of the provider sets that are required by a label
// attr, as specified through attr.label(providers = ...). Like Bazel,
// this check should only be performed against rule targets.
func checkRequiredProviders(
	resolvedLabel string,
	providerInstances []*model_starlark_pb.Struct,
	requiredProviders []*model_starlark_pb.ProviderSet,
) error {
	// Empty sets are satisfied by any target.
	for _, providerSet := range requiredProviders {
		if len(providerSet.GetProviderIdentifiers()) == 0 {
			return nil
		}
	}

	// Both the provider instances and the identifiers in each of the
	// provider sets are sorted. Perform a single forward pass over
	// the provider instances, while keeping track of the next
	// provider to match for each set. Sets for which a provider is
	// found to be missing are marked with a negative index.
	nextIndices := make([]int, len(requiredProviders))
	for _, providerInstance := range providerInstances {
		providerIdentifier := providerInstance.GetProviderInstanceProperties().GetProviderIdentifier()
		for i, providerSet := range requiredProviders {
			nextIndex := nextIndices[i]
			if nextIndex < 0 {
				continue
			}
			switch strings.Compare(providerSet.GetProviderIdentifiers()[nextIndex], providerIdentifier) {
			case -1:
				// The provider to match sorts before the
				// current provider instance, meaning it
				// is not present.
				nextIndices[i] = -1
			case 0:
				nextIndex++
				if nextIndex == len(providerSet.GetProviderIdentifiers()) {
					return nil
				}
				nextIndices[i] = nextIndex
			}
		}
	}

	sets := make([]string, 0, len(requiredProviders))
	for _, providerSet := range requiredProviders {
		sets = append(sets, "["+strings.Join(providerSet.GetProviderIdentifiers(), ", ")+"]")
	}
	return fmt.Errorf("target %#v does not have all mandatory providers of any of the sets %s", resolvedLabel, strings.Join(sets, ", "))
}

// newConfiguredTargetReferenceCreator returns a function that can be
// used as the label creator when decoding the value of a label attr. It
// resolves labels, and converts them to references to targets that are
// configured in the output configuration of a single entry of a
// labelAttrConfiguration.
//
// If any dependencies are missing, missingDependencies is set, and None
// is returned. Targets that don't meet the requirements of the label
// attr cause an error to be returned. If onConfiguredTarget is
// provided, it is called for every target that is referenced.
//
// Aspects that are provided are applied to all targets that are
// referenced.
func newConfiguredTargetReferenceCreator[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	e configuredTargetReferenceCreatorEnvironment[TReference, TMetadata],
	configuration *labelAttrConfiguration[TReference],
	entry *labelAttrConfigurationEntry[TReference],
	visibilityFromPackage label.CanonicalPackage,
	requirements *labelAttrRequirements[TReference, TMetadata],
	aspects model_core.Message[[]*model_starlark_pb.AppliedAspect, TReference],
	missingDependencies *bool,
	onConfiguredTarget func(resolvedLabel string, providerInstances model_core.Message[[]*model_starlark_pb.Struct, TReference]) error,
) func(label.ResolvedLabel) (eval.Value, error) {
	return func(originalLabel label.ResolvedLabel) (eval.Value, error) {
		canonicalOriginalLabel, err := originalLabel.AsCanonical()
		if err != nil {
			return nil, err
		}

		// Resolve the label, either by using the labels that
		// were resolved previously, or by checking visibility
		// and resolving aliases.
		var resolvedLabelStr string
		if configuration.defaultValueLabelsResolved {
			resolvedLabelStr, err = lookupResolvedLabel(configuration.originalLabels, entry.resolvedLabels, canonicalOriginalLabel.String())
			if err != nil {
				return nil, err
			}
		} else {
			patchedConfigurationReference := model_core.Patch(e, entry.outputConfigurationReference)
			resolvedLabelValue := e.GetVisibleTargetValue(
				model_core.NewPatchedMessage(
					model_analysis_pb.VisibleTarget_Key_builder{
						FromPackage:            visibilityFromPackage.String(),
						ToLabel:                canonicalOriginalLabel.String(),
						ConfigurationReference: patchedConfigurationReference.Message,
					}.Build(),
					patchedConfigurationReference.Patcher,
				),
			)
			if !resolvedLabelValue.IsSet() {
				*missingDependencies = true
				return eval.None, nil
			}
			resolvedLabelStr = resolvedLabelValue.Message.GetLabel()
		}
		if resolvedLabelStr == "" {
			return eval.None, nil
		}
		resolvedLabel, err := label.NewCanonicalLabel(resolvedLabelStr)
		if err != nil {
			return nil, fmt.Errorf("invalid label %#v: %w", resolvedLabelStr, err)
		}

		// Obtain the providers of the target.
		patchedConfigurationReference := model_core.Patch(e, entry.outputConfigurationReference)
		targetProviders := e.GetTargetProvidersValue(
			model_core.NewPatchedMessage(
				model_analysis_pb.TargetProviders_Key_builder{
					Label: resolvedLabelStr,
					Variant: newTargetVariant(
						patchedConfigurationReference.Message,
						model_core.PatchList(e, aspects).Merge(patchedConfigurationReference.Patcher),
					),
				}.Build(),
				patchedConfigurationReference.Patcher,
			),
		)
		if !targetProviders.IsSet() {
			*missingDependencies = true
			return eval.None, nil
		}
		if err := requirements.check(resolvedLabelStr, targetProviders); err != nil {
			return nil, err
		}
		providerInstances := model_core.Nested(targetProviders, targetProviders.Message.GetProviderInstances())
		if onConfiguredTarget != nil {
			if err := onConfiguredTarget(resolvedLabelStr, providerInstances); err != nil {
				return nil, err
			}
		}

		return model_starlark.NewTargetReference(
			originalLabel,
			model_starlark.NewConfiguredTargetReference[TReference, TMetadata](
				resolvedLabel,
				providerInstances,
			),
		), nil
	}
}

// newUnconfiguredTargetReference is a label creator that can be used
// when decoding the value of a label attr whose dependencies should be
// left unconfigured. It returns target references that don't contain
// any providers.
func newUnconfiguredTargetReference[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](originalLabel label.ResolvedLabel) (eval.Value, error) {
	return model_starlark.NewTargetReference[TReference, TMetadata](
		originalLabel,
		/* configured = */ nil,
	), nil
}

// configuredSubrule contains the definition of a subrule and the values
// of its attrs, which are provided to the subrule's implementation
// function as keyword arguments.
type configuredSubrule[TReference any] struct {
	definition model_core.Message[*model_starlark_pb.Subrule_Definition, TReference]
	kwargs     []eval.NamedArg
}

// configureResolvedLabelAttrs converts the values of label attrs that
// always use their default value and whose labels have already been
// resolved to Starlark values. This is the case for the attrs of
// subrules, as computed by ConfiguredRule, and the label attrs of
// aspects, as computed by ConfiguredAspect.
//
// The values are returned as keyword arguments, so that they can be
// provided to the implementation function of a subrule. If files is
// set, the values of ctx.executable, ctx.file and ctx.files are
// collected as well.
func configureResolvedLabelAttrs[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	ctx context.Context,
	e configuredTargetReferenceCreatorEnvironment[TReference, TMetadata],
	thread *eval.Thread,
	starlarkValueEncoders *StarlarkValueEncoders[TReference],
	namedAttrs model_core.Message[[]*model_starlark_pb.NamedAttr, TReference],
	configuredAttrs model_core.Message[[]*model_analysis_pb.ConfiguredLabelAttr, TReference],
	files *labelAttrFiles[TReference, TMetadata],
) ([]eval.NamedArg, error) {
	if len(configuredAttrs.Message) != len(namedAttrs.Message) {
		return nil, fmt.Errorf("%d attrs were provided, while %d configured attrs were provided", len(namedAttrs.Message), len(configuredAttrs.Message))
	}

	missingDependencies := false
	kwargs := make([]eval.NamedArg, 0, len(namedAttrs.Message))
	for i, namedAttr := range namedAttrs.Message {
		labelOptions, isScalar, _ := getLabelAttrOptions(namedAttr.GetAttr())
		requirements, err := newLabelAttrRequirements[TReference, TMetadata](ctx, thread, &starlarkValueEncoders.Readers, namedAttr.GetAttr())
		if err != nil {
			return nil, fmt.Errorf("attr %#v: %w", namedAttr.GetName(), err)
		}
		configuration := newLabelAttrConfigurationFromProto(
			model_core.Nested(configuredAttrs, configuredAttrs.Message[i]),
			labelOptions.GetCfg(),
		)
		if !configuration.defaultValueLabelsResolved {
			return nil, fmt.Errorf("labels in the default value of attr %#v have not been resolved", namedAttr.GetName())
		}
		defaultValue := namedAttr.GetAttr().GetDefault()
		if defaultValue == nil {
			return nil, fmt.Errorf("missing value for mandatory attr %#v", namedAttr.GetName())
		}
		valuePart := model_core.Nested(namedAttrs, defaultValue)
		if err := setLabelAttrConfigurationDefaultValue(ctx, &configuration, &starlarkValueEncoders.Readers, valuePart); err != nil {
			return nil, fmt.Errorf("attr %#v: %w", namedAttr.GetName(), err)
		}

		var filesCollector *labelAttrFilesCollector[TReference, TMetadata]
		var onConfiguredTarget func(string, model_core.Message[[]*model_starlark_pb.Struct, TReference]) error
		if files != nil {
			filesCollector = files.newAttr(namedAttr)
			onConfiguredTarget = filesCollector.addTarget
		}

		var attrBuilder model_starlark.AttrValueBuilder
		if len(configuration.entries) == 0 {
			decodedPart, err := model_starlark.DecodeAttrValuePart[TReference, TMetadata](
				thread,
				valuePart,
				starlarkValueEncoders.newValueDecodingOptions(newUnconfiguredTargetReference[TReference, TMetadata]),
			)
			if err != nil {
				return nil, err
			}
			if err := attrBuilder.Add(thread, decodedPart); err != nil {
				return nil, err
			}
		} else {
			if filesCollector != nil {
				filesCollector.setConfigured()
			}
			for i := range configuration.entries {
				decodedPart, err := model_starlark.DecodeAttrValuePart[TReference, TMetadata](
					thread,
					valuePart,
					starlarkValueEncoders.newValueDecodingOptions(
						newConfiguredTargetReferenceCreator[TReference, TMetadata](
							e,
							&configuration,
							&configuration.entries[i],
							/* visibilityFromPackage = */ label.CanonicalPackage{},
							requirements,
							/* aspects = */ model_core.NewSimpleMessage[TReference]([]*model_starlark_pb.AppliedAspect(nil)),
							&missingDependencies,
							onConfiguredTarget,
						),
					),
				)
				if err != nil {
					return nil, err
				}
				if isScalar && configuration.mayHaveMultipleConfigurations {
					decodedPart, err = decodedPart.AsList(thread /* omitNone = */, false)
					if err != nil {
						return nil, err
					}
				}
				if err := attrBuilder.Add(thread, decodedPart); err != nil {
					return nil, err
				}
			}
		}
		if missingDependencies {
			continue
		}

		attr, err := attrBuilder.Build(thread)
		if err != nil {
			return nil, err
		}
		if attr == nil {
			return nil, fmt.Errorf("value of attr %#v does not have any parts", namedAttr.GetName())
		}
		if filesCollector != nil {
			if err := filesCollector.finish(); err != nil {
				return nil, err
			}
		}
		kwargs = append(kwargs, eval.NamedArg{
			Name:  namedAttr.GetName(),
			Value: attr,
		})
	}
	if missingDependencies {
		return nil, evaluation.ErrMissingDependency
	}
	return kwargs, nil
}
