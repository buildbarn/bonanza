package analysis

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"iter"
	"maps"
	"slices"
	"sort"
	"strings"

	"bonanza.build/pkg/label"
	model_command "bonanza.build/pkg/model/command"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	"bonanza.build/pkg/model/core/inlinedtree"
	model_encoding "bonanza.build/pkg/model/encoding"
	"bonanza.build/pkg/model/evaluation"
	model_filesystem "bonanza.build/pkg/model/filesystem"
	model_format "bonanza.build/pkg/model/format"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_command_pb "bonanza.build/pkg/proto/model/command"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/syntax"
	"bonanza.build/pkg/starlark/unpack"
	"bonanza.build/pkg/storage/object"
	"github.com/buildbarn/bb-storage/pkg/filesystem"
	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
	"github.com/buildbarn/bb-storage/pkg/util"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// ruleTargetAnalysis holds the state that is used while analyzing a
// rule target. The analysis is performed in multiple phases, each of
// which may detect that dependencies are missing. Phases continue
// processing all attrs if dependencies are missing, so that all
// missing dependencies are requested in as few restarts as possible.
type ruleTargetAnalysis[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	computer               *baseComputer[TReference, TMetadata]
	context                context.Context
	environment            ConfiguredTargetEnvironment[TReference, TMetadata]
	thread                 *eval.Thread
	starlarkValueEncoders  *StarlarkValueEncoders[TReference]
	analysisEncoders       *AnalysisEncoders[TReference]
	targetLabel            label.CanonicalLabel
	ruleIdentifier         label.CanonicalStarlarkIdentifier
	ruleDefinition         model_core.Message[*model_starlark_pb.Rule_Definition, TReference]
	ruleTarget             model_core.Message[*model_starlark_pb.RuleTarget, TReference]
	configurationReference model_core.Message[*model_core_pb.DecodableReference, TReference]
	outputRegistrar        *targetOutputRegistrar[TReference, TMetadata]

	missingDependencies bool

	// Values that are provided to transitions and the rule
	// implementation function.
	attrValues               map[string]any
	edgeTransitionAttrValues map[string]any
	files                    *labelAttrFiles[TReference, TMetadata]

	// If set, aspects that are being applied to the rule target,
	// and need to be propagated to the targets referenced by its
	// label attrs, keyed by attr name.
	propagatedAspects func(attrName string) model_core.Message[[]*model_starlark_pb.AppliedAspect, TReference]
	outputsValues     map[string]any
	splitAttrValues   map[string]any
}

// getPropagatedAspects returns the aspects that need to be applied to
// the targets referenced by a label attr of the rule.
func (a *ruleTargetAnalysis[TReference, TMetadata]) getPropagatedAspects(attrName string) model_core.Message[[]*model_starlark_pb.AppliedAspect, TReference] {
	if a.propagatedAspects == nil {
		return model_core.NewSimpleMessage[TReference]([]*model_starlark_pb.AppliedAspect(nil))
	}
	return a.propagatedAspects(attrName)
}

// getAspectParameters computes the parameters of an aspect that is
// applied to the targets referenced by a label attr of the rule. The
// values are taken from the attrs of the rule target with the same
// names as the public attrs of the aspect.
func (a *ruleTargetAnalysis[TReference, TMetadata]) getAspectParameters(
	aspect model_core.Message[*model_analysis_pb.LabelAttrAspects_Value_Aspect, TReference],
	valueEncodingOptions *model_starlark.ValueEncodingOptions[TReference, TMetadata],
) (*model_core.Decodable[model_core.CreatedObject[TMetadata]], error) {
	return encodeAspectParameters(
		a.thread,
		a.computer.referenceFormat,
		a.analysisEncoders.Encoder,
		valueEncodingOptions,
		aspect,
		func(publicAttr *model_starlark_pb.NamedAttr) (eval.Value, error) {
			attrValue, ok := a.attrValues[publicAttr.Name]
			if !ok {
				return nil, fmt.Errorf("aspect %#v requires rule %#v to have attr %#v", aspect.Message.Identifier, a.ruleIdentifier.String(), publicAttr.Name)
			}
			value, ok := attrValue.(eval.Value)
			if !ok {
				return nil, fmt.Errorf("value of attr %#v is not a Starlark value", publicAttr.Name)
			}
			return value, nil
		},
	)
}

// getLabelAttrAspects returns the aspects that need to be applied to
// the targets referenced by a label attr of the rule. These are the
// aspects that are propagated along the attr, followed by the aspects
// listed in attr.label(aspects = ...). Aspects that occur multiple
// times with identical parameters are only applied once.
func (a *ruleTargetAnalysis[TReference, TMetadata]) getLabelAttrAspects(namedAttr *model_starlark_pb.NamedAttr) (model_core.Message[[]*model_starlark_pb.AppliedAspect, TReference], error) {
	propagatedAspects := a.getPropagatedAspects(namedAttr.Name)
	labelOptions, _, _ := getLabelAttrOptions(namedAttr.Attr)
	attrAspectIdentifiers := labelOptions.GetAspects()
	if len(attrAspectIdentifiers) == 0 {
		return propagatedAspects, nil
	}

	e := a.environment
	labelAttrAspects := e.GetLabelAttrAspectsValue(&model_analysis_pb.LabelAttrAspects_Key{
		AspectIdentifiers: attrAspectIdentifiers,
	})
	valueEncodingOptionsFactory, err := a.computer.newValueEncodingOptionsFactory(e, a.starlarkValueEncoders)
	if err != nil {
		return model_core.Message[[]*model_starlark_pb.AppliedAspect, TReference]{}, err
	}
	if !labelAttrAspects.IsSet() {
		return model_core.Message[[]*model_starlark_pb.AppliedAspect, TReference]{}, evaluation.ErrMissingDependency
	}

	// Compute the parameters of the aspects listed by the attr.
	valueEncodingOptions := valueEncodingOptionsFactory.newValueEncodingOptions(a.context, nil)
	attrAspectParameters := make([]*model_core.Decodable[model_core.CreatedObject[TMetadata]], 0, len(labelAttrAspects.Message.Aspects))
	for _, aspect := range labelAttrAspects.Message.Aspects {
		parameters, err := a.getAspectParameters(model_core.Nested(labelAttrAspects, aspect), valueEncodingOptions)
		if err != nil {
			return model_core.Message[[]*model_starlark_pb.AppliedAspect, TReference]{}, err
		}
		attrAspectParameters = append(attrAspectParameters, parameters)
	}

	patchedAspects, err := model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) ([]*model_starlark_pb.AppliedAspect, error) {
		aspects := model_core.PatchList(e, propagatedAspects).Merge(patcher)
		for i, aspect := range labelAttrAspects.Message.Aspects {
			appliedAspect := &model_starlark_pb.AppliedAspect{
				Identifier: aspect.Identifier,
			}
			if parameters := attrAspectParameters[i]; parameters != nil {
				reference, err := patcher.CaptureAndAddDecodableReference(a.context, *parameters, e)
				if err != nil {
					return nil, err
				}
				appliedAspect.Parameters = reference
			}
			aspects = append(aspects, appliedAspect)
		}
		return aspects, nil
	})
	if err != nil {
		return model_core.Message[[]*model_starlark_pb.AppliedAspect, TReference]{}, err
	}
	aspects := model_core.Unpatch(e, patchedAspects).Decay()

	// Remove duplicate aspects. It is an error for an aspect to
	// occur multiple times with different parameters.
	type seenAspect struct {
		hasParameters bool
		parameters    model_core.Decodable[TReference]
	}
	seenAspects := make(map[string]seenAspect, len(aspects.Message))
	deduplicatedAspects := make([]*model_starlark_pb.AppliedAspect, 0, len(aspects.Message))
	for _, aspect := range aspects.Message {
		var current seenAspect
		if aspect.Parameters != nil {
			parameters, err := model_core.FlattenDecodableReference(model_core.Nested(aspects, aspect.Parameters))
			if err != nil {
				return model_core.Message[[]*model_starlark_pb.AppliedAspect, TReference]{}, fmt.Errorf("invalid parameters of aspect %#v: %w", aspect.Identifier, err)
			}
			current = seenAspect{hasParameters: true, parameters: parameters}
		}
		if previous, ok := seenAspects[aspect.Identifier]; ok {
			if previous.hasParameters != current.hasParameters ||
				(current.hasParameters &&
					(previous.parameters.Value.GetLocalReference() != current.parameters.Value.GetLocalReference() ||
						!bytes.Equal(previous.parameters.GetDecodingParameters(), current.parameters.GetDecodingParameters()))) {
				return model_core.Message[[]*model_starlark_pb.AppliedAspect, TReference]{}, fmt.Errorf("aspect %#v is applied multiple times with different parameters", aspect.Identifier)
			}
			continue
		}
		seenAspects[aspect.Identifier] = current
		deduplicatedAspects = append(deduplicatedAspects, aspect)
	}
	return model_core.Nested(aspects, deduplicatedAspects), nil
}

// attrs iterates over all attrs of the rule, providing the value of the
// attr in the rule target if the attr is public.
func (a *ruleTargetAnalysis[TReference, TMetadata]) attrs() iter.Seq2[*model_starlark_pb.NamedAttr, model_core.Message[*model_starlark_pb.RuleTarget_PublicAttrValue, TReference]] {
	return func(yield func(*model_starlark_pb.NamedAttr, model_core.Message[*model_starlark_pb.RuleTarget_PublicAttrValue, TReference]) bool) {
		// The number of public attr values has already been
		// validated by getConfigurationFreeAttrValues().
		publicAttrValues := a.ruleTarget.Message.PublicAttrValues
		for _, namedAttr := range a.ruleDefinition.Message.Attrs {
			var publicAttrValue *model_starlark_pb.RuleTarget_PublicAttrValue
			if !strings.HasPrefix(namedAttr.Name, "_") {
				publicAttrValue = publicAttrValues[0]
				publicAttrValues = publicAttrValues[1:]
			}
			if !yield(namedAttr, model_core.Nested(a.ruleTarget, publicAttrValue)) {
				return
			}
		}
	}
}

// configureNonLabelAttrs computes the values of attrs that are not
// label attrs, but whose values depend on the configuration due to them
// using select(). Values of output attrs are registered as outputs of
// the target.
func (a *ruleTargetAnalysis[TReference, TMetadata]) configureNonLabelAttrs() error {
	targetPackage := a.targetLabel.GetCanonicalPackage()
	thread := a.thread
	for namedAttr, publicAttrValue := range a.attrs() {
		if _, ok := a.attrValues[namedAttr.Name]; ok {
			// Attr was already computed previously.
			continue
		}
		if _, _, isLabelAttr := getLabelAttrOptions(namedAttr.Attr); isLabelAttr {
			continue
		}

		valueParts, _, err := getAttrValueParts(
			a.environment,
			a.configurationReference,
			targetPackage,
			model_core.Nested(a.ruleDefinition, namedAttr),
			publicAttrValue,
		)
		if err != nil {
			if errors.Is(err, evaluation.ErrMissingDependency) {
				a.missingDependencies = true
				continue
			}
			return err
		}

		var attrValueBuilder model_starlark.AttrValueBuilder
		var attrOutputs []eval.Value
		for _, valuePart := range valueParts.Message {
			decodedPart, err := model_starlark.DecodeAttrValuePart[TReference, TMetadata](
				thread, model_core.Nested(valueParts, valuePart),
				a.starlarkValueEncoders.newValueDecodingOptions(func(resolvedLabel label.ResolvedLabel) (eval.Value, error) {
					switch namedAttr.Attr.GetType().(type) {
					case *model_starlark_pb.Attr_Output, *model_starlark_pb.Attr_OutputList:
						canonicalLabel, err := resolvedLabel.AsCanonical()
						if err != nil {
							return nil, err
						}
						canonicalPackage := canonicalLabel.GetCanonicalPackage()
						if canonicalPackage != targetPackage {
							return nil, fmt.Errorf("output attr %#v contains to label %#v, which refers to a different package", namedAttr.Name, canonicalLabel.String())
						}
						f, err := a.outputRegistrar.registerOutput(canonicalLabel.GetTargetName(), nil, model_starlark_pb.File_Owner_FILE)
						if err != nil {
							return nil, fmt.Errorf("output attr %#v: %w", namedAttr.Name, err)
						}
						attrOutputs = append(attrOutputs, f)
						return model_starlark.NewLabel[TReference, TMetadata](resolvedLabel), nil
					default:
						return nil, fmt.Errorf("value of attr %#v contains labels, which is not expected for this type", namedAttr.Name)
					}
				}),
			)
			if err != nil {
				return err
			}
			if err := attrValueBuilder.Add(thread, decodedPart); err != nil {
				return err
			}
		}
		attrValue, err := attrValueBuilder.Build(thread)
		if err != nil {
			return err
		}
		a.attrValues[namedAttr.Name] = attrValue
		a.edgeTransitionAttrValues[namedAttr.Name] = attrValue

		switch namedAttr.Attr.GetType().(type) {
		case *model_starlark_pb.Attr_Output:
			switch len(attrOutputs) {
			case 0:
				a.outputsValues[namedAttr.Name] = eval.None
			case 1:
				a.outputsValues[namedAttr.Name] = attrOutputs[0]
			default:
				return fmt.Errorf("value of attr %#v contains multiple labels, which is not expected for attrs of type output", namedAttr.Name)
			}
		case *model_starlark_pb.Attr_OutputList:
			a.outputsValues[namedAttr.Name] = eval.NewList(attrOutputs)
		}
	}
	return nil
}

// stringsToStarlarkList converts a list of Go strings to a Starlark
// list of strings.
func stringsToStarlarkList(l []string) eval.Value {
	values := make([]eval.Value, 0, len(l))
	for _, s := range l {
		values = append(values, eval.String(s))
	}
	return eval.NewList(values)
}

var commandLineOptionFeaturesLabel = "@@bazel_tools+//command_line_option:features"

// getFeatures computes the features that are enabled and disabled for
// the rule target, which are exposed through ctx.features and
// ctx.disabled_features. This is done by merging the features provided
// on the command line, through repo() and package(), and through the
// target's "features" attr. Rules that don't have a "features" attr
// don't have any features enabled or disabled.
func (a *ruleTargetAnalysis[TReference, TMetadata]) getFeatures() (model_starlark.FeatureSet, error) {
	targetFeaturesValue, ok := a.attrValues["features"]
	if !ok {
		return model_starlark.FeatureSet{}, nil
	}
	var targetFeatures []string
	if err := unpack.List(unpack.String).UnpackInto(a.thread, targetFeaturesValue.(eval.Value), &targetFeatures); err != nil {
		return model_starlark.FeatureSet{}, fmt.Errorf("attr \"features\": %w", err)
	}

	valueReaders := &a.starlarkValueEncoders.Readers
	commandLineFeaturesValue, err := a.computer.getBuildSettingValue(
		a.context,
		a.environment,
		&a.analysisEncoders.Readers,
		valueReaders,
		a.targetLabel.GetCanonicalPackage(),
		commandLineOptionFeaturesLabel,
		a.configurationReference,
	)
	if err != nil {
		if errors.Is(err, evaluation.ErrMissingDependency) {
			a.missingDependencies = true
			return model_starlark.FeatureSet{}, nil
		}
		return model_starlark.FeatureSet{}, fmt.Errorf("build setting %s: %w", commandLineOptionFeaturesLabel, err)
	}
	commandLineFeaturesList, ok := commandLineFeaturesValue.Message.Kind.(*model_starlark_pb.Value_List)
	if !ok {
		return model_starlark.FeatureSet{}, fmt.Errorf("value of build setting %s is not a list", commandLineOptionFeaturesLabel)
	}
	var commandLineFeatures []string
	var errIter error
	for element := range model_starlark.AllListLeafElements(
		a.context,
		valueReaders.List,
		model_core.Nested(commandLineFeaturesValue, commandLineFeaturesList.List.Elements),
		&errIter,
	) {
		feature, ok, err := model_starlark.GetString(a.context, valueReaders.FileReader, element, maximumLargeStringSizeBytes)
		if err != nil {
			return model_starlark.FeatureSet{}, fmt.Errorf("value of build setting %s: %w", commandLineOptionFeaturesLabel, err)
		}
		if !ok {
			return model_starlark.FeatureSet{}, fmt.Errorf("value of build setting %s contains an element that is not a string", commandLineOptionFeaturesLabel)
		}
		commandLineFeatures = append(commandLineFeatures, feature)
	}
	if errIter != nil {
		return model_starlark.FeatureSet{}, fmt.Errorf("value of build setting %s: %w", commandLineOptionFeaturesLabel, errIter)
	}

	inheritableAttrs := a.ruleTarget.Message.InheritableAttrs
	inheritedFeatures, err := model_starlark.NewFeatureSet(inheritableAttrs.GetEnabledFeatures(), inheritableAttrs.GetDisabledFeatures())
	if err != nil {
		return model_starlark.FeatureSet{}, fmt.Errorf("features provided to repo() and package(): %w", err)
	}
	return model_starlark.ParseFeatures(commandLineFeatures).
		Merge(inheritedFeatures).
		Merge(model_starlark.ParseFeatures(targetFeatures)), nil
}

