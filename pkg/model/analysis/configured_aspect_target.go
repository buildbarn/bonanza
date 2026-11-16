package analysis

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_parser "bonanza.build/pkg/model/parser"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/unpack"
	"bonanza.build/pkg/storage/object"

	"google.golang.org/protobuf/proto"
)

// getPropagatedAspectsByAttr returns a function that yields the aspects
// that need to be applied to the targets referenced by a label attr of
// a rule target, given the aspects that are applied to the rule target
// and the properties of these aspects as computed by AspectChain.
func getPropagatedAspectsByAttr[TReference any](
	aspects model_core.Message[[]*model_starlark_pb.AppliedAspect, TReference],
	aspectChain *model_analysis_pb.AspectChain_Value,
) (func(attrName string) model_core.Message[[]*model_starlark_pb.AppliedAspect, TReference], error) {
	getAspects := func(aspectIndices []uint32) (model_core.Message[[]*model_starlark_pb.AppliedAspect, TReference], error) {
		propagatedAspects := make([]*model_starlark_pb.AppliedAspect, 0, len(aspectIndices))
		for _, aspectIndex := range aspectIndices {
			if aspectIndex >= uint32(len(aspects.Message)) {
				return model_core.Message[[]*model_starlark_pb.AppliedAspect, TReference]{}, fmt.Errorf("aspect index %d is out of range", aspectIndex)
			}
			propagatedAspects = append(propagatedAspects, aspects.Message[aspectIndex])
		}
		return model_core.Nested(aspects, propagatedAspects), nil
	}

	alwaysPropagatingAspects, err := getAspects(aspectChain.AlwaysPropagatingAspectIndices)
	if err != nil {
		return nil, err
	}
	attrPropagatingAspects := make(map[string]model_core.Message[[]*model_starlark_pb.AppliedAspect, TReference], len(aspectChain.AttrPropagatingAspects))
	for _, entry := range aspectChain.AttrPropagatingAspects {
		// Aspects need to be applied in the order in which
		// they are listed, so merge the indices of aspects that
		// propagate along all attrs and along this specific attr.
		aspectIndices := append(slices.Clone(aspectChain.AlwaysPropagatingAspectIndices), entry.AspectIndices...)
		slices.Sort(aspectIndices)
		attrAspects, err := getAspects(slices.Compact(aspectIndices))
		if err != nil {
			return nil, err
		}
		attrPropagatingAspects[entry.AttrName] = attrAspects
	}
	return func(attrName string) model_core.Message[[]*model_starlark_pb.AppliedAspect, TReference] {
		if attrAspects, ok := attrPropagatingAspects[attrName]; ok {
			return attrAspects
		}
		return alwaysPropagatingAspects
	}, nil
}

