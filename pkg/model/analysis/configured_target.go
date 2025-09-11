package analysis

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
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
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/unpack"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/filesystem"
	"github.com/buildbarn/bb-storage/pkg/filesystem/path"
	"github.com/buildbarn/bb-storage/pkg/util"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
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
func (c *baseComputer[TReference, TMetadata]) constraintValuesToConstraints(ctx context.Context, e getProviderFromVisibleConfiguredTargetEnvironment[TReference, TMetadata], fromPackage label.CanonicalPackage, constraintValues []string) ([]*model_analysis_pb.Constraint, error) {
	constraints := make(map[string]string, len(constraintValues))
	missingDependencies := false
	for _, constraintValue := range constraintValues {
		constrainValueInfoProvider, _, err := getProviderFromVisibleConfiguredTarget(
			e,
			fromPackage.String(),
			constraintValue,
			model_core.NewSimpleMessage[model_core.CloneableReference[TMetadata]]((*model_core_pb.DecodableReference)(nil)),
			model_core.CloningObjectManager[TMetadata]{},
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
		listReader := c.valueReaders.List
		for key, value := range model_starlark.AllStructFields(ctx, listReader, constrainValueInfoProvider, &errIter) {
			switch key {
			case "constraint":
				constraintSettingInfoProvider, ok := value.Message.Kind.(*model_starlark_pb.Value_Struct)
				if !ok {
					return nil, fmt.Errorf("field \"constraint\" of ConstraintValueInfo provider of target %#v is not a struct")
				}
				var errIter error
				for key, value := range model_starlark.AllStructFields(
					ctx,
					listReader,
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
							return nil, fmt.Errorf("field \"constraint.default_constraint_value\" of ConstraintValueInfo provider of target %#v is not a Label or None")
						}
					case "label":
						v, ok := value.Message.Kind.(*model_starlark_pb.Value_Label)
						if !ok {
							return nil, fmt.Errorf("field \"constraint.label\" of ConstraintValueInfo provider of target %#v is not a Label")
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
					return nil, fmt.Errorf("field \"label\" of ConstraintValueInfo provider of target %#v is not a Label")
				}
				actualConstraintValue = &v.Label
			}
		}
		if errIter != nil {
			return nil, errIter
		}
		if actualConstraintSetting == nil {
			return nil, fmt.Errorf("ConstraintValueInfo provider of target %#v does not contain field \"constraint.label\"")
		}
		if actualConstraintValue == nil {
			return nil, fmt.Errorf("ConstraintValueInfo provider of target %#v does not contain field \"label\"")
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
	in model_core.Message[*model_starlark_pb.Struct, TReference],
	fieldMapper func(name string, value model_core.Message[*model_starlark_pb.Value, TReference]) (any, error),
) (model_core.PatchedMessage[*model_starlark_pb.Struct, TMetadata], error) {
	fields := map[string]any{}
	var errIter error
	for name, value := range model_starlark.AllStructFields(
		ctx,
		c.valueReaders.List,
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

	encodedFields, _, err := model_starlark.NewStructFromDict[TReference, TMetadata](nil, fields).
		EncodeStructFields(map[starlark.Value]struct{}{}, c.getValueEncodingOptions(e, nil))
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
	emptyDefaultInfo model_core.Message[*model_starlark_pb.Struct, TReference],
	file model_core.Message[*model_starlark_pb.File, TReference],
	identifierGenerator model_starlark.ReferenceEqualIdentifierGenerator,
) (PatchedConfiguredTargetValue[TMetadata], error) {
	newDefaultInfo, err := c.mapStructFields(
		ctx,
		e,
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

func (c *baseComputer[TReference, TMetadata]) configureAttrValueParts(
	ctx context.Context,
	e ConfiguredTargetEnvironment[TReference, TMetadata],
	thread *starlark.Thread,
	namedAttr model_core.Message[*model_starlark_pb.NamedAttr, TReference],
	valueParts model_core.Message[[]*model_starlark_pb.Value, TReference],
	configurationReference model_core.Message[*model_core_pb.DecodableReference, TReference],
	visibilityFromPackage label.CanonicalPackage,
	execGroupPlatformLabels map[string]string,
) (starlark.Value, error) {
	// See if any transitions need to be applied.
	var cfg *model_starlark_pb.Transition
	isScalar := false
	switch attrType := namedAttr.Message.Attr.GetType().(type) {
	case *model_starlark_pb.Attr_Label:
		cfg = attrType.Label.ValueOptions.GetCfg()
		isScalar = true
	case *model_starlark_pb.Attr_LabelKeyedStringDict:
		cfg = attrType.LabelKeyedStringDict.DictKeyOptions.GetCfg()
	case *model_starlark_pb.Attr_LabelList:
		cfg = attrType.LabelList.ListValueOptions.GetCfg()
	}

	var configurationReferences []model_core.Message[*model_core_pb.DecodableReference, TReference]
	mayHaveMultipleConfigurations := false
	if cfg != nil {
		var patchedResult model_core.PatchedMessage[*model_analysis_pb.UserDefinedTransition_Value_Success, TMetadata]
		var err error
		patchedResult, mayHaveMultipleConfigurations, err = c.performTransition(
			ctx,
			e,
			model_core.Nested(namedAttr, cfg),
			configurationReference,
			model_starlark.NewStructFromDict[TReference, TMetadata](nil, map[string]any{
				// TODO!
			}),
			execGroupPlatformLabels,
		)
		if err != nil {
			return nil, err
		}
		result := model_core.Unpatch(e, patchedResult).Decay()
		for _, entry := range result.Message.Entries {
			configurationReferences = append(configurationReferences, model_core.Nested(result, entry.OutputConfigurationReference))
		}
	}

	var attr starlark.Value
	if len(configurationReferences) == 0 {
		for _, valuePart := range valueParts.Message {
			decodedPart, err := model_starlark.DecodeValue[TReference, TMetadata](
				model_core.Nested(valueParts, valuePart),
				/* currentIdentifier = */ nil,
				c.getValueDecodingOptions(ctx, func(originalLabel label.ResolvedLabel) (starlark.Value, error) {
					// We should leave the target
					// unconfigured. Provide a
					// target reference that does
					// not contain any providers.
					return model_starlark.NewTargetReference[TReference, TMetadata](
						originalLabel,
						/* configured = */ nil,
					), nil
				}),
			)
			if err != nil {
				return nil, err
			}
			if err := concatenateAttrValueParts(thread, &attr, decodedPart); err != nil {
				return nil, err
			}
		}
	} else {
		missingDependencies := false
		for _, configurationReference := range configurationReferences {
			valueDecodingOptions := c.getValueDecodingOptions(ctx, func(originalLabel label.ResolvedLabel) (starlark.Value, error) {
				// Resolve the label.
				canonicalOriginalLabel, err := originalLabel.AsCanonical()
				if err != nil {
					return nil, err
				}
				patchedConfigurationReference1 := model_core.Patch(e, configurationReference)
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
					return starlark.None, nil
				}
				if resolvedLabelStr := resolvedLabelValue.Message.Label; resolvedLabelStr != "" {
					resolvedLabel, err := label.NewCanonicalLabel(resolvedLabelStr)
					if err != nil {
						return nil, fmt.Errorf("invalid label %#v: %w", resolvedLabelStr, err)
					}

					// Obtain the providers of the target.
					patchedConfigurationReference2 := model_core.Patch(e, configurationReference)
					configuredTarget := e.GetConfiguredTargetValue(
						model_core.NewPatchedMessage(
							&model_analysis_pb.ConfiguredTarget_Key{
								Label:                  resolvedLabelStr,
								ConfigurationReference: patchedConfigurationReference2.Message,
							},
							patchedConfigurationReference2.Patcher,
						),
					)
					if !configuredTarget.IsSet() {
						missingDependencies = true
						return starlark.None, nil
					}

					return model_starlark.NewTargetReference(
						originalLabel,
						model_starlark.NewConfiguredTargetReference[TReference, TMetadata](
							resolvedLabel,
							model_core.Nested(configuredTarget, configuredTarget.Message.ProviderInstances),
						),
					), nil
				} else {
					return starlark.None, nil
				}
			})
			for _, valuePart := range valueParts.Message {
				decodedPart, err := model_starlark.DecodeValue[TReference, TMetadata](
					model_core.Nested(valueParts, valuePart),
					/* currentIdentifier = */ nil,
					valueDecodingOptions,
				)
				if err != nil {
					return nil, err
				}
				if isScalar && mayHaveMultipleConfigurations {
					decodedPart = starlark.NewList([]starlark.Value{decodedPart})
				}
				if err := concatenateAttrValueParts(thread, &attr, decodedPart); err != nil {
					return nil, err
				}
			}
		}
		if missingDependencies {
			return nil, evaluation.ErrMissingDependency
		}
	}

	if attr == nil {
		return nil, errors.New("attr value does not have any parts")
	}
	attr.Freeze()
	return attr, nil
}

func concatenateAttrValueParts(thread *starlark.Thread, left *starlark.Value, right starlark.Value) error {
	if *left == nil {
		// Initial round.
		*left = right
		return nil
	}

	concatenationOperator := syntax.PLUS
	if _, ok := (*left).(*starlark.Dict); ok {
		concatenationOperator = syntax.PIPE
	}
	v, err := starlark.Binary(thread, concatenationOperator, *left, right)
	if err != nil {
		return err
	}
	*left = v
	return nil
}

func (c *baseComputer[TReference, TMetadata]) ComputeConfiguredTargetValue(ctx context.Context, key model_core.Message[*model_analysis_pb.ConfiguredTarget_Key, TReference], e ConfiguredTargetEnvironment[TReference, TMetadata]) (PatchedConfiguredTargetValue[TMetadata], error) {
	targetLabel, err := label.NewCanonicalLabel(key.Message.Label)
	if err != nil {
		return PatchedConfiguredTargetValue[TMetadata]{}, fmt.Errorf("invalid target label: %w", err)
	}
	emptyDefaultInfoValue := e.GetEmptyDefaultInfoValue(&model_analysis_pb.EmptyDefaultInfo_Key{})
	targetValue := e.GetTargetValue(&model_analysis_pb.Target_Key{
		Label: targetLabel.String(),
	})
	if !emptyDefaultInfoValue.IsSet() || !targetValue.IsSet() {
		return PatchedConfiguredTargetValue[TMetadata]{}, evaluation.ErrMissingDependency
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
		actionEncoder, gotActionEncoder := e.GetActionEncoderObjectValue(&model_analysis_pb.ActionEncoderObject_Key{})
		directoryCreationParameters, gotDirectoryCreationParameters := e.GetDirectoryCreationParametersObjectValue(&model_analysis_pb.DirectoryCreationParametersObject_Key{})
		fileCreationParameters, gotFileCreationParameters := e.GetFileCreationParametersObjectValue(&model_analysis_pb.FileCreationParametersObject_Key{})
		ruleValue := e.GetCompiledBzlFileGlobalValue(&model_analysis_pb.CompiledBzlFileGlobal_Key{
			Identifier: ruleIdentifier.String(),
		})
		ruleImplementationWrappers, gotRuleImplementationWrappers := e.GetRuleImplementationWrappersValue(&model_analysis_pb.RuleImplementationWrappers_Key{})
		if !allBuiltinsModulesNames.IsSet() ||
			!gotActionEncoder ||
			!gotDirectoryCreationParameters ||
			!gotFileCreationParameters ||
			!ruleValue.IsSet() ||
			!gotRuleImplementationWrappers {
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

		thread := c.newStarlarkThread(ctx, e, allBuiltinsModulesNames.Message.BuiltinsModuleNames)

		// Set all common attrs.
		attrValues := make(map[string]any, len(ruleDefinition.Message.Attrs)+2)
		name := starlark.String(targetLabel.GetTargetName().String())
		attrValues["name"] = name

		tags := make([]starlark.Value, 0, len(ruleTarget.Tags))
		for _, tag := range ruleTarget.Tags {
			tags = append(tags, starlark.String(tag))
		}
		tagsList := starlark.NewList(tags)
		attrValues["tags"] = tagsList

		attrValues["testonly"] = starlark.Bool(ruleTarget.InheritableAttrs.GetTestonly())

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

			var attrValue starlark.Value
			for _, valuePart := range valueParts {
				decodedPart, err := model_starlark.DecodeValue[TReference, TMetadata](
					valuePart,
					/* currentIdentifier = */ nil,
					c.getValueDecodingOptions(ctx, func(resolvedLabel label.ResolvedLabel) (starlark.Value, error) {
						return model_starlark.NewLabel[TReference, TMetadata](resolvedLabel), nil
					}),
				)
				if err != nil {
					return PatchedConfiguredTargetValue[TMetadata]{}, err
				}
				if err := concatenateAttrValueParts(thread, &attrValue, decodedPart); err != nil {
					return PatchedConfiguredTargetValue[TMetadata]{}, err
				}
			}
			attrValue.Freeze()

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

			var attrValue starlark.Value
			var attrOutputs []starlark.Value
			for _, valuePart := range valueParts.Message {
				decodedPart, err := model_starlark.DecodeValue[TReference, TMetadata](
					model_core.Nested(valueParts, valuePart),
					/* currentIdentifier = */ nil,
					c.getValueDecodingOptions(ctx, func(resolvedLabel label.ResolvedLabel) (starlark.Value, error) {
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
								return nil, fmt.Errorf("output attr %#v: %w", err)
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
				if err := concatenateAttrValueParts(thread, &attrValue, decodedPart); err != nil {
					return PatchedConfiguredTargetValue[TMetadata]{}, err
				}
			}
			attrValue.Freeze()
			attrValues[namedAttr.Name] = attrValue
			edgeTransitionAttrValues[namedAttr.Name] = attrValue

			switch namedAttr.Attr.GetType().(type) {
			case *model_starlark_pb.Attr_Output:
				if len(attrOutputs) == 0 {
					outputsValues[namedAttr.Name] = starlark.None
				} else if len(attrOutputs) == 1 {
					outputsValues[namedAttr.Name] = attrOutputs[0]
				} else {
					return PatchedConfiguredTargetValue[TMetadata]{}, fmt.Errorf("value of attr %#v contains multiple labels, which is not expected for attrs of type output", namedAttr.Name)
				}
			case *model_starlark_pb.Attr_OutputList:
				outputsValues[namedAttr.Name] = starlark.NewList(attrOutputs)
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
			execCompatibleWith, err := c.constraintValuesToConstraints(
				ctx,
				e,
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
				toolchainInfos:        make([]starlark.Value, len(toolchainIdentifiers)),
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

			var attrValue starlark.Value
			var splitAttrValue *starlark.Dict
			if mayHaveMultipleConfigurations {
				splitAttrValue = starlark.NewDict(len(transition.Message.Entries))
			}

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
					decodedPart, err := model_starlark.DecodeValue[TReference, TMetadata](
						model_core.Nested(valueParts, valuePart),
						/* currentIdentifier = */ nil,
						c.getValueDecodingOptions(ctx, func(originalLabel label.ResolvedLabel) (starlark.Value, error) {
							return model_starlark.NewTargetReference[TReference, TMetadata](
								originalLabel,
								/* configured = */ nil,
							), nil
						}),
					)
					if err != nil {
						return PatchedConfiguredTargetValue[TMetadata]{}, err
					}
					if err := concatenateAttrValueParts(thread, &attrValue, decodedPart); err != nil {
						return PatchedConfiguredTargetValue[TMetadata]{}, err
					}
				}
			} else {
				if executable {
					executableValues[namedAttr.Name] = starlark.None
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

					var splitAttrEntry starlark.Value
					valueDecodingOptions := c.getValueDecodingOptions(ctx, func(originalLabel label.ResolvedLabel) (starlark.Value, error) {
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
							return starlark.None, nil
						}
						if resolvedLabelStr := resolvedLabelValue.Message.Label; resolvedLabelStr != "" {
							canonicalResolvedLabel, err := label.NewCanonicalLabel(resolvedLabelStr)
							if err != nil {
								return nil, fmt.Errorf("invalid label %#v: %w", resolvedLabelStr, err)
							}

							// Obtain the providers of the target.
							patchedConfigurationReference2 := model_core.Patch(e, outputConfigurationReference)
							configuredTarget := e.GetConfiguredTargetValue(
								model_core.NewPatchedMessage(
									&model_analysis_pb.ConfiguredTarget_Key{
										Label:                  resolvedLabelStr,
										ConfigurationReference: patchedConfigurationReference2.Message,
									},
									patchedConfigurationReference2.Patcher,
								),
							)
							if !configuredTarget.IsSet() {
								missingDependencies = true
								return starlark.None, nil
							}
							providerInstances := model_core.Nested(configuredTarget, configuredTarget.Message.ProviderInstances)

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
								c.valueReaders.List,
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
									c.valueReaders.List,
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
									c.valueReaders.List,
									model_core.Nested(filesToRunStruct, filesToRunStruct.Message.Fields),
									"executable",
								)
								if err != nil {
									return nil, fmt.Errorf("failed to obtain field \"files_to_run.executable\" of DefaultInfo provider of target with label %#v: %w", resolvedLabelStr, err)
								}
								decodedExecutable, err := model_starlark.DecodeValue[TReference, TMetadata](
									executableField,
									/* currentIdentifier = */ nil,
									c.getValueDecodingOptions(ctx, func(resolvedLabel label.ResolvedLabel) (starlark.Value, error) {
										return model_starlark.NewLabel[TReference, TMetadata](resolvedLabel), nil
									}),
								)
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
						} else {
							return starlark.None, nil
						}
					})
					for i, valuePart := range valueParts.Message {
						decodedPart, err := model_starlark.DecodeValue[TReference, TMetadata](
							model_core.Nested(valueParts, valuePart),
							/* currentIdentifier = */ nil,
							valueDecodingOptions,
						)
						if err != nil {
							return PatchedConfiguredTargetValue[TMetadata]{}, fmt.Errorf("decoding attr %#v transition %#v value part %d: %w", namedAttr.Name, transitionEntry.Key, i, err)
						}
						if isScalar && mayHaveMultipleConfigurations {
							if decodedPart == starlark.None {
								decodedPart = starlark.NewList(nil)
							} else {
								decodedPart = starlark.NewList([]starlark.Value{decodedPart})
							}
						}
						if err := concatenateAttrValueParts(thread, &attrValue, decodedPart); err != nil {
							return PatchedConfiguredTargetValue[TMetadata]{}, fmt.Errorf("concatenate attr value parts: %w", err)
						}
						if mayHaveMultipleConfigurations {
							if err := concatenateAttrValueParts(thread, &splitAttrEntry, decodedPart); err != nil {
								return PatchedConfiguredTargetValue[TMetadata]{}, fmt.Errorf("concatenate split attr value parts: %w", err)
							}
						}
					}

					if mayHaveMultipleConfigurations {
						if err := splitAttrValue.SetKey(thread, starlark.String(transitionEntry.Key), splitAttrEntry); err != nil {
							return PatchedConfiguredTargetValue[TMetadata]{}, err
						}
					}
				}
			}
			if !missingDependencies {
				attrValue.Freeze()
				attrValues[namedAttr.Name] = attrValue

				if mayHaveMultipleConfigurations {
					splitAttrValue.Freeze()
					splitAttrValues[namedAttr.Name] = splitAttrValue
				}

				filesElements, err := model_starlark.NewDepsetContentsFromList[TReference, TMetadata](
					filesDepsetElements,
					model_starlark_pb.Depset_DEFAULT,
				).ToList(thread)
				if err != nil {
					return PatchedConfiguredTargetValue[TMetadata]{}, fmt.Errorf("converting files depset to list: %w", err)
				}
				files := starlark.NewList(filesElements)
				files.Freeze()
				if allowSingleFile {
					switch l := files.Len(); l {
					case 0:
						fileValues[namedAttr.Name] = starlark.None
					case 1:
						fileValues[namedAttr.Name] = files.Index(0)
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
			actionEncoder:               actionEncoder,
			directoryCreationParameters: directoryCreationParameters,
			fileCreationParameters:      fileCreationParameters,
		}

		thread.SetLocal(model_starlark.SubruleInvokerKey, func(subruleIdentifier label.CanonicalStarlarkIdentifier, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
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
				starlark.Tuple{
					model_starlark.NewNamedFunction(
						model_starlark.NewProtoNamedFunctionDefinition[TReference, TMetadata](
							model_core.Nested(subruleDefinition, subruleDefinition.Message.Implementation),
						),
					),
					&subruleContext[TReference, TMetadata]{ruleContext: rc},
				},
				args...,
			)
			implementationKwargs := append(
				make([]starlark.Tuple, 0, len(kwargs)+len(subruleDefinition.Message.Attrs)),
				kwargs...,
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
					starlark.Tuple{
						starlark.String(namedAttr.Name),
						value,
					},
				)
			}

			if missingDependencies {
				return nil, evaluation.ErrMissingDependency
			}

			return starlark.Call(
				thread,
				ruleImplementationWrappers.Subrule,
				implementationArgs,
				implementationKwargs,
			)
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
		returnValue, err := starlark.Call(
			thread,
			ruleImplementationWrappers.Rule,
			/* args = */ starlark.Tuple{
				starlark.NewBuiltin("current_ctx_capturer", func(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
					// The rule implementation wrapper
					// function may augment ctx. Capture
					// it, so that we can let
					// native.current_ctx() return it.
					var currentCtx starlark.Value
					if err := starlark.UnpackArgs(
						b.Name(), args, kwargs,
						"ctx", &currentCtx,
					); err != nil {
						return nil, err
					}
					thread.SetLocal(model_starlark.CurrentCtxKey, currentCtx)

					return starlark.Call(
						thread,
						model_starlark.NewNamedFunction(
							model_starlark.NewProtoNamedFunctionDefinition[TReference, TMetadata](
								model_core.Nested(ruleDefinition, ruleDefinition.Message.Implementation),
							),
						),
						args,
						kwargs,
					)
				}),
				rc,
			},
			/* kwargs = */ nil,
		)
		if err != nil {
			if !errors.Is(err, evaluation.ErrMissingDependency) {
				var evalErr *starlark.EvalError
				if errors.As(err, &evalErr) {
					return PatchedConfiguredTargetValue[TMetadata]{}, errors.New(evalErr.Backtrace())
				}
			}
			return PatchedConfiguredTargetValue[TMetadata]{}, err
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

		// Convert list of providers to a map where the provider
		// identifier is the key.
		providersSeen := make(map[label.CanonicalStarlarkIdentifier]struct{}, len(providerInstances))
		encodedProviderInstances := make([]*model_starlark_pb.Struct, 0, len(providerInstances)+1)
		patcher := model_core.NewReferenceMessagePatcher[TMetadata]()
		for i, providerInstance := range providerInstances {
			providerIdentifier, err := providerInstance.GetProviderIdentifier()
			if err != nil {
				return PatchedConfiguredTargetValue[TMetadata]{}, fmt.Errorf("struct returned at index %d: %w", i, err)
			}
			if _, ok := providersSeen[providerIdentifier]; ok {
				return PatchedConfiguredTargetValue[TMetadata]{}, fmt.Errorf("implementation function returned multiple structs for provider %#v", providerIdentifier.String())
			}
			providersSeen[providerIdentifier] = struct{}{}

			v, _, err := providerInstance.Encode(map[starlark.Value]struct{}{}, c.getValueEncodingOptions(e, nil))
			if err != nil {
				return PatchedConfiguredTargetValue[TMetadata]{}, err
			}
			encodedProviderInstances = append(encodedProviderInstances, v.Message)
			patcher.Merge(v.Patcher)
		}

		// If the rule did not return an instance of
		// DefaultInfo, inject an empty instance.
		if _, ok := providersSeen[defaultInfoProviderIdentifier]; !ok {
			patchedDefaultInfo := model_core.Patch(e, emptyDefaultInfo)
			encodedProviderInstances = append(encodedProviderInstances, patchedDefaultInfo.Message)
			patcher.Merge(patchedDefaultInfo.Patcher)
		}

		slices.SortFunc(encodedProviderInstances, func(a, b *model_starlark_pb.Struct) int {
			return strings.Compare(
				a.ProviderInstanceProperties.ProviderIdentifier,
				b.ProviderInstanceProperties.ProviderIdentifier,
			)
		})

		// Construct list of outputs of the target.
		outputsTreeBuilder := btree.NewSplitProllyBuilder(
			/* minimumSizeBytes = */ 32*1024,
			/* maximumSizeBytes = */ 128*1024,
			btree.NewObjectCreatingNodeMerger(
				c.getValueObjectEncoder(),
				c.getReferenceFormat(),
				/* parentNodeComputer = */ func(createdObject model_core.Decodable[model_core.CreatedObject[TMetadata]], childNodes []*model_analysis_pb.ConfiguredTarget_Value_Output) model_core.PatchedMessage[*model_analysis_pb.ConfiguredTarget_Value_Output, TMetadata] {
					var firstPackageRelativePath string
					switch firstElement := childNodes[0].Level.(type) {
					case *model_analysis_pb.ConfiguredTarget_Value_Output_Leaf_:
						firstPackageRelativePath = firstElement.Leaf.PackageRelativePath
					case *model_analysis_pb.ConfiguredTarget_Value_Output_Parent_:
						firstPackageRelativePath = firstElement.Parent.FirstPackageRelativePath
					}
					return model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.ConfiguredTarget_Value_Output {
						return &model_analysis_pb.ConfiguredTarget_Value_Output{
							Level: &model_analysis_pb.ConfiguredTarget_Value_Output_Parent_{
								Parent: &model_analysis_pb.ConfiguredTarget_Value_Output_Parent{
									Reference:                patcher.CaptureAndAddDecodableReference(createdObject, e),
									FirstPackageRelativePath: firstPackageRelativePath,
								},
							},
						}
					})
				},
			),
		)
		outputsByPackageRelativePath := outputRegistrar.outputsByPackageRelativePath
		for _, packageRelativePath := range slices.Sorted(maps.Keys(outputsByPackageRelativePath)) {
			output := outputsByPackageRelativePath[packageRelativePath]
			if !output.definition.IsSet() {
				return PatchedConfiguredTargetValue[TMetadata]{}, fmt.Errorf("file %#v is not an output of any action", packageRelativePath)
			}
			if err := outputsTreeBuilder.PushChild(model_core.NewPatchedMessage(
				&model_analysis_pb.ConfiguredTarget_Value_Output{
					Level: &model_analysis_pb.ConfiguredTarget_Value_Output_Leaf_{
						Leaf: &model_analysis_pb.ConfiguredTarget_Value_Output_Leaf{
							PackageRelativePath: packageRelativePath,
							Definition:          output.definition.Message,
						},
					},
				},
				output.definition.Patcher,
			)); err != nil {
				return PatchedConfiguredTargetValue[TMetadata]{}, err
			}
		}
		outputsList, err := outputsTreeBuilder.FinalizeList()
		if err != nil {
			return PatchedConfiguredTargetValue[TMetadata]{}, err
		}
		patcher.Merge(outputsList.Patcher)

		// Construct list of actions of the target.
		actionsTreeBuilder := btree.NewSplitProllyBuilder(
			/* minimumSizeBytes = */ 32*1024,
			/* maximumSizeBytes = */ 128*1024,
			btree.NewObjectCreatingNodeMerger(
				c.getValueObjectEncoder(),
				c.getReferenceFormat(),
				/* parentNodeComputer = */ func(createdObject model_core.Decodable[model_core.CreatedObject[TMetadata]], childNodes []*model_analysis_pb.ConfiguredTarget_Value_Action) model_core.PatchedMessage[*model_analysis_pb.ConfiguredTarget_Value_Action, TMetadata] {
					var firstID []byte
					switch firstElement := childNodes[0].Level.(type) {
					case *model_analysis_pb.ConfiguredTarget_Value_Action_Leaf_:
						firstID = firstElement.Leaf.Id
					case *model_analysis_pb.ConfiguredTarget_Value_Action_Parent_:
						firstID = firstElement.Parent.FirstId
					}
					return model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.ConfiguredTarget_Value_Action {
						return &model_analysis_pb.ConfiguredTarget_Value_Action{
							Level: &model_analysis_pb.ConfiguredTarget_Value_Action_Parent_{
								Parent: &model_analysis_pb.ConfiguredTarget_Value_Action_Parent{
									Reference: patcher.CaptureAndAddDecodableReference(createdObject, e),
									FirstId:   firstID,
								},
							},
						}
					})
				},
			),
		)
		slices.SortFunc(rc.actions, func(a, b model_core.PatchedMessage[*model_analysis_pb.ConfiguredTarget_Value_Action_Leaf, TMetadata]) int {
			return bytes.Compare(a.Message.Id, b.Message.Id)
		})
		for _, action := range rc.actions {
			if err := actionsTreeBuilder.PushChild(model_core.NewPatchedMessage(
				&model_analysis_pb.ConfiguredTarget_Value_Action{
					Level: &model_analysis_pb.ConfiguredTarget_Value_Action_Leaf_{
						Leaf: action.Message,
					},
				},
				action.Patcher,
			)); err != nil {
				return PatchedConfiguredTargetValue[TMetadata]{}, err
			}
		}
		actionsList, err := actionsTreeBuilder.FinalizeList()
		if err != nil {
			return PatchedConfiguredTargetValue[TMetadata]{}, err
		}
		patcher.Merge(actionsList.Patcher)

		return model_core.NewPatchedMessage(
			&model_analysis_pb.ConfiguredTarget_Value{
				ProviderInstances: encodedProviderInstances,
				Outputs:           outputsList.Message,
				Actions:           actionsList.Message,
			},
			patcher,
		), nil
	case *model_starlark_pb.Target_Definition_SourceFileTarget:
		// Handcraft a DefaultInfo provider for this source file.
		identifierGenerator, err := c.getReferenceEqualIdentifierGenerator(model_core.Nested(key, proto.Message(key.Message)))
		if err != nil {
			return PatchedConfiguredTargetValue[TMetadata]{}, err
		}
		return c.getSingleFileConfiguredTargetValue(
			ctx,
			e,
			emptyDefaultInfo,
			model_core.NewSimpleMessage[TReference](
				&model_starlark_pb.File{
					Label: targetLabel.String(),
				},
			),
			identifierGenerator,
		)
	default:
		return PatchedConfiguredTargetValue[TMetadata]{}, errors.New("only source file targets and rule targets can be configured")
	}
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

type targetOutputRegistrar[TReference object.BasicReference, TMetadata model_core.CloneableReferenceMetadata] struct {
	configurationReference model_core.Message[*model_core_pb.DecodableReference, TReference]
	targetLabel            label.CanonicalLabel

	outputsByPackageRelativePath map[string]*targetOutput[TMetadata]
	outputsByFile                map[*model_starlark.File[TReference, TMetadata]]*targetOutput[TMetadata]
}

func (or *targetOutputRegistrar[TReference, TMetadata]) registerOutput(filename label.TargetName, sibling *model_starlark.File[TReference, TMetadata], fileType model_starlark_pb.File_Owner_Type) (starlark.Value, error) {
	// If a sibling is provided, path resolution needs to start in
	// the directory containing containing the sibling.
	if sibling != nil {
		siblingLabelStr := sibling.GetDefinition().Message.Label
		siblingLabel, err := label.NewCanonicalLabel(siblingLabelStr)
		if err != nil {
			return nil, fmt.Errorf("invalid label for sibling %#v: %w", siblingLabelStr)
		}
		if siblingLabel.GetCanonicalPackage() != or.targetLabel.GetCanonicalPackage() {
			return nil, fmt.Errorf("sibling %#v is not declared in the same package", siblingLabel.String())
		}
		filename = siblingLabel.GetTargetName().GetSibling(filename)
	}

	o := &targetOutput[TMetadata]{
		packageRelativePath: filename,
		fileType:            fileType,
	}
	or.outputsByPackageRelativePath[filename.String()] = o
	f := model_starlark.NewFile[TReference, TMetadata](
		model_core.Nested(or.configurationReference, &model_starlark_pb.File{
			Owner: &model_starlark_pb.File_Owner{
				ConfigurationReference: or.configurationReference.Message,
				TargetName:             or.targetLabel.GetTargetName().String(),
				Type:                   fileType,
			},
			Label: or.targetLabel.GetCanonicalPackage().AppendTargetName(filename).String(),
		}),
	)
	or.outputsByFile[f] = o
	return f, nil
}

// targetOutputRegistrar is able to convert model_starlark.File objects
// that were declared as part of the current target to targetOutputs.
// This allows them to be associated to the action that yields them.
var _ unpack.UnpackerInto[*targetOutput[model_core.CloneableReferenceMetadata]] = (*targetOutputRegistrar[object.BasicReference, model_core.CloneableReferenceMetadata])(nil)

func (or *targetOutputRegistrar[TReference, TMetadata]) UnpackInto(thread *starlark.Thread, v starlark.Value, dst **targetOutput[TMetadata]) error {
	var f *model_starlark.File[TReference, TMetadata]
	if err := unpack.Type[*model_starlark.File[TReference, TMetadata]]("File").UnpackInto(thread, v, &f); err != nil {
		return err
	}
	o, ok := or.outputsByFile[f]
	if !ok {
		return fmt.Errorf("%s was not declared as part of this target", f.String())
	}
	*dst = o
	return nil
}

func (or *targetOutputRegistrar[TReference, TMetadata]) Canonicalize(thread *starlark.Thread, v starlark.Value) (starlark.Value, error) {
	var o *targetOutput[TMetadata]
	if err := or.UnpackInto(thread, v, &o); err != nil {
		return nil, err
	}
	return v, nil
}

func (or *targetOutputRegistrar[TReference, TMetadata]) GetConcatenationOperator() syntax.Token {
	return syntax.PLUS
}

type ruleContext[TReference object.BasicReference, TMetadata BaseComputerReferenceMetadata] struct {
	computer                    *baseComputer[TReference, TMetadata]
	context                     context.Context
	environment                 ConfiguredTargetEnvironment[TReference, TMetadata]
	ruleIdentifier              label.CanonicalStarlarkIdentifier
	targetLabel                 label.CanonicalLabel
	configurationReference      model_core.Message[*model_core_pb.DecodableReference, TReference]
	ruleDefinition              model_core.Message[*model_starlark_pb.Rule_Definition, TReference]
	ruleTarget                  model_core.Message[*model_starlark_pb.RuleTarget, TReference]
	attr                        starlark.Value
	splitAttr                   starlark.Value
	buildSettingValue           starlark.Value
	executable                  starlark.Value
	executableFileToFilesToRun  map[*model_starlark.File[TReference, TMetadata]]model_core.Message[*model_starlark_pb.Struct, TReference]
	file                        starlark.Value
	files                       starlark.Value
	outputs                     starlark.Value
	execGroups                  []ruleContextExecGroupState
	tags                        *starlark.List
	outputRegistrar             *targetOutputRegistrar[TReference, TMetadata]
	actionEncoder               model_encoding.BinaryEncoder
	directoryCreationParameters *model_filesystem.DirectoryCreationParameters
	fileCreationParameters      *model_filesystem.FileCreationParameters
	actions                     []model_core.PatchedMessage[*model_analysis_pb.ConfiguredTarget_Value_Action_Leaf, TMetadata]
}

var _ starlark.HasAttrs = (*ruleContext[object.GlobalReference, BaseComputerReferenceMetadata])(nil)

func (rc *ruleContext[TReference, TMetadata]) String() string {
	return fmt.Sprintf("<ctx for %s>", rc.targetLabel.String())
}

func (ruleContext[TReference, TMetadata]) Type() string {
	return "ctx"
}

func (ruleContext[TReference, TMetadata]) Freeze() {
}

func (ruleContext[TReference, TMetadata]) Truth() starlark.Bool {
	return starlark.True
}

func (ruleContext[TReference, TMetadata]) Hash(thread *starlark.Thread) (uint32, error) {
	return 0, errors.New("ctx cannot be hashed")
}

func (rc *ruleContext[TReference, TMetadata]) Attr(thread *starlark.Thread, name string) (starlark.Value, error) {
	switch name {
	case "actions":
		return &ruleContextActions[TReference, TMetadata]{
			ruleContext: rc,
		}, nil
	case "attr":
		return rc.attr, nil
	case "bin_dir":
		configurationComponent, err := model_starlark.ConfigurationReferenceToComponent(rc.configurationReference)
		if err != nil {
			return nil, err
		}
		return model_starlark.NewStructFromDict[TReference, TMetadata](nil, map[string]any{
			"path": starlark.String(model_starlark.ComponentStrBazelOut + "/" + configurationComponent + "/" + model_starlark.ComponentStrBin),
		}), nil
	case "build_setting_value":
		if rc.buildSettingValue == nil {
			buildSettingDefault := rc.ruleTarget.Message.BuildSettingDefault
			if buildSettingDefault == nil {
				return nil, nil
			}

			targetLabelStr := rc.targetLabel.String()
			override, err := btree.Find(
				rc.context,
				rc.computer.buildSettingOverrideReader,
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

			value, err := model_starlark.DecodeValue[TReference, TMetadata](
				encodedValue,
				/* currentIdentifier = */ nil,
				rc.computer.getValueDecodingOptions(rc.context, func(resolvedLabel label.ResolvedLabel) (starlark.Value, error) {
					return nil, errors.New("did not expect label values")
				}),
			)
			if err != nil {
				return nil, err
			}
			rc.buildSettingValue = value
		}
		return rc.buildSettingValue, nil
	case "exec_groups":
		return &ruleContextExecGroups[TReference, TMetadata]{
			ruleContext: rc,
		}, nil
	case "executable":
		return rc.executable, nil
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
		return nil, nil
	}
}

var ruleContextAttrNames = []string{
	"actions",
	"attr",
	"bin_dir",
	"exec_groups",
	"executable",
	"file",
	"files",
	"info_file",
	"label",
	"outputs",
	"split_attr",
	"version_file",
}

func (rc *ruleContext[TReference, TMetadata]) AttrNames() []string {
	attrNames := append([]string(nil), ruleContextAttrNames...)
	if rc.ruleTarget.Message.BuildSettingDefault != nil {
		attrNames = append(attrNames, "build_setting_value")
	}
	return attrNames
}

func toSymlinkEntryDepset[TReference object.BasicReference, TMetadata BaseComputerReferenceMetadata](v any, identifierGenerator model_starlark.ReferenceEqualIdentifierGenerator) *model_starlark.Depset[TReference, TMetadata] {
	var entries []any
	switch typedV := v.(type) {
	case *model_starlark.Depset[TReference, TMetadata]:
		return typedV
	case map[string]string:
		entries = make([]any, 0, len(typedV))
		for _, path := range slices.Sorted(maps.Keys(typedV)) {
			entries = append(entries, model_starlark.NewStructFromDict[TReference, TMetadata](
				nil,
				map[string]any{
					"path":        path,
					"target_file": typedV[path],
				},
			))
		}
	case nil:
		// Return an empty depset.
	default:
		panic("unknown type")
	}
	return model_starlark.NewDepset(
		model_starlark.NewDepsetContentsFromList[TReference, TMetadata](entries, model_starlark_pb.Depset_DEFAULT),
		identifierGenerator,
	)
}

func (rc *ruleContext[TReference, TMetadata]) setOutputToStaticDirectory(output *targetOutput[TMetadata], capturableDirectory model_filesystem.CapturableDirectory[TMetadata, TMetadata]) error {
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
				Source: &model_analysis_pb.TargetOutputDefinition_StaticPackageDirectory{
					StaticPackageDirectory: createdDirectory.Message.Message,
				},
			},
			createdDirectory.Message.Patcher,
		),
	)
}

// getFileOrFilesToRunProvider takes a File or a FilesToRunProvider and
// returns it as a typed value.
func (rc *ruleContext[TReference, TMetadata]) getFileOrFilesToRunProvider(thread *starlark.Thread, value any) (*model_starlark.File[TReference, TMetadata], *model_starlark.Struct[TReference, TMetadata], error) {
	switch typedValue := value.(type) {
	case *model_starlark.File[TReference, TMetadata]:
		if filesToRun, ok := rc.executableFileToFilesToRun[typedValue]; ok {
			// File originated from ctx.executable. return the
			// FilesToRunProvider from which it originated
			// instead. This is done so that if such a File is
			// provided to ctx.actions.run(tools=[...]), its
			// runfiles are added to the input root as well.
			decodedFilesToRun, err := model_starlark.DecodeStruct[TReference, TMetadata](
				filesToRun,
				rc.computer.getValueDecodingOptions(rc.context, func(resolvedLabel label.ResolvedLabel) (starlark.Value, error) {
					return model_starlark.NewLabel[TReference, TMetadata](resolvedLabel), nil
				}),
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

type ruleContextActions[TReference object.BasicReference, TMetadata BaseComputerReferenceMetadata] struct {
	ruleContext *ruleContext[TReference, TMetadata]
}

var _ starlark.HasAttrs = (*ruleContextActions[object.GlobalReference, BaseComputerReferenceMetadata])(nil)

func (ruleContextActions[TReference, TMetadata]) String() string {
	return "<ctx.actions>"
}

func (ruleContextActions[TReference, TMetadata]) Type() string {
	return "ctx.actions"
}

func (ruleContextActions[TReference, TMetadata]) Freeze() {
}

func (ruleContextActions[TReference, TMetadata]) Truth() starlark.Bool {
	return starlark.True
}

func (ruleContextActions[TReference, TMetadata]) Hash(thread *starlark.Thread) (uint32, error) {
	return 0, errors.New("ctx.actions cannot be hashed")
}

func (rca *ruleContextActions[TReference, TMetadata]) Attr(thread *starlark.Thread, name string) (starlark.Value, error) {
	switch name {
	case "args":
		return starlark.NewBuiltin("ctx.actions.args", rca.doArgs), nil
	case "declare_directory":
		return starlark.NewBuiltin("ctx.actions.declare_directory", rca.doDeclareDirectory), nil
	case "declare_file":
		return starlark.NewBuiltin("ctx.actions.declare_file", rca.doDeclareFile), nil
	case "declare_symlink":
		return starlark.NewBuiltin("ctx.actions.declare_symlink", rca.doDeclareSymlink), nil
	case "expand_template":
		return starlark.NewBuiltin("ctx.actions.expand_template", rca.doExpandTemplate), nil
	case "run":
		return starlark.NewBuiltin("ctx.actions.run", rca.doRun), nil
	case "symlink":
		return starlark.NewBuiltin("ctx.actions.symlink", rca.doSymlink), nil
	case "transform_info_file":
		return starlark.NewBuiltin("ctx.actions.transform_info_file", rca.doTransformInfoFile), nil
	case "transform_version_file":
		return starlark.NewBuiltin("ctx.actions.transform_version_file", rca.doTransformVersionFile), nil
	case "write":
		return starlark.NewBuiltin("ctx.actions.write", rca.doWrite), nil
	default:
		return nil, nil
	}
}

var ruleContextActionsAttrNames = []string{
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

func (rca *ruleContextActions[TReference, TMetadata]) AttrNames() []string {
	return ruleContextActionsAttrNames
}

func (rca *ruleContextActions[TReference, TMetadata]) doArgs(thread *starlark.Thread, b *starlark.Builtin, arguments starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	return &args[TReference, TMetadata]{
		paramFileFormat: model_analysis_pb.Args_Leaf_UseParamFile_SHELL,
	}, nil
}

func (rca *ruleContextActions[TReference, TMetadata]) doDeclareDirectory(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) > 1 {
		return nil, fmt.Errorf("%s: got %d positional arguments, want at most 1", b.Name(), len(args))
	}
	var filename label.TargetName
	var sibling *model_starlark.File[TReference, TMetadata]
	rc := rca.ruleContext
	if err := starlark.UnpackArgs(
		b.Name(), args, kwargs,
		"filename", unpack.Bind(thread, &filename, unpack.TargetName),
		"sibling?", unpack.Bind(thread, &sibling, unpack.IfNotNone(unpack.Type[*model_starlark.File[TReference, TMetadata]]("File"))),
	); err != nil {
		return nil, err
	}

	return rc.outputRegistrar.registerOutput(filename, sibling, model_starlark_pb.File_Owner_DIRECTORY)
}

func (rca *ruleContextActions[TReference, TMetadata]) doDeclareFile(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) > 1 {
		return nil, fmt.Errorf("%s: got %d positional arguments, want at most 1", b.Name(), len(args))
	}
	var filename label.TargetName
	var sibling *model_starlark.File[TReference, TMetadata]
	rc := rca.ruleContext
	if err := starlark.UnpackArgs(
		b.Name(), args, kwargs,
		"filename", unpack.Bind(thread, &filename, unpack.TargetName),
		"sibling?", unpack.Bind(thread, &sibling, unpack.IfNotNone(unpack.Type[*model_starlark.File[TReference, TMetadata]]("File"))),
	); err != nil {
		return nil, err
	}

	return rc.outputRegistrar.registerOutput(filename, sibling, model_starlark_pb.File_Owner_FILE)
}

func (rca *ruleContextActions[TReference, TMetadata]) doDeclareSymlink(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) > 1 {
		return nil, fmt.Errorf("%s: got %d positional arguments, want at most 1", b.Name(), len(args))
	}
	var filename label.TargetName
	var sibling *model_starlark.File[TReference, TMetadata]
	rc := rca.ruleContext
	if err := starlark.UnpackArgs(
		b.Name(), args, kwargs,
		"filename", unpack.Bind(thread, &filename, unpack.TargetName),
		"sibling?", unpack.Bind(thread, &sibling, unpack.IfNotNone(unpack.Type[*model_starlark.File[TReference, TMetadata]]("File"))),
	); err != nil {
		return nil, err
	}

	return rc.outputRegistrar.registerOutput(filename, sibling, model_starlark_pb.File_Owner_SYMLINK)
}

func (rca *ruleContextActions[TReference, TMetadata]) doExpandTemplate(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) != 0 {
		return nil, fmt.Errorf("%s: got %d positional arguments, want 0", b.Name(), len(args))
	}
	var output *targetOutput[TMetadata]
	var template *model_starlark.File[TReference, TMetadata]
	isExecutable := false
	var substitutions map[string]string
	rc := rca.ruleContext
	if err := starlark.UnpackArgs(
		b.Name(), args, kwargs,
		// Required arguments.
		"output", unpack.Bind(thread, &output, rc.outputRegistrar),
		"template", unpack.Bind(thread, &template, unpack.Type[*model_starlark.File[TReference, TMetadata]]("File")),
		// Optional arguments.
		// TODO: Add TemplateDict and computed_substitutions.
		"is_executable?", unpack.Bind(thread, &isExecutable, unpack.Bool),
		"substitutions?", unpack.Bind(thread, &substitutions, unpack.Dict(unpack.String, unpack.String)),
	); err != nil {
		return nil, err
	}

	substitutionsList := make([]*model_analysis_pb.TargetOutputDefinition_ExpandTemplate_Substitution, 0, len(substitutions))
	for _, needle := range slices.Sorted(maps.Keys(substitutions)) {
		substitutionsList = append(substitutionsList, &model_analysis_pb.TargetOutputDefinition_ExpandTemplate_Substitution{
			Needle:      []byte(needle),
			Replacement: []byte(substitutions[needle]),
		})
	}

	if output.fileType != model_starlark_pb.File_Owner_FILE {
		return nil, errors.New("output was not declared as a regular file")
	}
	patchedTemplate := model_core.Patch(rc.environment, template.GetDefinition())
	return starlark.None, output.setDefinition(
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
func (rc *ruleContext[TReference, TMetadata]) newFilesToRunProviderFromStruct(thread *starlark.Thread, s *model_starlark.Struct[TReference, TMetadata]) (model_core.PatchedMessage[*model_analysis_pb.FilesToRunProvider_Leaf, TMetadata], *model_starlark.File[TReference, TMetadata], error) {
	// Unpack individual struct fields.
	type filesToRunProviderFields struct {
		executable           *model_starlark.File[TReference, TMetadata]
		runfilesFiles        *model_starlark.Depset[TReference, TMetadata]
		runfilesSymlinks     *model_starlark.Depset[TReference, TMetadata]
		runfilesRootSymlinks *model_starlark.Depset[TReference, TMetadata]
	}
	var fields filesToRunProviderFields
	if err := unpack.Attrs(
		func(thread *starlark.Thread, fields *filesToRunProviderFields) []unpack.AttrUnpacker {
			return []unpack.AttrUnpacker{
				{"executable", unpack.Bind(thread, &fields.executable, unpack.Type[*model_starlark.File[TReference, TMetadata]]("File"))},
				{"_runfiles_files", unpack.Bind(thread, &fields.runfilesFiles, unpack.Type[*model_starlark.Depset[TReference, TMetadata]]("depset"))},
				{"_runfiles_symlinks", unpack.Bind(thread, &fields.runfilesSymlinks, unpack.Type[*model_starlark.Depset[TReference, TMetadata]]("depset"))},
				{"_runfiles_root_symlinks", unpack.Bind(thread, &fields.runfilesRootSymlinks, unpack.Type[*model_starlark.Depset[TReference, TMetadata]]("depset"))},
			}
		},
	).UnpackInto(thread, s, &fields); err != nil {
		return model_core.PatchedMessage[*model_analysis_pb.FilesToRunProvider_Leaf, TMetadata]{}, nil, err
	}

	// Encode individual fields.
	valueEncodingOptions := rc.computer.getValueEncodingOptions(rc.environment, nil)
	executable := model_core.Patch(rc.environment, fields.executable.GetDefinition())
	runfilesFiles, _, err := fields.runfilesFiles.EncodeList(map[starlark.Value]struct{}{}, valueEncodingOptions)
	if err != nil {
		return model_core.PatchedMessage[*model_analysis_pb.FilesToRunProvider_Leaf, TMetadata]{}, nil, fmt.Errorf("runfiles files: %w", err)
	}
	runfilesSymlinks, _, err := fields.runfilesSymlinks.EncodeList(map[starlark.Value]struct{}{}, valueEncodingOptions)
	if err != nil {
		return model_core.PatchedMessage[*model_analysis_pb.FilesToRunProvider_Leaf, TMetadata]{}, nil, fmt.Errorf("runfiles symlinks: %w", err)
	}
	runfilesRootSymlinks, _, err := fields.runfilesRootSymlinks.EncodeList(map[starlark.Value]struct{}{}, valueEncodingOptions)
	if err != nil {
		return model_core.PatchedMessage[*model_analysis_pb.FilesToRunProvider_Leaf, TMetadata]{}, nil, fmt.Errorf("runfiles root symlinks: %w", err)
	}

	patchedFilesToRunProvider, err := inlinedtree.Build(
		inlinedtree.CandidateList[*model_analysis_pb.FilesToRunProvider_Leaf, TMetadata]{
			// Fields that should always be inlined into the
			// FilesToRunProvider.
			{
				ExternalMessage: model_core.NewPatchedMessage((model_core.Marshalable)(nil), executable.Patcher),
				ParentAppender: func(
					filesToRunProvider model_core.PatchedMessage[*model_analysis_pb.FilesToRunProvider_Leaf, TMetadata],
					externalObject *model_core.Decodable[model_core.CreatedObject[TMetadata]],
				) {
					filesToRunProvider.Message.Executable = executable.Message
				},
			},
			// Fields that can be stored externally if needed.
			{
				ExternalMessage: model_core.ProtoListToMarshalable(runfilesFiles),
				Encoder:         valueEncodingOptions.ObjectEncoder,
				ParentAppender: func(
					filesToRunProvider model_core.PatchedMessage[*model_analysis_pb.FilesToRunProvider_Leaf, TMetadata],
					externalObject *model_core.Decodable[model_core.CreatedObject[TMetadata]],
				) {
					filesToRunProvider.Message.RunfilesFiles = btree.MaybeMergeNodes(
						runfilesFiles.Message,
						externalObject,
						filesToRunProvider.Patcher,
						valueEncodingOptions.ComputeListParentNode,
					)
				},
			},
			{
				ExternalMessage: model_core.ProtoListToMarshalable(runfilesSymlinks),
				Encoder:         valueEncodingOptions.ObjectEncoder,
				ParentAppender: func(
					filesToRunProvider model_core.PatchedMessage[*model_analysis_pb.FilesToRunProvider_Leaf, TMetadata],
					externalObject *model_core.Decodable[model_core.CreatedObject[TMetadata]],
				) {
					filesToRunProvider.Message.RunfilesSymlinks = btree.MaybeMergeNodes(
						runfilesSymlinks.Message,
						externalObject,
						filesToRunProvider.Patcher,
						valueEncodingOptions.ComputeListParentNode,
					)
				},
			},
			{
				ExternalMessage: model_core.ProtoListToMarshalable(runfilesRootSymlinks),
				Encoder:         valueEncodingOptions.ObjectEncoder,
				ParentAppender: func(
					filesToRunProvider model_core.PatchedMessage[*model_analysis_pb.FilesToRunProvider_Leaf, TMetadata],
					externalObject *model_core.Decodable[model_core.CreatedObject[TMetadata]],
				) {
					filesToRunProvider.Message.RunfilesRootSymlinks = btree.MaybeMergeNodes(
						runfilesRootSymlinks.Message,
						externalObject,
						filesToRunProvider.Patcher,
						valueEncodingOptions.ComputeListParentNode,
					)
				},
			},
		},
		rc.computer.getInlinedTreeOptions(),
	)
	return patchedFilesToRunProvider, fields.executable, err
}

func (rca *ruleContextActions[TReference, TMetadata]) doRun(thread *starlark.Thread, b *starlark.Builtin, fnArgs starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(fnArgs) != 0 {
		return nil, fmt.Errorf("%s: got %d positional arguments, want 0", b.Name(), len(fnArgs))
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
	var resourceSet *model_starlark.NamedFunction[TReference, TMetadata]
	var toolchain *label.ResolvedLabel
	var tools []any
	useDefaultShellEnv := false
	rc := rca.ruleContext
	if err := starlark.UnpackArgs(
		b.Name(), fnArgs, kwargs,
		// Required arguments.
		"executable", unpack.Bind(thread, &executable, unpack.Or([]unpack.UnpackerInto[any]{
			unpack.Decay(unpack.String),
			unpack.Decay(unpack.Type[*model_starlark.File[TReference, TMetadata]]("File")),
			unpack.Decay(unpack.Type[*model_starlark.Struct[TReference, TMetadata]]("struct")),
		})),
		"outputs", unpack.Bind(thread, &outputs, unpack.List(rc.outputRegistrar)),
		// Optional arguments.
		"arguments?", unpack.Bind(thread, &arguments, unpack.List(unpack.Or([]unpack.UnpackerInto[any]{
			unpack.Decay(unpack.Type[*args[TReference, TMetadata]]("Args")),
			unpack.Decay(unpack.String),
		}))),
		"env?", unpack.Bind(thread, &env, unpack.Dict(unpack.String, unpack.String)),
		"exec_group?", unpack.Bind(thread, &execGroup, unpack.IfNotNone(unpack.String)),
		"execution_requirements?", unpack.Bind(thread, &executionRequirements, unpack.IfNotNone(unpack.Dict(unpack.String, unpack.String))),
		"inputs?", unpack.Bind(thread, &inputs, unpack.Or([]unpack.UnpackerInto[*model_starlark.Depset[TReference, TMetadata]]{
			unpack.Type[*model_starlark.Depset[TReference, TMetadata]]("depset"),
			model_starlark.NewListToDepsetUnpackerInto[TReference, TMetadata](
				unpack.Canonicalize(unpack.Type[*model_starlark.File[TReference, TMetadata]]("File")),
			),
		})),
		"mnemonic?", unpack.Bind(thread, &mnemonic, unpack.IfNotNone(unpack.String)),
		"progress_message?", unpack.Bind(thread, &progressMessage, unpack.IfNotNone(unpack.String)),
		"resource_set?", unpack.Bind(thread, &resourceSet, unpack.IfNotNone(unpack.Pointer(model_starlark.NewNamedFunctionUnpackerInto[TReference, TMetadata]()))),
		"toolchain?", unpack.Bind(thread, &toolchain, unpack.IfNotNone(unpack.Pointer(model_starlark.NewLabelOrStringUnpackerInto[TReference, TMetadata](model_starlark.CurrentFilePackage(thread, 1))))),
		"tools?", unpack.Bind(thread, &tools, unpack.Or([]unpack.UnpackerInto[[]any]{
			unpack.Singleton(unpack.Decay(unpack.Type[*model_starlark.Depset[TReference, TMetadata]]("depset"))),
			unpack.List(unpack.Or([]unpack.UnpackerInto[any]{
				unpack.Decay(unpack.Type[*model_starlark.Depset[TReference, TMetadata]]("depset")),
				unpack.Decay(unpack.Type[*model_starlark.File[TReference, TMetadata]]("File")),
				unpack.Decay(unpack.Type[*model_starlark.Struct[TReference, TMetadata]]("struct")),
			})),
		})),
		"use_default_shell_env?", unpack.Bind(thread, &useDefaultShellEnv, unpack.Bool),
	); err != nil {
		return nil, err
	}

	valueEncodingOptions := rc.computer.getValueEncodingOptions(rc.environment, nil)
	var inputsDirect []starlark.Value
	toolsParentNodeComputer := func(createdObject model_core.Decodable[model_core.CreatedObject[TMetadata]], childNodes []*model_analysis_pb.FilesToRunProvider) model_core.PatchedMessage[*model_analysis_pb.FilesToRunProvider, TMetadata] {
		return model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.FilesToRunProvider {
			return &model_analysis_pb.FilesToRunProvider{
				Level: &model_analysis_pb.FilesToRunProvider_Parent_{
					Parent: &model_analysis_pb.FilesToRunProvider_Parent{
						Reference: patcher.CaptureAndAddDecodableReference(
							createdObject,
							valueEncodingOptions.ObjectCapturer,
						),
					},
				},
			}
		})
	}
	toolsBuilder := btree.NewSplitProllyBuilder(
		valueEncodingOptions.ObjectMinimumSizeBytes,
		valueEncodingOptions.ObjectMaximumSizeBytes,
		btree.NewObjectCreatingNodeMerger(
			valueEncodingOptions.ObjectEncoder,
			valueEncodingOptions.ObjectReferenceFormat,
			toolsParentNodeComputer,
		),
	)

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
	if err := stringArgumentsListBuilder.PushChild(
		model_core.NewSimplePatchedMessage[TMetadata](
			&model_starlark_pb.List_Element{
				Level: &model_starlark_pb.List_Element_Leaf{
					Leaf: &model_starlark_pb.Value{
						Kind: &model_starlark_pb.Value_Str{
							Str: argv0,
						},
					},
				},
			},
		),
	); err != nil {
		return nil, err
	}

	argsParentNodeComputer := func(createdObject model_core.Decodable[model_core.CreatedObject[TMetadata]], childNodes []*model_analysis_pb.Args) model_core.PatchedMessage[*model_analysis_pb.Args, TMetadata] {
		return model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.Args {
			return &model_analysis_pb.Args{
				Level: &model_analysis_pb.Args_Parent_{
					Parent: &model_analysis_pb.Args_Parent{
						Reference: patcher.CaptureAndAddDecodableReference(
							createdObject,
							valueEncodingOptions.ObjectCapturer,
						),
					},
				},
			}
		})
	}
	argsListBuilder := btree.NewSplitProllyBuilder(
		valueEncodingOptions.ObjectMinimumSizeBytes,
		valueEncodingOptions.ObjectMaximumSizeBytes,
		btree.NewObjectCreatingNodeMerger(
			valueEncodingOptions.ObjectEncoder,
			valueEncodingOptions.ObjectReferenceFormat,
			argsParentNodeComputer,
		),
	)
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
			encodedArgs, err := typedArgument.Encode(map[starlark.Value]struct{}{}, valueEncodingOptions)
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
			if err := stringArgumentsListBuilder.PushChild(
				model_core.NewSimplePatchedMessage[TMetadata](
					&model_starlark_pb.List_Element{
						Level: &model_starlark_pb.List_Element_Leaf{
							Leaf: &model_starlark_pb.Value{
								Kind: &model_starlark_pb.Value_Str{
									Str: typedArgument,
								},
							},
						},
					},
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
		env,
		rc.actionEncoder,
		valueEncodingOptions.ObjectReferenceFormat,
		rc.environment,
	)
	if err != nil {
		return nil, err
	}

	execGroups := rc.ruleDefinition.Message.ExecGroups
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
		rc.actionEncoder,
		rc.computer.getInlinedTreeOptions(),
		rc.environment,
	)
	if err != nil {
		return nil, err
	}

	// Tools such as compilers tend to expect that parent
	// directories of output files already exist. Create a directory
	// hierachy containing the parent directories of all output
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
	encodedInputs, _, err := mergedInputs.EncodeList(map[starlark.Value]struct{}{}, valueEncodingOptions)
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
			{
				ExternalMessage: model_core.NewSimplePatchedMessage[TMetadata]((model_core.Marshalable)(nil)),
				ParentAppender: func(
					actionDefinition model_core.PatchedMessage[*model_analysis_pb.TargetActionDefinition, TMetadata],
					externalObject *model_core.Decodable[model_core.CreatedObject[TMetadata]],
				) {
					actionDefinition.Message.PlatformPkixPublicKey = rc.execGroups[execGroupIndex].platformPkixPublicKey
					actionDefinition.Message.UseDefaultShellEnv = useDefaultShellEnv
				},
			},
			// Fields that can be stored externally if needed.
			{
				ExternalMessage: model_core.ProtoListToMarshalable(argsList),
				Encoder:         valueEncodingOptions.ObjectEncoder,
				ParentAppender: func(
					actionDefinition model_core.PatchedMessage[*model_analysis_pb.TargetActionDefinition, TMetadata],
					externalObject *model_core.Decodable[model_core.CreatedObject[TMetadata]],
				) {
					actionDefinition.Message.Arguments = btree.MaybeMergeNodes(
						argsList.Message,
						externalObject,
						actionDefinition.Patcher,
						argsParentNodeComputer,
					)
				},
			},
			{
				ExternalMessage: model_core.ProtoListToMarshalable(envList),
				Encoder:         rc.actionEncoder,
				ParentAppender: func(
					actionDefinition model_core.PatchedMessage[*model_analysis_pb.TargetActionDefinition, TMetadata],
					externalObject *model_core.Decodable[model_core.CreatedObject[TMetadata]],
				) {
					actionDefinition.Message.Env = btree.MaybeMergeNodes(
						envList.Message,
						externalObject,
						actionDefinition.Patcher,
						envParentNodeComputer,
					)
				},
			},
			{
				ExternalMessage: model_core.ProtoListToMarshalable(encodedInputs),
				Encoder:         valueEncodingOptions.ObjectEncoder,
				ParentAppender: func(
					actionDefinition model_core.PatchedMessage[*model_analysis_pb.TargetActionDefinition, TMetadata],
					externalObject *model_core.Decodable[model_core.CreatedObject[TMetadata]],
				) {
					actionDefinition.Message.Inputs = btree.MaybeMergeNodes(
						encodedInputs.Message,
						externalObject,
						actionDefinition.Patcher,
						valueEncodingOptions.ComputeListParentNode,
					)
				},
			},
			{
				ExternalMessage: model_core.ProtoListToMarshalable(toolsList),
				Encoder:         valueEncodingOptions.ObjectEncoder,
				ParentAppender: func(
					actionDefinition model_core.PatchedMessage[*model_analysis_pb.TargetActionDefinition, TMetadata],
					externalObject *model_core.Decodable[model_core.CreatedObject[TMetadata]],
				) {
					actionDefinition.Message.Tools = btree.MaybeMergeNodes(
						toolsList.Message,
						externalObject,
						actionDefinition.Patcher,
						toolsParentNodeComputer,
					)
				},
			},
			{
				ExternalMessage: model_core.ProtoToMarshalable(outputPathPatternChildren),
				Encoder:         rc.actionEncoder,
				ParentAppender: func(
					actionDefinition model_core.PatchedMessage[*model_analysis_pb.TargetActionDefinition, TMetadata],
					externalObject *model_core.Decodable[model_core.CreatedObject[TMetadata]],
				) {
					actionDefinition.Message.OutputPathPattern = model_command.GetPathPatternWithChildren(
						outputPathPatternChildren,
						externalObject,
						actionDefinition.Patcher,
						rc.environment,
					)
				},
			},
			{
				ExternalMessage: model_core.ProtoToMarshalable(createdInitialOutputDirectory.Message),
				Encoder:         rc.actionEncoder,
				ParentAppender: func(
					actionDefinition model_core.PatchedMessage[*model_analysis_pb.TargetActionDefinition, TMetadata],
					externalObject *model_core.Decodable[model_core.CreatedObject[TMetadata]],
				) {
					actionDefinition.Message.InitialOutputDirectory = model_filesystem.GetDirectoryWithContents(
						&createdInitialOutputDirectory,
						externalObject,
						actionDefinition.Patcher,
						rc.environment,
					)
				},
			},
		},
		rc.computer.getInlinedTreeOptions(),
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
	return starlark.None, nil
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

func (rca *ruleContextActions[TReference, TMetadata]) doSymlink(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var output *targetOutput[TMetadata]
	var targetFile *model_starlark.File[TReference, TMetadata]
	var targetPath path.Parser
	isExecutable := false
	progressMessage := ""
	useExecRootForSource := false
	rc := rca.ruleContext
	if err := starlark.UnpackArgs(
		b.Name(), args, kwargs,
		"output", unpack.Bind(thread, &output, rc.outputRegistrar),
		"target_file?", unpack.Bind(thread, &targetFile, unpack.IfNotNone(unpack.Type[*model_starlark.File[TReference, TMetadata]]("File"))),
		"target_path?", unpack.Bind(thread, &targetPath, unpack.PathParser(path.UNIXFormat)),
		"is_executable?", unpack.Bind(thread, &isExecutable, unpack.Bool),
		"progress_message?", unpack.Bind(thread, &progressMessage, unpack.IfNotNone(unpack.String)),
		"use_exec_root_for_source?", unpack.Bind(thread, &useExecRootForSource, unpack.Bool),
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
		return starlark.None, output.setDefinition(
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

	return starlark.None, rc.setOutputToStaticDirectory(
		output,
		&singleSymlinkDirectory[TMetadata, TMetadata]{
			components: output.packageRelativePath.ToComponents(),
			target:     targetPath,
		},
	)
}

func (rca *ruleContextActions[TReference, TMetadata]) doTransformInfoFile(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	return starlark.None, nil
}

func (rca *ruleContextActions[TReference, TMetadata]) doTransformVersionFile(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	return starlark.None, nil
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

func (d *singleFileDirectory[TFile, TDirectory]) Readlink(name path.Component) (path.Parser, error) {
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

func (rca *ruleContextActions[TReference, TMetadata]) doWrite(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var output *targetOutput[TMetadata]
	var content string
	isExecutable := false
	rc := rca.ruleContext
	if err := starlark.UnpackArgs(
		b.Name(), args, kwargs,
		"output", unpack.Bind(thread, &output, rc.outputRegistrar),
		// TODO: Accept Args.
		"content", unpack.Bind(thread, &content, unpack.String),
		"is_executable?", unpack.Bind(thread, &isExecutable, unpack.Bool),
	); err != nil {
		return nil, err
	}

	if output.fileType != model_starlark_pb.File_Owner_FILE {
		return nil, errors.New("output was not declared as a regular file")
	}

	fileContents, err := model_filesystem.CreateFileMerkleTree(
		rc.context,
		rc.fileCreationParameters,
		strings.NewReader(content),
		model_filesystem.NewSimpleFileMerkleTreeCapturer(rc.environment),
	)
	if err != nil {
		return nil, err
	}

	return starlark.None, rc.setOutputToStaticDirectory(
		output,
		&singleFileDirectory[TMetadata, TMetadata]{
			components:   output.packageRelativePath.ToComponents(),
			isExecutable: isExecutable,
			file:         model_filesystem.NewSimpleCapturableFile(fileContents),
		},
	)
}

type ruleContextExecGroups[TReference object.BasicReference, TMetadata BaseComputerReferenceMetadata] struct {
	ruleContext *ruleContext[TReference, TMetadata]
}

var _ starlark.Mapping = (*ruleContextExecGroups[object.GlobalReference, BaseComputerReferenceMetadata])(nil)

func (ruleContextExecGroups[TReference, TMetadata]) String() string {
	return "<ctx.exec_groups>"
}

func (ruleContextExecGroups[TReference, TMetadata]) Type() string {
	return "ctx.exec_groups"
}

func (ruleContextExecGroups[TReference, TMetadata]) Freeze() {
}

func (ruleContextExecGroups[TReference, TMetadata]) Truth() starlark.Bool {
	return starlark.True
}

func (ruleContextExecGroups[TReference, TMetadata]) Hash(thread *starlark.Thread) (uint32, error) {
	return 0, errors.New("ctx.exec_groups cannot be hashed")
}

func (rca *ruleContextExecGroups[TReference, TMetadata]) Get(thread *starlark.Thread, key starlark.Value) (starlark.Value, bool, error) {
	var execGroupName string
	if err := unpack.String.UnpackInto(thread, key, &execGroupName); err != nil {
		return nil, false, err
	}

	rc := rca.ruleContext
	execGroups := rc.ruleDefinition.Message.ExecGroups
	execGroupIndex, ok := sort.Find(
		len(execGroups),
		func(i int) int { return strings.Compare(execGroupName, execGroups[i].Name) },
	)
	if !ok {
		return nil, false, fmt.Errorf("rule does not have an exec group with name %#v", execGroupName)
	}
	return model_starlark.NewStructFromDict[TReference, TMetadata](nil, map[string]any{
		"toolchains": &toolchainContext[TReference, TMetadata]{
			ruleContext:    rc,
			execGroupIndex: execGroupIndex,
		},
	}), true, nil
}

type toolchainContext[TReference object.BasicReference, TMetadata BaseComputerReferenceMetadata] struct {
	ruleContext    *ruleContext[TReference, TMetadata]
	execGroupIndex int
}

var _ starlark.Mapping = (*toolchainContext[object.GlobalReference, BaseComputerReferenceMetadata])(nil)

func (toolchainContext[TReference, TMetadata]) String() string {
	return "<toolchain context>"
}

func (toolchainContext[TReference, TMetadata]) Type() string {
	return "ToolchainContext"
}

func (toolchainContext[TReference, TMetadata]) Freeze() {
}

func (toolchainContext[TReference, TMetadata]) Truth() starlark.Bool {
	return starlark.True
}

func (toolchainContext[TReference, TMetadata]) Hash(thread *starlark.Thread) (uint32, error) {
	return 0, errors.New("ToolchainContext cannot be hashed")
}

func (tc *toolchainContext[TReference, TMetadata]) Get(thread *starlark.Thread, v starlark.Value) (starlark.Value, bool, error) {
	rc := tc.ruleContext
	labelUnpackerInto := unpack.Stringer(model_starlark.NewLabelOrStringUnpackerInto[TReference, TMetadata](model_starlark.CurrentFilePackage(thread, 0)))
	var toolchainType string
	if err := labelUnpackerInto.UnpackInto(thread, v, &toolchainType); err != nil {
		return nil, false, err
	}

	namedExecGroup := rc.ruleDefinition.Message.ExecGroups[tc.execGroupIndex]
	execGroupDefinition := namedExecGroup.ExecGroup
	if execGroupDefinition == nil {
		return nil, false, errors.New("rule definition lacks exec group definition")
	}

	toolchains := execGroupDefinition.Toolchains
	toolchainIndex, ok := sort.Find(
		len(toolchains),
		func(i int) int { return strings.Compare(toolchainType, toolchains[i].ToolchainType) },
	)
	if !ok {
		return nil, false, fmt.Errorf("exec group %#v does not depend on toolchain type %#v", namedExecGroup.Name, toolchainType)
	}

	execGroup := &rc.execGroups[tc.execGroupIndex]
	toolchainInfo := execGroup.toolchainInfos[toolchainIndex]
	if toolchainInfo == nil {
		toolchainIdentifier := execGroup.toolchainIdentifiers[toolchainIndex]
		if toolchainIdentifier == "" {
			// Toolchain was optional, and no matching
			// toolchain was found.
			toolchainInfo = starlark.None
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
				model_core.Nested(encodedToolchainInfo, &model_starlark_pb.Struct{
					ProviderInstanceProperties: &model_starlark_pb.Provider_InstanceProperties{
						ProviderIdentifier: toolchainInfoProviderIdentifier.String(),
					},
					Fields: encodedToolchainInfo.Message,
				}),
				rc.computer.getValueDecodingOptions(rc.context, func(resolvedLabel label.ResolvedLabel) (starlark.Value, error) {
					return model_starlark.NewLabel[TReference, TMetadata](resolvedLabel), nil
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

type ruleContextExecGroupState struct {
	platformPkixPublicKey []byte
	toolchainIdentifiers  []string
	toolchainInfos        []starlark.Value
}

type getProviderFromConfiguredTargetEnvironment[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	GetConfiguredTargetValue(key model_core.PatchedMessage[*model_analysis_pb.ConfiguredTarget_Key, TMetadata]) model_core.Message[*model_analysis_pb.ConfiguredTarget_Value, TReference]
}

// getProviderFromConfiguredTarget looks up a single provider that is
// provided by a configured target.
func getProviderFromConfiguredTarget[TReference any, TMetadata model_core.ReferenceMetadata](e getProviderFromConfiguredTargetEnvironment[TReference, TMetadata], targetLabel string, configurationReference model_core.PatchedMessage[*model_core_pb.DecodableReference, TMetadata], providerIdentifier label.CanonicalStarlarkIdentifier) (model_core.Message[*model_starlark_pb.Struct_Fields, TReference], error) {
	configuredTargetValue := e.GetConfiguredTargetValue(
		model_core.NewPatchedMessage(
			&model_analysis_pb.ConfiguredTarget_Key{
				Label:                  targetLabel,
				ConfigurationReference: configurationReference.Message,
			},
			configurationReference.Patcher,
		),
	)
	if !configuredTargetValue.IsSet() {
		return model_core.Message[*model_starlark_pb.Struct_Fields, TReference]{}, evaluation.ErrMissingDependency
	}

	providerIdentifierStr := providerIdentifier.String()
	providerInstances := configuredTargetValue.Message.ProviderInstances
	if providerIndex, ok := sort.Find(
		len(providerInstances),
		func(i int) int {
			return strings.Compare(providerIdentifierStr, providerInstances[i].ProviderInstanceProperties.GetProviderIdentifier())
		},
	); ok {
		return model_core.Nested(configuredTargetValue, providerInstances[providerIndex].Fields), nil
	}
	return model_core.Message[*model_starlark_pb.Struct_Fields, TReference]{}, fmt.Errorf("target did not yield provider %#v", providerIdentifierStr)
}

type getProviderFromVisibleConfiguredTargetEnvironment[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	GetConfiguredTargetValue(model_core.PatchedMessage[*model_analysis_pb.ConfiguredTarget_Key, TMetadata]) model_core.Message[*model_analysis_pb.ConfiguredTarget_Value, TReference]
	GetVisibleTargetValue(model_core.PatchedMessage[*model_analysis_pb.VisibleTarget_Key, TMetadata]) model_core.Message[*model_analysis_pb.VisibleTarget_Value, TReference]
}

func getProviderFromVisibleConfiguredTarget[TReference any, TConfigurationReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	e getProviderFromVisibleConfiguredTargetEnvironment[TReference, TMetadata],
	fromPackage string,
	targetLabel string,
	configurationReference model_core.Message[*model_core_pb.DecodableReference, TConfigurationReference],
	configurationObjectCapturer model_core.ExistingObjectCapturer[TConfigurationReference, TMetadata],
	providerIdentifier label.CanonicalStarlarkIdentifier,
) (model_core.Message[*model_starlark_pb.Struct_Fields, TReference], string, error) {
	patchedConfigurationReference := model_core.Patch(
		configurationObjectCapturer,
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
			configurationObjectCapturer,
			configurationReference,
		),
		providerIdentifier,
	)
	return p, visibleTarget.Message.Label, err
}

// argsAdd records all arguments provided to Args.add(), Args.add_all()
// and Args.add_joined().
type argsAdd[TReference any, TMetadata model_core.CloneableReferenceMetadata] struct {
	startWith         *wrapperspb.StringValue
	values            starlark.Value
	expandDirectories bool
	mapEach           *model_starlark.NamedFunction[TReference, TMetadata]
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
type args[TReference object.BasicReference, TMetadata model_core.CloneableReferenceMetadata] struct {
	adds            []argsAdd[TReference, TMetadata]
	paramFileFormat model_analysis_pb.Args_Leaf_UseParamFile_Format
	useParamFile    *argsUseParamFile
}

var _ starlark.HasAttrs = (*args[object.LocalReference, model_core.CloneableReferenceMetadata])(nil)

func (args[TReference, TMetadata]) String() string {
	return "<Args>"
}

func (args[TReference, TMetadata]) Type() string {
	return "Args"
}

func (args[TReference, TMetadata]) Freeze() {}

func (args[TReference, TMetadata]) Truth() starlark.Bool {
	return starlark.True
}

func (args[TReference, TMetadata]) Hash(thread *starlark.Thread) (uint32, error) {
	return 0, errors.New("Args cannot be hashed")
}

func (a *args[TReference, TMetadata]) Attr(thread *starlark.Thread, name string) (starlark.Value, error) {
	switch name {
	case "add":
		return starlark.NewBuiltin("Args.add", a.doAdd), nil
	case "add_all":
		return starlark.NewBuiltin("Args.add_all", a.doAddAll), nil
	case "add_joined":
		return starlark.NewBuiltin("Args.add_joined", a.doAddJoined), nil
	case "set_param_file_format":
		return starlark.NewBuiltin("Args.set_param_file_format", a.doSetParamFileFormat), nil
	case "use_param_file":
		return starlark.NewBuiltin("Args.use_param_file", a.doUseParamFile), nil
	default:
		return nil, nil
	}
}

var argsAttrNames = []string{
	"add",
	"add_all",
	"add_joined",
	"set_param_file_format",
	"use_param_file",
}

func (args[TReference, TMetadata]) AttrNames() []string {
	return argsAttrNames
}

func (a *args[TReference, TMetadata]) Encode(path map[starlark.Value]struct{}, options *model_starlark.ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_analysis_pb.Args_Leaf, TMetadata], error) {
	var useParamFile *model_analysis_pb.Args_Leaf_UseParamFile
	if u := a.useParamFile; u != nil {
		useParamFile = &model_analysis_pb.Args_Leaf_UseParamFile{
			Format:       a.paramFileFormat,
			ParamFileArg: u.paramFileArg,
			UseAlways:    u.useAlways,
		}
	}

	addsListBuilder := btree.NewSplitProllyBuilder(
		options.ObjectMinimumSizeBytes,
		options.ObjectMaximumSizeBytes,
		btree.NewObjectCreatingNodeMerger(
			options.ObjectEncoder,
			options.ObjectReferenceFormat,
			func(createdObject model_core.Decodable[model_core.CreatedObject[TMetadata]], childNodes []*model_analysis_pb.Args_Leaf_Add) model_core.PatchedMessage[*model_analysis_pb.Args_Leaf_Add, TMetadata] {
				return model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.Args_Leaf_Add {
					return &model_analysis_pb.Args_Leaf_Add{
						Level: &model_analysis_pb.Args_Leaf_Add_Parent_{
							Parent: &model_analysis_pb.Args_Leaf_Add_Parent{
								Reference: patcher.CaptureAndAddDecodableReference(
									createdObject,
									options.ObjectCapturer,
								),
							},
						},
					}
				})
			},
		),
	)
	for _, add := range a.adds {
		leaf := &model_analysis_pb.Args_Leaf_Add_Leaf{
			StartWith:         add.startWith,
			ExpandDirectories: add.expandDirectories,
			FormatEach:        add.formatEach,
			OmitIfEmpty:       add.omitIfEmpty,
			Uniquify:          add.uniquify,
		}

		values, _, err := model_starlark.EncodeValue(add.values, map[starlark.Value]struct{}{}, nil, options)
		if err != nil {
			return model_core.PatchedMessage[*model_analysis_pb.Args_Leaf, TMetadata]{}, err
		}
		leaf.Values = values.Message
		patcher := values.Patcher

		if add.mapEach != nil {
			mapEach, _, err := add.mapEach.Encode(path, options)
			if err != nil {
				return model_core.PatchedMessage[*model_analysis_pb.Args_Leaf, TMetadata]{}, err
			}
			leaf.MapEach = mapEach.Message
			patcher.Merge(mapEach.Patcher)
		}

		add.setStyle(leaf)

		if err := addsListBuilder.PushChild(
			model_core.NewPatchedMessage(
				&model_analysis_pb.Args_Leaf_Add{
					Level: &model_analysis_pb.Args_Leaf_Add_Leaf_{
						Leaf: leaf,
					},
				},
				patcher,
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

func (a *args[TReference, TMetadata]) doAdd(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var startWith *wrapperspb.StringValue
	var value starlark.Value
	valueUnpackerInto := unpack.Or([]unpack.UnpackerInto[starlark.Value]{
		unpack.Canonicalize(unpack.String),
		unpack.Canonicalize(unpack.Type[*model_starlark.File[TReference, TMetadata]]("File")),
		unpack.Canonicalize(unpack.Type[model_starlark.Label[TReference, TMetadata]]("Label")),
	})
	switch len(args) {
	case 1:
		if err := starlark.UnpackArgs(
			b.Name(), args, nil,
			"value", unpack.Bind(thread, &value, valueUnpackerInto),
		); err != nil {
			return nil, err
		}
	case 2:
		var argName string
		if err := starlark.UnpackArgs(
			b.Name(), args, nil,
			"arg_name", unpack.Bind(thread, &argName, unpack.String),
			"value", unpack.Bind(thread, &value, valueUnpackerInto),
		); err != nil {
			return nil, err
		}
		startWith = &wrapperspb.StringValue{
			Value: argName,
		}
	default:
		return nil, fmt.Errorf("%s: got %d positional arguments, want 1 or 2", b.Name(), len(args))
	}

	format := "%s"
	if err := starlark.UnpackArgs(
		b.Name(), nil, kwargs,
		"format?", unpack.Bind(thread, &format, unpack.String),
	); err != nil {
		return nil, err
	}

	a.adds = append(a.adds, argsAdd[TReference, TMetadata]{
		startWith:  startWith,
		values:     starlark.NewList([]starlark.Value{value}),
		formatEach: format,
		setStyle: func(leaf *model_analysis_pb.Args_Leaf_Add_Leaf) {
			leaf.Style = &model_analysis_pb.Args_Leaf_Add_Leaf_Separate_{
				Separate: &model_analysis_pb.Args_Leaf_Add_Leaf_Separate{},
			}
		},
	})
	return a, nil
}

func (a *args[TReference, TMetadata]) doAddAllJoinedParseArgs(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple) (startWith *string, values starlark.Value, err error) {
	valuesUnpackerInto := unpack.Or([]unpack.UnpackerInto[starlark.Value]{
		unpack.Canonicalize(unpack.List(unpack.Any)),
		unpack.Canonicalize(unpack.Type[*model_starlark.Depset[TReference, TMetadata]]("depset")),
	})
	switch len(args) {
	case 1:
		if err := starlark.UnpackArgs(
			b.Name(), args, nil,
			"values", unpack.Bind(thread, &values, valuesUnpackerInto),
		); err != nil {
			return nil, nil, err
		}
	case 2:
		if err := starlark.UnpackArgs(
			b.Name(), args, nil,
			"arg_name", unpack.Bind(thread, &startWith, unpack.Pointer(unpack.String)),
			"values", unpack.Bind(thread, &values, valuesUnpackerInto),
		); err != nil {
			return nil, nil, err
		}
	default:
		return nil, nil, fmt.Errorf("%s: got %d positional arguments, want 1 or 2", b.Name(), len(args))
	}
	return startWith, values, nil
}

func (a *args[TReference, TMetadata]) doAddAll(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	startWith, values, err := a.doAddAllJoinedParseArgs(thread, b, args)
	if err != nil {
		return nil, err
	}

	var mapEach *model_starlark.NamedFunction[TReference, TMetadata]
	formatEach := "%s"
	var beforeEachStr *string
	omitIfEmpty := true
	uniquify := false
	expandDirectories := true
	var terminateWithStr *string
	allowClosure := false
	if err := starlark.UnpackArgs(
		b.Name(), nil, kwargs,
		"map_each?", unpack.Bind(thread, &mapEach, unpack.Pointer(model_starlark.NewNamedFunctionUnpackerInto[TReference, TMetadata]())),
		"format_each?", unpack.Bind(thread, &formatEach, unpack.IfNotNone(unpack.String)),
		"before_each?", unpack.Bind(thread, &beforeEachStr, unpack.IfNotNone(unpack.Pointer(unpack.String))),
		"omit_if_empty?", unpack.Bind(thread, &omitIfEmpty, unpack.Bool),
		"uniquify?", unpack.Bind(thread, &uniquify, unpack.Bool),
		"expand_directories?", unpack.Bind(thread, &expandDirectories, unpack.Bool),
		"terminate_with?", unpack.Bind(thread, &terminateWithStr, unpack.IfNotNone(unpack.Pointer(unpack.String))),
		"allow_closure?", unpack.Bind(thread, &allowClosure, unpack.Bool),
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

func (a *args[TReference, TMetadata]) doAddJoined(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	startWith, values, err := a.doAddAllJoinedParseArgs(thread, b, args)
	if err != nil {
		return nil, err
	}

	var joinWith string
	var mapEach *model_starlark.NamedFunction[TReference, TMetadata]
	formatEach := "%s"
	formatJoined := "%s"
	omitIfEmpty := true
	uniquify := false
	expandDirectories := true
	allowClosure := false
	if err := starlark.UnpackArgs(
		b.Name(), nil, kwargs,
		"join_with?", unpack.Bind(thread, &joinWith, unpack.String),
		"map_each?", unpack.Bind(thread, &mapEach, unpack.Pointer(model_starlark.NewNamedFunctionUnpackerInto[TReference, TMetadata]())),
		"format_each?", unpack.Bind(thread, &formatEach, unpack.IfNotNone(unpack.String)),
		"format_joined?", unpack.Bind(thread, &formatJoined, unpack.IfNotNone(unpack.String)),
		"omit_if_empty?", unpack.Bind(thread, &omitIfEmpty, unpack.Bool),
		"uniquify?", unpack.Bind(thread, &uniquify, unpack.Bool),
		"expand_directories?", unpack.Bind(thread, &expandDirectories, unpack.Bool),
		"allow_closure?", unpack.Bind(thread, &allowClosure, unpack.Bool),
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

func (a *args[TReference, TMetadata]) doSetParamFileFormat(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var format string
	if err := starlark.UnpackArgs(
		b.Name(), args, kwargs,
		"format", unpack.Bind(thread, &format, unpack.String),
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

func (a *args[TReference, TMetadata]) doUseParamFile(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if len(args) > 1 {
		return nil, fmt.Errorf("%s: got %d positional arguments, want at most 1", b.Name(), len(args))
	}
	var paramFileArg string
	useAlways := false
	if err := starlark.UnpackArgs(
		b.Name(), args, kwargs,
		"param_file_arg", unpack.Bind(thread, &paramFileArg, unpack.String),
		"use_always?", unpack.Bind(thread, &useAlways, unpack.Bool),
	); err != nil {
		return nil, err
	}
	a.useParamFile = &argsUseParamFile{
		paramFileArg: paramFileArg,
		useAlways:    useAlways,
	}
	return a, nil
}

type subruleContext[TReference object.BasicReference, TMetadata BaseComputerReferenceMetadata] struct {
	ruleContext *ruleContext[TReference, TMetadata]
}

var _ starlark.HasAttrs = (*subruleContext[object.GlobalReference, BaseComputerReferenceMetadata])(nil)

func (sc *subruleContext[TReference, TMetadata]) String() string {
	rc := sc.ruleContext
	return fmt.Sprintf("<subrule_ctx for %s>", rc.targetLabel.String())
}

func (subruleContext[TReference, TMetadata]) Type() string {
	return "subrule_ctx"
}

func (subruleContext[TReference, TMetadata]) Freeze() {
}

func (subruleContext[TReference, TMetadata]) Truth() starlark.Bool {
	return starlark.True
}

func (subruleContext[TReference, TMetadata]) Hash(thread *starlark.Thread) (uint32, error) {
	return 0, errors.New("subrule_ctx cannot be hashed")
}

func (sc *subruleContext[TReference, TMetadata]) Attr(thread *starlark.Thread, name string) (starlark.Value, error) {
	// TODO: Add subrule_ctx.toolchains.
	rc := sc.ruleContext
	switch name {
	case "actions":
		return &ruleContextActions[TReference, TMetadata]{
			ruleContext: rc,
		}, nil
	case "label":
		return model_starlark.NewLabel[TReference, TMetadata](rc.targetLabel.AsResolved()), nil
	default:
		return nil, nil
	}
}

var subruleContextAttrNames = []string{
	"actions",
	"label",
}

func (subruleContext[TReference, TMetadata]) AttrNames() []string {
	return subruleContextAttrNames
}
