package analysis

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
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
			IsFileTarget: true,
		},
		newDefaultInfo.Patcher,
	), nil
}

// computeConfiguredTargetValueWithAspects applies the last aspect in
// the list of aspects provided in the key to the target.
func (*baseComputer[TReference, TMetadata]) computeConfiguredTargetValueWithAspects(ctx context.Context, key model_core.Message[*model_analysis_pb.ConfiguredTarget_Key, TReference], e ConfiguredTargetEnvironment[TReference, TMetadata]) (PatchedConfiguredTargetValue[TMetadata], error) {
	targetLabel, err := label.NewCanonicalLabel(key.Message.Label)
	if err != nil {
		return PatchedConfiguredTargetValue[TMetadata]{}, fmt.Errorf("invalid target label: %w", err)
	}
	targetValue := e.GetTargetValue(&model_analysis_pb.Target_Key{
		Label: targetLabel.String(),
	})
	if !targetValue.IsSet() {
		return PatchedConfiguredTargetValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	if targetValue.Message.Definition == nil {
		return PatchedConfiguredTargetValue[TMetadata]{}, errors.New("target does not exist")
	}

	aspects := key.Message.Aspects
	switch targetKind := targetValue.Message.Definition.GetKind().(type) {
	case *model_starlark_pb.Target_Definition_PredeclaredOutputFileTarget:
		// Aspects never apply to file targets. However, aspects
		// that set apply_to_generating_rules are applied to the
		// rule target that generates the output file instead,
		// and the providers they yield are attached to the
		// output file. This is done as follows:
		//
		// 1. If the last aspect does not set
		//    apply_to_generating_rules, it cannot yield any
		//    providers for the output file. Return an empty
		//    set of providers.
		//
		// 2. Otherwise, remove all preceding aspects that don't
		//    set apply_to_generating_rules, as those could not
		//    have been applied to the output file either.
		//
		// 3. Apply the remaining aspects to the rule target
		//    that generates the output file, and return the
		//    providers yielded by the last aspect. These are the
		//    same for all output files of the rule target, as
		//    they are computed by a single ConfiguredTarget
		//    whose key does not depend on the output file.
		//
		// Which aspects set apply_to_generating_rules is
		// determined by GeneratingRuleAspects, so that changes to
		// other properties of the aspects don't invalidate this
		// computation.
		//
		// Predeclared output files are configured in the same
		// configuration as the rule target that generates them,
		// meaning the rule target can be configured using the
		// configuration of the output file. The providers of
		// the rule target itself are not attached to the output
		// file, as TargetProviders only merges the providers of
		// the aspects with those of the output file target.
		aspectIdentifiers := make([]string, 0, len(aspects))
		for _, aspect := range aspects {
			aspectIdentifiers = append(aspectIdentifiers, aspect.Identifier)
		}
		generatingRuleAspectsValue := e.GetGeneratingRuleAspectsValue(&model_analysis_pb.GeneratingRuleAspects_Key{
			AspectIdentifiers: aspectIdentifiers,
		})
		if !generatingRuleAspectsValue.IsSet() {
			return PatchedConfiguredTargetValue[TMetadata]{}, evaluation.ErrMissingDependency
		}
		aspectIndices := generatingRuleAspectsValue.Message.AspectIndices
		if len(aspectIndices) == 0 || aspectIndices[len(aspectIndices)-1] != uint32(len(aspects)-1) {
			return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.ConfiguredTarget_Value{}), nil
		}
		generatingRuleAspects := make([]*model_starlark_pb.AppliedAspect, 0, len(aspectIndices))
		for _, aspectIndex := range aspectIndices {
			generatingRuleAspects = append(generatingRuleAspects, aspects[aspectIndex])
		}

		ownerTargetName, err := label.NewTargetName(targetKind.PredeclaredOutputFileTarget.OwnerTargetName)
		if err != nil {
			return PatchedConfiguredTargetValue[TMetadata]{}, fmt.Errorf("invalid owner target name: %w", err)
		}
		ownerConfiguredTarget := e.GetConfiguredTargetValue(
			model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.ConfiguredTarget_Key {
				return &model_analysis_pb.ConfiguredTarget_Key{
					Label:                  targetLabel.GetCanonicalPackage().AppendTargetName(ownerTargetName).String(),
					ConfigurationReference: model_core.Patch(e, model_core.Nested(key, key.Message.ConfigurationReference)).Merge(patcher),
					Aspects:                model_core.PatchList(e, model_core.Nested(key, generatingRuleAspects)).Merge(patcher),
				}
			}),
		)
		if !ownerConfiguredTarget.IsSet() {
			return PatchedConfiguredTargetValue[TMetadata]{}, evaluation.ErrMissingDependency
		}
		return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.ConfiguredTarget_Value {
			return &model_analysis_pb.ConfiguredTarget_Value{
				ProviderInstances: model_core.PatchList(e, model_core.Nested(ownerConfiguredTarget, ownerConfiguredTarget.Message.ProviderInstances)).Merge(patcher),
			}
		}), nil
	case *model_starlark_pb.Target_Definition_RuleTarget, *model_starlark_pb.Target_Definition_TestSuite:
		// TODO: Add support for applying aspects.
		return PatchedConfiguredTargetValue[TMetadata]{}, errors.New("applying aspects to rule targets is not supported yet")
	default:
		// Aspects never apply to package groups and source
		// files.
		return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.ConfiguredTarget_Value{}), nil
	}
}