// computeAspectTargetValue applies the last aspect in the list of
// aspects provided in the key to a rule target, by invoking the
// implementation function of the aspect.
func (c *baseComputer[TReference, TMetadata]) computeAspectTargetValue(
	ctx context.Context,
	key model_core.Message[*model_analysis_pb.ConfiguredTarget_Key, TReference],
	e ConfiguredTargetEnvironment[TReference, TMetadata],
	targetLabel label.CanonicalLabel,
	ruleTarget model_core.Message[*model_starlark_pb.RuleTarget, TReference],
) (PatchedConfiguredTargetValue[TMetadata], error) {
	aspects := model_core.Nested(key, key.Message.Variant.GetAspects())
	lastAspect := aspects.Message[len(aspects.Message)-1]
	aspectIdentifier, err := label.NewCanonicalStarlarkIdentifier(lastAspect.Identifier)
	if err != nil {
		return PatchedConfiguredTargetValue[TMetadata]{}, fmt.Errorf("invalid aspect identifier: %w", err)
	}
	ruleIdentifier, err := label.NewCanonicalStarlarkIdentifier(ruleTarget.Message.RuleIdentifier)
	if err != nil {
		return PatchedConfiguredTargetValue[TMetadata]{}, fmt.Errorf("invalid rule identifier: %w", err)
	}

	// Request all dependencies that only depend on the key.
	analysisEncoders, gotAnalysisEncoders := getAnalysisEncoders(e)
	starlarkValueEncoders, gotStarlarkValueEncoders := getStarlarkValueEncoders(e)
	allBuiltinsModulesNames := e.GetBuiltinsModuleNamesValue(&model_analysis_pb.BuiltinsModuleNames_Key{})
	ruleImplementationWrappers := e.GetRuleImplementationWrappersValue(&model_analysis_pb.RuleImplementationWrappers_Key{})
	actionEncoders, gotActionEncoders := e.GetActionEncodersObject(&model_analysis_pb.ActionEncoders_Key{})
	directoryCreationParameters, gotDirectoryCreationParameters := e.GetDirectoryCreationParametersObject(&model_analysis_pb.DirectoryCreationParameters_Key{})
	fileCreationParameters, gotFileCreationParameters := e.GetFileCreationParametersObject(&model_analysis_pb.FileCreationParameters_Key{})
	aspectIdentifiers := make([]string, 0, len(aspects.Message))
	for _, aspect := range aspects.Message {
		aspectIdentifiers = append(aspectIdentifiers, aspect.Identifier)
	}
	aspectChain := e.GetAspectChainValue(&model_analysis_pb.AspectChain_Key{
		AspectIdentifiers: aspectIdentifiers,
	})
	configurationReference := model_core.Nested(key, key.Message.Variant.GetConfigurationReference())
	patchedConfigurationReference := model_core.Patch(e, configurationReference)
	configuredAspect := e.GetConfiguredAspectValue(
		model_core.NewPatchedMessage(
			&model_analysis_pb.ConfiguredAspect_Key{
				Identifier:             aspectIdentifier.String(),
				ConfigurationReference: patchedConfigurationReference.Message,
			},
			patchedConfigurationReference.Patcher,
		),
	)
	aspectDefinition, aspectDefinitionErr := getAspectDefinition(e, aspectIdentifier)
	ruleDefinition, ruleDefinitionErr := getRuleDefinition(e, ruleIdentifier)
	for _, err := range []error{aspectDefinitionErr, ruleDefinitionErr} {
		if err != nil && !errors.Is(err, evaluation.ErrMissingDependency) {
			return PatchedConfiguredTargetValue[TMetadata]{}, err
		}
	}
	if !gotAnalysisEncoders ||
		!gotStarlarkValueEncoders ||
		!gotActionEncoders ||
		!gotDirectoryCreationParameters ||
		!gotFileCreationParameters ||
		!allBuiltinsModulesNames.IsSet() ||
		!ruleImplementationWrappers.IsSet() ||
		!aspectChain.IsSet() ||
		!configuredAspect.IsSet() ||
		aspectDefinitionErr != nil ||
		ruleDefinitionErr != nil {
		return PatchedConfiguredTargetValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	// TODO: Add support for these features.
	if aspectDefinition.Message.PropagationPredicate != nil {
		return PatchedConfiguredTargetValue[TMetadata]{}, errors.New("aspects with a propagation predicate are not supported yet")
	}
	if aspectDefinition.Message.ToolchainsAspects != nil {
		return PatchedConfiguredTargetValue[TMetadata]{}, errors.New("aspects that propagate to toolchains are not supported yet")
	}

	// The aspect is only applied to targets of rules that advertise
	// the providers that the aspect requires.
	if !model_starlark.ProviderSetsSatisfiedBy(aspectDefinition.Message.RequiredProviders, ruleDefinition.Message.Provides) {
		return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.ConfiguredTarget_Value{}), nil
	}

	valueEncodingOptionsFactory, err := c.newValueEncodingOptionsFactory(e, starlarkValueEncoders)
	if err != nil {
		return PatchedConfiguredTargetValue[TMetadata]{}, err
	}

	// Obtain the providers of the target that are visible to the
	// aspect. These are the providers of the rule target, and those
	// of the preceding aspects that provide the providers that the
	// aspect requires.
	targetProviderSources := []providerInstanceSource[TReference]{}
	missingDependencies := false
	for i, prefixLength := range append([]uint32{0}, aspectChain.Message.VisibleAspectIndices...) {
		if i > 0 {
			prefixLength++
		}
		if prefixLength >= uint32(len(aspects.Message)) {
			return PatchedConfiguredTargetValue[TMetadata]{}, fmt.Errorf("visible aspect index %d is out of range", prefixLength-1)
		}
		prefixConfiguredTarget := e.GetConfiguredTargetValue(
			model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.ConfiguredTarget_Key {
				return &model_analysis_pb.ConfiguredTarget_Key{
					Label: targetLabel.String(),
					Variant: newTargetVariant(
						model_core.Patch(e, configurationReference).Merge(patcher),
						model_core.PatchList(e, model_core.Nested(aspects, aspects.Message[:prefixLength])).Merge(patcher),
					),
				}
			}),
		)
		if !prefixConfiguredTarget.IsSet() {
			missingDependencies = true
			continue
		}
		description := fmt.Sprintf("target %#v", targetLabel.String())
		if prefixLength > 0 {
			description = fmt.Sprintf("aspect %#v", aspects.Message[prefixLength-1].Identifier)
		}
		targetProviderSources = append(targetProviderSources, providerInstanceSource[TReference]{
			description:       description,
			providerInstances: model_core.Nested(prefixConfiguredTarget, prefixConfiguredTarget.Message.ProviderInstances),
		})
	}

	// Configure the attrs of the rule target, applying aspects to
	// the targets referenced by label attrs if they propagate along
	// them. These are provided to the aspect through ctx.rule.
	thread, err := c.newStarlarkThread(ctx, e, starlarkValueEncoders, allBuiltinsModulesNames.Message.BuiltinsModuleNames)
	if err != nil {
		return PatchedConfiguredTargetValue[TMetadata]{}, err
	}
	propagatedAspects, err := getPropagatedAspectsByAttr(aspects, aspectChain.Message)
	if err != nil {
		return PatchedConfiguredTargetValue[TMetadata]{}, err
	}
	ruleOutputRegistrar := &targetOutputRegistrar[TReference, TMetadata]{
		variant:     model_core.Nested(key, newTargetVariant(configurationReference.Message, nil)),
		targetLabel: targetLabel,

		outputsByPackageRelativePath: map[string]*targetOutput[TMetadata]{},
		outputsByFile:                map[*model_starlark.File[TReference, TMetadata]]*targetOutput[TMetadata]{},
	}
	defer func() {
		for _, output := range ruleOutputRegistrar.outputsByPackageRelativePath {
			output.definition.Discard()
		}
	}()
	a, _, ruleExecGroups, err := c.configureRuleTargetAttrs(
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
		ruleOutputRegistrar,
		propagatedAspects,
	)
	if err != nil {
		return PatchedConfiguredTargetValue[TMetadata]{}, err
	}

	// Configure the attrs of the aspect and the subrules it may
	// invoke.
	aspectExecGroupStates := make([]targetContextExecGroupState, 0, len(configuredAspect.Message.ExecGroups))
	for _, execGroup := range configuredAspect.Message.ExecGroups {
		aspectExecGroupStates = append(aspectExecGroupStates, targetContextExecGroupState{
			platformPkixPublicKey: execGroup.PlatformPkixPublicKey,
			toolchainIdentifiers:  execGroup.ToolchainIdentifiers,
			toolchainInfos:        make([]eval.Value, len(execGroup.ToolchainIdentifiers)),
		})
	}
	a.prefetchToolchains(aspectExecGroupStates)
	subrules, err := a.configureSubrules(model_core.Nested(configuredAspect, configuredAspect.Message.Subrules))
	if err != nil {
		return PatchedConfiguredTargetValue[TMetadata]{}, err
	}
	aspectFiles := newLabelAttrFiles[TReference, TMetadata](ctx, thread, &starlarkValueEncoders.Readers)
	aspectAttrValues, err := configureAspectAttrs(
		ctx,
		e,
		thread,
		analysisEncoders,
		starlarkValueEncoders,
		aspectDefinition,
		model_core.Nested(aspects, lastAspect.Parameters),
		model_core.Nested(configuredAspect, configuredAspect.Message.LabelAttrs),
		aspectFiles,
	)
	if err != nil {
		if !errors.Is(err, evaluation.ErrMissingDependency) {
			return PatchedConfiguredTargetValue[TMetadata]{}, err
		}
		missingDependencies = true
	}
	if missingDependencies || a.missingDependencies {
		return PatchedConfiguredTargetValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	// Construct the target that is provided to the implementation
	// function of the aspect.
	targetProviders := targetProviderSources[0].providerInstances
	if len(targetProviderSources) > 1 {
		mergedTargetProviders, err := model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) ([]*model_starlark_pb.Struct, error) {
			return mergeProviderInstances(
				ctx,
				e,
				&starlarkValueEncoders.Readers,
				valueEncodingOptionsFactory.newValueEncodingOptions(ctx, nil),
				targetProviderSources,
				patcher,
			)
		})
		if err != nil {
			return PatchedConfiguredTargetValue[TMetadata]{}, err
		}
		targetProviders = model_core.Unpatch(e, mergedTargetProviders).Decay()
	}
	target := model_starlark.NewTargetReference(
		targetLabel.AsResolved(),
		model_starlark.NewConfiguredTargetReference[TReference, TMetadata](targetLabel, targetProviders),
	)

	// Construct ctx.rule, providing access to the attrs and
	// toolchains of the rule target.
	ruleTargetContext := &targetContext[TReference, TMetadata]{
		computer:                    c,
		context:                     ctx,
		environment:                 e,
		valueEncodingOptionsFactory: valueEncodingOptionsFactory,
		starlarkValueEncoders:       starlarkValueEncoders,
		analysisEncoders:            analysisEncoders,
		targetLabel:                 targetLabel,
		configurationReference:      configurationReference,
		namedExecGroups:             model_core.Nested(ruleDefinition, ruleDefinition.Message.ExecGroups),
		execGroups:                  ruleExecGroups,
		executableFileToFilesToRun:  a.files.executableFileToFilesToRun,
	}
	var ruleToolchains eval.Value = eval.None
	if defaultExecGroupIndex, ok := sort.Find(
		len(ruleDefinition.Message.ExecGroups),
		func(i int) int { return strings.Compare("", ruleDefinition.Message.ExecGroups[i].Name) },
	); ok {
		ruleToolchains = &toolchainContext[TReference, TMetadata]{
			targetContext:  ruleTargetContext,
			execGroupIndex: defaultExecGroupIndex,
		}
	}
	rule := model_starlark.NewStructFromDict[TReference, TMetadata](nil, map[string]any{
		"attr":       model_starlark.NewStructFromDict[TReference, TMetadata](nil, a.attrValues),
		"executable": model_starlark.NewStructFromDict[TReference, TMetadata](nil, a.files.executableValues),
		"file":       model_starlark.NewStructFromDict[TReference, TMetadata](nil, a.files.fileValues),
		"files":      model_starlark.NewStructFromDict[TReference, TMetadata](nil, a.files.filesValues),
		"kind":       eval.String(ruleIdentifier.GetStarlarkIdentifier().String()),
		"toolchains": ruleToolchains,
	})

	// Construct ctx for the aspect. Outputs declared by the aspect
	// are placed in the same package as the rule target, but are
	// owned by the aspect.
	aspectOutputRegistrar := &targetOutputRegistrar[TReference, TMetadata]{
		variant:     model_core.Nested(key, key.Message.Variant),
		targetLabel: targetLabel,

		outputsByPackageRelativePath: map[string]*targetOutput[TMetadata]{},
		outputsByFile:                map[*model_starlark.File[TReference, TMetadata]]*targetOutput[TMetadata]{},
	}
	defer func() {
		for _, output := range aspectOutputRegistrar.outputsByPackageRelativePath {
			output.definition.Discard()
		}
	}()
	ac := &aspectContext[TReference, TMetadata]{
		targetContext: &targetContext[TReference, TMetadata]{
			computer:                    c,
			context:                     ctx,
			environment:                 e,
			valueEncodingOptionsFactory: valueEncodingOptionsFactory,
			starlarkValueEncoders:       starlarkValueEncoders,
			analysisEncoders:            analysisEncoders,
			targetLabel:                 targetLabel,
			configurationReference:      configurationReference,
			namedExecGroups:             model_core.Nested(aspectDefinition, aspectDefinition.Message.ExecGroups),
			execGroups:                  aspectExecGroupStates,
			executableFileToFilesToRun:  aspectFiles.executableFileToFilesToRun,
			outputRegistrar:             aspectOutputRegistrar,
			actionEncoder:               actionEncoders.Encoder,
			directoryCreationParameters: directoryCreationParameters,
			fileCreationParameters:      fileCreationParameters,
		},
		attr:       model_starlark.NewStructFromDict[TReference, TMetadata](nil, aspectAttrValues),
		executable: model_starlark.NewStructFromDict[TReference, TMetadata](nil, aspectFiles.executableValues),
		file:       model_starlark.NewStructFromDict[TReference, TMetadata](nil, aspectFiles.fileValues),
		files:      model_starlark.NewStructFromDict[TReference, TMetadata](nil, aspectFiles.filesValues),
		rule:       rule,
	}
	defer func() {
		ac.actions.Discard()
	}()

	registerSubruleInvoker(thread, ac.targetContext, aspectDefinition.Message.SubruleIdentifiers, subrules, ruleImplementationWrappers)
	identifierGenerator, err := c.getReferenceEqualIdentifierGenerator(model_core.Nested(key, proto.Message(key.Message)))
	if err != nil {
		return PatchedConfiguredTargetValue[TMetadata]{}, err
	}
	thread.SetLocal(model_starlark.ReferenceEqualIdentifierGeneratorKey, identifierGenerator)

	// Invoke the aspect implementation wrapper, providing it the
	// implementation function, the target and ctx.
	returnValue, err := eval.Call(
		thread,
		decodeImplementationWrapper[TReference, TMetadata](model_core.Nested(ruleImplementationWrappers, ruleImplementationWrappers.Message.Aspect)),
		eval.Args{
			Positional: []eval.Value{
				model_starlark.DecodeFunction[TReference, TMetadata](model_core.Nested(aspectDefinition, aspectDefinition.Message.Implementation)),
				target,
				ac,
			},
		},
	)
	if err != nil {
		return PatchedConfiguredTargetValue[TMetadata]{}, formatStarlarkError(err)
	}
	var providerInstances []*model_starlark.Struct[TReference, TMetadata]
	structUnpackerInto := unpack.Type[*model_starlark.Struct[TReference, TMetadata]]("struct")
	if err := unpack.IfNotNone(
		unpack.Or([]unpack.UnpackerInto[[]*model_starlark.Struct[TReference, TMetadata]]{
			unpack.Singleton(structUnpackerInto),
			unpack.List(structUnpackerInto),
		}),
	).UnpackInto(thread, returnValue, &providerInstances); err != nil {
		return PatchedConfiguredTargetValue[TMetadata]{}, fmt.Errorf("failed to unpack aspect implementation function return value: %w", err)
	}

	return model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (*model_analysis_pb.ConfiguredTarget_Value, error) {
		defaultInfoProviderIdentifierStr := defaultInfoProviderIdentifier.String()
		providersSeen := make(map[string]struct{}, len(providerInstances))
		encodedProviderInstances := make([]*model_starlark_pb.Struct, 0, len(providerInstances))
		for i, providerInstance := range providerInstances {
			providerIdentifier, err := providerInstance.GetProviderIdentifier()
			if err != nil {
				return nil, fmt.Errorf("struct returned at index %d: %w", i, err)
			}
			providerIdentifierStr := providerIdentifier.String()
			if providerIdentifierStr == defaultInfoProviderIdentifierStr {
				return nil, errors.New("aspect implementation function returned an instance of DefaultInfo, which is not permitted")
			}
			if _, ok := providersSeen[providerIdentifierStr]; ok {
				return nil, fmt.Errorf("aspect implementation function returned multiple structs for provider %#v", providerIdentifierStr)
			}
			providersSeen[providerIdentifierStr] = struct{}{}

			v, err := providerInstance.Encode(thread, nil, valueEncodingOptionsFactory.newValueEncodingOptions(ctx, nil))
			if err != nil {
				return nil, err
			}
			encodedProviderInstances = append(encodedProviderInstances, v.Merge(patcher))
		}
		if err := checkProvides(providersSeen, aspectDefinition.Message.Provides); err != nil {
			return nil, err
		}
		slices.SortFunc(encodedProviderInstances, func(a, b *model_starlark_pb.Struct) int {
			return strings.Compare(
				a.ProviderInstanceProperties.ProviderIdentifier,
				b.ProviderInstanceProperties.ProviderIdentifier,
			)
		})

		outputs, actions, err := c.encodeTargetOutputsAndActions(ctx, e, analysisEncoders, aspectOutputRegistrar, ac.targetContext, patcher)
		if err != nil {
			return nil, err
		}
		return &model_analysis_pb.ConfiguredTarget_Value{
			ProviderInstances: encodedProviderInstances,
			Outputs:           outputs,
			Actions:           actions,
		}, nil
	})
}