// configureLabelAttrs computes the values of all label attrs. Outgoing
// edge transitions and the labels in default values are obtained from
// ConfiguredRuleUsage and ConfiguredRule if possible. Otherwise they
// are computed for this target specifically.
//
// usageLabelAttrs is empty if ConfiguredRuleUsage was not needed.
func (a *ruleTargetAnalysis[TReference, TMetadata]) configureLabelAttrs(
	ruleLabelAttrs model_core.Message[[]*model_analysis_pb.IndexedConfiguredLabelAttr, TReference],
	usageLabelAttrs model_core.Message[[]*model_analysis_pb.IndexedConfiguredLabelAttr, TReference],
	execGroupPlatformLabels map[string]string,
) error {
	ruleLabelAttrsCursor := newIndexedConfiguredLabelAttrCursor(ruleLabelAttrs)
	usageLabelAttrsCursor := newIndexedConfiguredLabelAttrCursor(usageLabelAttrs)
	edgeTransitionAttrValuesStruct := model_starlark.NewStructFromDict[TReference, TMetadata](nil, a.edgeTransitionAttrValues)
	attrIndex := -1
	for namedAttr, publicAttrValue := range a.attrs() {
		attrIndex++
		labelOptions, isScalar, isLabelAttr := getLabelAttrOptions(namedAttr.Attr)
		if !isLabelAttr {
			continue
		}
		if labelOptions == nil {
			return fmt.Errorf("attr %#v does not have label options", namedAttr.Name)
		}
		usageLabelAttr, hasUsageLabelAttr, err := usageLabelAttrsCursor.get(attrIndex)
		if err != nil {
			return fmt.Errorf("configured rule usage: %w", err)
		}
		ruleLabelAttr, hasRuleLabelAttr, err := ruleLabelAttrsCursor.get(attrIndex)
		if err != nil {
			return fmt.Errorf("configured rule: %w", err)
		}

		// Both ConfiguredRuleUsage and ConfiguredRule may contain
		// an entry for the same attr. In that case the entry in
		// ConfiguredRuleUsage has the labels in the default value
		// resolved, so it takes precedence.
		var configuration labelAttrConfiguration[TReference]
		if hasUsageLabelAttr {
			// Outgoing edge transition was applied, or labels
			// in the default value were resolved as part of
			// ConfiguredRuleUsage.
			configuration = newLabelAttrConfigurationFromProto(usageLabelAttr, labelOptions.Cfg)
		} else if hasRuleLabelAttr {
			// Outgoing edge transition was applied as part of
			// ConfiguredRule.
			configuration = newLabelAttrConfigurationFromProto(ruleLabelAttr, labelOptions.Cfg)
		} else {
			// Outgoing edge transition depends on attrs that
			// are explicitly set by this target. User defined
			// transitions get access to all non-label attr
			// values.
			patchedTransition, mayHaveMultipleConfigurations, err := a.computer.performTransition(
				a.context,
				a.environment,
				a.analysisEncoders,
				a.starlarkValueEncoders,
				model_core.Nested(a.ruleDefinition, labelOptions.Cfg),
				a.configurationReference,
				edgeTransitionAttrValuesStruct,
				execGroupPlatformLabels,
			)
			if err != nil {
				if errors.Is(err, evaluation.ErrMissingDependency) {
					a.missingDependencies = true
					continue
				}
				return fmt.Errorf("attr %#v: %w", namedAttr.Name, err)
			}
			configuration = newLabelAttrConfigurationFromTransition(
				model_core.Unpatch(a.environment, patchedTransition).Decay(),
				mayHaveMultipleConfigurations,
			)
		}

		if err := a.configureLabelAttr(namedAttr, isScalar, publicAttrValue, &configuration); err != nil {
			return err
		}
	}
	if err := usageLabelAttrsCursor.finish(); err != nil {
		return fmt.Errorf("configured rule usage: %w", err)
	}
	if err := ruleLabelAttrsCursor.finish(); err != nil {
		return fmt.Errorf("configured rule: %w", err)
	}
	return nil
}

// configureLabelAttr computes the value of a single label attr, and
// the corresponding values of ctx.executable, ctx.file, ctx.files and
// ctx.split_attr.
func (a *ruleTargetAnalysis[TReference, TMetadata]) configureLabelAttr(
	namedAttr *model_starlark_pb.NamedAttr,
	isScalar bool,
	publicAttrValue model_core.Message[*model_starlark_pb.RuleTarget_PublicAttrValue, TReference],
	configuration *labelAttrConfiguration[TReference],
) error {
	filesCollector := a.files.newAttr(namedAttr)
	aspects, err := a.getLabelAttrAspects(namedAttr)
	if err != nil {
		if errors.Is(err, evaluation.ErrMissingDependency) {
			a.missingDependencies = true
			return nil
		}
		return fmt.Errorf("attr %#v: %w", namedAttr.Name, err)
	}

	requirements, err := newLabelAttrRequirements[TReference, TMetadata](a.context, a.thread, &a.starlarkValueEncoders.Readers, namedAttr.Attr)
	if err != nil {
		return fmt.Errorf("attr %#v: %w", namedAttr.Name, err)
	}

	thread := a.thread
	targetPackage := a.targetLabel.GetCanonicalPackage()
	missingDependencies := false
	var attrValueBuilder model_starlark.AttrValueBuilder
	var splitAttrEntries [][2]eval.Value
	if len(configuration.entries) == 0 {
		// Leave targets unconfigured. Perform select() without
		// a configuration.
		valueParts, _, err := getAttrValueParts(
			a.environment,
			model_core.NewSimpleMessage[TReference]((*model_core_pb.DecodableReference)(nil)),
			targetPackage,
			model_core.Nested(a.ruleDefinition, namedAttr),
			publicAttrValue,
		)
		if err != nil {
			if errors.Is(err, evaluation.ErrMissingDependency) {
				a.missingDependencies = true
				return nil
			}
			return err
		}
		for _, valuePart := range valueParts.Message {
			decodedPart, err := model_starlark.DecodeAttrValuePart[TReference, TMetadata](
				thread, model_core.Nested(valueParts, valuePart),
				a.starlarkValueEncoders.newValueDecodingOptions(newUnconfiguredTargetReference[TReference, TMetadata]),
			)
			if err != nil {
				return err
			}
			if err := attrValueBuilder.Add(thread, decodedPart); err != nil {
				return err
			}
		}
	} else {
		filesCollector.setConfigured()
		if configuration.defaultValueLabelsResolved {
			defaultValue := namedAttr.Attr.GetDefault()
			if defaultValue == nil {
				return fmt.Errorf("missing value for mandatory attr %#v", namedAttr.Name)
			}
			if err := setLabelAttrConfigurationDefaultValue(a.context, configuration, &a.starlarkValueEncoders.Readers, model_core.Nested(a.ruleDefinition, defaultValue)); err != nil {
				return fmt.Errorf("attr %#v: %w", namedAttr.Name, err)
			}
		}
		for i := range configuration.entries {
			entry := &configuration.entries[i]
			var valueParts model_core.Message[[]*model_starlark_pb.Value, TReference]
			var usedDefaultValue bool
			if configuration.defaultValueLabelsResolved {
				// The attr is left at its default value,
				// and all labels contained in it have
				// already been resolved.
				defaultValue := namedAttr.Attr.GetDefault()
				if defaultValue == nil {
					return fmt.Errorf("missing value for mandatory attr %#v", namedAttr.Name)
				}
				valueParts = model_core.Nested(a.ruleDefinition, []*model_starlark_pb.Value{defaultValue})
				usedDefaultValue = true
			} else {
				var err error
				valueParts, usedDefaultValue, err = getAttrValueParts(
					a.environment,
					entry.outputConfigurationReference,
					targetPackage,
					model_core.Nested(a.ruleDefinition, namedAttr),
					publicAttrValue,
				)
				if err != nil {
					if errors.Is(err, evaluation.ErrMissingDependency) {
						a.missingDependencies = true
						return nil
					}
					return err
				}
			}

			// Whether an explicit value or a default attr value
			// is used determines how visibility is computed. For
			// explicit values, visibility is computed relative
			// to the package declaring the target. For default
			// values, the package declaring the rule is used.
			visibilityFromPackage := targetPackage
			if usedDefaultValue {
				visibilityFromPackage = a.ruleIdentifier.GetCanonicalLabel().GetCanonicalPackage()
			}

			valueDecodingOptions := a.starlarkValueEncoders.newValueDecodingOptions(
				newConfiguredTargetReferenceCreator[TReference, TMetadata](
					a.environment,
					configuration,
					entry,
					visibilityFromPackage,
					requirements,
					aspects,
					&missingDependencies,
					filesCollector.addTarget,
				),
			)
			var splitAttrEntryBuilder model_starlark.AttrValueBuilder
			for i, valuePart := range valueParts.Message {
				decodedPart, err := model_starlark.DecodeAttrValuePart[TReference, TMetadata](
					thread, model_core.Nested(valueParts, valuePart),
					valueDecodingOptions,
				)
				if err != nil {
					return fmt.Errorf("decoding attr %#v transition %#v value part %d: %w", namedAttr.Name, entry.key, i, err)
				}
				if isScalar && configuration.mayHaveMultipleConfigurations {
					decodedPart, err = decodedPart.AsList(thread /* omitNone = */, true)
					if err != nil {
						return err
					}
				}
				if err := attrValueBuilder.Add(thread, decodedPart); err != nil {
					return fmt.Errorf("concatenate attr value parts: %w", err)
				}
				if configuration.mayHaveMultipleConfigurations {
					if err := splitAttrEntryBuilder.Add(thread, decodedPart); err != nil {
						return fmt.Errorf("concatenate split attr value parts: %w", err)
					}
				}
			}

			if configuration.mayHaveMultipleConfigurations {
				splitAttrEntry, err := splitAttrEntryBuilder.Build(thread)
				if err != nil {
					return err
				}
				splitAttrEntries = append(splitAttrEntries, [2]eval.Value{eval.String(entry.key), splitAttrEntry})
			}
		}
	}
	if missingDependencies {
		a.missingDependencies = true
		return nil
	}

	attrValue, err := attrValueBuilder.Build(thread)
	if err != nil {
		return err
	}
	a.attrValues[namedAttr.Name] = attrValue

	if configuration.mayHaveMultipleConfigurations {
		immutableSplitAttrValue, err := eval.NewImmutableDict(thread, splitAttrEntries)
		if err != nil {
			return err
		}
		a.splitAttrValues[namedAttr.Name] = immutableSplitAttrValue
	}

	return filesCollector.finish()
}

// configureSubrules computes the values of the attrs of all subrules
// that may be invoked by the rule implementation function. Doing this
// eagerly prevents restarts of the implementation function when
// subrules are invoked.
func (a *ruleTargetAnalysis[TReference, TMetadata]) configureSubrules(configuredSubrules model_core.Message[[]*model_analysis_pb.ConfiguredSubrule, TReference]) (map[string]configuredSubrule[TReference], error) {
	subrules := make(map[string]configuredSubrule[TReference], len(configuredSubrules.Message))
	for _, configuredSubruleValue := range configuredSubrules.Message {
		subruleDefinition, err := getSubruleDefinition(a.environment, configuredSubruleValue.Identifier)
		if err != nil {
			if errors.Is(err, evaluation.ErrMissingDependency) {
				a.missingDependencies = true
				continue
			}
			return nil, err
		}
		kwargs, err := configureResolvedLabelAttrs[TReference, TMetadata](
			a.context,
			a.environment,
			a.thread,
			a.starlarkValueEncoders,
			model_core.Nested(subruleDefinition, subruleDefinition.Message.Attrs),
			model_core.Nested(configuredSubrules, configuredSubruleValue.Attrs),
			/* files = */ nil,
		)
		if err != nil {
			if errors.Is(err, evaluation.ErrMissingDependency) {
				a.missingDependencies = true
				continue
			}
			return nil, fmt.Errorf("subrule %#v: %w", configuredSubruleValue.Identifier, err)
		}
		subrules[configuredSubruleValue.Identifier] = configuredSubrule[TReference]{
			definition: subruleDefinition,
			kwargs:     kwargs,
		}
	}
	return subrules, nil
}

// prefetchToolchains requests the ToolchainInfo providers of all
// resolved toolchains, so that accessing ctx.toolchains does not cause
// any restarts of the implementation function. Any errors are reported
// when the toolchain is accessed.
func (a *ruleTargetAnalysis[TReference, TMetadata]) prefetchToolchains(execGroups []targetContextExecGroupState) {
	for _, execGroup := range execGroups {
		for _, toolchainIdentifier := range execGroup.toolchainIdentifiers {
			if toolchainIdentifier != "" {
				if _, err := getProviderFromConfiguredTarget(
					a.environment,
					toolchainIdentifier,
					model_core.Patch(a.environment, a.configurationReference),
					toolchainInfoProviderIdentifier,
				); errors.Is(err, evaluation.ErrMissingDependency) {
					a.missingDependencies = true
				}
			}
		}
	}
}

// registerSubruleInvoker registers a callback in the Starlark thread
// that is invoked when the implementation function of a rule or aspect
// calls into a subrule. permittedSubruleIdentifiers contains the
// identifiers of the subrules that the rule or aspect declares.
func registerSubruleInvoker[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	thread *eval.Thread,
	tc *targetContext[TReference, TMetadata],
	permittedSubruleIdentifiers []string,
	subrules map[string]configuredSubrule[TReference],
	ruleImplementationWrappers model_core.Message[*model_analysis_pb.RuleImplementationWrappers_Value, TReference],
) {
	// Subrules may only invoke subrules that they declare
	// themselves. Keep track of the stack of (sub)rules that are
	// currently being invoked.
	permittedSubruleIdentifiersStack := [][]string{permittedSubruleIdentifiers}
	thread.SetLocal(model_starlark.SubruleInvokerKey, func(subruleIdentifier label.CanonicalStarlarkIdentifier, args eval.Args) (eval.Value, error) {
		subruleIdentifierStr := subruleIdentifier.String()
		if !slices.Contains(permittedSubruleIdentifiersStack[len(permittedSubruleIdentifiersStack)-1], subruleIdentifierStr) {
			return nil, fmt.Errorf("subrule %#v cannot be invoked from within the current rule, aspect or subrule", subruleIdentifierStr)
		}
		subrule, ok := subrules[subruleIdentifierStr]
		if !ok {
			return nil, fmt.Errorf("subrule %#v was not configured", subruleIdentifierStr)
		}

		implementationArgs := append(
			[]eval.Value{
				model_starlark.DecodeFunction[TReference, TMetadata](model_core.Nested(subrule.definition, subrule.definition.Message.Implementation)),
				&subruleContext[TReference, TMetadata]{targetContext: tc},
			},
			args.Positional...,
		)
		implementationKwargs := append(
			append(
				make([]eval.NamedArg, 0, len(args.Named)+len(subrule.kwargs)),
				args.Named...,
			),
			subrule.kwargs...,
		)

		permittedSubruleIdentifiersStack = append(permittedSubruleIdentifiersStack, subrule.definition.Message.SubruleIdentifiers)
		defer func() {
			permittedSubruleIdentifiersStack = permittedSubruleIdentifiersStack[:len(permittedSubruleIdentifiersStack)-1]
		}()
		return eval.Call(thread, decodeImplementationWrapper[TReference, TMetadata](model_core.Nested(ruleImplementationWrappers, ruleImplementationWrappers.Message.Subrule)), eval.Args{Positional: implementationArgs, Named: implementationKwargs})
	})
}

// invokeImplementation invokes the rule implementation function, and
// returns the provider instances that it returned.
func (a *ruleTargetAnalysis[TReference, TMetadata]) invokeImplementation(
	rc *ruleContext[TReference, TMetadata],
	ruleImplementationWrappers model_core.Message[*model_analysis_pb.RuleImplementationWrappers_Value, TReference],
) ([]*model_starlark.Struct[TReference, TMetadata], error) {
	// Instead of calling the implementation function directly, we
	// call the rule implementation wrapper function, having both the
	// actual implementation function and ctx as arguments.
	thread := a.thread
	ruleDefinition := a.ruleDefinition
	returnValue, err := eval.Call(thread, decodeImplementationWrapper[TReference, TMetadata](model_core.Nested(ruleImplementationWrappers, ruleImplementationWrappers.Message.Rule)), eval.Args{Positional: /* args = */ []eval.Value{
		eval.NewBuiltin("current_ctx_capturer", func(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
			// The rule implementation wrapper function may
			// augment ctx. Capture it, so that we can let
			// native.current_ctx() return it.
			var currentCtx eval.Value
			if err := eval.UnpackArgs(
				thread, b.Name(), args,
				"ctx", &currentCtx,
			); err != nil {
				return nil, err
			}
			thread.SetLocal(model_starlark.CurrentCtxKey, currentCtx)

			return eval.Call(
				thread,
				model_starlark.DecodeFunction[TReference, TMetadata](model_core.Nested(ruleDefinition, ruleDefinition.Message.Implementation)),
				args,
			)
		}),
		rc,
	}})
	if err != nil {
		return nil, formatStarlarkError(err)
	}

	// Bazel permits returning either a single provider, or a list of
	// providers.
	var providerInstances []*model_starlark.Struct[TReference, TMetadata]
	structUnpackerInto := unpack.Type[*model_starlark.Struct[TReference, TMetadata]]("struct")
	if err := unpack.IfNotNone(
		unpack.Or([]unpack.UnpackerInto[[]*model_starlark.Struct[TReference, TMetadata]]{
			unpack.Singleton(structUnpackerInto),
			unpack.List(structUnpackerInto),
		}),
	).UnpackInto(thread, returnValue, &providerInstances); err != nil {
		return nil, fmt.Errorf("failed to unpack implementation function return value: %w", err)
	}
	return providerInstances, nil
}

