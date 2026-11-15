package analysis

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	"bonanza.build/pkg/model/evaluation"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/unpack"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/protobuf/proto"
)

var (
	constraintValueInfoProviderIdentifier      = util.Must(label.NewCanonicalStarlarkIdentifier("@@builtins_core+//:exports.bzl%ConstraintValueInfo"))
	defaultInfoProviderIdentifier              = util.Must(label.NewCanonicalStarlarkIdentifier("@@builtins_core+//:exports.bzl%DefaultInfo"))
	packageSpecificationInfoProviderIdentifier = util.Must(label.NewCanonicalStarlarkIdentifier("@@builtins_core+//:exports.bzl%PackageSpecificationInfo"))
	toolchainInfoProviderIdentifier            = util.Must(label.NewCanonicalStarlarkIdentifier("@@builtins_core+//:exports.bzl%ToolchainInfo"))
	filesToRunProviderIdentifier               = util.Must(label.NewCanonicalStarlarkIdentifier("@@builtins_core+//:exports.bzl%FilesToRunProvider"))
)

// constraintValuesToConstraints converts a list of labels of constraint
// values to a list of Constraint messages that include both the
// constraint setting and constraint value labels. These can be used to
// perform matching of constraints.
func constraintValuesToConstraints[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](ctx context.Context, e getProviderFromVisibleConfiguredTargetEnvironment[TReference, TMetadata], valueReaders *model_starlark.ValueReaders[TReference], fromPackage label.CanonicalPackage, constraintValues []string) ([]*model_analysis_pb.Constraint, error) {
	constraints := make(map[string]string, len(constraintValues))
	missingDependencies := false
	for _, constraintValue := range constraintValues {
		constrainValueInfoProvider, _, err := getProviderFromVisibleConfiguredTarget(
			e,
			fromPackage.String(),
			constraintValue,
			model_core.NewSimpleMessage[TReference]((*model_core_pb.DecodableReference)(nil)),
			constraintValueInfoProviderIdentifier,
		)
		if err != nil {
			if errors.Is(err, evaluation.ErrMissingDependency) {
				missingDependencies = true
				continue
			}
			return nil, err
		}

		var actualConstraintSetting, actualConstraintValue, defaultConstraintValue *string
		var errIter error
		for key, value := range model_starlark.AllStructFields(ctx, valueReaders.List, constrainValueInfoProvider, &errIter) {
			switch key {
			case "constraint":
				constraintSettingInfoProvider, ok := value.Message.Kind.(*model_starlark_pb.Value_Struct)
				if !ok {
					return nil, fmt.Errorf("field \"constraint\" of ConstraintValueInfo provider of target %#v is not a struct", constraintValue)
				}
				var errIter error
				for key, value := range model_starlark.AllStructFields(
					ctx,
					valueReaders.List,
					model_core.Nested(value, constraintSettingInfoProvider.Struct.Fields),
					&errIter,
				) {
					switch key {
					case "default_constraint_value":
						switch v := value.Message.Kind.(type) {
						case *model_starlark_pb.Value_Label:
							defaultConstraintValue = &v.Label
						case *model_starlark_pb.Value_None:
						default:
							return nil, fmt.Errorf("field \"constraint.default_constraint_value\" of ConstraintValueInfo provider of target %#v is not a Label or None", constraintValue)
						}
					case "label":
						v, ok := value.Message.Kind.(*model_starlark_pb.Value_Label)
						if !ok {
							return nil, fmt.Errorf("field \"constraint.label\" of ConstraintValueInfo provider of target %#v is not a Label", constraintValue)
						}
						actualConstraintSetting = &v.Label
					}
				}
				if errIter != nil {
					return nil, err
				}
			case "label":
				v, ok := value.Message.Kind.(*model_starlark_pb.Value_Label)
				if !ok {
					return nil, fmt.Errorf("field \"label\" of ConstraintValueInfo provider of target %#v is not a Label", constraintValue)
				}
				actualConstraintValue = &v.Label
			}
		}
		if errIter != nil {
			return nil, errIter
		}
		if actualConstraintSetting == nil {
			return nil, fmt.Errorf("ConstraintValueInfo provider of target %#v does not contain field \"constraint.label\"", constraintValue)
		}
		if actualConstraintValue == nil {
			return nil, fmt.Errorf("ConstraintValueInfo provider of target %#v does not contain field \"label\"", constraintValue)
		}
		effectiveConstraintValue := *actualConstraintValue
		if defaultConstraintValue != nil && effectiveConstraintValue == *defaultConstraintValue {
			effectiveConstraintValue = ""
		}

		if _, ok := constraints[*actualConstraintSetting]; ok {
			return nil, fmt.Errorf("got multiple constraint values for constraint setting %#v", *actualConstraintSetting)
		}
		constraints[*actualConstraintSetting] = effectiveConstraintValue

	}
	if missingDependencies {
		return nil, evaluation.ErrMissingDependency
	}

	sortedConstraints := make([]*model_analysis_pb.Constraint, 0, len(constraints))
	for _, constraintSetting := range slices.Sorted(maps.Keys(constraints)) {
		sortedConstraints = append(
			sortedConstraints,
			&model_analysis_pb.Constraint{
				Setting: constraintSetting,
				Value:   constraints[constraintSetting],
			},
		)
	}
	return sortedConstraints, nil
}

// mapStructFields creates a struct that has the same provider type and
// fields as an originally provided struct, but has its fields mapped to
// potentially different values.
func (c *baseComputer[TReference, TMetadata]) mapStructFields(
	ctx context.Context,
	e ConfiguredTargetEnvironment[TReference, TMetadata],
	starlarkValueEncoders *StarlarkValueEncoders[TReference],
	in model_core.Message[*model_starlark_pb.Struct, TReference],
	fieldMapper func(name string, value model_core.Message[*model_starlark_pb.Value, TReference]) (any, error),
) (model_core.PatchedMessage[*model_starlark_pb.Struct, TMetadata], error) {
	valueReaders := &starlarkValueEncoders.Readers
	valueEncodingOptionsFactory, err := c.newValueEncodingOptionsFactory(e, starlarkValueEncoders)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Struct, TMetadata]{}, err
	}
	fields := map[string]any{}
	var errIter error
	for name, value := range model_starlark.AllStructFields(
		ctx,
		valueReaders.List,
		model_core.Nested(in, in.Message.GetFields()),
		&errIter,
	) {
		mappedValue, err := fieldMapper(name, value)
		if err != nil {
			return model_core.PatchedMessage[*model_starlark_pb.Struct, TMetadata]{}, fmt.Errorf("field %#v: %w", name, err)
		}
		fields[name] = mappedValue
	}
	if errIter != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Struct, TMetadata]{}, errIter
	}

	// The fields are either backed by storage, or depsets
	// containing values that are backed by storage. Encoding these
	// does not require any values to be decoded.
	thread := eval.NewThread(ctx, eval.Universe())
	encodedFields, err := model_starlark.NewStructFromDict[TReference, TMetadata](nil, fields).
		EncodeStructFields(thread, nil, valueEncodingOptionsFactory.newValueEncodingOptions(ctx, nil))
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Struct, TMetadata]{}, err
	}

	providerInstanceProperties := model_core.Patch(e, model_core.Nested(in, in.Message.GetProviderInstanceProperties()))
	patcher := encodedFields.Patcher
	patcher.Merge(providerInstanceProperties.Patcher)
	return model_core.NewPatchedMessage(
		&model_starlark_pb.Struct{
			ProviderInstanceProperties: providerInstanceProperties.Message,
			Fields:                     encodedFields.Message,
		},
		patcher,
	), nil
}