// getAspectParameters returns the values of the public attrs of an
// aspect that are set to values that differ from their default values,
// as stored in AppliedAspect.parameters. These objects are created by
// analysis functions, meaning they need to be read using the analysis
// readers. Objects containing the values of the fields are read using
// the Starlark value readers.
func getAspectParameters[TReference object.BasicReference](
	ctx context.Context,
	analysisReaders *AnalysisReaders[TReference],
	valueReaders *model_starlark.ValueReaders[TReference],
	parameters model_core.Message[*model_core_pb.DecodableReference, TReference],
) (map[string]model_core.Message[*model_starlark_pb.Value, TReference], error) {
	if parameters.Message == nil {
		return nil, nil
	}
	fields, err := model_parser.Dereference(ctx, analysisReaders.AspectParameters, parameters)
	if err != nil {
		return nil, fmt.Errorf("failed to read aspect parameters: %w", err)
	}
	parameterValues := map[string]model_core.Message[*model_starlark_pb.Value, TReference]{}
	var errIter error
	for key, value := range model_starlark.AllStructFields(ctx, valueReaders.List, fields, &errIter) {
		parameterValues[key] = value
	}
	if errIter != nil {
		return nil, fmt.Errorf("failed to iterate aspect parameters: %w", errIter)
	}
	return parameterValues, nil
}