// configureRuleTargetAttrs computes the values of the attrs of a rule
// target, and the state of its exec groups. This is used both when
// analyzing the rule target itself, and when applying aspects to it, as
// aspects have access to the attrs of the rule target through
// ctx.rule.attr. In the latter case, propagatedAspects can be used to
// apply aspects to the targets referenced by label attrs.
//
// The ToolchainInfo providers of the toolchains of the exec groups are
// not requested, as aspects access the toolchains of the rule target
// as targets through ctx.rule.toolchains. Callers that need these
// providers should call prefetchToolchains().
//
// Dependencies that are missing either cause ErrMissingDependency to be
// returned, or the missingDependencies field of the returned
// ruleTargetAnalysis to be set. The latter allows callers to request
// additional dependencies before restarting.
func (c *baseComputer[TReference, TMetadata]) configureRuleTargetAttrs(
	ctx context.Context,
	e ConfiguredTargetEnvironment[TReference, TMetadata],
	thread *eval.Thread,
	starlarkValueEncoders *StarlarkValueEncoders[TReference],
	analysisEncoders *AnalysisEncoders[TReference],
	targetLabel label.CanonicalLabel,
	ruleIdentifier label.CanonicalStarlarkIdentifier,
	ruleDefinition model_core.Message[*model_starlark_pb.Rule_Definition, TReference],
	ruleTarget model_core.Message[*model_starlark_pb.RuleTarget, TReference],
	configurationReference model_core.Message[*model_core_pb.DecodableReference, TReference],
	outputRegistrar *targetOutputRegistrar[TReference, TMetadata],
	propagatedAspects func(attrName string) model_core.Message[[]*model_starlark_pb.AppliedAspect, TReference],
) (*ruleTargetAnalysis[TReference, TMetadata], model_core.Message[*model_analysis_pb.ConfiguredRule_Value, TReference], []targetContextExecGroupState, error) {
	attrValues, edgeTransitionAttrValues, err := getConfigurationFreeAttrValues[TReference, TMetadata](
		thread,
		starlarkValueEncoders,
		targetLabel,
		ruleTarget,
		ruleDefinition,
	)
	if err != nil {
		return nil, model_core.Message[*model_analysis_pb.ConfiguredRule_Value, TReference]{}, nil, err
	}

	a := &ruleTargetAnalysis[TReference, TMetadata]{
		computer:               c,
		context:                ctx,
		environment:            e,
		thread:                 thread,
		starlarkValueEncoders:  starlarkValueEncoders,
		analysisEncoders:       analysisEncoders,
		targetLabel:            targetLabel,
		ruleIdentifier:         ruleIdentifier,
		ruleDefinition:         ruleDefinition,
		ruleTarget:             ruleTarget,
		configurationReference: configurationReference,
		outputRegistrar:        outputRegistrar,

		attrValues:               attrValues,
		edgeTransitionAttrValues: edgeTransitionAttrValues,
		files:                    newLabelAttrFiles[TReference, TMetadata](ctx, thread, &starlarkValueEncoders.Readers),
		outputsValues:            map[string]any{},
		splitAttrValues:          map[string]any{},
		propagatedAspects:        propagatedAspects,
	}

	// Obtain properties of the rule that are shared with other
	// targets, and the values of non-label attrs that use
	// select(). These are all independent of each other, so
	// request them at the same time.
	missingConfiguredRuleUsage := false
	var usageLabelAttrs model_core.Message[[]*model_analysis_pb.IndexedConfiguredLabelAttr, TReference]
	if explicitPublicAttrIndices, needed := getConfiguredRuleUsageExplicitPublicAttrIndices(ruleDefinition.Message, ruleTarget.Message.PublicAttrValues); needed {
		patchedConfigurationReference := model_core.Patch(e, configurationReference)
		configuredRuleUsage := e.GetConfiguredRuleUsageValue(
			model_core.NewPatchedMessage(
				&model_analysis_pb.ConfiguredRuleUsage_Key{
					Identifier:                ruleIdentifier.String(),
					ConfigurationReference:    patchedConfigurationReference.Message,
					ExplicitPublicAttrIndices: explicitPublicAttrIndices,
				},
				patchedConfigurationReference.Patcher,
			),
		)
		if configuredRuleUsage.IsSet() {
			usageLabelAttrs = model_core.Nested(configuredRuleUsage, configuredRuleUsage.Message.LabelAttrs)
		} else {
			missingConfiguredRuleUsage = true
		}
	}
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
	if err := a.configureNonLabelAttrs(); err != nil {
		return nil, model_core.Message[*model_analysis_pb.ConfiguredRule_Value, TReference]{}, nil, err
	}
	if missingConfiguredRuleUsage || !configuredRule.IsSet() || a.missingDependencies {
		return nil, model_core.Message[*model_analysis_pb.ConfiguredRule_Value, TReference]{}, nil, evaluation.ErrMissingDependency
	}

	execGroupPlatformLabels, err := getExecGroupPlatformLabels(ruleDefinition.Message, configuredRule)
	if err != nil {
		return nil, model_core.Message[*model_analysis_pb.ConfiguredRule_Value, TReference]{}, nil, err
	}
	execGroups := make([]targetContextExecGroupState, 0, len(configuredRule.Message.ExecGroups))
	for _, execGroup := range configuredRule.Message.ExecGroups {
		execGroups = append(execGroups, targetContextExecGroupState{
			platformPkixPublicKey: execGroup.PlatformPkixPublicKey,
			toolchainIdentifiers:  execGroup.ToolchainIdentifiers,
			toolchainInfos:        make([]eval.Value, len(execGroup.ToolchainIdentifiers)),
		})
	}

	// Obtain the values of label attrs.
	if err := a.configureLabelAttrs(
		model_core.Nested(configuredRule, configuredRule.Message.LabelAttrs),
		usageLabelAttrs,
		execGroupPlatformLabels,
	); err != nil {
		return nil, model_core.Message[*model_analysis_pb.ConfiguredRule_Value, TReference]{}, nil, err
	}
	if err := a.checkAttrsNonEmpty(); err != nil {
		return nil, model_core.Message[*model_analysis_pb.ConfiguredRule_Value, TReference]{}, nil, err
	}
	return a, configuredRule, execGroups, nil
}

// checkAttrsNonEmpty returns an error if any of the attrs declared
// with allow_empty = False has an empty value. This check is performed
// after select() has been resolved, as that is the earliest point at
// which the value of a configurable attr is known.
func (a *ruleTargetAnalysis[TReference, TMetadata]) checkAttrsNonEmpty() error {
	for _, namedAttr := range a.ruleDefinition.Message.Attrs {
		compositeOptions, ok := model_starlark.GetAttrCompositeOptions(namedAttr.Attr)
		if !ok || compositeOptions.GetAllowEmpty() {
			continue
		}
		attrValue, ok := a.attrValues[namedAttr.Name].(interface {
			Len(*eval.Thread) (int, error)
		})
		if !ok {
			// Value was not computed due to missing
			// dependencies, or it is None.
			continue
		}
		n, err := attrValue.Len(a.thread)
		if err != nil {
			return fmt.Errorf("attr %#v: %w", namedAttr.Name, err)
		}
		if n == 0 {
			return fmt.Errorf("attr %#v must be non empty", namedAttr.Name)
		}
	}
	return nil
}

// computeRuleTargetValue analyzes a rule target by invoking the rule's
// implementation function.
func (c *baseComputer[TReference, TMetadata]) computeRuleTargetValue(
	ctx context.Context,
	key model_core.Message[*model_analysis_pb.ConfiguredTarget_Key, TReference],
	e ConfiguredTargetEnvironment[TReference, TMetadata],
	targetLabel label.CanonicalLabel,
	ruleTarget model_core.Message[*model_starlark_pb.RuleTarget, TReference],
	analysisEncoders *AnalysisEncoders[TReference],
	starlarkValueEncoders *StarlarkValueEncoders[TReference],
	valueEncodingOptionsFactory *valueEncodingOptionsFactory[TReference, TMetadata],
	emptyDefaultInfo model_core.Message[*model_starlark_pb.Struct, TReference],
) (PatchedConfiguredTargetValue[TMetadata], error) {
	ruleIdentifier, err := label.NewCanonicalStarlarkIdentifier(ruleTarget.Message.RuleIdentifier)
	if err != nil {
		return PatchedConfiguredTargetValue[TMetadata]{}, err
	}

	allBuiltinsModulesNames := e.GetBuiltinsModuleNamesValue(&model_analysis_pb.BuiltinsModuleNames_Key{})
	actionEncoders, gotActionEncoders := e.GetActionEncodersObject(&model_analysis_pb.ActionEncoders_Key{})
	directoryCreationParameters, gotDirectoryCreationParameters := e.GetDirectoryCreationParametersObject(&model_analysis_pb.DirectoryCreationParameters_Key{})
	fileCreationParameters, gotFileCreationParameters := e.GetFileCreationParametersObject(&model_analysis_pb.FileCreationParameters_Key{})
	ruleDefinition, ruleDefinitionErr := getRuleDefinition(e, ruleIdentifier)
	ruleImplementationWrappers := e.GetRuleImplementationWrappersValue(&model_analysis_pb.RuleImplementationWrappers_Key{})
	if ruleDefinitionErr != nil && !errors.Is(ruleDefinitionErr, evaluation.ErrMissingDependency) {
		return PatchedConfiguredTargetValue[TMetadata]{}, ruleDefinitionErr
	}
	if ruleDefinitionErr != nil ||
		!allBuiltinsModulesNames.IsSet() ||
		!gotActionEncoders ||
		!gotDirectoryCreationParameters ||
		!gotFileCreationParameters ||
		!ruleImplementationWrappers.IsSet() {
		return PatchedConfiguredTargetValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	thread, err := c.newStarlarkThread(ctx, e, starlarkValueEncoders, allBuiltinsModulesNames.Message.BuiltinsModuleNames)
	if err != nil {
		return PatchedConfiguredTargetValue[TMetadata]{}, err
	}
	configurationReference := model_core.Nested(key, key.Message.Variant.GetConfigurationReference())
	outputRegistrar := &targetOutputRegistrar[TReference, TMetadata]{
		variant:     model_core.Nested(key, key.Message.Variant),
		targetLabel: targetLabel,

		outputsByPackageRelativePath: map[string]*targetOutput[TMetadata]{},
		outputsByFile:                map[*model_starlark.File[TReference, TMetadata]]*targetOutput[TMetadata]{},
	}
	defer func() {
		for _, output := range outputRegistrar.outputsByPackageRelativePath {
			output.definition.Discard()
		}
	}()

	a, configuredRule, execGroups, err := c.configureRuleTargetAttrs(
		ctx,
		e,
		thread,
		starlarkValueEncoders,
		analysisEncoders,
		targetLabel,
		ruleIdentifier,
		ruleDefinition,
		ruleTarget,
		configurationReference,
		outputRegistrar,
		/* propagatedAspects = */ nil,
	)
	if err != nil {
		return PatchedConfiguredTargetValue[TMetadata]{}, err
	}
	a.prefetchToolchains(execGroups)
	subrules, err := a.configureSubrules(model_core.Nested(configuredRule, configuredRule.Message.Subrules))
	if err != nil {
		return PatchedConfiguredTargetValue[TMetadata]{}, err
	}
	features, err := a.getFeatures()
	if err != nil {
		return PatchedConfiguredTargetValue[TMetadata]{}, err
	}
	if a.missingDependencies {
		return PatchedConfiguredTargetValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	rc := &ruleContext[TReference, TMetadata]{
		targetContext: &targetContext[TReference, TMetadata]{
			computer:                    c,
			context:                     ctx,
			environment:                 e,
			valueEncodingOptionsFactory: valueEncodingOptionsFactory,
			starlarkValueEncoders:       starlarkValueEncoders,
			analysisEncoders:            analysisEncoders,
			targetLabel:                 targetLabel,
			configurationReference:      configurationReference,
			namedExecGroups:             model_core.Nested(ruleDefinition, ruleDefinition.Message.ExecGroups),
			execGroups:                  execGroups,
			executableFileToFilesToRun:  a.files.executableFileToFilesToRun,
			outputRegistrar:             outputRegistrar,
			actionEncoder:               actionEncoders.Encoder,
			directoryCreationParameters: directoryCreationParameters,
			fileCreationParameters:      fileCreationParameters,
		},
		ruleIdentifier: ruleIdentifier,
		ruleDefinition: ruleDefinition,
		ruleTarget:     ruleTarget,
		features:       features,
		attr:           model_starlark.NewStructFromDict[TReference, TMetadata](nil, a.attrValues),
		splitAttr:      model_starlark.NewStructFromDict[TReference, TMetadata](nil, a.splitAttrValues),
		executable:     model_starlark.NewStructFromDict[TReference, TMetadata](nil, a.files.executableValues),
		file:           model_starlark.NewStructFromDict[TReference, TMetadata](nil, a.files.fileValues),
		files:          model_starlark.NewStructFromDict[TReference, TMetadata](nil, a.files.filesValues),
		outputs:        model_starlark.NewStructFromDict[TReference, TMetadata](nil, a.outputsValues),
	}
	defer func() {
		rc.actions.Discard()
	}()

	registerSubruleInvoker(thread, rc.targetContext, ruleDefinition.Message.SubruleIdentifiers, subrules, ruleImplementationWrappers)
	identifierGenerator, err := c.getReferenceEqualIdentifierGenerator(model_core.Nested(key, proto.Message(key.Message)))
	if err != nil {
		return PatchedConfiguredTargetValue[TMetadata]{}, err
	}
	thread.SetLocal(model_starlark.ReferenceEqualIdentifierGeneratorKey, identifierGenerator)

	providerInstances, err := a.invokeImplementation(rc, ruleImplementationWrappers)
	if err != nil {
		return PatchedConfiguredTargetValue[TMetadata]{}, err
	}
	return c.encodeRuleTargetValue(ctx, e, thread, analysisEncoders, valueEncodingOptionsFactory, emptyDefaultInfo, providerInstances, ruleDefinition.Message.Provides, outputRegistrar, rc.targetContext)
}

// checkProvides checks whether the implementation function of a rule
// returned instances of all providers that the rule advertises through
// rule(provides = ...).
func checkProvides(providersSeen map[string]struct{}, provides []string) error {
	var missing []string
	for _, providerIdentifier := range provides {
		if _, ok := providersSeen[providerIdentifier]; !ok {
			missing = append(missing, providerIdentifier)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("implementation function did not return providers %s, even though they are listed in provides", strings.Join(missing, ", "))
	}
	return nil
}

// encodeTargetOutputsAndActions converts the outputs and actions that
// were declared by the implementation function of a rule or aspect to
// B-trees that can be stored in a ConfiguredTarget value.
func (c *baseComputer[TReference, TMetadata]) encodeTargetOutputsAndActions(
	ctx context.Context,
	e ConfiguredTargetEnvironment[TReference, TMetadata],
	analysisEncoders *AnalysisEncoders[TReference],
	outputRegistrar *targetOutputRegistrar[TReference, TMetadata],
	tc *targetContext[TReference, TMetadata],
	patcher *model_core.ReferenceMessagePatcher[TMetadata],
) ([]*model_analysis_pb.ConfiguredTarget_Value_Output, []*model_analysis_pb.ConfiguredTarget_Value_Action, error) {
	// Construct list of outputs of the target.
	outputsTreeBuilder := btree.NewHeightAwareBuilder(
		btree.NewProllyChunkerFactory[TMetadata](
			c.chunkingParameters,
			/* isParent = */ func(output *model_analysis_pb.ConfiguredTarget_Value_Output) bool {
				return output.GetParent() != nil
			},
		),
		btree.NewObjectCreatingNodeMerger(
			analysisEncoders.Encoder,
			c.referenceFormat,
			/* parentNodeComputer = */ btree.Capturing(ctx, e, func(createdObject model_core.Decodable[model_core.MetadataEntry[TMetadata]], childNodes model_core.Message[[]*model_analysis_pb.ConfiguredTarget_Value_Output, object.LocalReference]) model_core.PatchedMessage[*model_analysis_pb.ConfiguredTarget_Value_Output, TMetadata] {
				var firstPackageRelativePath string
				switch firstElement := childNodes.Message[0].Level.(type) {
				case *model_analysis_pb.ConfiguredTarget_Value_Output_Leaf_:
					firstPackageRelativePath = firstElement.Leaf.PackageRelativePath
				case *model_analysis_pb.ConfiguredTarget_Value_Output_Parent_:
					firstPackageRelativePath = firstElement.Parent.FirstPackageRelativePath
				}
				return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.ConfiguredTarget_Value_Output {
					return &model_analysis_pb.ConfiguredTarget_Value_Output{
						Level: &model_analysis_pb.ConfiguredTarget_Value_Output_Parent_{
							Parent: &model_analysis_pb.ConfiguredTarget_Value_Output_Parent{
								Reference:                patcher.AddDecodableReference(createdObject),
								FirstPackageRelativePath: firstPackageRelativePath,
							},
						},
					}
				})
			}),
		),
	)
	defer outputsTreeBuilder.Discard()

	outputsByPackageRelativePath := outputRegistrar.outputsByPackageRelativePath
	for _, packageRelativePath := range slices.Sorted(maps.Keys(outputsByPackageRelativePath)) {
		output := outputsByPackageRelativePath[packageRelativePath]
		if !output.definition.IsSet() {
			return nil, nil, fmt.Errorf("file %#v is not an output of any action", packageRelativePath)
		}
		if err := outputsTreeBuilder.PushChild(
			model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.ConfiguredTarget_Value_Output {
				return &model_analysis_pb.ConfiguredTarget_Value_Output{
					Level: &model_analysis_pb.ConfiguredTarget_Value_Output_Leaf_{
						Leaf: &model_analysis_pb.ConfiguredTarget_Value_Output_Leaf{
							PackageRelativePath: packageRelativePath,
							Definition:          output.definition.Merge(patcher),
						},
					},
				}
			}),
		); err != nil {
			return nil, nil, err
		}
	}
	outputsList, err := outputsTreeBuilder.FinalizeList()
	if err != nil {
		return nil, nil, err
	}
	patcher.Merge(outputsList.Patcher)

	// Construct list of actions of the target.
	actionsTreeBuilder := btree.NewHeightAwareBuilder(
		btree.NewProllyChunkerFactory[TMetadata](
			c.chunkingParameters,
			/* isParent = */ func(action *model_analysis_pb.ConfiguredTarget_Value_Action) bool {
				return action.GetParent() != nil
			},
		),
		btree.NewObjectCreatingNodeMerger(
			analysisEncoders.Encoder,
			c.referenceFormat,
			/* parentNodeComputer = */ btree.Capturing(ctx, e, func(createdObject model_core.Decodable[model_core.MetadataEntry[TMetadata]], childNodes model_core.Message[[]*model_analysis_pb.ConfiguredTarget_Value_Action, object.LocalReference]) model_core.PatchedMessage[*model_analysis_pb.ConfiguredTarget_Value_Action, TMetadata] {
				var firstID []byte
				switch firstElement := childNodes.Message[0].Level.(type) {
				case *model_analysis_pb.ConfiguredTarget_Value_Action_Leaf_:
					firstID = firstElement.Leaf.Id
				case *model_analysis_pb.ConfiguredTarget_Value_Action_Parent_:
					firstID = firstElement.Parent.FirstId
				}
				return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.ConfiguredTarget_Value_Action {
					return &model_analysis_pb.ConfiguredTarget_Value_Action{
						Level: &model_analysis_pb.ConfiguredTarget_Value_Action_Parent_{
							Parent: &model_analysis_pb.ConfiguredTarget_Value_Action_Parent{
								Reference: patcher.AddDecodableReference(createdObject),
								FirstId:   firstID,
							},
						},
					}
				})
			}),
		),
	)
	defer actionsTreeBuilder.Discard()

	slices.SortFunc(tc.actions, func(a, b model_core.PatchedMessage[*model_analysis_pb.ConfiguredTarget_Value_Action_Leaf, TMetadata]) int {
		return bytes.Compare(a.Message.Id, b.Message.Id)
	})
	for _, action := range tc.actions {
		if err := actionsTreeBuilder.PushChild(
			model_core.MustBuildPatchedMessage(
				func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.ConfiguredTarget_Value_Action {
					return &model_analysis_pb.ConfiguredTarget_Value_Action{
						Level: &model_analysis_pb.ConfiguredTarget_Value_Action_Leaf_{
							Leaf: action.Merge(patcher),
						},
					}
				},
			),
		); err != nil {
			return nil, nil, err
		}
	}
	actionsList, err := actionsTreeBuilder.FinalizeList()
	if err != nil {
		return nil, nil, err
	}
	patcher.Merge(actionsList.Patcher)

	return outputsList.Message, actionsList.Message, nil
}