// getSingleFileConfiguredTargetValue creates a DefaultInfo that
// references a single file. This is used to create the DefaultInfo
// instances of source files and predeclared output files.
func (c *baseComputer[TReference, TMetadata]) getSingleFileConfiguredTargetValue(
	ctx context.Context,
	e ConfiguredTargetEnvironment[TReference, TMetadata],
	starlarkValueEncoders *StarlarkValueEncoders[TReference],
	emptyDefaultInfo model_core.Message[*model_starlark_pb.Struct, TReference],
	file model_core.Message[*model_starlark_pb.File, TReference],
	identifierGenerator model_starlark.ReferenceEqualIdentifierGenerator,
) (PatchedConfiguredTargetValue[TMetadata], error) {
	newDefaultInfo, err := c.mapStructFields(
		ctx,
		e,
		starlarkValueEncoders,
		emptyDefaultInfo,
		func(name string, value model_core.Message[*model_starlark_pb.Value, TReference]) (any, error) {
			switch name {
			case "files":
				return model_starlark.NewDepset(
					model_starlark.NewDepsetContentsFromList[TReference, TMetadata](
						[]any{
							model_core.Nested(
								file,
								&model_starlark_pb.List_Element{
									Level: &model_starlark_pb.List_Element_Leaf{
										Leaf: &model_starlark_pb.Value{
											Kind: &model_starlark_pb.Value_File{
												File: file.Message,
											},
										},
									},
								},
							),
						},
						model_starlark_pb.Depset_DEFAULT,
					),
					identifierGenerator,
				), nil
			case "files_to_run":
				filesToRun, ok := value.Message.Kind.(*model_starlark_pb.Value_Struct)
				if !ok {
					return nil, errors.New("not a FilesToRunProvider")
				}
				patchedFilesToRun, err := c.mapStructFields(
					ctx,
					e,
					starlarkValueEncoders,
					model_core.Nested(value, filesToRun.Struct),
					func(name string, value model_core.Message[*model_starlark_pb.Value, TReference]) (any, error) {
						switch name {
						case "executable":
							return model_core.Nested(
								file,
								&model_starlark_pb.Value{
									Kind: &model_starlark_pb.Value_File{
										File: file.Message,
									},
								},
							), nil
						default:
							return value, nil
						}
					},
				)
				if err != nil {
					return nil, err
				}
				newFilesToRun := model_core.Unpatch(e, patchedFilesToRun).Decay()
				return model_core.Nested(
					newFilesToRun,
					&model_starlark_pb.Value{
						Kind: &model_starlark_pb.Value_Struct{
							Struct: newFilesToRun.Message,
						},
					},
				), nil
			default:
				return value, nil
			}
		},
	)
	if err != nil {
		return PatchedConfiguredTargetValue[TMetadata]{}, err
	}

	return model_core.NewPatchedMessage(
		&model_analysis_pb.ConfiguredTarget_Value{
			ProviderInstances: []*model_starlark_pb.Struct{
				newDefaultInfo.Message,
			},
		},
		newDefaultInfo.Patcher,
	), nil
}