// configureAspectAttrs computes the values of the attrs of an aspect,
// which are provided to the aspect implementation function through
// ctx.attr. Public attrs are parameters of the aspect, whose values are
// stored in AppliedAspect.parameters if they differ from their default
// values. Private attrs always use their default value. The labels
// contained in private label attrs have already been resolved by
// ConfiguredAspect.
func configureAspectAttrs[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	ctx context.Context,
	e configuredTargetReferenceCreatorEnvironment[TReference, TMetadata],
	thread *eval.Thread,
	analysisEncoders *AnalysisEncoders[TReference],
	starlarkValueEncoders *StarlarkValueEncoders[TReference],
	aspectDefinition model_core.Message[*model_starlark_pb.Aspect_Definition, TReference],
	parameters model_core.Message[*model_core_pb.DecodableReference, TReference],
	configuredLabelAttrs model_core.Message[[]*model_analysis_pb.IndexedConfiguredLabelAttr, TReference],
	files *labelAttrFiles[TReference, TMetadata],
) (map[string]any, error) {
	parameterValues, err := getAspectParameters(ctx, &analysisEncoders.Readers, &starlarkValueEncoders.Readers, parameters)
	if err != nil {
		return nil, err
	}

	namedAttrs := aspectDefinition.Message.Attrs
	attrValues := make(map[string]any, len(namedAttrs))
	var labelAttrs []*model_starlark_pb.NamedAttr
	var labelAttrConfigurations []*model_analysis_pb.ConfiguredLabelAttr
	remainingConfiguredLabelAttrs := configuredLabelAttrs.Message
	for attrIndex, namedAttr := range namedAttrs {
		if _, _, isLabelAttr := getLabelAttrOptions(namedAttr.Attr); isLabelAttr {
			if len(remainingConfiguredLabelAttrs) == 0 || remainingConfiguredLabelAttrs[0].AttrIndex != uint32(attrIndex) {
				return nil, fmt.Errorf("configured aspect does not contain a value for label attr %#v", namedAttr.Name)
			}
			labelAttrs = append(labelAttrs, namedAttr)
			labelAttrConfigurations = append(labelAttrConfigurations, remainingConfiguredLabelAttrs[0].Configured)
			remainingConfiguredLabelAttrs = remainingConfiguredLabelAttrs[1:]
			continue
		}

		// Attrs that are not label attrs use the value of the
		// parameter if set, or their default value otherwise.
		var encodedValue model_core.Message[*model_starlark_pb.Value, TReference]
		if parameterValue, ok := parameterValues[namedAttr.Name]; ok {
			if !strings.HasPrefix(namedAttr.Name, "_") {
				delete(parameterValues, namedAttr.Name)
				encodedValue = parameterValue
			}
		}
		if encodedValue.Message == nil {
			defaultValue := namedAttr.Attr.GetDefault()
			if defaultValue == nil {
				return nil, fmt.Errorf("missing value for mandatory attr %#v", namedAttr.Name)
			}
			encodedValue = model_core.Nested(aspectDefinition, defaultValue)
		}
		value, err := model_starlark.DecodeValue[TReference, TMetadata](thread, encodedValue, nil)
		if err != nil {
			return nil, fmt.Errorf("attr %#v: %w", namedAttr.Name, err)
		}
		attrValues[namedAttr.Name] = value
	}
	for name := range parameterValues {
		return nil, fmt.Errorf("aspect parameter %#v does not correspond to a public attr of the aspect", name)
	}
	if len(remainingConfiguredLabelAttrs) > 0 {
		return nil, fmt.Errorf("configured aspect contains a value for attr index %d, which does not refer to a label attr", remainingConfiguredLabelAttrs[0].AttrIndex)
	}

	kwargs, err := configureResolvedLabelAttrs[TReference, TMetadata](
		ctx,
		e,
		thread,
		starlarkValueEncoders,
		model_core.Nested(aspectDefinition, labelAttrs),
		model_core.Nested(configuredLabelAttrs, labelAttrConfigurations),
		files,
	)
	if err != nil {
		return nil, err
	}
	for _, kwarg := range kwargs {
		attrValues[kwarg.Name] = kwarg.Value
	}
	return attrValues, nil
}