// encodeRuleTargetValue converts the provider instances returned by
// the rule implementation function, and the outputs and actions that
// were declared by it, to a ConfiguredTarget value.
func (c *baseComputer[TReference, TMetadata]) encodeRuleTargetValue(
	ctx context.Context,
	e ConfiguredTargetEnvironment[TReference, TMetadata],
	thread *eval.Thread,
	analysisEncoders *AnalysisEncoders[TReference],
	valueEncodingOptionsFactory *valueEncodingOptionsFactory[TReference, TMetadata],
	emptyDefaultInfo model_core.Message[*model_starlark_pb.Struct, TReference],
	providerInstances []*model_starlark.Struct[TReference, TMetadata],
	provides []string,
	outputRegistrar *targetOutputRegistrar[TReference, TMetadata],
	tc *targetContext[TReference, TMetadata],
) (PatchedConfiguredTargetValue[TMetadata], error) {
	return model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (*model_analysis_pb.ConfiguredTarget_Value, error) {
		// Convert list of providers to a map where the provider
		// identifier is the key.
		providersSeen := make(map[string]struct{}, len(providerInstances)+1)
		encodedProviderInstances := make([]*model_starlark_pb.Struct, 0, len(providerInstances)+1)
		for i, providerInstance := range providerInstances {
			providerIdentifier, err := providerInstance.GetProviderIdentifier()
			if err != nil {
				return nil, fmt.Errorf("struct returned at index %d: %w", i, err)
			}
			providerIdentifierStr := providerIdentifier.String()
			if _, ok := providersSeen[providerIdentifierStr]; ok {
				return nil, fmt.Errorf("implementation function returned multiple structs for provider %#v", providerIdentifierStr)
			}
			providersSeen[providerIdentifierStr] = struct{}{}

			v, err := providerInstance.Encode(thread, nil, valueEncodingOptionsFactory.newValueEncodingOptions(ctx, nil))
			if err != nil {
				return nil, err
			}
			encodedProviderInstances = append(encodedProviderInstances, v.Merge(patcher))
		}

		// If the rule did not return an instance of
		// DefaultInfo, inject an empty instance.
		defaultInfoProviderIdentifierStr := defaultInfoProviderIdentifier.String()
		if _, ok := providersSeen[defaultInfoProviderIdentifierStr]; !ok {
			encodedProviderInstances = append(
				encodedProviderInstances,
				model_core.Patch(e, emptyDefaultInfo).Merge(patcher),
			)
			providersSeen[defaultInfoProviderIdentifierStr] = struct{}{}
		}

		// Like Bazel, check the advertised providers after
		// DefaultInfo has been injected, meaning that rules
		// listing DefaultInfo don't need to return it.
		if err := checkProvides(providersSeen, provides); err != nil {
			return nil, err
		}

		slices.SortFunc(encodedProviderInstances, func(a, b *model_starlark_pb.Struct) int {
			return strings.Compare(
				a.ProviderInstanceProperties.ProviderIdentifier,
				b.ProviderInstanceProperties.ProviderIdentifier,
			)
		})

		outputs, actions, err := c.encodeTargetOutputsAndActions(ctx, e, analysisEncoders, outputRegistrar, tc, patcher)
		if err != nil {
			return nil, err
		}

		// TODO: We should use inlinedtree.Build() here.
		return &model_analysis_pb.ConfiguredTarget_Value{
			ProviderInstances: encodedProviderInstances,
			Outputs:           outputs,
			Actions:           actions,
		}, nil
	})
}

func getAttrValueParts[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	e getValueFromSelectGroupEnvironment[TReference, TMetadata],
	configurationReference model_core.Message[*model_core_pb.DecodableReference, TReference],
	ruleTargetPackage label.CanonicalPackage,
	namedAttr model_core.Message[*model_starlark_pb.NamedAttr, TReference],
	publicAttrValue model_core.Message[*model_starlark_pb.RuleTarget_PublicAttrValue, TReference],
) (valueParts model_core.Message[[]*model_starlark_pb.Value, TReference], usedDefaultValue bool, err error) {
	if !strings.HasPrefix(namedAttr.Message.Name, "_") {
		// Attr is public. Extract the value from the rule target.
		selectGroups := publicAttrValue.Message.ValueParts
		if len(selectGroups) == 0 {
			return model_core.Message[[]*model_starlark_pb.Value, TReference]{}, false, fmt.Errorf("attr %#v has no select groups", namedAttr.Message.Name)
		}

		valueParts := make([]*model_starlark_pb.Value, 0, len(selectGroups))
		missingDependencies := false
		for _, selectGroup := range selectGroups {
			valuePart, err := getValueFromSelectGroup(
				e,
				configurationReference,
				ruleTargetPackage,
				selectGroup,
				false,
			)
			if err == nil {
				valueParts = append(valueParts, valuePart)
			} else if errors.Is(err, evaluation.ErrMissingDependency) {
				missingDependencies = true
			} else {
				return model_core.Message[[]*model_starlark_pb.Value, TReference]{}, false, err
			}
		}
		if missingDependencies {
			return model_core.Message[[]*model_starlark_pb.Value, TReference]{}, false, evaluation.ErrMissingDependency
		}

		// Use the value from the rule target if it's not None.
		if len(valueParts) > 1 {
			return model_core.Nested(publicAttrValue, valueParts), false, nil
		}
		if _, ok := valueParts[0].Kind.(*model_starlark_pb.Value_None); !ok {
			return model_core.Nested(publicAttrValue, valueParts), false, nil
		}
	}

	// No value provided. Use the default value from the rule definition.
	defaultValue := namedAttr.Message.Attr.GetDefault()
	if defaultValue == nil {
		return model_core.Message[[]*model_starlark_pb.Value, TReference]{}, false, fmt.Errorf("missing value for mandatory attr %#v", namedAttr.Message.Name)
	}
	return model_core.Nested(namedAttr, []*model_starlark_pb.Value{defaultValue}), true, nil
}

// getConfigurationFreeAttrValues computes the values of the attrs of
// a rule target that don't depend on any configuration. It returns two
// maps:
//
//   - The attr values that can already be provided to ctx.attr. This
//     includes the common attrs "name", "tags" and "testonly", and
//     excludes label and output attrs, as these depend on the
//     configuration.
//
//   - The attr values that are provided to user-defined edge
//     transitions. This includes label and output attrs, whose values
//     are provided as plain labels.
func getConfigurationFreeAttrValues[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	thread *eval.Thread,
	starlarkValueEncoders *StarlarkValueEncoders[TReference],
	targetLabel label.CanonicalLabel,
	ruleTarget model_core.Message[*model_starlark_pb.RuleTarget, TReference],
	ruleDefinition model_core.Message[*model_starlark_pb.Rule_Definition, TReference],
) (attrValues, edgeTransitionAttrValues map[string]any, err error) {
	// Set all common attrs.
	attrValues = make(map[string]any, len(ruleDefinition.Message.Attrs)+2)
	attrValues["name"] = eval.String(targetLabel.GetTargetName().String())

	tags := make([]eval.Value, 0, len(ruleTarget.Message.Tags))
	for _, tag := range ruleTarget.Message.Tags {
		tags = append(tags, eval.String(tag))
	}
	attrValues["tags"] = eval.NewList(tags)

	attrValues["testonly"] = eval.Bool(ruleTarget.Message.InheritableAttrs.GetTestonly())

	edgeTransitionAttrValues = make(map[string]any, len(ruleDefinition.Message.Attrs)+2)
	for k, v := range attrValues {
		edgeTransitionAttrValues[k] = v
	}

	ruleTargetPublicAttrValues := ruleTarget.Message.PublicAttrValues
GetConfigurationFreeAttrValues:
	for _, namedAttr := range ruleDefinition.Message.Attrs {
		var publicAttrValue *model_starlark_pb.RuleTarget_PublicAttrValue
		if !strings.HasPrefix(namedAttr.Name, "_") {
			if len(ruleTargetPublicAttrValues) == 0 {
				return nil, nil, errors.New("rule target has fewer public attr values than the rule definition has public attrs")
			}
			publicAttrValue = ruleTargetPublicAttrValues[0]
			ruleTargetPublicAttrValues = ruleTargetPublicAttrValues[1:]
		}

		var valueParts []model_core.Message[*model_starlark_pb.Value, TReference]
		if !strings.HasPrefix(namedAttr.Name, "_") {
			// Attr is public. Extract the value from the
			// rule target.
			selectGroups := publicAttrValue.ValueParts
			if len(selectGroups) == 0 {
				return nil, nil, fmt.Errorf("attr %#v has no select groups", namedAttr.Name)
			}
			for _, selectGroup := range selectGroups {
				if len(selectGroup.Conditions) > 0 {
					// Conditions are present, meaning the
					// value depends on a configuration.
					continue GetConfigurationFreeAttrValues
				}
				noMatch, ok := selectGroup.NoMatch.(*model_starlark_pb.Select_Group_NoMatchValue)
				if !ok {
					// No default value provided.
					continue GetConfigurationFreeAttrValues
				}
				valueParts = append(valueParts, model_core.Nested(ruleTarget, noMatch.NoMatchValue))
			}

			// If the value is None, fall back to the default
			// value from the rule definition.
			if len(valueParts) == 1 {
				if _, ok := valueParts[0].Message.Kind.(*model_starlark_pb.Value_None); ok {
					valueParts = valueParts[:0]
				}
			}
		}

		// No value provided. Use the default value from the rule
		// definition.
		if len(valueParts) == 0 {
			defaultValue := namedAttr.Attr.GetDefault()
			if defaultValue == nil {
				return nil, nil, fmt.Errorf("missing value for mandatory attr %#v", namedAttr.Name)
			}
			valueParts = append(valueParts, model_core.Nested(ruleDefinition, defaultValue))
		}

		var attrValueBuilder model_starlark.AttrValueBuilder
		for _, valuePart := range valueParts {
			decodedPart, err := model_starlark.DecodeAttrValuePart[TReference, TMetadata](
				thread, valuePart,
				starlarkValueEncoders.newValueDecodingOptions(func(resolvedLabel label.ResolvedLabel) (eval.Value, error) {
					return model_starlark.NewLabel[TReference, TMetadata](resolvedLabel), nil
				}),
			)
			if err != nil {
				return nil, nil, err
			}
			if err := attrValueBuilder.Add(thread, decodedPart); err != nil {
				return nil, nil, err
			}
		}
		attrValue, err := attrValueBuilder.Build(thread)
		if err != nil {
			return nil, nil, err
		}

		switch namedAttr.Attr.GetType().(type) {
		case *model_starlark_pb.Attr_Label, *model_starlark_pb.Attr_LabelKeyedStringDict, *model_starlark_pb.Attr_LabelList,
			*model_starlark_pb.Attr_StringKeyedLabelDict, *model_starlark_pb.Attr_Output, *model_starlark_pb.Attr_OutputList:
			// Don't set these, as they depend on the
			// configuration.
		default:
			attrValues[namedAttr.Name] = attrValue
		}

		edgeTransitionAttrValues[namedAttr.Name] = attrValue
	}
	if l := len(ruleTargetPublicAttrValues); l != 0 {
		return nil, nil, fmt.Errorf("rule target has %d more public attr values than the rule definition has public attrs", l)
	}
	return attrValues, edgeTransitionAttrValues, nil
}

type targetOutput[TMetadata model_core.ReferenceMetadata] struct {
	// Constant fields.
	packageRelativePath label.TargetName
	fileType            model_starlark_pb.File_Owner_Type

	// Variable fields.
	definition model_core.PatchedMessage[*model_analysis_pb.TargetOutputDefinition, TMetadata]
}

func (o *targetOutput[TMetadata]) setDefinition(definition model_core.PatchedMessage[*model_analysis_pb.TargetOutputDefinition, TMetadata]) error {
	if o.definition.IsSet() {
		return fmt.Errorf("file %#v is an output of multiple actions", o.packageRelativePath.String())
	}
	o.definition = definition
	return nil
}

type targetOutputRegistrar[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	// The variant of the target that declares the outputs, as stored
	// in File.Owner.variant. If outputs are declared by an aspect, it
	// contains the aspects that are applied to the target.
	variant     model_core.Message[*model_starlark_pb.TargetVariant, TReference]
	targetLabel label.CanonicalLabel

	outputsByPackageRelativePath map[string]*targetOutput[TMetadata]
	outputsByFile                map[*model_starlark.File[TReference, TMetadata]]*targetOutput[TMetadata]

	// The File objects that were returned when declaring outputs,
	// so that declaring the same output again returns the same
	// object. This map is created lazily.
	filesByPackageRelativePath map[string]*model_starlark.File[TReference, TMetadata]
}

// getFileTypeDescription returns a human readable description of a type
// of file, for use in error messages.
func getFileTypeDescription(fileType model_starlark_pb.File_Owner_Type) string {
	switch fileType {
	case model_starlark_pb.File_Owner_FILE:
		return "regular file"
	case model_starlark_pb.File_Owner_DIRECTORY:
		return "directory"
	case model_starlark_pb.File_Owner_SYMLINK:
		return "symbolic link"
	default:
		return fileType.String()
	}
}

func (or *targetOutputRegistrar[TReference, TMetadata]) registerOutput(filename label.TargetName, sibling *model_starlark.File[TReference, TMetadata], fileType model_starlark_pb.File_Owner_Type) (eval.Value, error) {
	// If a sibling is provided, path resolution needs to start in
	// the directory containing the sibling.
	if sibling != nil {
		siblingLabelStr := sibling.GetDefinition().Message.Label
		siblingLabel, err := label.NewCanonicalLabel(siblingLabelStr)
		if err != nil {
			return nil, fmt.Errorf("invalid label for sibling %#v: %w", siblingLabelStr, err)
		}
		if siblingLabel.GetCanonicalPackage() != or.targetLabel.GetCanonicalPackage() {
			return nil, fmt.Errorf("sibling %#v is not declared in the same package", siblingLabel.String())
		}
		filename = siblingLabel.GetTargetName().GetSibling(filename)
	}

	// Like Bazel, declaring the same output multiple times yields
	// the same file, as long as its type is the same.
	filenameStr := filename.String()
	if existingOutput, ok := or.outputsByPackageRelativePath[filenameStr]; ok {
		if existingOutput.fileType != fileType {
			return nil, fmt.Errorf("file %#v has already been declared as a %s, not a %s", filenameStr, getFileTypeDescription(existingOutput.fileType), getFileTypeDescription(fileType))
		}
		f, ok := or.filesByPackageRelativePath[filenameStr]
		if !ok {
			return nil, fmt.Errorf("file %#v was declared, but its file object was not retained", filenameStr)
		}
		return f, nil
	}

	o := &targetOutput[TMetadata]{
		packageRelativePath: filename,
		fileType:            fileType,
	}
	or.outputsByPackageRelativePath[filenameStr] = o
	f := model_starlark.NewFile[TReference, TMetadata](
		model_core.Nested(or.variant, &model_starlark_pb.File{
			Owner: &model_starlark_pb.File_Owner{
				Variant:    or.variant.Message,
				TargetName: or.targetLabel.GetTargetName().String(),
				Type:       fileType,
			},
			Label: or.targetLabel.GetCanonicalPackage().AppendTargetName(filename).String(),
		}),
	)
	or.outputsByFile[f] = o
	if or.filesByPackageRelativePath == nil {
		or.filesByPackageRelativePath = map[string]*model_starlark.File[TReference, TMetadata]{}
	}
	or.filesByPackageRelativePath[filenameStr] = f
	return f, nil
}

// targetOutputRegistrar is able to convert model_starlark.File objects
// that were declared as part of the current target to targetOutputs.
// This allows them to be associated to the action that yields them.
var _ unpack.UnpackerInto[*targetOutput[model_core.ReferenceMetadata]] = (*targetOutputRegistrar[object.BasicReference, model_core.ReferenceMetadata])(nil)

func (or *targetOutputRegistrar[TReference, TMetadata]) UnpackInto(thread *eval.Thread, v eval.Value, dst **targetOutput[TMetadata]) error {
	var f *model_starlark.File[TReference, TMetadata]
	if err := unpack.Type[*model_starlark.File[TReference, TMetadata]]("File").UnpackInto(thread, v, &f); err != nil {
		return err
	}
	o, ok := or.outputsByFile[f]
	if !ok {
		repr, err := eval.Repr(thread, f)
		if err != nil {
			return err
		}
		return fmt.Errorf("%s was not declared as part of this target", repr)
	}
	*dst = o
	return nil
}

func (or *targetOutputRegistrar[TReference, TMetadata]) Canonicalize(thread *eval.Thread, v eval.Value) (eval.Value, error) {
	var o *targetOutput[TMetadata]
	if err := or.UnpackInto(thread, v, &o); err != nil {
		return nil, err
	}
	return v, nil
}

func (targetOutputRegistrar[TReference, TMetadata]) GetConcatenationOperator() syntax.Token {
	return syntax.PLUS
}

// targetContext contains the state that is shared by the ctx objects
// that are provided to the implementation functions of rules, aspects
// and subrules. This includes the ability to declare outputs and
// actions, and to access the toolchains of exec groups.
type targetContext[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	computer                    *baseComputer[TReference, TMetadata]
	context                     context.Context
	environment                 ConfiguredTargetEnvironment[TReference, TMetadata]
	valueEncodingOptionsFactory *valueEncodingOptionsFactory[TReference, TMetadata]
	starlarkValueEncoders       *StarlarkValueEncoders[TReference]
	analysisEncoders            *AnalysisEncoders[TReference]
	targetLabel                 label.CanonicalLabel
	configurationReference      model_core.Message[*model_core_pb.DecodableReference, TReference]
	namedExecGroups             model_core.Message[[]*model_starlark_pb.NamedExecGroup, TReference]
	execGroups                  []targetContextExecGroupState
	executableFileToFilesToRun  map[*model_starlark.File[TReference, TMetadata]]model_core.Message[*model_starlark_pb.Struct, TReference]
	outputRegistrar             *targetOutputRegistrar[TReference, TMetadata]
	actionEncoder               model_encoding.DeterministicBinaryEncoder
	directoryCreationParameters *model_filesystem.DirectoryCreationParameters
	fileCreationParameters      *model_filesystem.FileCreationParameters
	actions                     model_core.PatchedMessageList[*model_analysis_pb.ConfiguredTarget_Value_Action_Leaf, TMetadata]
}

// ruleContext is the ctx object that is provided to the
// implementation function of a rule.
type ruleContext[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	*targetContext[TReference, TMetadata]

	ruleIdentifier    label.CanonicalStarlarkIdentifier
	ruleDefinition    model_core.Message[*model_starlark_pb.Rule_Definition, TReference]
	ruleTarget        model_core.Message[*model_starlark_pb.RuleTarget, TReference]
	features          model_starlark.FeatureSet
	attr              eval.Value
	splitAttr         eval.Value
	buildSettingValue eval.Value
	executable        eval.Value
	file              eval.Value
	files             eval.Value
	outputs           eval.Value
	tags              eval.List
}

var _ eval.HasAttrs = (*ruleContext[object.GlobalReference, model_core.ReferenceMetadata])(nil)

// Repr writes the representation of the value.
func (rc *ruleContext[TReference, TMetadata]) Repr(_ *eval.Thread, printer *eval.Printer) error {
	printer.WriteString(fmt.Sprintf("<ctx for %s>", rc.targetLabel.String()))
	return nil
}

func (ruleContext[TReference, TMetadata]) Type() string {
	return "ctx"
}

func (ruleContext[TReference, TMetadata]) Truth(*eval.Thread) (bool, error) {
	return true, nil
}