// ConfiguredTargetEnvironmentForTesting is used to generate mocks for
// testing ComputeConfiguredTargetValue().
type ConfiguredTargetEnvironmentForTesting ConfiguredTargetEnvironment[model_core.CreatedObjectTree, model_core.CreatedObjectTree]

func (c *baseComputer[TReference, TMetadata]) ComputeConfiguredTargetValue(ctx context.Context, key model_core.Message[*model_analysis_pb.ConfiguredTarget_Key, TReference], e ConfiguredTargetEnvironment[TReference, TMetadata]) (PatchedConfiguredTargetValue[TMetadata], error) {
	if len(key.Message.Aspects) > 0 {
		return c.computeConfiguredTargetValueWithAspects(ctx, key, e)
	}

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
	if targetValue.Message.Definition == nil {
		return PatchedConfiguredTargetValue[TMetadata]{}, errors.New("target does not exist")
	}

	emptyDefaultInfo := model_core.Nested(emptyDefaultInfoValue, emptyDefaultInfoValue.Message.DefaultInfo)

	switch targetValue.Message.Definition.GetKind().(type) {
	case *model_starlark_pb.Target_Definition_PackageGroup, *model_starlark_pb.Target_Definition_SourceFileTarget:
		// The incoming edge transition of these targets, as
		// computed by TargetIncomingEdgeTransition, yields the
		// empty configuration, as their properties don't depend
		// on it.
		if key.Message.ConfigurationReference != nil {
			return PatchedConfiguredTargetValue[TMetadata]{}, errors.New("package groups and source file targets cannot be configured with a configuration")
		}
	}

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
		// The incoming edge transition of a predeclared output
		// file is that of the rule target that owns it, so the
		// configuration is also that of the owner.
		ownerTargetNameStr := targetKind.PredeclaredOutputFileTarget.OwnerTargetName
		ownerConfigurationReference := model_core.Nested(key, key.Message.ConfigurationReference)

		// Handcraft a DefaultInfo provider for this output file.
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
				ownerConfigurationReference,
				&model_starlark_pb.File{
					Owner: &model_starlark_pb.File_Owner{
						ConfigurationReference: ownerConfigurationReference.Message,
						TargetName:             ownerTargetNameStr,
						Type:                   model_starlark_pb.File_Owner_FILE,
					},
					Label: targetLabel.String(),
				},
			),
			identifierGenerator,
		)
	case *model_starlark_pb.Target_Definition_RuleTarget:
		return c.computeRuleTargetValue(
			ctx,
			key,
			e,
			targetLabel,
			model_core.Nested(targetValue, targetKind.RuleTarget),
			analysisEncoders,
			starlarkValueEncoders,
			valueEncodingOptionsFactory,
			emptyDefaultInfo,
		)
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