// aspectContext is the ctx object that is provided to the
// implementation function of an aspect.
type aspectContext[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	*targetContext[TReference, TMetadata]

	attr       eval.Value
	executable eval.Value
	file       eval.Value
	files      eval.Value
	rule       eval.Value
}

var _ eval.HasAttrs = (*aspectContext[object.GlobalReference, model_core.ReferenceMetadata])(nil)

// Repr writes the representation of the value.
func (ac *aspectContext[TReference, TMetadata]) Repr(_ *eval.Thread, printer *eval.Printer) error {
	printer.WriteString(fmt.Sprintf("<aspect ctx for %s>", ac.targetLabel.String()))
	return nil
}

func (aspectContext[TReference, TMetadata]) Type() string {
	return "ctx"
}

func (aspectContext[TReference, TMetadata]) Truth(*eval.Thread) (bool, error) {
	return true, nil
}

func (ac *aspectContext[TReference, TMetadata]) Attr(thread *eval.Thread, name string) (eval.Value, error) {
	switch name {
	case "actions":
		return &targetContextActions[TReference, TMetadata]{
			targetContext: ac.targetContext,
		}, nil
	case "attr":
		return ac.attr, nil
	case "bin_dir":
		configurationComponent, err := model_starlark.ConfigurationReferenceToComponent(ac.configurationReference)
		if err != nil {
			return nil, err
		}
		return model_starlark.NewStructFromDict[TReference, TMetadata](nil, map[string]any{
			"path": eval.String(model_starlark.ComponentStrBazelOut + "/" + configurationComponent + "/" + model_starlark.ComponentStrBin),
		}), nil
	case "exec_groups":
		return &targetContextExecGroups[TReference, TMetadata]{
			targetContext: ac.targetContext,
		}, nil
	case "executable":
		return ac.executable, nil
	case "file":
		return ac.file, nil
	case "files":
		return ac.files, nil
	case "label":
		return model_starlark.NewLabel[TReference, TMetadata](ac.targetLabel.AsResolved()), nil
	case "rule":
		return ac.rule, nil
	default:
		return nil, eval.NoSuchAttrError{Value: ac, Name: name}
	}
}

var aspectContextAttrNames = []string{
	"actions",
	"attr",
	"bin_dir",
	"exec_groups",
	"executable",
	"file",
	"files",
	"label",
	"rule",
}

func (aspectContext[TReference, TMetadata]) AttrNames(*eval.Thread) ([]string, error) {
	return aspectContextAttrNames, nil
}