func (rc *ruleContext[TReference, TMetadata]) Attr(thread *eval.Thread, name string) (eval.Value, error) {
	switch name {
	case "actions":
		return &targetContextActions[TReference, TMetadata]{
			targetContext: rc.targetContext,
		}, nil
	case "attr":
		return rc.attr, nil
	case "bin_dir":
		configurationComponent, err := model_starlark.ConfigurationReferenceToComponent(rc.configurationReference)
		if err != nil {
			return nil, err
		}
		return model_starlark.NewStructFromDict[TReference, TMetadata](nil, map[string]any{
			"path": eval.String(model_starlark.ComponentStrBazelOut + "/" + configurationComponent + "/" + model_starlark.ComponentStrBin),
		}), nil
	case "build_setting_value":
		if rc.buildSettingValue == nil {
			buildSettingDefault := rc.ruleTarget.Message.BuildSettingDefault
			if buildSettingDefault == nil {
				return nil, eval.NoSuchAttrError{Value: rc, Name: name}
			}

			targetLabelStr := rc.targetLabel.String()
			override, err := btree.Find(
				rc.context,
				rc.analysisEncoders.Readers.BuildSettingOverride,
				getBuildSettingOverridesFromReference(rc.configurationReference),
				func(entry model_core.Message[*model_analysis_pb.BuildSettingOverride, TReference]) (int, *model_core_pb.DecodableReference) {
					switch level := entry.Message.Level.(type) {
					case *model_analysis_pb.BuildSettingOverride_Leaf_:
						return strings.Compare(targetLabelStr, level.Leaf.Label), nil
					case *model_analysis_pb.BuildSettingOverride_Parent_:
						return strings.Compare(targetLabelStr, level.Parent.FirstLabel), level.Parent.Reference
					default:
						return 0, nil
					}
				},
			)
			if err != nil {
				return nil, err
			}

			var encodedValue model_core.Message[*model_starlark_pb.Value, TReference]
			if override.IsSet() {
				overrideLeaf, ok := override.Message.Level.(*model_analysis_pb.BuildSettingOverride_Leaf_)
				if !ok {
					return nil, errors.New("build setting override is not a valid leaf")
				}
				encodedValue = model_core.Nested(override, overrideLeaf.Leaf.Value)
			} else {
				encodedValue = model_core.Nested(rc.ruleTarget, rc.ruleTarget.Message.BuildSettingDefault)
			}

			value, err := model_starlark.DecodeValue[TReference, TMetadata](thread, encodedValue, nil)
			if err != nil {
				return nil, err
			}
			rc.buildSettingValue = value
		}
		return rc.buildSettingValue, nil
	case "disabled_features":
		return stringsToStarlarkList(rc.features.GetDisabled()), nil
	case "exec_groups":
		return &targetContextExecGroups[TReference, TMetadata]{
			targetContext: rc.targetContext,
		}, nil
	case "executable":
		return rc.executable, nil
	case "features":
		return stringsToStarlarkList(rc.features.GetEnabled()), nil
	case "file":
		return rc.file, nil
	case "files":
		return rc.files, nil
	case "info_file":
		// TODO: Fill all of this in properly.
		return model_starlark.NewFile[TReference, TMetadata](
			model_core.NewSimpleMessage[TReference](
				&model_starlark_pb.File{
					Owner: &model_starlark_pb.File_Owner{
						TargetName: "stamp",
						Type:       model_starlark_pb.File_Owner_FILE,
					},
					Label: "@@builtins_core+//:stable-status.txt",
				},
			),
		), nil
	case "label":
		return model_starlark.NewLabel[TReference, TMetadata](rc.targetLabel.AsResolved()), nil
	case "outputs":
		return rc.outputs, nil
	case "split_attr":
		return rc.splitAttr, nil
	case "version_file":
		// TODO: Fill all of this in properly.
		return model_starlark.NewFile[TReference, TMetadata](
			model_core.NewSimpleMessage[TReference](
				&model_starlark_pb.File{
					Owner: &model_starlark_pb.File_Owner{
						TargetName: "stamp",
						Type:       model_starlark_pb.File_Owner_FILE,
					},
					Label: "@@builtins_core+//:volatile-status.txt",
				},
			),
		), nil
	default:
		return nil, eval.NoSuchAttrError{Value: rc, Name: name}
	}
}

var ruleContextAttrNames = []string{
	"actions",
	"attr",
	"bin_dir",
	"disabled_features",
	"exec_groups",
	"executable",
	"features",
	"file",
	"files",
	"info_file",
	"label",
	"outputs",
	"split_attr",
	"version_file",
}

func (rc *ruleContext[TReference, TMetadata]) AttrNames(*eval.Thread) ([]string, error) {
	attrNames := append([]string(nil), ruleContextAttrNames...)
	if rc.ruleTarget.Message.BuildSettingDefault != nil {
		attrNames = append(attrNames, "build_setting_value")
	}
	return attrNames, nil
}

func (rc *targetContext[TReference, TMetadata]) setOutputToStaticDirectory(output *targetOutput[TMetadata], capturableDirectory model_filesystem.CapturableDirectory[TMetadata, TMetadata]) error {
	var createdDirectory model_filesystem.CreatedDirectory[TMetadata]
	group, groupCtx := errgroup.WithContext(rc.context)
	group.Go(func() error {
		return model_filesystem.CreateDirectoryMerkleTree(
			groupCtx,
			semaphore.NewWeighted(1),
			group,
			rc.directoryCreationParameters,
			capturableDirectory,
			model_filesystem.NewSimpleDirectoryMerkleTreeCapturer(rc.environment),
			&createdDirectory,
		)
	})
	if err := group.Wait(); err != nil {
		return err
	}

	return output.setDefinition(
		model_core.NewPatchedMessage(
			&model_analysis_pb.TargetOutputDefinition{
				Source: &model_analysis_pb.TargetOutputDefinition_StaticPackageDirectory_{
					StaticPackageDirectory: &model_analysis_pb.TargetOutputDefinition_StaticPackageDirectory{
						Entries: createdDirectory.Message.Message,
					},
				},
			},
			createdDirectory.Message.Patcher,
		),
	)
}

// getFileOrFilesToRunProvider takes a File or a FilesToRunProvider and
// returns it as a typed value.
func (rc *targetContext[TReference, TMetadata]) getFileOrFilesToRunProvider(thread *eval.Thread, value any) (*model_starlark.File[TReference, TMetadata], *model_starlark.Struct[TReference, TMetadata], error) {
	switch typedValue := value.(type) {
	case *model_starlark.File[TReference, TMetadata]:
		if filesToRun, ok := rc.executableFileToFilesToRun[typedValue]; ok {
			// File originated from ctx.executable. return the
			// FilesToRunProvider from which it originated
			// instead. This is done so that if such a File is
			// provided to ctx.actions.run(tools=[...]), its
			// runfiles are added to the input root as well.
			decodedFilesToRun, err := model_starlark.DecodeStruct[TReference, TMetadata](
				thread,
				filesToRun,
			)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to decode FilesToRunProvider: %w", err)
			}
			return nil, decodedFilesToRun, nil
		}
		return typedValue, nil, nil
	case *model_starlark.Struct[TReference, TMetadata]:
		return nil, typedValue, nil
	default:
		panic("not a File or FilesToRunProvider")
	}
}

type targetContextActions[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	targetContext *targetContext[TReference, TMetadata]
}

var _ eval.HasAttrs = (*targetContextActions[object.GlobalReference, model_core.ReferenceMetadata])(nil)

// Repr writes the representation of the value.
func (targetContextActions[TReference, TMetadata]) Repr(_ *eval.Thread, p *eval.Printer) error {
	p.WriteString("<ctx.actions>")
	return nil
}

func (targetContextActions[TReference, TMetadata]) Type() string {
	return "ctx.actions"
}

func (targetContextActions[TReference, TMetadata]) Truth(*eval.Thread) (bool, error) {
	return true, nil
}

func (rca *targetContextActions[TReference, TMetadata]) Attr(thread *eval.Thread, name string) (eval.Value, error) {
	switch name {
	case "args":
		return eval.NewBuiltin("ctx.actions.args", rca.doArgs), nil
	case "declare_directory":
		return eval.NewBuiltin("ctx.actions.declare_directory", rca.doDeclareDirectory), nil
	case "declare_file":
		return eval.NewBuiltin("ctx.actions.declare_file", rca.doDeclareFile), nil
	case "declare_symlink":
		return eval.NewBuiltin("ctx.actions.declare_symlink", rca.doDeclareSymlink), nil
	case "expand_template":
		return eval.NewBuiltin("ctx.actions.expand_template", rca.doExpandTemplate), nil
	case "run":
		return eval.NewBuiltin("ctx.actions.run", rca.doRun), nil
	case "symlink":
		return eval.NewBuiltin("ctx.actions.symlink", rca.doSymlink), nil
	case "transform_info_file":
		return eval.NewBuiltin("ctx.actions.transform_info_file", rca.doTransformInfoFile), nil
	case "transform_version_file":
		return eval.NewBuiltin("ctx.actions.transform_version_file", rca.doTransformVersionFile), nil
	case "write":
		return eval.NewBuiltin("ctx.actions.write", rca.doWrite), nil
	default:
		return nil, eval.NoSuchAttrError{Value: rca, Name: name}
	}
}

var targetContextActionsAttrNames = []string{
	"args",
	"declare_directory",
	"declare_file",
	"declare_symlink",
	"expand_template",
	"run",
	"symlink",
	"transform_info_file",
	"transform_version_file",
	"write",
}

func (targetContextActions[TReference, TMetadata]) AttrNames(*eval.Thread) ([]string, error) {
	return targetContextActionsAttrNames, nil
}

func (rca *targetContextActions[TReference, TMetadata]) doArgs(thread *eval.Thread, b *eval.Builtin, arguments eval.Args) (eval.Value, error) {
	return &args[TReference, TMetadata]{
		encoder:         rca.targetContext.analysisEncoders.Encoder,
		paramFileFormat: model_analysis_pb.Args_Leaf_UseParamFile_SHELL,
	}, nil
}

func (rca *targetContextActions[TReference, TMetadata]) doDeclareDirectory(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
	if len(args.Positional) > 1 {
		return nil, fmt.Errorf("%s: got %d positional arguments, want at most 1", b.Name(), len(args.Positional))
	}
	var filename label.TargetName
	var sibling *model_starlark.File[TReference, TMetadata]
	rc := rca.targetContext
	if err := eval.UnpackArgs(
		thread, b.Name(), args,
		"filename", unpack.Bind(&filename, unpack.TargetName),
		"sibling?", unpack.Bind(&sibling, unpack.IfNotNone(unpack.Type[*model_starlark.File[TReference, TMetadata]]("File"))),
	); err != nil {
		return nil, err
	}

	return rc.outputRegistrar.registerOutput(filename, sibling, model_starlark_pb.File_Owner_DIRECTORY)
}

func (rca *targetContextActions[TReference, TMetadata]) doDeclareFile(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
	if len(args.Positional) > 1 {
		return nil, fmt.Errorf("%s: got %d positional arguments, want at most 1", b.Name(), len(args.Positional))
	}
	var filename label.TargetName
	var sibling *model_starlark.File[TReference, TMetadata]
	rc := rca.targetContext
	if err := eval.UnpackArgs(
		thread, b.Name(), args,
		"filename", unpack.Bind(&filename, unpack.TargetName),
		"sibling?", unpack.Bind(&sibling, unpack.IfNotNone(unpack.Type[*model_starlark.File[TReference, TMetadata]]("File"))),
	); err != nil {
		return nil, err
	}

	return rc.outputRegistrar.registerOutput(filename, sibling, model_starlark_pb.File_Owner_FILE)
}

func (rca *targetContextActions[TReference, TMetadata]) doDeclareSymlink(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
	if len(args.Positional) > 1 {
		return nil, fmt.Errorf("%s: got %d positional arguments, want at most 1", b.Name(), len(args.Positional))
	}
	var filename label.TargetName
	var sibling *model_starlark.File[TReference, TMetadata]
	rc := rca.targetContext
	if err := eval.UnpackArgs(
		thread, b.Name(), args,
		"filename", unpack.Bind(&filename, unpack.TargetName),
		"sibling?", unpack.Bind(&sibling, unpack.IfNotNone(unpack.Type[*model_starlark.File[TReference, TMetadata]]("File"))),
	); err != nil {
		return nil, err
	}

	return rc.outputRegistrar.registerOutput(filename, sibling, model_starlark_pb.File_Owner_SYMLINK)
}

func (rca *targetContextActions[TReference, TMetadata]) doExpandTemplate(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
	if len(args.Positional) != 0 {
		return nil, fmt.Errorf("%s: got %d positional arguments, want 0", b.Name(), len(args.Positional))
	}
	var output *targetOutput[TMetadata]
	var template *model_starlark.File[TReference, TMetadata]
	isExecutable := false
	var substitutions map[string][]byte
	rc := rca.targetContext
	if err := eval.UnpackArgs(
		thread, b.Name(), args,
		// Required arguments.
		"output", unpack.Bind(&output, rc.outputRegistrar),
		"template", unpack.Bind(&template, unpack.Type[*model_starlark.File[TReference, TMetadata]]("File")),
		// Optional arguments.
		// TODO: Add TemplateDict and computed_substitutions.
		"is_executable?", unpack.Bind(&isExecutable, unpack.Bool),
		"substitutions?", unpack.Bind(&substitutions, unpack.Dict(unpack.String, unpack.Bytes)),
	); err != nil {
		return nil, err
	}

	substitutionsList := make([]*model_analysis_pb.TargetOutputDefinition_ExpandTemplate_Substitution, 0, len(substitutions))
	for _, needle := range slices.Sorted(maps.Keys(substitutions)) {
		substitutionsList = append(substitutionsList, &model_analysis_pb.TargetOutputDefinition_ExpandTemplate_Substitution{
			Needle:      []byte(needle),
			Replacement: substitutions[needle],
		})
	}

	if output.fileType != model_starlark_pb.File_Owner_FILE {
		return nil, errors.New("output was not declared as a regular file")
	}
	patchedTemplate := model_core.Patch(rc.environment, template.GetDefinition())
	return eval.None, output.setDefinition(
		model_core.NewPatchedMessage(
			&model_analysis_pb.TargetOutputDefinition{
				Source: &model_analysis_pb.TargetOutputDefinition_ExpandTemplate_{
					ExpandTemplate: &model_analysis_pb.TargetOutputDefinition_ExpandTemplate{
						Template:      patchedTemplate.Message,
						IsExecutable:  isExecutable,
						Substitutions: substitutionsList,
					},
				},
			},
			patchedTemplate.Patcher,
		),
	)
}

// promoteStringArgumentsToArgs promotes a non-empty list of strings to
// an Args object that when evaluated expands to the same values.
func promoteStringArgumentsToArgs[TMetadata model_core.ReferenceMetadata](
	stringArgumentsListBuilder btree.Builder[*model_starlark_pb.List_Element, TMetadata],
	argsList btree.Builder[*model_analysis_pb.Args, TMetadata],
) error {
	stringArgumentsList, err := stringArgumentsListBuilder.FinalizeList()
	if err != nil {
		return err
	}
	return argsList.PushChild(
		model_core.NewPatchedMessage(
			&model_analysis_pb.Args{
				Level: &model_analysis_pb.Args_Leaf_{
					Leaf: &model_analysis_pb.Args_Leaf{
						Adds: []*model_analysis_pb.Args_Leaf_Add{{
							Level: &model_analysis_pb.Args_Leaf_Add_Leaf_{
								Leaf: &model_analysis_pb.Args_Leaf_Add_Leaf{
									Values: &model_starlark_pb.Value{
										Kind: &model_starlark_pb.Value_List{
											List: &model_starlark_pb.List{
												Elements: stringArgumentsList.Message,
											},
										},
									},
									FormatEach: "%s",
									Style: &model_analysis_pb.Args_Leaf_Add_Leaf_Separate_{
										Separate: &model_analysis_pb.Args_Leaf_Add_Leaf_Separate{},
									},
								},
							},
						}},
					},
				},
			},
			stringArgumentsList.Patcher,
		),
	)
}