func (c *baseComputer[TReference, TMetadata]) ComputeConfiguredTargetValue(ctx context.Context, key model_core.Message[*model_analysis_pb.ConfiguredTarget_Key, TReference], e ConfiguredTargetEnvironment[TReference, TMetadata]) (PatchedConfiguredTargetValue[TMetadata], error) {
	analysisEncoders, gotAnalysisEncoders := getAnalysisEncoders(e)
	starlarkValueEncoders, gotStarlarkValueEncoders := getStarlarkValueEncoders(e)

	valueEncodingOptionsFactory, err := c.newValueEncodingOptionsFactory(e, starlarkValueEncoders)
	if err != nil {
		return PatchedConfiguredTargetValue[TMetadata]{}, err
	}

	targetLabel, err := label.NewCanonicalLabel(key.Message.Label)
	if err != nil {
		return PatchedConfiguredTargetValue[TMetadata]{}, fmt.Errorf("invalid target label: %w", err)
	}
	emptyDefaultInfoValue := e.GetEmptyDefaultInfoValue(&model_analysis_pb.EmptyDefaultInfo_Key{})
	targetValue := e.GetTargetValue(&model_analysis_pb.Target_Key{
		Label: targetLabel.String(),
	})
	if !emptyDefaultInfoValue.IsSet() || !targetValue.IsSet() || !gotAnalysisEncoders || !gotStarlarkValueEncoders {
		return PatchedConfiguredTargetValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	valueReaders := &starlarkValueEncoders.Readers
	if targetValue.Message.Definition == nil {
		return PatchedConfiguredTargetValue[TMetadata]{}, errors.New("target does not exist")
	}

	emptyDefaultInfo := model_core.Nested(emptyDefaultInfoValue, emptyDefaultInfoValue.Message.DefaultInfo)
	switch targetKind := targetValue.Message.Definition.GetKind().(type) {
	case *model_starlark_pb.Target_Definition_PackageGroup:
		patchedDefaultInfo := model_core.Patch(e, emptyDefaultInfo)
		return model_core.NewPatchedMessage(
			&model_analysis_pb.ConfiguredTarget_Value{
				ProviderInstances: []*model_starlark_pb.Struct{
					patchedDefaultInfo.Message,
					{
						ProviderInstanceProperties: &model_starlark_pb.Provider_InstanceProperties{
							ProviderIdentifier: packageSpecificationInfoProviderIdentifier.String(),
						},
						Fields: &model_starlark_pb.Struct_Fields{},
					},
				},
			},
			patchedDefaultInfo.Patcher,
		), nil
	case *model_starlark_pb.Target_Definition_PredeclaredOutputFileTarget:
		// Handcraft a DefaultInfo provider for this source file.
		identifierGenerator, err := c.getReferenceEqualIdentifierGenerator(model_core.Nested(key, proto.Message(key.Message)))
		if err != nil {
			return PatchedConfiguredTargetValue[TMetadata]{}, err
		}

		return c.getSingleFileConfiguredTargetValue(
			ctx,
			e,
			starlarkValueEncoders,
			emptyDefaultInfo,
			model_core.Nested(
				key,
				&model_starlark_pb.File{
					Owner: &model_starlark_pb.File_Owner{
						ConfigurationReference: key.Message.ConfigurationReference,
						TargetName:             targetKind.PredeclaredOutputFileTarget.OwnerTargetName,
						Type:                   model_starlark_pb.File_Owner_FILE,
					},
					Label: targetLabel.String(),
				},
			),
			identifierGenerator,
		)
	case *model_starlark_pb.Target_Definition_RuleTarget:
		ruleTarget := targetKind.RuleTarget
		ruleIdentifier, err := label.NewCanonicalStarlarkIdentifier(ruleTarget.RuleIdentifier)
		if err != nil {
			return PatchedConfiguredTargetValue[TMetadata]{}, err
		}

		allBuiltinsModulesNames := e.GetBuiltinsModuleNamesValue(&model_analysis_pb.BuiltinsModuleNames_Key{})
		actionEncoders, gotActionEncoders := e.GetActionEncodersObject(&model_analysis_pb.ActionEncoders_Key{})
		directoryCreationParameters, gotDirectoryCreationParameters := e.GetDirectoryCreationParametersObject(&model_analysis_pb.DirectoryCreationParameters_Key{})
		fileCreationParameters, gotFileCreationParameters := e.GetFileCreationParametersObject(&model_analysis_pb.FileCreationParameters_Key{})
		ruleValue := e.GetCompiledBzlFileGlobalValue(&model_analysis_pb.CompiledBzlFileGlobal_Key{
			Identifier: ruleIdentifier.String(),
		})
		ruleImplementationWrappers := e.GetRuleImplementationWrappersValue(&model_analysis_pb.RuleImplementationWrappers_Key{})
		if !allBuiltinsModulesNames.IsSet() ||
			!gotActionEncoders ||
			!gotDirectoryCreationParameters ||
			!gotFileCreationParameters ||
			!ruleValue.IsSet() ||
			!ruleImplementationWrappers.IsSet() {
			return PatchedConfiguredTargetValue[TMetadata]{}, evaluation.ErrMissingDependency
		}
		v, ok := ruleValue.Message.Global.GetKind().(*model_starlark_pb.Value_Rule)
		if !ok {
			return PatchedConfiguredTargetValue[TMetadata]{}, fmt.Errorf("%#v is not a rule", ruleIdentifier.String())
		}
		d, ok := v.Rule.Kind.(*model_starlark_pb.Rule_Definition_)
		if !ok {
			return PatchedConfiguredTargetValue[TMetadata]{}, fmt.Errorf("%#v is not a rule definition", ruleIdentifier.String())
		}
		ruleDefinition := model_core.Nested(ruleValue, d.Definition)

		thread, err := c.newStarlarkThread(ctx, e, starlarkValueEncoders, allBuiltinsModulesNames.Message.BuiltinsModuleNames)
		if err != nil {
			return PatchedConfiguredTargetValue[TMetadata]{}, err
		}

		// Set all common attrs.
		attrValues := make(map[string]any, len(ruleDefinition.Message.Attrs)+2)
		name := eval.String(targetLabel.GetTargetName().String())
		attrValues["name"] = name

		tags := make([]eval.Value, 0, len(ruleTarget.Tags))
		for _, tag := range ruleTarget.Tags {
			tags = append(tags, eval.String(tag))
		}
		tagsList := eval.NewList(tags)
		attrValues["tags"] = tagsList

		attrValues["testonly"] = eval.Bool(ruleTarget.InheritableAttrs.GetTestonly())

		edgeTransitionAttrValues := make(map[string]any, len(ruleDefinition.Message.Attrs)+2)
		for k, v := range attrValues {
			edgeTransitionAttrValues[k] = v
		}

		// Obtain all attr values that don't depend on any
		// configuration, as these need to be provided to any
		// incoming edge transitions.
		ruleTargetPublicAttrValues := ruleTarget.PublicAttrValues
	GetConfigurationFreeAttrValues:
		for _, namedAttr := range ruleDefinition.Message.Attrs {
			var publicAttrValue *model_starlark_pb.RuleTarget_PublicAttrValue
			if !strings.HasPrefix(namedAttr.Name, "_") {
				if len(ruleTargetPublicAttrValues) == 0 {
					return PatchedConfiguredTargetValue[TMetadata]{}, errors.New("rule target has fewer public attr values than the rule definition has public attrs")
				}
				publicAttrValue = ruleTargetPublicAttrValues[0]
				ruleTargetPublicAttrValues = ruleTargetPublicAttrValues[1:]
			}

			var valueParts []model_core.Message[*model_starlark_pb.Value, TReference]
			if !strings.HasPrefix(namedAttr.Name, "_") {
				// Attr is public. Extract the value
				// from the rule target.
				selectGroups := publicAttrValue.ValueParts
				if len(selectGroups) == 0 {
					return PatchedConfiguredTargetValue[TMetadata]{}, fmt.Errorf("attr %#v has no select groups", namedAttr.Name)
				}
				for _, selectGroup := range selectGroups {
					if len(selectGroup.Conditions) > 0 {
						// Conditions are present, meaning the value
						// depends on a configuration.
						continue GetConfigurationFreeAttrValues
					}
					noMatch, ok := selectGroup.NoMatch.(*model_starlark_pb.Select_Group_NoMatchValue)
					if !ok {
						// No default value provided.
						continue GetConfigurationFreeAttrValues
					}
					valueParts = append(valueParts, model_core.Nested(targetValue, noMatch.NoMatchValue))
				}

				// If the value is None, fall back to the
				// default value from the rule definition.
				if len(valueParts) == 1 {
					if _, ok := valueParts[0].Message.Kind.(*model_starlark_pb.Value_None); ok {
						valueParts = valueParts[:0]
					}
				}
			}

			// No value provided. Use the default value from the
			// rule definition.
			if len(valueParts) == 0 {
				defaultValue := namedAttr.Attr.GetDefault()
				if defaultValue == nil {
					return PatchedConfiguredTargetValue[TMetadata]{}, fmt.Errorf("missing value for mandatory attr %#v", namedAttr.Name)
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
					return PatchedConfiguredTargetValue[TMetadata]{}, err
				}
				if err := attrValueBuilder.Add(thread, decodedPart); err != nil {
					return PatchedConfiguredTargetValue[TMetadata]{}, err
				}
			}
			attrValue, err := attrValueBuilder.Build(thread)
			if err != nil {
				return PatchedConfiguredTargetValue[TMetadata]{}, err
			}

			switch namedAttr.Attr.GetType().(type) {
			case *model_starlark_pb.Attr_Label, *model_starlark_pb.Attr_LabelList, *model_starlark_pb.Attr_LabelKeyedStringDict,
				*model_starlark_pb.Attr_Output, *model_starlark_pb.Attr_OutputList:
				// Don't set these, as they depend on
				// the configuration.
			default:
				attrValues[namedAttr.Name] = attrValue
			}

			edgeTransitionAttrValues[namedAttr.Name] = attrValue
		}
		if l := len(ruleTargetPublicAttrValues); l != 0 {
			return PatchedConfiguredTargetValue[TMetadata]{}, fmt.Errorf("rule target has %d more public attr values than the rule definition has public attrs", l)
		}

		// If provided, apply a user defined incoming edge transition.
		configurationReference := model_core.Nested(key, key.Message.ConfigurationReference)
		if cfgTransition := ruleDefinition.Message.CfgTransition; cfgTransition != nil {
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
				return PatchedConfiguredTargetValue[TMetadata]{}, err
			}

			entries := patchedConfigurationReferences.Message.Entries
			if l := len(entries); l != 1 {
				return PatchedConfiguredTargetValue[TMetadata]{}, fmt.Errorf("incoming edge transition used by rule %#v is a 1:%d transition, while a 1:1 transition was expected", ruleIdentifier.String(), l)
			}

			configurationReferences := model_core.Unpatch(e, patchedConfigurationReferences).Decay()
			configurationReference = model_core.Nested(configurationReferences, entries[0].OutputConfigurationReference)
		}

		// Compute non-label attrs that depend on a
		// configuration, due to them using select().
		missingDependencies := false
		outputsValues := map[string]any{}
		ruleTargetPublicAttrValues = ruleTarget.PublicAttrValues
		targetPackage := targetLabel.GetCanonicalPackage()
		outputRegistrar := targetOutputRegistrar[TReference, TMetadata]{
			configurationReference: configurationReference,
			targetLabel:            targetLabel,

			outputsByPackageRelativePath: map[string]*targetOutput[TMetadata]{},
			outputsByFile:                map[*model_starlark.File[TReference, TMetadata]]*targetOutput[TMetadata]{},
		}
		defer func() {
			for _, output := range outputRegistrar.outputsByPackageRelativePath {
				output.definition.Discard()
			}
		}()

	GetNonLabelAttrValues:
		for _, namedAttr := range ruleDefinition.Message.Attrs {
			var publicAttrValue *model_starlark_pb.RuleTarget_PublicAttrValue
			if !strings.HasPrefix(namedAttr.Name, "_") {
				publicAttrValue = ruleTargetPublicAttrValues[0]
				ruleTargetPublicAttrValues = ruleTargetPublicAttrValues[1:]
			}
			if _, ok := attrValues[namedAttr.Name]; ok {
				// Attr was already computed previously.
				continue
			}

			switch namedAttr.Attr.GetType().(type) {
			case *model_starlark_pb.Attr_Label, *model_starlark_pb.Attr_LabelList, *model_starlark_pb.Attr_LabelKeyedStringDict:
				continue GetNonLabelAttrValues
			}

			valueParts, _, err := getAttrValueParts(
				e,
				configurationReference,
				targetPackage,
				model_core.Nested(ruleDefinition, namedAttr),
				model_core.Nested(targetValue, publicAttrValue),
			)
			if err != nil {
				if errors.Is(err, evaluation.ErrMissingDependency) {
					missingDependencies = true
					continue GetNonLabelAttrValues
				}
				return PatchedConfiguredTargetValue[TMetadata]{}, err
			}

			var attrValueBuilder model_starlark.AttrValueBuilder
			var attrOutputs []eval.Value
			for _, valuePart := range valueParts.Message {
				decodedPart, err := model_starlark.DecodeAttrValuePart[TReference, TMetadata](
					thread, model_core.Nested(valueParts, valuePart),
					starlarkValueEncoders.newValueDecodingOptions(func(resolvedLabel label.ResolvedLabel) (eval.Value, error) {
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
							f, err := outputRegistrar.registerOutput(canonicalLabel.GetTargetName(), nil, model_starlark_pb.File_Owner_FILE)
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
					return PatchedConfiguredTargetValue[TMetadata]{}, err
				}
				if err := attrValueBuilder.Add(thread, decodedPart); err != nil {
					return PatchedConfiguredTargetValue[TMetadata]{}, err
				}
			}
			attrValue, err := attrValueBuilder.Build(thread)
			if err != nil {
				return PatchedConfiguredTargetValue[TMetadata]{}, err
			}
			attrValues[namedAttr.Name] = attrValue
			edgeTransitionAttrValues[namedAttr.Name] = attrValue

			switch namedAttr.Attr.GetType().(type) {
			case *model_starlark_pb.Attr_Output:
				if len(attrOutputs) == 0 {
					outputsValues[namedAttr.Name] = eval.None
				} else if len(attrOutputs) == 1 {
					outputsValues[namedAttr.Name] = attrOutputs[0]
				} else {
					return PatchedConfiguredTargetValue[TMetadata]{}, fmt.Errorf("value of attr %#v contains multiple labels, which is not expected for attrs of type output", namedAttr.Name)
				}
			case *model_starlark_pb.Attr_OutputList:
				outputsValues[namedAttr.Name] = eval.NewList(attrOutputs)
			}
		}

		// Resolve all toolchains and execution platforms.
		namedExecGroups := ruleDefinition.Message.ExecGroups
		execGroups := make([]ruleContextExecGroupState, 0, len(namedExecGroups))
		execGroupPlatformLabels := map[string]string{}
		for _, namedExecGroup := range namedExecGroups {
			execGroupDefinition := namedExecGroup.ExecGroup
			if execGroupDefinition == nil {
				return PatchedConfiguredTargetValue[TMetadata]{}, fmt.Errorf("missing definition of exec group %#v", namedExecGroup.Name)
			}
			execCompatibleWith, err := constraintValuesToConstraints(
				ctx,
				e,
				valueReaders,
				targetPackage,
				execGroupDefinition.ExecCompatibleWith,
			)
			if err != nil {
				return PatchedConfiguredTargetValue[TMetadata]{}, fmt.Errorf("invalid constraint values for exec group %#v: %w", namedExecGroup.Name, err)
			}
			patchedConfigurationReference := model_core.Patch(e, configurationReference)
			resolvedToolchains := e.GetResolvedToolchainsValue(
				model_core.NewPatchedMessage(
					&model_analysis_pb.ResolvedToolchains_Key{
						ExecCompatibleWith:     execCompatibleWith,
						ConfigurationReference: patchedConfigurationReference.Message,
						Toolchains:             execGroupDefinition.Toolchains,
					},
					patchedConfigurationReference.Patcher,
				),
			)
			if !resolvedToolchains.IsSet() {
				missingDependencies = true
				continue
			}
			toolchainIdentifiers := resolvedToolchains.Message.ToolchainIdentifiers
			if actual, expected := len(toolchainIdentifiers), len(execGroupDefinition.Toolchains); actual != expected {
				return PatchedConfiguredTargetValue[TMetadata]{}, fmt.Errorf("obtained %d resolved toolchains, while exec group %#v depends on %d toolchains", actual, namedExecGroup.Name, expected)
			}

			execGroups = append(execGroups, ruleContextExecGroupState{
				platformPkixPublicKey: resolvedToolchains.Message.PlatformPkixPublicKey,
				toolchainIdentifiers:  toolchainIdentifiers,
				toolchainInfos:        make([]eval.Value, len(toolchainIdentifiers)),
			})
			execGroupPlatformLabels[namedExecGroup.Name] = resolvedToolchains.Message.PlatformLabel
		}

		if missingDependencies {
			return PatchedConfiguredTargetValue[TMetadata]{}, evaluation.ErrMissingDependency
		}

		// Last but not least, get the values of label attr.
		executableValues := map[string]any{}
		executableFileToFilesToRun := map[*model_starlark.File[TReference, TMetadata]]model_core.Message[*model_starlark_pb.Struct, TReference]{}
		fileValues := map[string]any{}
		filesValues := map[string]any{}
		splitAttrValues := map[string]any{}
		ruleTargetPublicAttrValues = ruleTarget.PublicAttrValues
		edgeTransitionAttrValuesStruct := model_starlark.NewStructFromDict[TReference, TMetadata](nil, edgeTransitionAttrValues)
	GetLabelAttrValues:
		for _, namedAttr := range ruleDefinition.Message.Attrs {
			var publicAttrValue *model_starlark_pb.RuleTarget_PublicAttrValue
			if !strings.HasPrefix(namedAttr.Name, "_") {
				publicAttrValue = ruleTargetPublicAttrValues[0]
				ruleTargetPublicAttrValues = ruleTargetPublicAttrValues[1:]
			}
			if _, ok := attrValues[namedAttr.Name]; ok {
				// Attr was already computed previously.
				continue
			}

			isScalar := false
			var labelOptions *model_starlark_pb.Attr_LabelOptions
			allowSingleFile := false
			executable := false
			switch attrType := namedAttr.Attr.GetType().(type) {
			case *model_starlark_pb.Attr_Label:
				labelOptions = attrType.Label.ValueOptions
				isScalar = true
				allowSingleFile = attrType.Label.AllowSingleFile
				executable = attrType.Label.Executable
			case *model_starlark_pb.Attr_LabelKeyedStringDict:
				labelOptions = attrType.LabelKeyedStringDict.DictKeyOptions
			case *model_starlark_pb.Attr_LabelList:
				labelOptions = attrType.LabelList.ListValueOptions
			default:
				panic("only label attr types should be processed at this point")
			}
			if labelOptions == nil {
				return PatchedConfiguredTargetValue[TMetadata]{}, fmt.Errorf("attr %#v does not have label options", namedAttr.Name)
			}

			// Perform outgoing edge transition. User
			// defined transitions get access to all
			// non-label attr values.
			patchedTransition, mayHaveMultipleConfigurations, err := c.performTransition(
				ctx,
				e,
				analysisEncoders,
				starlarkValueEncoders,
				model_core.Nested(ruleDefinition, labelOptions.Cfg),
				configurationReference,
				edgeTransitionAttrValuesStruct,
				execGroupPlatformLabels,
			)
			if err != nil {
				if errors.Is(err, evaluation.ErrMissingDependency) {
					missingDependencies = true
					continue GetLabelAttrValues
				}
				return PatchedConfiguredTargetValue[TMetadata]{}, err
			}
			transition := model_core.Unpatch(e, patchedTransition).Decay()

			var attrValueBuilder model_starlark.AttrValueBuilder
			var splitAttrEntries [][2]eval.Value

			var filesDepsetElements []any

			if len(transition.Message.Entries) == 0 {
				// We should leave targets unconfigured.
				// Perform select() without a configuration.
				valueParts, _, err := getAttrValueParts(
					e,
					model_core.NewSimpleMessage[TReference]((*model_core_pb.DecodableReference)(nil)),
					targetPackage,
					model_core.Nested(ruleDefinition, namedAttr),
					model_core.Nested(targetValue, publicAttrValue),
				)
				if err != nil {
					if errors.Is(err, evaluation.ErrMissingDependency) {
						missingDependencies = true
						continue GetLabelAttrValues
					}
					return PatchedConfiguredTargetValue[TMetadata]{}, err
				}

				// Provide a target reference that does
				// not contain any providers.
				for _, valuePart := range valueParts.Message {
					decodedPart, err := model_starlark.DecodeAttrValuePart[TReference, TMetadata](
						thread, model_core.Nested(valueParts, valuePart),
						starlarkValueEncoders.newValueDecodingOptions(func(originalLabel label.ResolvedLabel) (eval.Value, error) {
							return model_starlark.NewTargetReference[TReference, TMetadata](
								originalLabel,
								/* configured = */ nil,
							), nil
						}),
					)
					if err != nil {
						return PatchedConfiguredTargetValue[TMetadata]{}, err
					}
					if err := attrValueBuilder.Add(thread, decodedPart); err != nil {
						return PatchedConfiguredTargetValue[TMetadata]{}, err
					}
				}
			} else {
				if executable {
					executableValues[namedAttr.Name] = eval.None
				}
				for _, transitionEntry := range transition.Message.Entries {
					outputConfigurationReference := model_core.Nested(transition, transitionEntry.OutputConfigurationReference)
					valueParts, usedDefaultValue, err := getAttrValueParts(
						e,
						outputConfigurationReference,
						targetPackage,
						model_core.Nested(ruleDefinition, namedAttr),
						model_core.Nested(targetValue, publicAttrValue),
					)
					if err != nil {
						if errors.Is(err, evaluation.ErrMissingDependency) {
							missingDependencies = true
							continue GetLabelAttrValues
						}
						return PatchedConfiguredTargetValue[TMetadata]{}, err
					}

					// Whether an explicit value or a default attr
					// value is used determines how visibility is
					// computed. For explicit values, visibility is
					// computed relative to the package declaring
					// the target. For default values, the package
					// declaring the rule is used.
					var visibilityFromPackage label.CanonicalPackage
					if usedDefaultValue {
						visibilityFromPackage = ruleIdentifier.GetCanonicalLabel().GetCanonicalPackage()
					} else {
						visibilityFromPackage = targetPackage
					}

					var splitAttrEntryBuilder model_starlark.AttrValueBuilder
					valueDecodingOptions := starlarkValueEncoders.newValueDecodingOptions(func(originalLabel label.ResolvedLabel) (eval.Value, error) {
						// Resolve the label.
						canonicalOriginalLabel, err := originalLabel.AsCanonical()
						if err != nil {
							return nil, err
						}
						patchedConfigurationReference1 := model_core.Patch(e, outputConfigurationReference)
						resolvedLabelValue := e.GetVisibleTargetValue(
							model_core.NewPatchedMessage(
								&model_analysis_pb.VisibleTarget_Key{
									FromPackage:            visibilityFromPackage.String(),
									ToLabel:                canonicalOriginalLabel.String(),
									ConfigurationReference: patchedConfigurationReference1.Message,
								},
								patchedConfigurationReference1.Patcher,
							),
						)
						if !resolvedLabelValue.IsSet() {
							missingDependencies = true
							return eval.None, nil
						}
						resolvedLabelStr := resolvedLabelValue.Message.Label
						if resolvedLabelStr == "" {
							return eval.None, nil
						}
						canonicalResolvedLabel, err := label.NewCanonicalLabel(resolvedLabelStr)
						if err != nil {
							return nil, fmt.Errorf("invalid label %#v: %w", resolvedLabelStr, err)
						}

						// Obtain the providers of the target.
						patchedConfigurationReference2 := model_core.Patch(e, outputConfigurationReference)
						targetProviders := e.GetTargetProvidersValue(
							model_core.NewPatchedMessage(
								&model_analysis_pb.TargetProviders_Key{
									Label:                  resolvedLabelStr,
									ConfigurationReference: patchedConfigurationReference2.Message,
								},
								patchedConfigurationReference2.Patcher,
							),
						)
						if !targetProviders.IsSet() {
							missingDependencies = true
							return eval.None, nil
						}
						providerInstances := model_core.Nested(targetProviders, targetProviders.Message.ProviderInstances)

						defaultInfoProviderIdentifierStr := defaultInfoProviderIdentifier.String()
						defaultInfoIndex, ok := sort.Find(
							len(providerInstances.Message),
							func(i int) int {
								return strings.Compare(defaultInfoProviderIdentifierStr, providerInstances.Message[i].ProviderInstanceProperties.GetProviderIdentifier())
							},
						)
						if !ok {
							return nil, fmt.Errorf("target with label %#v did not yield provider %#v", resolvedLabelStr, defaultInfoProviderIdentifierStr)
						}

						files, err := model_starlark.GetStructFieldValue(
							ctx,
							valueReaders.List,
							model_core.Nested(providerInstances, providerInstances.Message[defaultInfoIndex].Fields),
							"files",
						)
						if err != nil {
							return nil, fmt.Errorf("failed to obtain field \"files\" of DefaultInfo provider of target with label %#v: %w", resolvedLabelStr, err)
						}
						valueDepset, ok := files.Message.Kind.(*model_starlark_pb.Value_Depset)
						if !ok {
							return nil, fmt.Errorf("field \"files\" of DefaultInfo provider of target with label %#v is not a depset", resolvedLabelStr)
						}
						for _, element := range valueDepset.Depset.Elements {
							// TODO: Validate extensions.
							filesDepsetElements = append(filesDepsetElements, model_core.Nested(files, element))
						}

						if executable {
							filesToRun, err := model_starlark.GetStructFieldValue(
								ctx,
								valueReaders.List,
								model_core.Nested(providerInstances, providerInstances.Message[defaultInfoIndex].Fields),
								"files_to_run",
							)
							if err != nil {
								return nil, fmt.Errorf("failed to obtain field \"files\" of DefaultInfo provider of target with label %#v: %w", resolvedLabelStr, err)
							}
							filesToRunStructValue, ok := filesToRun.Message.Kind.(*model_starlark_pb.Value_Struct)
							if !ok {
								return nil, fmt.Errorf("field \"files_to_run\" of DefaultInfo provider of target with label %#v is not a struct", resolvedLabelStr)
							}
							filesToRunStruct := model_core.Nested(filesToRun, filesToRunStructValue.Struct)
							executableField, err := model_starlark.GetStructFieldValue(
								ctx,
								valueReaders.List,
								model_core.Nested(filesToRunStruct, filesToRunStruct.Message.Fields),
								"executable",
							)
							if err != nil {
								return nil, fmt.Errorf("failed to obtain field \"files_to_run.executable\" of DefaultInfo provider of target with label %#v: %w", resolvedLabelStr, err)
							}
							decodedExecutable, err := model_starlark.DecodeValue[TReference, TMetadata](thread, executableField, nil)
							if err != nil {
								return nil, fmt.Errorf("decode field \"files_to_run.executable\" of DefaultInfo provider of target with label %#v: %w", resolvedLabelStr, err)
							}
							typedExecutable, ok := decodedExecutable.(*model_starlark.File[TReference, TMetadata])
							if !ok {
								return nil, fmt.Errorf("field \"files_to_run.executable\" of DefaultInfo provider of target with label %#v is not a File", resolvedLabelStr)
							}
							executableValues[namedAttr.Name] = typedExecutable
							executableFileToFilesToRun[typedExecutable] = filesToRunStruct
						}

						return model_starlark.NewTargetReference(
							originalLabel,
							model_starlark.NewConfiguredTargetReference[TReference, TMetadata](
								canonicalResolvedLabel,
								providerInstances,
							),
						), nil
					})
					for i, valuePart := range valueParts.Message {
						decodedPart, err := model_starlark.DecodeAttrValuePart[TReference, TMetadata](
							thread, model_core.Nested(valueParts, valuePart),
							valueDecodingOptions,
						)
						if err != nil {
							return PatchedConfiguredTargetValue[TMetadata]{}, fmt.Errorf("decoding attr %#v transition %#v value part %d: %w", namedAttr.Name, transitionEntry.Key, i, err)
						}
						if isScalar && mayHaveMultipleConfigurations {
							decodedPart, err = decodedPart.AsList(thread /* omitNone = */, true)
							if err != nil {
								return PatchedConfiguredTargetValue[TMetadata]{}, err
							}
						}
						if err := attrValueBuilder.Add(thread, decodedPart); err != nil {
							return PatchedConfiguredTargetValue[TMetadata]{}, fmt.Errorf("concatenate attr value parts: %w", err)
						}
						if mayHaveMultipleConfigurations {
							if err := splitAttrEntryBuilder.Add(thread, decodedPart); err != nil {
								return PatchedConfiguredTargetValue[TMetadata]{}, fmt.Errorf("concatenate split attr value parts: %w", err)
							}
						}
					}

					if mayHaveMultipleConfigurations {
						splitAttrEntry, err := splitAttrEntryBuilder.Build(thread)
						if err != nil {
							return PatchedConfiguredTargetValue[TMetadata]{}, err
						}
						splitAttrEntries = append(splitAttrEntries, [2]eval.Value{eval.String(transitionEntry.Key), splitAttrEntry})
					}
				}
			}
			if !missingDependencies {
				attrValue, err := attrValueBuilder.Build(thread)
				if err != nil {
					return PatchedConfiguredTargetValue[TMetadata]{}, err
				}
				attrValues[namedAttr.Name] = attrValue

				if mayHaveMultipleConfigurations {
					immutableSplitAttrValue, err := eval.NewImmutableDict(thread, splitAttrEntries)
					if err != nil {
						return PatchedConfiguredTargetValue[TMetadata]{}, err
					}
					splitAttrValues[namedAttr.Name] = immutableSplitAttrValue
				}

				filesElements, err := model_starlark.NewDepsetContentsFromList[TReference, TMetadata](
					filesDepsetElements,
					model_starlark_pb.Depset_DEFAULT,
				).ToList(thread)
				if err != nil {
					return PatchedConfiguredTargetValue[TMetadata]{}, fmt.Errorf("converting files depset to list: %w", err)
				}
				files := eval.NewImmutableList(filesElements)
				if allowSingleFile {
					switch l := len(filesElements); l {
					case 0:
						fileValues[namedAttr.Name] = eval.None
					case 1:
						fileValues[namedAttr.Name] = filesElements[0]
					default:
						return PatchedConfiguredTargetValue[TMetadata]{}, fmt.Errorf("attr %#v has allow_single_file=True, but its value expands to %d targets", namedAttr.Name, l)
					}
				} else {
					filesValues[namedAttr.Name] = files
				}
			}
		}
		if missingDependencies {
			return PatchedConfiguredTargetValue[TMetadata]{}, evaluation.ErrMissingDependency
		}

		rc := &ruleContext[TReference, TMetadata]{
			computer:                    c,
			context:                     ctx,
			valueEncodingOptionsFactory: valueEncodingOptionsFactory,
			starlarkValueEncoders:       starlarkValueEncoders,
			analysisEncoders:            analysisEncoders,
			environment:                 e,
			ruleIdentifier:              ruleIdentifier,
			targetLabel:                 targetLabel,
			configurationReference:      configurationReference,
			ruleDefinition:              ruleDefinition,
			ruleTarget:                  model_core.Nested(targetValue, ruleTarget),
			attr:                        model_starlark.NewStructFromDict[TReference, TMetadata](nil, attrValues),
			splitAttr:                   model_starlark.NewStructFromDict[TReference, TMetadata](nil, splitAttrValues),
			executable:                  model_starlark.NewStructFromDict[TReference, TMetadata](nil, executableValues),
			executableFileToFilesToRun:  executableFileToFilesToRun,
			file:                        model_starlark.NewStructFromDict[TReference, TMetadata](nil, fileValues),
			files:                       model_starlark.NewStructFromDict[TReference, TMetadata](nil, filesValues),
			outputs:                     model_starlark.NewStructFromDict[TReference, TMetadata](nil, outputsValues),
			execGroups:                  execGroups,
			outputRegistrar:             &outputRegistrar,
			actionEncoder:               actionEncoders.Encoder,
			directoryCreationParameters: directoryCreationParameters,
			fileCreationParameters:      fileCreationParameters,
		}
		defer func() {
			rc.actions.Discard()
		}()

		thread.SetLocal(model_starlark.SubruleInvokerKey, func(subruleIdentifier label.CanonicalStarlarkIdentifier, args eval.Args) (eval.Value, error) {
			// TODO: Subrules are allowed to be nested. Keep a stack!
			permittedSubruleIdentifiers := ruleDefinition.Message.SubruleIdentifiers

			subruleIdentifierStr := subruleIdentifier.String()
			if _, ok := sort.Find(
				len(permittedSubruleIdentifiers),
				func(i int) int { return strings.Compare(subruleIdentifierStr, permittedSubruleIdentifiers[i]) },
			); !ok {
				return nil, fmt.Errorf("subrule %#v cannot be invoked from within the current (sub)rule", subruleIdentifierStr)
			}
			subruleValue := e.GetCompiledBzlFileGlobalValue(&model_analysis_pb.CompiledBzlFileGlobal_Key{
				Identifier: subruleIdentifierStr,
			})
			if !subruleValue.IsSet() {
				return nil, evaluation.ErrMissingDependency
			}
			v, ok := subruleValue.Message.Global.GetKind().(*model_starlark_pb.Value_Subrule)
			if !ok {
				return nil, fmt.Errorf("%#v is not a subrule", subruleIdentifierStr)
			}
			d, ok := v.Subrule.Kind.(*model_starlark_pb.Subrule_Definition_)
			if !ok {
				return nil, fmt.Errorf("%#v is not a subrule definition", subruleIdentifierStr)
			}
			subruleDefinition := model_core.Nested(subruleValue, d.Definition)

			missingDependencies := false

			implementationArgs := append(
				[]eval.Value{
					model_starlark.DecodeFunction[TReference, TMetadata](model_core.Nested(subruleDefinition, subruleDefinition.Message.Implementation)),
					&subruleContext[TReference, TMetadata]{ruleContext: rc},
				},
				args.Positional...,
			)
			implementationKwargs := append(
				make([]eval.NamedArg, 0, len(args.Named)+len(subruleDefinition.Message.Attrs)),
				args.Named...,
			)
			for _, namedAttr := range subruleDefinition.Message.Attrs {
				defaultValue := namedAttr.Attr.GetDefault()
				if defaultValue == nil {
					return nil, fmt.Errorf("missing value for mandatory attr %#v", namedAttr.Name)
				}
				// TODO: Is this using the correct configuration?
				value, err := rc.computer.configureAttrValueParts(
					rc.context,
					rc.environment,
					analysisEncoders,
					rc.starlarkValueEncoders,
					thread,
					model_core.Nested(subruleDefinition, namedAttr),
					model_core.Nested(rc.ruleDefinition, []*model_starlark_pb.Value{defaultValue}),
					rc.configurationReference,
					rc.ruleIdentifier.GetCanonicalLabel().GetCanonicalPackage(),
					execGroupPlatformLabels,
				)
				if err != nil {
					if errors.Is(err, evaluation.ErrMissingDependency) {
						missingDependencies = true
						continue
					}
					return nil, err
				}
				implementationKwargs = append(
					implementationKwargs,
					eval.NamedArg{
						Name:  namedAttr.Name,
						Value: value,
					},
				)
			}

			if missingDependencies {
				return nil, evaluation.ErrMissingDependency
			}

			return eval.Call(thread, decodeImplementationWrapper[TReference, TMetadata](model_core.Nested(ruleImplementationWrappers, ruleImplementationWrappers.Message.Subrule)), eval.Args{Positional: implementationArgs, Named: implementationKwargs})
		})

		identifierGenerator, err := c.getReferenceEqualIdentifierGenerator(model_core.Nested(key, proto.Message(key.Message)))
		if err != nil {
			return PatchedConfiguredTargetValue[TMetadata]{}, err
		}
		thread.SetLocal(model_starlark.ReferenceEqualIdentifierGeneratorKey, identifierGenerator)

		// Invoke the rule implementation function. Instead of
		// calling it directly, we call the rule implementation
		// wrapper function, having both the actual
		// implementation function and ctx as arguments.
		returnValue, err := eval.Call(thread, decodeImplementationWrapper[TReference, TMetadata](model_core.Nested(ruleImplementationWrappers, ruleImplementationWrappers.Message.Rule)), eval.Args{Positional: /* args = */ []eval.Value{
			eval.NewBuiltin("current_ctx_capturer", func(thread *eval.Thread, b *eval.Builtin, args eval.Args) (eval.Value, error) {
				// The rule implementation wrapper
				// function may augment ctx. Capture
				// it, so that we can let
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
			return PatchedConfiguredTargetValue[TMetadata]{}, formatStarlarkError(err)
		}

		// Bazel permits returning either a single provider, or
		// a list of providers.
		var providerInstances []*model_starlark.Struct[TReference, TMetadata]
		structUnpackerInto := unpack.Type[*model_starlark.Struct[TReference, TMetadata]]("struct")
		if err := unpack.IfNotNone(
			unpack.Or([]unpack.UnpackerInto[[]*model_starlark.Struct[TReference, TMetadata]]{
				unpack.Singleton(structUnpackerInto),
				unpack.List(structUnpackerInto),
			}),
		).UnpackInto(thread, returnValue, &providerInstances); err != nil {
			return PatchedConfiguredTargetValue[TMetadata]{}, fmt.Errorf("failed to unpack implementation function return value: %w", err)
		}

		return model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (*model_analysis_pb.ConfiguredTarget_Value, error) {
			// Convert list of providers to a map where the provider
			// identifier is the key.
			providersSeen := make(map[label.CanonicalStarlarkIdentifier]struct{}, len(providerInstances))
			encodedProviderInstances := make([]*model_starlark_pb.Struct, 0, len(providerInstances)+1)
			for i, providerInstance := range providerInstances {
				providerIdentifier, err := providerInstance.GetProviderIdentifier()
				if err != nil {
					return nil, fmt.Errorf("struct returned at index %d: %w", i, err)
				}
				if _, ok := providersSeen[providerIdentifier]; ok {
					return nil, fmt.Errorf("implementation function returned multiple structs for provider %#v", providerIdentifier.String())
				}
				providersSeen[providerIdentifier] = struct{}{}

				v, err := providerInstance.Encode(thread, nil, valueEncodingOptionsFactory.newValueEncodingOptions(ctx, nil))
				if err != nil {
					return nil, err
				}
				encodedProviderInstances = append(encodedProviderInstances, v.Merge(patcher))
			}

			// If the rule did not return an instance of
			// DefaultInfo, inject an empty instance.
			if _, ok := providersSeen[defaultInfoProviderIdentifier]; !ok {
				encodedProviderInstances = append(
					encodedProviderInstances,
					model_core.Patch(e, emptyDefaultInfo).Merge(patcher),
				)
			}

			slices.SortFunc(encodedProviderInstances, func(a, b *model_starlark_pb.Struct) int {
				return strings.Compare(
					a.ProviderInstanceProperties.ProviderIdentifier,
					b.ProviderInstanceProperties.ProviderIdentifier,
				)
			})

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
					return nil, fmt.Errorf("file %#v is not an output of any action", packageRelativePath)
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
					return nil, err
				}
			}
			outputsList, err := outputsTreeBuilder.FinalizeList()
			if err != nil {
				return nil, err
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

			slices.SortFunc(rc.actions, func(a, b model_core.PatchedMessage[*model_analysis_pb.ConfiguredTarget_Value_Action_Leaf, TMetadata]) int {
				return bytes.Compare(a.Message.Id, b.Message.Id)
			})
			for _, action := range rc.actions {
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
					return nil, err
				}
			}
			actionsList, err := actionsTreeBuilder.FinalizeList()
			if err != nil {
				return nil, err
			}
			patcher.Merge(actionsList.Patcher)

			// TODO: We should use inlinedtree.Build() here.
			return &model_analysis_pb.ConfiguredTarget_Value{
				ProviderInstances: encodedProviderInstances,
				Outputs:           outputsList.Message,
				Actions:           actionsList.Message,
			}, nil
		})
	case *model_starlark_pb.Target_Definition_SourceFileTarget:
		// Handcraft a DefaultInfo provider for this source file.
		identifierGenerator, err := c.getReferenceEqualIdentifierGenerator(model_core.Nested(key, proto.Message(key.Message)))
		if err != nil {
			return PatchedConfiguredTargetValue[TMetadata]{}, err
		}
		return c.getSingleFileConfiguredTargetValue(
			ctx,
			e,
			starlarkValueEncoders,
			emptyDefaultInfo,
			model_core.NewSimpleMessage[TReference](
				&model_starlark_pb.File{
					Label: targetLabel.String(),
				},
			),
			identifierGenerator,
		)
	case *model_starlark_pb.Target_Definition_TestSuite:
		// Test suites don't produce any files. They are only
		// used to select tests to run.
		//
		// TODO: Bazel expands test suites in target patterns
		// provided on the command line, meaning that building
		// a test suite causes the tests it contains to be
		// built. Add support for this.
		patchedDefaultInfo := model_core.Patch(e, emptyDefaultInfo)
		return model_core.NewPatchedMessage(
			&model_analysis_pb.ConfiguredTarget_Value{
				ProviderInstances: []*model_starlark_pb.Struct{
					patchedDefaultInfo.Message,
				},
			},
			patchedDefaultInfo.Patcher,
		), nil
	default:
		return PatchedConfiguredTargetValue[TMetadata]{}, errors.New("only source file targets and rule targets can be configured")
	}
}

// getProviderFromConfiguredTarget looks up a single provider that is
// provided by a configured target.
func getProviderFromConfiguredTarget[TReference any, TMetadata model_core.ReferenceMetadata](e TargetProviderValueGetter[TReference, TMetadata], targetLabel string, configurationReference model_core.PatchedMessage[*model_core_pb.DecodableReference, TMetadata], providerIdentifier label.CanonicalStarlarkIdentifier) (model_core.Message[*model_starlark_pb.Struct_Fields, TReference], error) {
	providerIdentifierStr := providerIdentifier.String()
	targetProvider := e.GetTargetProviderValue(
		model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.TargetProvider_Key {
			return &model_analysis_pb.TargetProvider_Key{
				Label:                  targetLabel,
				ConfigurationReference: configurationReference.Merge(patcher),
				ProviderIdentifier:     providerIdentifierStr,
			}
		}),
	)
	if !targetProvider.IsSet() {
		return model_core.Message[*model_starlark_pb.Struct_Fields, TReference]{}, evaluation.ErrMissingDependency
	}
	fields := targetProvider.Message.Fields
	if fields == nil {
		return model_core.Message[*model_starlark_pb.Struct_Fields, TReference]{}, fmt.Errorf("target did not yield provider %#v", providerIdentifierStr)
	}
	return model_core.Nested(targetProvider, fields), nil
}

type getProviderFromVisibleConfiguredTargetEnvironment[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	model_core.ObjectManager[TReference, TMetadata]
	TargetProviderValueGetter[TReference, TMetadata]
	VisibleTargetValueGetter[TReference, TMetadata]
}

func getProviderFromVisibleConfiguredTarget[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	e getProviderFromVisibleConfiguredTargetEnvironment[TReference, TMetadata],
	fromPackage string,
	targetLabel string,
	configurationReference model_core.Message[*model_core_pb.DecodableReference, TReference],
	providerIdentifier label.CanonicalStarlarkIdentifier,
) (model_core.Message[*model_starlark_pb.Struct_Fields, TReference], string, error) {
	patchedConfigurationReference := model_core.Patch(
		e,
		configurationReference,
	)
	visibleTarget := e.GetVisibleTargetValue(
		model_core.NewPatchedMessage(
			&model_analysis_pb.VisibleTarget_Key{
				FromPackage:            fromPackage,
				ToLabel:                targetLabel,
				ConfigurationReference: patchedConfigurationReference.Message,
			},
			patchedConfigurationReference.Patcher,
		),
	)
	if !visibleTarget.IsSet() {
		return model_core.Message[*model_starlark_pb.Struct_Fields, TReference]{}, "", evaluation.ErrMissingDependency
	}
	p, err := getProviderFromConfiguredTarget(
		e,
		visibleTarget.Message.Label,
		model_core.Patch(
			e,
			configurationReference,
		),
		providerIdentifier,
	)
	return p, visibleTarget.Message.Label, err
}