// newFilesToRunProviderFromStruct converts a Starlark
// FilesToRunProvider object to a Protobuf message, so that it may be
// embedded in a target action definition.
func (rc *targetContext[TReference, TMetadata]) newFilesToRunProviderFromStruct(thread *eval.Thread, s *model_starlark.Struct[TReference, TMetadata]) (model_core.PatchedMessage[*model_analysis_pb.FilesToRunProvider_Leaf, TMetadata], *model_starlark.File[TReference, TMetadata], error) {
	// Unpack individual struct fields.
	type filesToRunProviderFields struct {
		executable           *model_starlark.File[TReference, TMetadata]
		runfilesFiles        *model_starlark.Depset[TReference, TMetadata]
		runfilesSymlinks     *model_starlark.Depset[TReference, TMetadata]
		runfilesRootSymlinks *model_starlark.Depset[TReference, TMetadata]
	}
	var fields filesToRunProviderFields
	if err := unpack.Attrs(
		func(thread *eval.Thread, fields *filesToRunProviderFields) []unpack.AttrUnpacker {
			return []unpack.AttrUnpacker{
				{Name: "executable", Unpacker: unpack.Bind(&fields.executable, unpack.Type[*model_starlark.File[TReference, TMetadata]]("File"))},
				{Name: "_runfiles_files", Unpacker: unpack.Bind(&fields.runfilesFiles, unpack.Type[*model_starlark.Depset[TReference, TMetadata]]("depset"))},
				{Name: "_runfiles_symlinks", Unpacker: unpack.Bind(&fields.runfilesSymlinks, unpack.Type[*model_starlark.Depset[TReference, TMetadata]]("depset"))},
				{Name: "_runfiles_root_symlinks", Unpacker: unpack.Bind(&fields.runfilesRootSymlinks, unpack.Type[*model_starlark.Depset[TReference, TMetadata]]("depset"))},
			}
		},
	).UnpackInto(thread, s, &fields); err != nil {
		return model_core.PatchedMessage[*model_analysis_pb.FilesToRunProvider_Leaf, TMetadata]{}, nil, err
	}

	// Encode individual fields.
	valueEncodingOptions := rc.valueEncodingOptionsFactory.newValueEncodingOptions(rc.context, nil)
	executable := model_core.Patch(rc.environment, fields.executable.GetDefinition())
	runfilesFiles, err := fields.runfilesFiles.EncodeList(thread, nil, valueEncodingOptions)
	if err != nil {
		return model_core.PatchedMessage[*model_analysis_pb.FilesToRunProvider_Leaf, TMetadata]{}, nil, fmt.Errorf("runfiles files: %w", err)
	}
	runfilesSymlinks, err := fields.runfilesSymlinks.EncodeList(thread, nil, valueEncodingOptions)
	if err != nil {
		return model_core.PatchedMessage[*model_analysis_pb.FilesToRunProvider_Leaf, TMetadata]{}, nil, fmt.Errorf("runfiles symlinks: %w", err)
	}
	runfilesRootSymlinks, err := fields.runfilesRootSymlinks.EncodeList(thread, nil, valueEncodingOptions)
	if err != nil {
		return model_core.PatchedMessage[*model_analysis_pb.FilesToRunProvider_Leaf, TMetadata]{}, nil, fmt.Errorf("runfiles root symlinks: %w", err)
	}

	patchedFilesToRunProvider, err := inlinedtree.Build(
		inlinedtree.CandidateList[*model_analysis_pb.FilesToRunProvider_Leaf, TMetadata]{
			// Fields that should always be inlined into the
			// FilesToRunProvider.
			inlinedtree.AlwaysInline(
				executable.Patcher,
				func(filesToRunProvider model_core.PatchedMessage[*model_analysis_pb.FilesToRunProvider_Leaf, TMetadata]) {
					filesToRunProvider.Message.Executable = executable.Message
				},
			),
			// Fields that can be stored externally if needed.
			{
				ExternalMessage: runfilesFiles.ToMarshaler(model_format.NewProtoList[model_starlark_pb.List_Element]()),
				Encoder:         rc.analysisEncoders.Encoder,
				ParentAppender: func(
					filesToRunProvider model_core.PatchedMessage[*model_analysis_pb.FilesToRunProvider_Leaf, TMetadata],
					externalObject *model_core.Decodable[model_core.CreatedObject[TMetadata]],
				) error {
					runfilesFiles, err := btree.MaybeMergeNodes(
						runfilesFiles.Message,
						externalObject,
						filesToRunProvider.Patcher,
						btree.Capturing(
							rc.context,
							valueEncodingOptions.ObjectCapturer,
							model_starlark.ComputeListParentNode,
						),
					)
					if err != nil {
						return err
					}
					filesToRunProvider.Message.RunfilesFiles = runfilesFiles
					return nil
				},
			},
			{
				ExternalMessage: runfilesSymlinks.ToMarshaler(model_format.NewProtoList[model_starlark_pb.List_Element]()),
				Encoder:         rc.analysisEncoders.Encoder,
				ParentAppender: func(
					filesToRunProvider model_core.PatchedMessage[*model_analysis_pb.FilesToRunProvider_Leaf, TMetadata],
					externalObject *model_core.Decodable[model_core.CreatedObject[TMetadata]],
				) error {
					runfilesSymlinks, err := btree.MaybeMergeNodes(
						runfilesSymlinks.Message,
						externalObject,
						filesToRunProvider.Patcher,
						btree.Capturing(
							rc.context,
							valueEncodingOptions.ObjectCapturer,
							model_starlark.ComputeListParentNode,
						),
					)
					if err != nil {
						return err
					}
					filesToRunProvider.Message.RunfilesSymlinks = runfilesSymlinks
					return nil
				},
			},
			{
				ExternalMessage: runfilesRootSymlinks.ToMarshaler(model_format.NewProtoList[model_starlark_pb.List_Element]()),
				Encoder:         rc.analysisEncoders.Encoder,
				ParentAppender: func(
					filesToRunProvider model_core.PatchedMessage[*model_analysis_pb.FilesToRunProvider_Leaf, TMetadata],
					externalObject *model_core.Decodable[model_core.CreatedObject[TMetadata]],
				) error {
					runfilesRootSymlinks, err := btree.MaybeMergeNodes(
						runfilesRootSymlinks.Message,
						externalObject,
						filesToRunProvider.Patcher,
						btree.Capturing(
							rc.context,
							valueEncodingOptions.ObjectCapturer,
							model_starlark.ComputeListParentNode,
						),
					)
					if err != nil {
						return err
					}
					filesToRunProvider.Message.RunfilesRootSymlinks = runfilesRootSymlinks
					return nil
				},
			},
		},
		rc.computer.chunkingParameters.GetInlinedTreeOptions(),
	)
	return patchedFilesToRunProvider, fields.executable, err
}

func (rca *targetContextActions[TReference, TMetadata]) doRun(thread *eval.Thread, b *eval.Builtin, fnArgs eval.Args) (eval.Value, error) {
	if len(fnArgs.Positional) != 0 {
		return nil, fmt.Errorf("%s: got %d positional arguments, want 0", b.Name(), len(fnArgs.Positional))
	}
	currentFilePackage, err := model_starlark.CurrentFilePackage(thread)
	if err != nil {
		return nil, err
	}
	var executable any
	var outputs []*targetOutput[TMetadata]
	var arguments []any
	var env map[string]string
	execGroup := ""
	var executionRequirements map[string]string
	var inputs *model_starlark.Depset[TReference, TMetadata]
	mnemonic := ""
	var progressMessage string
	var resourceSet eval.Function
	var toolchain *label.ResolvedLabel
	var tools []any
	useDefaultShellEnv := false
	rc := rca.targetContext
	if err := eval.UnpackArgs(
		thread, b.Name(), fnArgs,
		// Required arguments.
		"executable", unpack.Bind(&executable, unpack.Or([]unpack.UnpackerInto[any]{
			unpack.Decay(unpack.String),
			unpack.Decay(unpack.Type[*model_starlark.File[TReference, TMetadata]]("File")),
			unpack.Decay(unpack.Type[*model_starlark.Struct[TReference, TMetadata]]("struct")),
		})),
		"outputs", unpack.Bind(&outputs, unpack.List(rc.outputRegistrar)),
		// Optional arguments.
		"arguments?", unpack.Bind(&arguments, unpack.List(unpack.Or([]unpack.UnpackerInto[any]{
			unpack.Decay(unpack.Type[*args[TReference, TMetadata]]("Args")),
			unpack.Decay(unpack.String),
		}))),
		"env?", unpack.Bind(&env, unpack.Dict(unpack.String, unpack.String)),
		"exec_group?", unpack.Bind(&execGroup, unpack.IfNotNone(unpack.String)),
		"execution_requirements?", unpack.Bind(&executionRequirements, unpack.IfNotNone(unpack.Dict(unpack.String, unpack.String))),
		"inputs?", unpack.Bind(&inputs, unpack.Or([]unpack.UnpackerInto[*model_starlark.Depset[TReference, TMetadata]]{
			unpack.Type[*model_starlark.Depset[TReference, TMetadata]]("depset"),
			model_starlark.NewListToDepsetUnpackerInto[TReference, TMetadata](
				unpack.Canonicalize(unpack.Type[*model_starlark.File[TReference, TMetadata]]("File")),
			),
		})),
		"mnemonic?", unpack.Bind(&mnemonic, unpack.IfNotNone(unpack.String)),
		"progress_message?", unpack.Bind(&progressMessage, unpack.IfNotNone(unpack.String)),
		"resource_set?", unpack.Bind(&resourceSet, unpack.IfNotNone(unpack.Type[eval.Function]("function"))),
		"toolchain?", unpack.Bind(&toolchain, unpack.IfNotNone(unpack.Pointer(model_starlark.NewLabelOrStringUnpackerInto[TReference, TMetadata](currentFilePackage)))),
		"tools?", unpack.Bind(&tools, unpack.Or([]unpack.UnpackerInto[[]any]{
			unpack.Singleton(unpack.Decay(unpack.Type[*model_starlark.Depset[TReference, TMetadata]]("depset"))),
			unpack.List(unpack.Or([]unpack.UnpackerInto[any]{
				unpack.Decay(unpack.Type[*model_starlark.Depset[TReference, TMetadata]]("depset")),
				unpack.Decay(unpack.Type[*model_starlark.File[TReference, TMetadata]]("File")),
				unpack.Decay(unpack.Type[*model_starlark.Struct[TReference, TMetadata]]("struct")),
			})),
		})),
		"use_default_shell_env?", unpack.Bind(&useDefaultShellEnv, unpack.Bool),
	); err != nil {
		return nil, err
	}

	valueEncodingOptions := rc.valueEncodingOptionsFactory.newValueEncodingOptions(rc.context, nil)
	var inputsDirect []eval.Value
	toolsParentNodeComputer := btree.Capturing(rc.context, valueEncodingOptions.ObjectCapturer, func(createdObject model_core.Decodable[model_core.MetadataEntry[TMetadata]], childNodes model_core.Message[[]*model_analysis_pb.FilesToRunProvider, object.LocalReference]) model_core.PatchedMessage[*model_analysis_pb.FilesToRunProvider, TMetadata] {
		return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.FilesToRunProvider {
			return &model_analysis_pb.FilesToRunProvider{
				Level: &model_analysis_pb.FilesToRunProvider_Parent_{
					Parent: &model_analysis_pb.FilesToRunProvider_Parent{
						Reference: patcher.AddDecodableReference(createdObject),
					},
				},
			}
		})
	})
	toolsBuilder := btree.NewHeightAwareBuilder(
		btree.NewProllyChunkerFactory[TMetadata](
			valueEncodingOptions.ObjectChunkingParameters,
			/* isParent = */ func(filesToRunProvider *model_analysis_pb.FilesToRunProvider) bool {
				return filesToRunProvider.GetParent() != nil
			},
		),
		btree.NewObjectCreatingNodeMerger(
			rc.analysisEncoders.Encoder,
			valueEncodingOptions.ObjectReferenceFormat,
			toolsParentNodeComputer,
		),
	)
	defer toolsBuilder.Discard()

	// Derive argv0 from the executable. Even though it's not
	// explicitly documented, the executable is also treated as a
	// tool dependency.
	argv0, ok := executable.(string)
	if !ok {
		executableFile, executableFilesToRunProvider, err := rc.getFileOrFilesToRunProvider(thread, executable)
		if err != nil {
			return nil, fmt.Errorf("executable: %w", err)
		}

		if executableFile != nil {
			inputsDirect = append(inputsDirect, executableFile)
		} else {
			var patchedFilesToRun model_core.PatchedMessage[*model_analysis_pb.FilesToRunProvider_Leaf, TMetadata]
			patchedFilesToRun, executableFile, err = rc.newFilesToRunProviderFromStruct(thread, executableFilesToRunProvider)
			if err != nil {
				return nil, fmt.Errorf("executable: %w", err)
			}
			if err := toolsBuilder.PushChild(
				model_core.NewPatchedMessage(
					&model_analysis_pb.FilesToRunProvider{
						Level: &model_analysis_pb.FilesToRunProvider_Leaf_{
							Leaf: patchedFilesToRun.Message,
						},
					},
					patchedFilesToRun.Patcher,
				),
			); err != nil {
				return nil, fmt.Errorf("executable: %w", err)
			}
		}

		argv0, err = model_starlark.FileGetInputRootPath(executableFile.GetDefinition(), nil)
		if err != nil {
			return nil, fmt.Errorf("executable: %w", err)
		}
	}

	// Use the name of the first output file as a somewhat stable
	// identifier of the action. As this identifier is local to the
	// configured target, it doesn't need to be too long to be
	// unique.
	if len(outputs) == 0 {
		return nil, errors.New("action has no outputs")
	}
	actionID := []byte(outputs[0].packageRelativePath.String())
	if maxLength := 16; len(actionID) >= maxLength {
		h := sha256.Sum256(actionID)
		actionID = h[:maxLength]
	}

	// Encode all arguments. Arguments may be a mixture of strings
	// and Args objects.
	//
	// Promote any runs of string arguments to equivalent Args
	// objects taking a list. That way, computation of target
	// actions only needs to process Args objects.
	stringArgumentsListBuilder := model_starlark.NewListBuilder(valueEncodingOptions)
	encodedArgv0, err := model_starlark.EncodeValue[TReference, TMetadata](thread, eval.String(argv0), nil, nil, valueEncodingOptions)
	if err != nil {
		return nil, err
	}
	if err := stringArgumentsListBuilder.PushChild(
		model_core.NewPatchedMessage(
			&model_starlark_pb.List_Element{
				Level: &model_starlark_pb.List_Element_Leaf{
					Leaf: encodedArgv0.Message,
				},
			},
			encodedArgv0.Patcher,
		),
	); err != nil {
		return nil, err
	}

	argsParentNodeComputer := btree.Capturing(rc.context, valueEncodingOptions.ObjectCapturer, func(createdObject model_core.Decodable[model_core.MetadataEntry[TMetadata]], childNodes model_core.Message[[]*model_analysis_pb.Args, object.LocalReference]) model_core.PatchedMessage[*model_analysis_pb.Args, TMetadata] {
		return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.Args {
			return &model_analysis_pb.Args{
				Level: &model_analysis_pb.Args_Parent_{
					Parent: &model_analysis_pb.Args_Parent{
						Reference: patcher.AddDecodableReference(createdObject),
					},
				},
			}
		})
	})
	argsListBuilder := btree.NewHeightAwareBuilder(
		btree.NewProllyChunkerFactory[TMetadata](
			valueEncodingOptions.ObjectChunkingParameters,
			/* isParent = */ func(args *model_analysis_pb.Args) bool {
				return args.GetParent() != nil
			},
		),
		btree.NewObjectCreatingNodeMerger(
			rc.analysisEncoders.Encoder,
			valueEncodingOptions.ObjectReferenceFormat,
			argsParentNodeComputer,
		),
	)
	defer argsListBuilder.Discard()

	gotStringArguments := true
	for _, argument := range arguments {
		switch typedArgument := argument.(type) {
		case *args[TReference, TMetadata]:
			if gotStringArguments {
				if err := promoteStringArgumentsToArgs(stringArgumentsListBuilder, argsListBuilder); err != nil {
					return nil, err
				}
				gotStringArguments = false
			}
			encodedArgs, err := typedArgument.Encode(thread, nil, valueEncodingOptions)
			if err != nil {
				return nil, err
			}
			if err := argsListBuilder.PushChild(
				model_core.NewPatchedMessage(
					&model_analysis_pb.Args{
						Level: &model_analysis_pb.Args_Leaf_{
							Leaf: encodedArgs.Message,
						},
					},
					encodedArgs.Patcher,
				),
			); err != nil {
				return nil, err
			}
		case string:
			if !gotStringArguments {
				stringArgumentsListBuilder = model_starlark.NewListBuilder(valueEncodingOptions)
				gotStringArguments = true
			}
			encodedTypedArgument, err := model_starlark.EncodeValue[TReference, TMetadata](thread, eval.String(typedArgument), nil, nil, valueEncodingOptions)
			if err != nil {
				return nil, err
			}
			if err := stringArgumentsListBuilder.PushChild(
				model_core.NewPatchedMessage(
					&model_starlark_pb.List_Element{
						Level: &model_starlark_pb.List_Element_Leaf{
							Leaf: encodedTypedArgument.Message,
						},
					},
					encodedTypedArgument.Patcher,
				),
			); err != nil {
				return nil, err
			}
		default:
			panic("unexpected argument type")
		}
	}
	if gotStringArguments {
		if err := promoteStringArgumentsToArgs(stringArgumentsListBuilder, argsListBuilder); err != nil {
			return nil, err
		}
	}
	argsList, err := argsListBuilder.FinalizeList()
	if err != nil {
		return nil, err
	}

	envList, envParentNodeComputer, err := convertDictToEnvironmentVariableList(
		rc.context,
		env,
		rc.actionEncoder,
		valueEncodingOptions.ObjectReferenceFormat,
		rc.environment,
	)
	if err != nil {
		return nil, err
	}

	execGroups := rc.namedExecGroups.Message
	execGroupIndex, ok := sort.Find(
		len(execGroups),
		func(i int) int { return strings.Compare(execGroup, execGroups[i].Name) },
	)
	if !ok {
		return nil, fmt.Errorf("rule does not have an exec group with name %#v", execGroup)
	}

	// Determine the set of output paths to capture. Those need to
	// be stored in a tree, which the worker uses after execution
	// completes to determine which parts of the input root to
	// capture.
	var outputPathPatternSet model_command.PathPatternSet[TMetadata]
	for _, output := range outputs {
		if err := output.setDefinition(
			model_core.NewSimplePatchedMessage[TMetadata](
				&model_analysis_pb.TargetOutputDefinition{
					Source: &model_analysis_pb.TargetOutputDefinition_ActionId{
						ActionId: actionID,
					},
				},
			),
		); err != nil {
			return nil, err
		}
		outputPathPatternSet.Add(strings.SplitSeq(output.packageRelativePath.String(), "/"))
	}
	outputPathPatternChildren, err := outputPathPatternSet.ToProto(
		rc.context,
		rc.actionEncoder,
		rc.computer.chunkingParameters.GetInlinedTreeOptions(),
		rc.environment,
	)
	if err != nil {
		return nil, err
	}

	// Tools such as compilers tend to expect that parent
	// directories of output files already exist. Create a directory
	// hierarchy containing the parent directories of all output
	// files. This directory will get merged into the input root.
	var initialOutputDirectory changeTrackingDirectory[TReference, TMetadata]
	for _, output := range outputs {
		stack := util.NewNonEmptyStack(&initialOutputDirectory)
		var r path.ScopeWalker
		if output.fileType == model_starlark_pb.File_Owner_DIRECTORY {
			// For directory outputs, Bazel also creates the
			// directory itself. Not just its parents.
			r = &changeTrackingDirectoryNewDirectoryResolver[TReference, TMetadata]{stack: stack}
		} else {
			r = &changeTrackingDirectoryNewFileResolver[TReference, TMetadata]{stack: stack}
		}
		if err := path.Resolve(path.UNIXFormat.NewParser(output.packageRelativePath.String()), r); err != nil {
			return nil, fmt.Errorf("failed to create parent directory for output %#v: %w", output.packageRelativePath.String(), err)
		}
	}
	var createdInitialOutputDirectory model_filesystem.CreatedDirectory[TMetadata]
	group, groupCtx := errgroup.WithContext(rc.context)
	group.Go(func() error {
		return model_filesystem.CreateDirectoryMerkleTree(
			groupCtx,
			semaphore.NewWeighted(1),
			group,
			rc.directoryCreationParameters,
			&capturableChangeTrackingDirectory[TReference, TMetadata]{
				directory: &initialOutputDirectory,
				options:   &capturableChangeTrackingDirectoryOptions[TReference, TMetadata]{},
			},
			model_filesystem.NewSimpleDirectoryMerkleTreeCapturer(rc.environment),
			&createdInitialOutputDirectory,
		)
	})
	if err := group.Wait(); err != nil {
		return nil, err
	}

	// Gather inputs and tools.
	//
	// If tools are provided in the form of depsets, or Files that
	// are not provided by a label attribute marked executable=True,
	// they will not have their runfiles directories added to the
	// input root. Demote such tools to regular inputs.
	var inputsTransitive []*model_starlark.Depset[TReference, TMetadata]
	if inputs != nil {
		inputsTransitive = append(inputsTransitive, inputs)
	}
	for i, tool := range tools {
		if d, ok := tool.(*model_starlark.Depset[TReference, TMetadata]); ok {
			inputsTransitive = append(inputsTransitive, d)
		} else {
			if toolFile, toolFilesToRunProvider, err := rc.getFileOrFilesToRunProvider(thread, tool); err != nil {
				return nil, fmt.Errorf("tool at index %d: %w", i, err)
			} else if toolFile != nil {
				inputsDirect = append(inputsDirect, toolFile)
			} else {
				patchedFilesToRun, _, err := rc.newFilesToRunProviderFromStruct(thread, toolFilesToRunProvider)
				if err != nil {
					return nil, fmt.Errorf("tool at index %d: %w", i, err)
				}
				if err := toolsBuilder.PushChild(
					model_core.NewPatchedMessage(
						&model_analysis_pb.FilesToRunProvider{
							Level: &model_analysis_pb.FilesToRunProvider_Leaf_{
								Leaf: patchedFilesToRun.Message,
							},
						},
						patchedFilesToRun.Patcher,
					),
				); err != nil {
					return nil, fmt.Errorf("tool at index %d: %w", i, err)
				}
			}
		}
	}

	mergedInputs, err := model_starlark.NewDepsetContents(thread, inputsDirect, inputsTransitive, model_starlark_pb.Depset_DEFAULT)
	if err != nil {
		return nil, err
	}
	encodedInputs, err := mergedInputs.EncodeList(thread, nil, valueEncodingOptions)
	if err != nil {
		return nil, err
	}

	toolsList, err := toolsBuilder.FinalizeList()
	if err != nil {
		return nil, err
	}

	actionDefinition, err := inlinedtree.Build(
		inlinedtree.CandidateList[*model_analysis_pb.TargetActionDefinition, TMetadata]{
			// Fields that should always be inlined into the
			// action definition.
			inlinedtree.AlwaysInline(
				model_core.NewReferenceMessagePatcher[TMetadata](),
				func(actionDefinition model_core.PatchedMessage[*model_analysis_pb.TargetActionDefinition, TMetadata]) {
					actionDefinition.Message.PlatformPkixPublicKey = rc.execGroups[execGroupIndex].platformPkixPublicKey
					actionDefinition.Message.UseDefaultShellEnv = useDefaultShellEnv
				},
			),
			// Fields that can be stored externally if needed.
			{
				ExternalMessage: argsList.ToMarshaler(model_format.NewProtoList[model_analysis_pb.Args]()),
				Encoder:         rc.analysisEncoders.Encoder,
				ParentAppender: func(
					actionDefinition model_core.PatchedMessage[*model_analysis_pb.TargetActionDefinition, TMetadata],
					externalObject *model_core.Decodable[model_core.CreatedObject[TMetadata]],
				) error {
					arguments, err := btree.MaybeMergeNodes(
						argsList.Message,
						externalObject,
						actionDefinition.Patcher,
						argsParentNodeComputer,
					)
					if err != nil {
						return err
					}
					actionDefinition.Message.Arguments = arguments
					return nil
				},
			},
			{
				ExternalMessage: envList.ToMarshaler(model_format.NewProtoList[model_command_pb.EnvironmentVariableList_Element]()),
				Encoder:         rc.actionEncoder,
				ParentAppender: func(
					actionDefinition model_core.PatchedMessage[*model_analysis_pb.TargetActionDefinition, TMetadata],
					externalObject *model_core.Decodable[model_core.CreatedObject[TMetadata]],
				) error {
					env, err := btree.MaybeMergeNodes(
						envList.Message,
						externalObject,
						actionDefinition.Patcher,
						envParentNodeComputer,
					)
					if err != nil {
						return err
					}
					actionDefinition.Message.Env = env
					return nil
				},
			},
			{
				ExternalMessage: encodedInputs.ToMarshaler(model_format.NewProtoList[model_starlark_pb.List_Element]()),
				Encoder:         rc.analysisEncoders.Encoder,
				ParentAppender: func(
					actionDefinition model_core.PatchedMessage[*model_analysis_pb.TargetActionDefinition, TMetadata],
					externalObject *model_core.Decodable[model_core.CreatedObject[TMetadata]],
				) error {
					inputs, err := btree.MaybeMergeNodes(
						encodedInputs.Message,
						externalObject,
						actionDefinition.Patcher,
						btree.Capturing(
							rc.context,
							valueEncodingOptions.ObjectCapturer,
							model_starlark.ComputeListParentNode,
						),
					)
					if err != nil {
						return err
					}
					actionDefinition.Message.Inputs = inputs
					return nil
				},
			},
			{
				ExternalMessage: toolsList.ToMarshaler(model_format.NewProtoList[model_analysis_pb.FilesToRunProvider]()),
				Encoder:         rc.analysisEncoders.Encoder,
				ParentAppender: func(
					actionDefinition model_core.PatchedMessage[*model_analysis_pb.TargetActionDefinition, TMetadata],
					externalObject *model_core.Decodable[model_core.CreatedObject[TMetadata]],
				) error {
					tools, err := btree.MaybeMergeNodes(
						toolsList.Message,
						externalObject,
						actionDefinition.Patcher,
						toolsParentNodeComputer,
					)
					if err != nil {
						return err
					}
					actionDefinition.Message.Tools = tools
					return nil
				},
			},
			{
				ExternalMessage: outputPathPatternChildren.ToMarshaler(model_format.NewProto[model_command_pb.PathPattern_Children]()),
				Encoder:         rc.actionEncoder,
				ParentAppender: inlinedtree.Capturing(rc.context, rc.environment, func(
					actionDefinition model_core.PatchedMessage[*model_analysis_pb.TargetActionDefinition, TMetadata],
					externalObject *model_core.Decodable[model_core.MetadataEntry[TMetadata]],
				) {
					actionDefinition.Message.OutputPathPattern = model_command.GetPathPatternWithChildren(
						outputPathPatternChildren,
						externalObject,
						actionDefinition.Patcher,
					)
				}),
			},
			{
				ExternalMessage: createdInitialOutputDirectory.Message.ToMarshaler(model_format.NewProtoList[model_filesystem_pb.DirectoryEntry]()),
				Encoder:         rc.directoryCreationParameters.GetEncoder(),
				ParentAppender: func(
					actionDefinition model_core.PatchedMessage[*model_analysis_pb.TargetActionDefinition, TMetadata],
					externalObject *model_core.Decodable[model_core.CreatedObject[TMetadata]],
				) error {
					initialOutputDirectory, err := btree.MaybeMergeNodes(
						createdInitialOutputDirectory.Message.Message,
						externalObject,
						actionDefinition.Patcher,
						model_filesystem.NewDirectoryEntryParentNodeComputer(rc.context, rc.environment),
					)
					if err != nil {
						return err
					}
					actionDefinition.Message.InitialOutputDirectory = initialOutputDirectory
					return nil
				},
			},
		},
		rc.computer.chunkingParameters.GetInlinedTreeOptions(),
	)
	if err != nil {
		return nil, err
	}
	rc.actions = append(rc.actions, model_core.NewPatchedMessage(
		&model_analysis_pb.ConfiguredTarget_Value_Action_Leaf{
			Id:         actionID,
			Definition: actionDefinition.Message,
		},
		actionDefinition.Patcher,
	))
	return eval.None, nil
}

type singleSymlinkDirectory[TFile, TDirectory model_core.ReferenceMetadata] struct {
	components []path.Component
	target     path.Parser
}

func (singleSymlinkDirectory[TFile, TDirectory]) Close() error {
	return nil
}

func (d *singleSymlinkDirectory[TFile, TDirectory]) ReadDir() ([]filesystem.FileInfo, error) {
	fileType := filesystem.FileTypeDirectory
	if len(d.components) == 1 {
		fileType = filesystem.FileTypeSymlink
	}
	return []filesystem.FileInfo{
		filesystem.NewFileInfo(d.components[0], fileType, false),
	}, nil
}

func (d *singleSymlinkDirectory[TFile, TDirectory]) Readlink(name path.Component) (path.Parser, error) {
	return d.target, nil
}

func (d *singleSymlinkDirectory[TFile, TDirectory]) EnterCapturableDirectory(name path.Component) (*model_filesystem.CreatedDirectory[TDirectory], model_filesystem.CapturableDirectory[TDirectory, TFile], error) {
	return nil, &singleSymlinkDirectory[TFile, TDirectory]{
		components: d.components[1:],
		target:     d.target,
	}, nil
}

func (singleSymlinkDirectory[TFile, TDirectory]) OpenForFileMerkleTreeCreation(name path.Component) (model_filesystem.CapturableFile[TFile], error) {
	panic("directory only contains a symlink")
}

func (rca *targetContextActions[TReference, TMetadata]) doSymlink(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
	var output *targetOutput[TMetadata]
	var targetFile *model_starlark.File[TReference, TMetadata]
	var targetPath path.Parser
	isExecutable := false
	progressMessage := ""
	useExecRootForSource := false
	rc := rca.targetContext
	if err := eval.UnpackArgs(
		thread, b.Name(), args,
		"output", unpack.Bind(&output, rc.outputRegistrar),
		"target_file?", unpack.Bind(&targetFile, unpack.IfNotNone(unpack.Type[*model_starlark.File[TReference, TMetadata]]("File"))),
		"target_path?", unpack.Bind(&targetPath, unpack.PathParser(path.UNIXFormat)),
		"is_executable?", unpack.Bind(&isExecutable, unpack.Bool),
		"progress_message?", unpack.Bind(&progressMessage, unpack.IfNotNone(unpack.String)),
		"use_exec_root_for_source?", unpack.Bind(&useExecRootForSource, unpack.Bool),
	); err != nil {
		return nil, err
	}

	if useExecRootForSource {
		return nil, errors.New("this implementation does not support use_exec_root_for_source=True")
	}
	if isExecutable && output.fileType != model_starlark_pb.File_Owner_FILE {
		return nil, errors.New("is_executable=True can only be used in combination with regular file outputs")
	}

	if targetFile != nil {
		if targetPath != nil {
			return nil, errors.New("target_file and target_path cannot be specified at the same time")
		}
		if output.fileType != model_starlark_pb.File_Owner_DIRECTORY && output.fileType != model_starlark_pb.File_Owner_FILE {
			return nil, errors.New("target_file can only be used in combination with outputs that are declared as directories or regular files")
		}
		targetFileDefinition := targetFile.GetDefinition()
		if o := targetFileDefinition.Message.Owner; o != nil && output.fileType != o.Type {
			return nil, errors.New("output and target_file have different file types")
		}

		patchedTargetFileDefinition := model_core.Patch(rc.environment, targetFileDefinition)
		return eval.None, output.setDefinition(
			model_core.NewPatchedMessage(
				&model_analysis_pb.TargetOutputDefinition{
					Source: &model_analysis_pb.TargetOutputDefinition_Symlink_{
						Symlink: &model_analysis_pb.TargetOutputDefinition_Symlink{
							Target:       patchedTargetFileDefinition.Message,
							IsExecutable: isExecutable,
						},
					},
				},
				patchedTargetFileDefinition.Patcher,
			),
		)
	}

	if targetPath == nil {
		return nil, errors.New("one of target_file or target_path needs to be specified")
	}
	if output.fileType != model_starlark_pb.File_Owner_SYMLINK {
		return nil, errors.New("target_path can only be used in combination with outputs that are declared as symbolic links")
	}

	return eval.None, rc.setOutputToStaticDirectory(
		output,
		&singleSymlinkDirectory[TMetadata, TMetadata]{
			components: output.packageRelativePath.ToComponents(),
			target:     targetPath,
		},
	)
}

func (targetContextActions[TReference, TMetadata]) doTransformInfoFile(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
	return eval.None, nil
}

func (targetContextActions[TReference, TMetadata]) doTransformVersionFile(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
	return eval.None, nil
}

type singleFileDirectory[TFile, TDirectory model_core.ReferenceMetadata] struct {
	components   []path.Component
	isExecutable bool
	file         model_filesystem.CapturableFile[TFile]
}

func (singleFileDirectory[TFile, TDirectory]) Close() error {
	return nil
}

func (d *singleFileDirectory[TFile, TDirectory]) ReadDir() ([]filesystem.FileInfo, error) {
	fileType := filesystem.FileTypeDirectory
	isExecutable := false
	if len(d.components) == 1 {
		fileType = filesystem.FileTypeRegularFile
		isExecutable = d.isExecutable
	}
	return []filesystem.FileInfo{
		filesystem.NewFileInfo(d.components[0], fileType, isExecutable),
	}, nil
}

func (singleFileDirectory[TFile, TDirectory]) Readlink(name path.Component) (path.Parser, error) {
	panic("directory only contains a regular file")
}

func (d *singleFileDirectory[TFile, TDirectory]) EnterCapturableDirectory(name path.Component) (*model_filesystem.CreatedDirectory[TDirectory], model_filesystem.CapturableDirectory[TDirectory, TFile], error) {
	return nil, &singleFileDirectory[TFile, TDirectory]{
		components:   d.components[1:],
		isExecutable: d.isExecutable,
		file:         d.file,
	}, nil
}

func (d *singleFileDirectory[TFile, TDirectory]) OpenForFileMerkleTreeCreation(name path.Component) (model_filesystem.CapturableFile[TFile], error) {
	return d.file, nil
}

func (rca *targetContextActions[TReference, TMetadata]) doWrite(thread *eval.Thread, b *eval.Builtin, fnArgs eval.Args) (eval.Value, error) {
	if len(fnArgs.Positional) > 3 {
		return nil, fmt.Errorf("%s: got %d positional arguments, want at most 3", b.Name(), len(fnArgs.Positional))
	}
	var output *targetOutput[TMetadata]
	var content any
	isExecutable := false
	mnemonic := ""
	rc := rca.targetContext
	if err := eval.UnpackArgs(
		thread, b.Name(), fnArgs,
		"output", unpack.Bind(&output, rc.outputRegistrar),
		"content", unpack.Bind(&content, unpack.Or([]unpack.UnpackerInto[any]{
			unpack.Decay(unpack.Type[*args[TReference, TMetadata]]("Args")),
			unpack.Decay(model_starlark.NewStoredStringFileContentsUnpackerInto[TReference, TMetadata]()),
			unpack.Decay(unpack.Bytes),
		})),
		"is_executable?", unpack.Bind(&isExecutable, unpack.Bool),
		"mnemonic?", unpack.Bind(&mnemonic, unpack.IfNotNone(unpack.String)),
	); err != nil {
		return nil, err
	}

	if output.fileType != model_starlark_pb.File_Owner_FILE {
		return nil, errors.New("output was not declared as a regular file")
	}

	switch typedContent := content.(type) {
	case *args[TReference, TMetadata]:
		valueEncodingOptions := rc.valueEncodingOptionsFactory.newValueEncodingOptions(rc.context, nil)
		encodedContent, err := typedContent.Encode(thread, nil, valueEncodingOptions)
		if err != nil {
			return nil, err
		}
		return eval.None, output.setDefinition(
			model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.TargetOutputDefinition {
				return &model_analysis_pb.TargetOutputDefinition{
					Source: &model_analysis_pb.TargetOutputDefinition_Write_{
						Write: &model_analysis_pb.TargetOutputDefinition_Write{
							Content:      encodedContent.Merge(patcher),
							IsExecutable: isExecutable,
						},
					},
				}
			}),
		)
	case model_core.Message[*model_filesystem_pb.FileContents, TReference]:
		// Large string that is backed by storage. Its Merkle
		// tree is identical to the one that would be created
		// below, so there is no need to read the string.
		return eval.None, rc.setOutputToStaticDirectory(
			output,
			&singleFileDirectory[TMetadata, TMetadata]{
				components:   output.packageRelativePath.ToComponents(),
				isExecutable: isExecutable,
				file:         model_filesystem.NewSimpleCapturableFile(model_core.Patch(rc.environment, typedContent)),
			},
		)
	case []byte:
		fileContents, err := model_filesystem.CreateFileMerkleTree(
			rc.context,
			rc.fileCreationParameters,
			bytes.NewReader(typedContent),
			model_filesystem.NewSimpleFileMerkleTreeCapturer(rc.environment),
		)
		if err != nil {
			return nil, err
		}

		return eval.None, rc.setOutputToStaticDirectory(
			output,
			&singleFileDirectory[TMetadata, TMetadata]{
				components:   output.packageRelativePath.ToComponents(),
				isExecutable: isExecutable,
				file:         model_filesystem.NewSimpleCapturableFile(fileContents),
			},
		)
	default:
		panic("unexpected argument type")
	}
}

type targetContextExecGroups[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	targetContext *targetContext[TReference, TMetadata]
}

var _ eval.Mapping = (*targetContextExecGroups[object.GlobalReference, model_core.ReferenceMetadata])(nil)

// Repr writes the representation of the value.
func (targetContextExecGroups[TReference, TMetadata]) Repr(_ *eval.Thread, p *eval.Printer) error {
	p.WriteString("<ctx.exec_groups>")
	return nil
}

func (targetContextExecGroups[TReference, TMetadata]) Type() string {
	return "ctx.exec_groups"
}

func (targetContextExecGroups[TReference, TMetadata]) Truth(*eval.Thread) (bool, error) {
	return true, nil
}

func (rca *targetContextExecGroups[TReference, TMetadata]) Get(thread *eval.Thread, key eval.Value) (eval.Value, bool, error) {
	var execGroupName string
	if err := unpack.String.UnpackInto(thread, key, &execGroupName); err != nil {
		return nil, false, err
	}

	rc := rca.targetContext
	execGroups := rc.namedExecGroups.Message
	execGroupIndex, ok := sort.Find(
		len(execGroups),
		func(i int) int { return strings.Compare(execGroupName, execGroups[i].Name) },
	)
	if !ok {
		return nil, false, nil
	}
	return model_starlark.NewStructFromDict[TReference, TMetadata](nil, map[string]any{
		"toolchains": &toolchainContext[TReference, TMetadata]{
			targetContext:  rc,
			execGroupIndex: execGroupIndex,
		},
	}), true, nil
}

type toolchainContext[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	targetContext  *targetContext[TReference, TMetadata]
	execGroupIndex int
}

var _ eval.Mapping = (*toolchainContext[object.GlobalReference, model_core.ReferenceMetadata])(nil)

// Repr writes the representation of the value.
func (toolchainContext[TReference, TMetadata]) Repr(_ *eval.Thread, p *eval.Printer) error {
	p.WriteString("<toolchain context>")
	return nil
}

func (toolchainContext[TReference, TMetadata]) Type() string {
	return "ToolchainContext"
}

func (toolchainContext[TReference, TMetadata]) Truth(*eval.Thread) (bool, error) {
	return true, nil
}

func (tc *toolchainContext[TReference, TMetadata]) Get(thread *eval.Thread, v eval.Value) (eval.Value, bool, error) {
	rc := tc.targetContext
	currentFilePackage, err := model_starlark.CurrentFilePackage(thread)
	if err != nil {
		return nil, false, err
	}
	labelUnpackerInto := unpack.Stringer(model_starlark.NewLabelOrStringUnpackerInto[TReference, TMetadata](currentFilePackage))
	var toolchainType string
	if err := labelUnpackerInto.UnpackInto(thread, v, &toolchainType); err != nil {
		return nil, false, err
	}

	namedExecGroup := rc.namedExecGroups.Message[tc.execGroupIndex]
	execGroupDefinition := namedExecGroup.ExecGroup
	if execGroupDefinition == nil {
		return nil, false, errors.New("missing definition of exec group")
	}

	toolchains := execGroupDefinition.Toolchains
	toolchainIndex, ok := sort.Find(
		len(toolchains),
		func(i int) int { return strings.Compare(toolchainType, toolchains[i].ToolchainType) },
	)
	if !ok {
		// Exec group does not depend on the toolchain type.
		return nil, false, nil
	}

	execGroup := &rc.execGroups[tc.execGroupIndex]
	toolchainInfo := execGroup.toolchainInfos[toolchainIndex]
	if toolchainInfo == nil {
		toolchainIdentifier := execGroup.toolchainIdentifiers[toolchainIndex]
		if toolchainIdentifier == "" {
			// Toolchain was optional, and no matching
			// toolchain was found.
			toolchainInfo = eval.None
		} else {
			encodedToolchainInfo, err := getProviderFromConfiguredTarget(
				rc.environment,
				toolchainIdentifier,
				model_core.Patch(
					rc.environment,
					rc.configurationReference,
				),
				toolchainInfoProviderIdentifier,
			)
			if err != nil {
				return nil, true, err
			}

			toolchainInfo, err = model_starlark.DecodeStruct[TReference, TMetadata](
				thread,
				model_core.Nested(encodedToolchainInfo, &model_starlark_pb.Struct{
					ProviderInstanceProperties: &model_starlark_pb.Provider_InstanceProperties{
						ProviderIdentifier: toolchainInfoProviderIdentifier.String(),
					},
					Fields: encodedToolchainInfo.Message,
				}),
			)
			if err != nil {
				return nil, true, err
			}
		}
		execGroup.toolchainInfos[toolchainIndex] = toolchainInfo
	}
	return toolchainInfo, true, nil
}

type targetContextExecGroupState struct {
	platformPkixPublicKey []byte
	toolchainIdentifiers  []string
	toolchainInfos        []eval.Value
}

// argsAdd records all arguments provided to Args.add(), Args.add_all()
// and Args.add_joined().
type argsAdd[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	startWith         *wrapperspb.StringValue
	values            eval.Value
	expandDirectories bool
	mapEach           eval.Function
	formatEach        string
	omitIfEmpty       bool
	uniquify          bool
	setStyle          func(leaf *model_analysis_pb.Args_Leaf_Add_Leaf)
}

// argsUseParamFile records all arguments provided to
// Args.use_param_file().
type argsUseParamFile struct {
	paramFileArg string
	useAlways    bool
}

// args records the state of an Args object created through
// ctx.actions.args().
type args[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	encoder         model_encoding.DeterministicBinaryEncoder
	adds            []argsAdd[TReference, TMetadata]
	paramFileFormat model_analysis_pb.Args_Leaf_UseParamFile_Format
	useParamFile    *argsUseParamFile
}

var _ eval.HasAttrs = (*args[object.LocalReference, model_core.ReferenceMetadata])(nil)

// Repr writes the representation of the value.
func (args[TReference, TMetadata]) Repr(_ *eval.Thread, p *eval.Printer) error {
	p.WriteString("<Args>")
	return nil
}

func (args[TReference, TMetadata]) Type() string {
	return "Args"
}

func (args[TReference, TMetadata]) Truth(*eval.Thread) (bool, error) {
	return true, nil
}

func (a *args[TReference, TMetadata]) Attr(thread *eval.Thread, name string) (eval.Value, error) {
	switch name {
	case "add":
		return eval.NewBuiltin("Args.add", a.doAdd), nil
	case "add_all":
		return eval.NewBuiltin("Args.add_all", a.doAddAll), nil
	case "add_joined":
		return eval.NewBuiltin("Args.add_joined", a.doAddJoined), nil
	case "set_param_file_format":
		return eval.NewBuiltin("Args.set_param_file_format", a.doSetParamFileFormat), nil
	case "use_param_file":
		return eval.NewBuiltin("Args.use_param_file", a.doUseParamFile), nil
	default:
		return nil, eval.NoSuchAttrError{Value: a, Name: name}
	}
}

var argsAttrNames = []string{
	"add",
	"add_all",
	"add_joined",
	"set_param_file_format",
	"use_param_file",
}

func (args[TReference, TMetadata]) AttrNames(*eval.Thread) ([]string, error) {
	return argsAttrNames, nil
}

func (a *args[TReference, TMetadata]) Encode(thread *eval.Thread, path []eval.Value, options *model_starlark.ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_analysis_pb.Args_Leaf, TMetadata], error) {
	var useParamFile *model_analysis_pb.Args_Leaf_UseParamFile
	if u := a.useParamFile; u != nil {
		useParamFile = &model_analysis_pb.Args_Leaf_UseParamFile{
			Format:       a.paramFileFormat,
			ParamFileArg: u.paramFileArg,
			UseAlways:    u.useAlways,
		}
	}

	addsListBuilder := btree.NewHeightAwareBuilder(
		btree.NewProllyChunkerFactory[TMetadata](
			options.ObjectChunkingParameters,
			/* isParent = */ func(add *model_analysis_pb.Args_Leaf_Add) bool {
				return add.GetParent() != nil
			},
		),
		btree.NewObjectCreatingNodeMerger(
			a.encoder,
			options.ObjectReferenceFormat,
			btree.Capturing(options.Context, options.ObjectCapturer, func(createdObject model_core.Decodable[model_core.MetadataEntry[TMetadata]], childNodes model_core.Message[[]*model_analysis_pb.Args_Leaf_Add, object.LocalReference]) model_core.PatchedMessage[*model_analysis_pb.Args_Leaf_Add, TMetadata] {
				return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.Args_Leaf_Add {
					return &model_analysis_pb.Args_Leaf_Add{
						Level: &model_analysis_pb.Args_Leaf_Add_Parent_{
							Parent: &model_analysis_pb.Args_Leaf_Add_Parent{
								Reference: patcher.AddDecodableReference(createdObject),
							},
						},
					}
				})
			}),
		),
	)
	defer addsListBuilder.Discard()

	for _, add := range a.adds {
		leaf, err := model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (*model_analysis_pb.Args_Leaf_Add_Leaf, error) {
			leaf := &model_analysis_pb.Args_Leaf_Add_Leaf{
				StartWith:         add.startWith,
				ExpandDirectories: add.expandDirectories,
				FormatEach:        add.formatEach,
				OmitIfEmpty:       add.omitIfEmpty,
				Uniquify:          add.uniquify,
			}

			values, err := model_starlark.EncodeValue(thread, add.values, nil, nil, options)
			if err != nil {
				return nil, err
			}
			leaf.Values = values.Merge(patcher)

			if add.mapEach != nil {
				mapEach, err := model_starlark.EncodeFunction(thread, add.mapEach, path, options)
				if err != nil {
					return nil, err
				}
				leaf.MapEach = mapEach.Merge(patcher)
			}

			add.setStyle(leaf)
			return leaf, nil
		})
		if err != nil {
			return model_core.PatchedMessage[*model_analysis_pb.Args_Leaf, TMetadata]{}, err
		}

		if err := addsListBuilder.PushChild(
			model_core.NewPatchedMessage(
				&model_analysis_pb.Args_Leaf_Add{
					Level: &model_analysis_pb.Args_Leaf_Add_Leaf_{
						Leaf: leaf.Message,
					},
				},
				leaf.Patcher,
			),
		); err != nil {
			return model_core.PatchedMessage[*model_analysis_pb.Args_Leaf, TMetadata]{}, err
		}
	}
	addsList, err := addsListBuilder.FinalizeList()
	if err != nil {
		return model_core.PatchedMessage[*model_analysis_pb.Args_Leaf, TMetadata]{}, err
	}

	return model_core.NewPatchedMessage(
		&model_analysis_pb.Args_Leaf{
			Adds:         addsList.Message,
			UseParamFile: useParamFile,
		},
		addsList.Patcher,
	), nil
}

func (a *args[TReference, TMetadata]) doAdd(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
	var startWith *wrapperspb.StringValue
	var value eval.Value
	valueUnpackerInto := unpack.Or([]unpack.UnpackerInto[eval.Value]{
		unpack.Canonicalize(unpack.String),
		unpack.Canonicalize(unpack.Type[*model_starlark.File[TReference, TMetadata]]("File")),
		unpack.Canonicalize(unpack.Type[model_starlark.Label[TReference, TMetadata]]("Label")),
	})
	switch len(args.Positional) {
	case 1:
		if err := eval.UnpackArgs(
			thread, b.Name(), eval.Args{Positional: args.Positional},
			"value", unpack.Bind(&value, valueUnpackerInto),
		); err != nil {
			return nil, err
		}
	case 2:
		var argName string
		if err := eval.UnpackArgs(
			thread, b.Name(), eval.Args{Positional: args.Positional},
			"arg_name", unpack.Bind(&argName, unpack.String),
			"value", unpack.Bind(&value, valueUnpackerInto),
		); err != nil {
			return nil, err
		}
		startWith = &wrapperspb.StringValue{
			Value: argName,
		}
	default:
		return nil, fmt.Errorf("%s: got %d positional arguments, want 1 or 2", b.Name(), len(args.Positional))
	}

	format := "%s"
	if err := eval.UnpackArgs(
		thread, b.Name(), eval.Args{Named: args.Named},
		"format?", unpack.Bind(&format, unpack.String),
	); err != nil {
		return nil, err
	}

	a.adds = append(a.adds, argsAdd[TReference, TMetadata]{
		startWith:  startWith,
		values:     eval.NewList([]eval.Value{value}),
		formatEach: format,
		setStyle: func(leaf *model_analysis_pb.Args_Leaf_Add_Leaf) {
			leaf.Style = &model_analysis_pb.Args_Leaf_Add_Leaf_Separate_{
				Separate: &model_analysis_pb.Args_Leaf_Add_Leaf_Separate{},
			}
		},
	})
	return a, nil
}

func (args[TReference, TMetadata]) doAddAllJoinedParseArgs(thread *eval.Thread, b *eval.Builtin, args eval.Args) (startWith *string, values eval.Value, err error) {
	valuesUnpackerInto := unpack.Or([]unpack.UnpackerInto[eval.Value]{
		unpack.Canonicalize(unpack.List(unpack.Any)),
		unpack.Canonicalize(unpack.Type[*model_starlark.Depset[TReference, TMetadata]]("depset")),
	})
	switch len(args.Positional) {
	case 1:
		if err := eval.UnpackArgs(
			thread, b.Name(), eval.Args{Positional: args.Positional},
			"values", unpack.Bind(&values, valuesUnpackerInto),
		); err != nil {
			return nil, nil, err
		}
	case 2:
		if err := eval.UnpackArgs(
			thread, b.Name(), eval.Args{Positional: args.Positional},
			"arg_name", unpack.Bind(&startWith, unpack.Pointer(unpack.String)),
			"values", unpack.Bind(&values, valuesUnpackerInto),
		); err != nil {
			return nil, nil, err
		}
	default:
		return nil, nil, fmt.Errorf("%s: got %d positional arguments, want 1 or 2", b.Name(), len(args.Positional))
	}
	return startWith, values, nil
}

func (a *args[TReference, TMetadata]) doAddAll(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
	startWith, values, err := a.doAddAllJoinedParseArgs(thread, b, args)
	if err != nil {
		return nil, err
	}

	var mapEach eval.Function
	formatEach := "%s"
	var beforeEachStr *string
	omitIfEmpty := true
	uniquify := false
	expandDirectories := true
	var terminateWithStr *string
	allowClosure := false
	if err := eval.UnpackArgs(
		thread, b.Name(), eval.Args{Named: args.Named},
		"map_each?", unpack.Bind(&mapEach, unpack.Type[eval.Function]("function")),
		"format_each?", unpack.Bind(&formatEach, unpack.IfNotNone(unpack.String)),
		"before_each?", unpack.Bind(&beforeEachStr, unpack.IfNotNone(unpack.Pointer(unpack.String))),
		"omit_if_empty?", unpack.Bind(&omitIfEmpty, unpack.Bool),
		"uniquify?", unpack.Bind(&uniquify, unpack.Bool),
		"expand_directories?", unpack.Bind(&expandDirectories, unpack.Bool),
		"terminate_with?", unpack.Bind(&terminateWithStr, unpack.IfNotNone(unpack.Pointer(unpack.String))),
		"allow_closure?", unpack.Bind(&allowClosure, unpack.Bool),
	); err != nil {
		return nil, err
	}

	var beforeEach *wrapperspb.StringValue
	if beforeEachStr != nil {
		beforeEach = &wrapperspb.StringValue{
			Value: *beforeEachStr,
		}
	}
	var terminateWith *wrapperspb.StringValue
	if terminateWithStr != nil {
		terminateWith = &wrapperspb.StringValue{
			Value: *terminateWithStr,
		}
	}
	add := argsAdd[TReference, TMetadata]{
		values:            values,
		expandDirectories: expandDirectories,
		mapEach:           mapEach,
		formatEach:        formatEach,
		omitIfEmpty:       omitIfEmpty,
		uniquify:          uniquify,
		setStyle: func(leaf *model_analysis_pb.Args_Leaf_Add_Leaf) {
			leaf.Style = &model_analysis_pb.Args_Leaf_Add_Leaf_Separate_{
				Separate: &model_analysis_pb.Args_Leaf_Add_Leaf_Separate{
					BeforeEach:    beforeEach,
					TerminateWith: terminateWith,
				},
			}
		},
	}
	if startWith != nil {
		add.startWith = &wrapperspb.StringValue{
			Value: *startWith,
		}
	}
	a.adds = append(a.adds, add)
	return a, nil
}

func (a *args[TReference, TMetadata]) doAddJoined(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
	startWith, values, err := a.doAddAllJoinedParseArgs(thread, b, args)
	if err != nil {
		return nil, err
	}

	var joinWith string
	var mapEach eval.Function
	formatEach := "%s"
	formatJoined := "%s"
	omitIfEmpty := true
	uniquify := false
	expandDirectories := true
	allowClosure := false
	if err := eval.UnpackArgs(
		thread, b.Name(), eval.Args{Named: args.Named},
		"join_with?", unpack.Bind(&joinWith, unpack.String),
		"map_each?", unpack.Bind(&mapEach, unpack.Type[eval.Function]("function")),
		"format_each?", unpack.Bind(&formatEach, unpack.IfNotNone(unpack.String)),
		"format_joined?", unpack.Bind(&formatJoined, unpack.IfNotNone(unpack.String)),
		"omit_if_empty?", unpack.Bind(&omitIfEmpty, unpack.Bool),
		"uniquify?", unpack.Bind(&uniquify, unpack.Bool),
		"expand_directories?", unpack.Bind(&expandDirectories, unpack.Bool),
		"allow_closure?", unpack.Bind(&allowClosure, unpack.Bool),
	); err != nil {
		return nil, err
	}

	add := argsAdd[TReference, TMetadata]{
		values:            values,
		expandDirectories: expandDirectories,
		mapEach:           mapEach,
		formatEach:        formatEach,
		omitIfEmpty:       omitIfEmpty,
		uniquify:          uniquify,
		setStyle: func(leaf *model_analysis_pb.Args_Leaf_Add_Leaf) {
			leaf.Style = &model_analysis_pb.Args_Leaf_Add_Leaf_Joined_{
				Joined: &model_analysis_pb.Args_Leaf_Add_Leaf_Joined{
					JoinWith:     joinWith,
					FormatJoined: formatJoined,
				},
			}
		},
	}
	if startWith != nil {
		add.startWith = &wrapperspb.StringValue{
			Value: *startWith,
		}
	}
	a.adds = append(a.adds, add)
	return a, nil
}

func (a *args[TReference, TMetadata]) doSetParamFileFormat(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
	var format string
	if err := eval.UnpackArgs(
		thread, b.Name(), args,
		"format", unpack.Bind(&format, unpack.String),
	); err != nil {
		return nil, err
	}
	switch format {
	case "multiline":
		a.paramFileFormat = model_analysis_pb.Args_Leaf_UseParamFile_MULTILINE
	case "shell":
		a.paramFileFormat = model_analysis_pb.Args_Leaf_UseParamFile_SHELL
	case "flag_per_line":
		a.paramFileFormat = model_analysis_pb.Args_Leaf_UseParamFile_FLAG_PER_LINE
	default:
		return nil, fmt.Errorf("unknown param file format %#v", format)
	}
	return a, nil
}

func (a *args[TReference, TMetadata]) doUseParamFile(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
	if len(args.Positional) > 1 {
		return nil, fmt.Errorf("%s: got %d positional arguments, want at most 1", b.Name(), len(args.Positional))
	}
	var paramFileArg string
	useAlways := false
	if err := eval.UnpackArgs(
		thread, b.Name(), args,
		"param_file_arg", unpack.Bind(&paramFileArg, unpack.String),
		"use_always?", unpack.Bind(&useAlways, unpack.Bool),
	); err != nil {
		return nil, err
	}
	a.useParamFile = &argsUseParamFile{
		paramFileArg: paramFileArg,
		useAlways:    useAlways,
	}
	return a, nil
}

type subruleContext[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	targetContext *targetContext[TReference, TMetadata]
}

var _ eval.HasAttrs = (*subruleContext[object.GlobalReference, model_core.ReferenceMetadata])(nil)

// Repr writes the representation of the value.
func (sc *subruleContext[TReference, TMetadata]) Repr(_ *eval.Thread, printer *eval.Printer) error {
	printer.WriteString(fmt.Sprintf("<subrule_ctx for %s>", sc.targetContext.targetLabel.String()))
	return nil
}

func (subruleContext[TReference, TMetadata]) Type() string {
	return "subrule_ctx"
}

func (subruleContext[TReference, TMetadata]) Truth(*eval.Thread) (bool, error) {
	return true, nil
}

func (sc *subruleContext[TReference, TMetadata]) Attr(thread *eval.Thread, name string) (eval.Value, error) {
	// TODO: Add subrule_ctx.toolchains.
	rc := sc.targetContext
	switch name {
	case "actions":
		return &targetContextActions[TReference, TMetadata]{
			targetContext: rc,
		}, nil
	case "label":
		return model_starlark.NewLabel[TReference, TMetadata](rc.targetLabel.AsResolved()), nil
	default:
		return nil, eval.NoSuchAttrError{Value: sc, Name: name}
	}
}

var subruleContextAttrNames = []string{
	"actions",
	"label",
}

func (subruleContext[TReference, TMetadata]) AttrNames(*eval.Thread) ([]string, error) {
	return subruleContextAttrNames, nil
}
