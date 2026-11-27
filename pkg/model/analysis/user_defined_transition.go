package analysis

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"maps"
	"slices"
	"strconv"
	"strings"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	"bonanza.build/pkg/model/evaluation"
	model_format "bonanza.build/pkg/model/format"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/unpack"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

var commandLineOptionRepoRootPackage = util.Must(label.NewCanonicalPackage("@@bazel_tools+"))

// expectedTransitionOutput contains information about a declared output
// of a transition for which the implementation function of the
// transition is expected to yield a value.
type expectedTransitionOutput[TReference any] struct {
	label         string
	canonicalizer model_starlark.BuildSettingCanonicalizer
	defaultValue  model_core.Message[*model_starlark_pb.Value, TReference]
	isFlag        bool
}

type getExpectedTransitionOutputEnvironment[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	labelResolverEnvironment[TReference]

	CompiledBzlFileGlobalValueGetter[TReference]
	TargetValueGetter[TReference]
	VisibleTargetValueGetter[TReference, TMetadata]
}

// stringToStarlarkLabelOrNone converts a string containing a resolved
// label value to a Protobuf message of a Starlark Label object. If the
// string is empty, it is converted to None.
//
// This function can, for example, be used to convert a label setting's
// build_setting_default to a Starlark value.
func stringToStarlarkLabelOrNone(v string) *model_starlark_pb.Value {
	if v == "" {
		return model_starlark_pb.Value_builder{
			None: &emptypb.Empty{},
		}.Build()
	}
	return model_starlark_pb.Value_builder{
		Label: proto.String(v),
	}.Build()
}

func getExpectedTransitionOutput[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](e getExpectedTransitionOutputEnvironment[TReference, TMetadata], transitionPackage label.CanonicalPackage, apparentBuildSettingLabel label.ApparentLabel) (expectedTransitionOutput[TReference], error) {
	// Resolve the actual build setting target corresponding
	// to the string value provided as part of the
	// transition definition.
	canonicalBuildSettingLabel, err := label.Canonicalize(newLabelResolver(e), transitionPackage.GetCanonicalRepo(), apparentBuildSettingLabel)
	if err != nil {
		return expectedTransitionOutput[TReference]{}, err
	}
	visibleTargetValue := e.GetVisibleTargetValue(
		model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.VisibleTarget_Key{
			FromPackage:        canonicalBuildSettingLabel.GetCanonicalPackage().String(),
			ToLabel:            canonicalBuildSettingLabel.String(),
			StopAtLabelSetting: true,
		}),
	)
	if !visibleTargetValue.IsSet() {
		return expectedTransitionOutput[TReference]{}, evaluation.ErrMissingDependency
	}
	visibleBuildSettingLabel := visibleTargetValue.Message.GetLabel()

	// Determine how values associated with this build
	// setting need to be canonicalized.
	targetValue := e.GetTargetValue(&model_analysis_pb.Target_Key{
		Label: visibleBuildSettingLabel,
	})
	if !targetValue.IsSet() {
		return expectedTransitionOutput[TReference]{}, evaluation.ErrMissingDependency
	}
	definition := targetValue.Message.GetDefinition()
	if definition == nil {
		return expectedTransitionOutput[TReference]{}, fmt.Errorf("build setting %#v does not exist", visibleBuildSettingLabel)
	}
	switch definition.WhichKind() {
	case model_starlark_pb.Target_Definition_LabelSetting_case:
		// Build setting is a label_setting() or label_flag().
		return expectedTransitionOutput[TReference]{
			label: visibleBuildSettingLabel,
			canonicalizer: model_starlark.NewLabelBuildSettingCanonicalizer[TReference, TMetadata](
				transitionPackage,
				definition.GetLabelSetting().GetSingletonList(),
			),
			defaultValue: model_core.NewSimpleMessage[TReference](
				stringToStarlarkLabelOrNone(definition.GetLabelSetting().GetBuildSettingDefault()),
			),
			isFlag: definition.GetLabelSetting().GetFlag(),
		}, nil
	case model_starlark_pb.Target_Definition_RuleTarget_case:
		// Build setting is written in Starlark.
		if definition.GetRuleTarget().GetBuildSetting() == nil {
			return expectedTransitionOutput[TReference]{}, fmt.Errorf("rule %#v used by label setting %#v does not have \"build_setting\" set", definition.GetRuleTarget().GetRuleIdentifier(), visibleBuildSettingLabel)
		}
		ruleValue := e.GetCompiledBzlFileGlobalValue(&model_analysis_pb.CompiledBzlFileGlobal_Key{
			Identifier: definition.GetRuleTarget().GetRuleIdentifier(),
		})
		if !ruleValue.IsSet() {
			return expectedTransitionOutput[TReference]{}, evaluation.ErrMissingDependency
		}
		rule := ruleValue.Message.GetGlobal().GetRule()
		if rule == nil {
			return expectedTransitionOutput[TReference]{}, fmt.Errorf("identifier %#v used by build setting %#v is not a rule", definition.GetRuleTarget().GetRuleIdentifier(), visibleBuildSettingLabel)
		}
		ruleDefinition := rule.GetDefinition()
		if ruleDefinition == nil {
			return expectedTransitionOutput[TReference]{}, fmt.Errorf("rule %#v used by build setting %#v does not have a definition", definition.GetRuleTarget().GetRuleIdentifier(), visibleBuildSettingLabel)
		}
		buildSetting := ruleDefinition.GetBuildSetting()
		if buildSetting == nil {
			return expectedTransitionOutput[TReference]{}, fmt.Errorf("rule %#v used by build setting %#v does not have \"build_setting\" set", definition.GetRuleTarget().GetRuleIdentifier(), visibleBuildSettingLabel)
		}
		buildSettingType, err := model_starlark.DecodeBuildSettingType[TReference, TMetadata](buildSetting)
		if err != nil {
			return expectedTransitionOutput[TReference]{}, fmt.Errorf("failed to decode build setting type for rule %#v used by build setting %#v: %w", definition.GetRuleTarget().GetRuleIdentifier(), visibleBuildSettingLabel, err)
		}
		return expectedTransitionOutput[TReference]{
			label:         visibleBuildSettingLabel,
			canonicalizer: buildSettingType.GetCanonicalizer(transitionPackage),
			defaultValue:  model_core.Nested(targetValue, definition.GetRuleTarget().GetBuildSetting().GetDefault()),
			isFlag:        buildSetting.GetFlag(),
		}, nil
	default:
		return expectedTransitionOutput[TReference]{}, fmt.Errorf("target %#v is not a label setting or rule target", visibleBuildSettingLabel)
	}
}

// getApparentBuildSettingLabel converts a string containing a build
// setting label to an apparent label. Bazel provides a fictive
// //command_line_option package to refer to refer to integrated command
// line options. We map it to a package under @bazel_tools containing
// regular user defined build settings.
func getApparentBuildSettingLabel(transitionPackage label.CanonicalPackage, buildSettingLabel string) (label.ApparentLabel, error) {
	pkg := transitionPackage
	if strings.HasPrefix(buildSettingLabel, "//command_line_option:") {
		pkg = commandLineOptionRepoRootPackage
	}
	return pkg.AppendLabel(buildSettingLabel)
}

func getBuildSettingOverridesFromReference[TReference any](configurationReference model_core.Message[*model_core_pb.DecodableReference, TReference]) model_core.Message[[]*model_analysis_pb.BuildSettingOverride, TReference] {
	if configurationReference.Message == nil {
		return model_core.Nested(configurationReference, ([]*model_analysis_pb.BuildSettingOverride)(nil))
	}
	return model_core.Nested(configurationReference, []*model_analysis_pb.BuildSettingOverride{{
		Level: &model_analysis_pb.BuildSettingOverride_Parent_{
			Parent: &model_analysis_pb.BuildSettingOverride_Parent{
				Reference: configurationReference.Message,
			},
		},
	}})
}

// namedExpectedTransitionOutput contains information about a declared
// output of a transition for which the implementation function of the
// transition is expected to yield a value. It also includes the key at
// which the value is stored in the dictionary returned by the
// implementation function.
type namedExpectedTransitionOutput[TReference any] struct {
	dictKey        string
	expectedOutput expectedTransitionOutput[TReference]
}

func getCanonicalTransitionOutputValuesFromDict[TReference any](thread *eval.Thread, namedExpectedOutputs []namedExpectedTransitionOutput[TReference], outputs map[string]eval.Value) ([]buildSettingValueToApply[TReference], error) {
	if len(outputs) != len(namedExpectedOutputs) {
		return nil, fmt.Errorf("output dictionary contains %d keys, while the transition's definition only has %d outputs", len(outputs), len(namedExpectedOutputs))
	}

	values := make([]buildSettingValueToApply[TReference], 0, len(namedExpectedOutputs))
	for _, namedExpectedOutput := range namedExpectedOutputs {
		label := namedExpectedOutput.expectedOutput.label
		literalValue, ok := outputs[namedExpectedOutput.dictKey]
		if !ok {
			return nil, fmt.Errorf("no value for output %#v has been provided", label)
		}
		canonicalizedValue, err := namedExpectedOutput.expectedOutput.canonicalizer.Canonicalize(thread, literalValue)
		if err != nil {
			return nil, fmt.Errorf("failed to canonicalize output %#v: %w", label, err)
		}
		values = append(values, buildSettingValueToApply[TReference]{
			label:              label,
			canonicalizedValue: canonicalizedValue,
			defaultValue:       namedExpectedOutput.expectedOutput.defaultValue,
		})
	}
	return values, nil
}

// buildSettingValueToApply contains the value of a build setting that
// has to be inserted into the configuration. The default value of the
// build setting is also included, so that the override can be
// suppressed in case the desired value is equal to the default.
type buildSettingValueToApply[TReference any] struct {
	label              string
	canonicalizedValue eval.Value
	defaultValue       model_core.Message[*model_starlark_pb.Value, TReference]
}

func (c *baseComputer[TReference, TMetadata]) applyTransition(
	ctx context.Context,
	thread *eval.Thread,
	e model_core.ObjectCapturer[TReference, TMetadata],
	analysisEncoders *Encoders[TReference],
	configurationReference model_core.Message[*model_core_pb.DecodableReference, TReference],
	buildSettingValuesToApply []buildSettingValueToApply[TReference],
	valueEncodingOptions *model_starlark.ValueEncodingOptions[TReference, TMetadata],
) (model_core.PatchedMessage[*model_core_pb.DecodableReference, TMetadata], error) {
	analysisReaders := &analysisEncoders.Readers
	var errIter error
	existingIter, existingIterStop := iter.Pull(btree.AllLeaves(
		ctx,
		analysisReaders.BuildSettingOverride,
		getBuildSettingOverridesFromReference(configurationReference),
		func(override model_core.Message[*model_analysis_pb.BuildSettingOverride, TReference]) (*model_core_pb.DecodableReference, error) {
			return override.Message.GetParent().GetReference(), nil
		},
		&errIter,
	))
	defer existingIterStop()

	// TODO: Use a proper encoder!
	treeBuilder := btree.NewHeightAwareBuilder(
		btree.NewProllyChunkerFactory[TMetadata](
			c.chunkingParameters,
			/* isParent = */ func(buildSettingOverride *model_analysis_pb.BuildSettingOverride) bool {
				return buildSettingOverride.GetParent() != nil
			},
		),
		btree.NewObjectCreatingNodeMerger(
			analysisEncoders.Encoder,
			c.referenceFormat,
			/* parentNodeComputer = */ btree.Capturing(ctx, e, func(createdObject model_core.Decodable[model_core.MetadataEntry[TMetadata]], childNodes model_core.Message[[]*model_analysis_pb.BuildSettingOverride, object.LocalReference]) model_core.PatchedMessage[*model_analysis_pb.BuildSettingOverride, TMetadata] {
				var firstLabel string
				switch firstEntry := childNodes.Message[0].Level.(type) {
				case *model_analysis_pb.BuildSettingOverride_Leaf_:
					firstLabel = firstEntry.Leaf.Label
				case *model_analysis_pb.BuildSettingOverride_Parent_:
					firstLabel = firstEntry.Parent.FirstLabel
				}
				return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.BuildSettingOverride {
					return &model_analysis_pb.BuildSettingOverride{
						Level: &model_analysis_pb.BuildSettingOverride_Parent_{
							Parent: &model_analysis_pb.BuildSettingOverride_Parent{
								Reference:  patcher.AddDecodableReference(createdObject),
								FirstLabel: firstLabel,
							},
						},
					}
				})
			}),
		),
	)

	existingOverride, existingOverrideOK := existingIter()
	for existingOverrideOK || len(buildSettingValuesToApply) > 0 {
		var cmp int
		if !existingOverrideOK {
			cmp = 1
		} else if len(buildSettingValuesToApply) == 0 {
			cmp = -1
		} else {
			level, ok := existingOverride.Message.Level.(*model_analysis_pb.BuildSettingOverride_Leaf_)
			if !ok {
				return model_core.PatchedMessage[*model_core_pb.DecodableReference, TMetadata]{}, errors.New("build setting override is not a valid leaf")
			}
			cmp = strings.Compare(level.Leaf.Label, buildSettingValuesToApply[0].label)
		}
		if cmp < 0 {
			// Preserve existing build setting.
			treeBuilder.PushChild(model_core.Patch(e, existingOverride))
		} else {
			// Either replace or remove an existing build
			// setting override, or inject a new one.
			buildSettingValueToApply := buildSettingValuesToApply[0]
			buildSettingValuesToApply = buildSettingValuesToApply[1:]
			encodedValue, err := model_starlark.EncodeValue(
				thread,
				buildSettingValueToApply.canonicalizedValue,
				/* path = */ nil,
				/* identifier = */ nil,
				valueEncodingOptions,
			)
			if err != nil {
				return model_core.PatchedMessage[*model_core_pb.DecodableReference, TMetadata]{}, fmt.Errorf("failed to encode \"build_setting_default\": %w", err)
			}

			// Only store the build setting override if its
			// value differs from the default value. This
			// ensures that the configuration remains
			// canonical.
			sortedEncodedValue, _ := encodedValue.SortAndSetReferences()
			sortedDefaultValue, _ := model_core.Patch(
				c.discardingObjectCapturer,
				buildSettingValueToApply.defaultValue,
			).SortAndSetReferences()
			if !model_core.TopLevelMessagesEqual(sortedEncodedValue, sortedDefaultValue) {
				treeBuilder.PushChild(
					model_core.NewPatchedMessage(
						&model_analysis_pb.BuildSettingOverride{
							Level: &model_analysis_pb.BuildSettingOverride_Leaf_{
								Leaf: &model_analysis_pb.BuildSettingOverride_Leaf{
									Label: buildSettingValueToApply.label,
									Value: encodedValue.Message,
								},
							},
						},
						encodedValue.Patcher,
					),
				)
			}
		}
		if cmp <= 0 {
			existingOverride, existingOverrideOK = existingIter()
		}
	}
	if errIter != nil {
		return model_core.PatchedMessage[*model_core_pb.DecodableReference, TMetadata]{}, errIter
	}
	buildSettingOverrides, err := treeBuilder.FinalizeList()
	if err != nil {
		return model_core.PatchedMessage[*model_core_pb.DecodableReference, TMetadata]{}, fmt.Errorf("failed to finalize build setting overrides: %w", err)
	}
	if len(buildSettingOverrides.Message) == 0 {
		return model_core.NewSimplePatchedMessage[TMetadata, *model_core_pb.DecodableReference](nil), nil
	}

	createdConfiguration, err := model_core.MarshalAndEncodeDeterministic(
		buildSettingOverrides.ToMarshaler(model_format.NewProtoList[model_analysis_pb.BuildSettingOverride]()),
		c.referenceFormat,
		analysisEncoders.Encoder,
	)
	if err != nil {
		return model_core.PatchedMessage[*model_core_pb.DecodableReference, TMetadata]{}, fmt.Errorf("failed to marshal configuration: %w", err)
	}
	return model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (*model_core_pb.DecodableReference, error) {
		return patcher.CaptureAndAddDecodableReference(ctx, createdConfiguration, e)
	})
}

type getBuildSettingValueEnvironment[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	model_core.ExistingObjectCapturer[TReference, TMetadata]
	TargetProviderValueGetter[TReference, TMetadata]
	TargetValueGetter[TReference]
	VisibleTargetValueGetter[TReference, TMetadata]
}

var featureFlagInfoProviderIdentifier = util.Must(label.NewCanonicalStarlarkIdentifier("@@builtins_core+//:exports.bzl%FeatureFlagInfo"))

func (*baseComputer[TReference, TMetadata]) getBuildSettingValue(ctx context.Context, e getBuildSettingValueEnvironment[TReference, TMetadata], analysisReaders *Readers[TReference], valueReaders *model_starlark.ValueReaders[TReference], fromPackage label.CanonicalPackage, buildSettingLabel string, configurationReference model_core.Message[*model_core_pb.DecodableReference, TReference]) (model_core.Message[*model_starlark_pb.Value, TReference], error) {
	patchedConfigurationReference := model_core.Patch(e, configurationReference)
	visibleTargetValue := e.GetVisibleTargetValue(
		model_core.NewPatchedMessage(
			&model_analysis_pb.VisibleTarget_Key{
				ConfigurationReference: patchedConfigurationReference.Message,
				FromPackage:            fromPackage.String(),
				ToLabel:                buildSettingLabel,
				StopAtLabelSetting:     true,
			},
			patchedConfigurationReference.Patcher,
		),
	)
	if !visibleTargetValue.IsSet() {
		return model_core.Message[*model_starlark_pb.Value, TReference]{}, evaluation.ErrMissingDependency
	}
	visibleBuildSettingLabel := visibleTargetValue.Message.GetLabel()

	// Determine the current value of the build setting.
	if buildSettingOverride, err := btree.Find(
		ctx,
		analysisReaders.BuildSettingOverride,
		getBuildSettingOverridesFromReference(configurationReference),
		func(entry model_core.Message[*model_analysis_pb.BuildSettingOverride, TReference]) (int, *model_core_pb.DecodableReference) {
			switch entry.Message.GetLevel().(type) {
			case *model_analysis_pb.BuildSettingOverride_Leaf_:
				return strings.Compare(visibleBuildSettingLabel, entry.Message.GetLeaf().GetLabel()), nil
			case *model_analysis_pb.BuildSettingOverride_Parent_:
				return strings.Compare(visibleBuildSettingLabel, entry.Message.GetParent().GetFirstLabel()), entry.Message.GetParent().GetReference()
			default:
				return 0, nil
			}
		},
	); err != nil {
		return model_core.Message[*model_starlark_pb.Value, TReference]{}, err
	} else if buildSettingOverride.IsSet() {
		// Configuration contains an override for the
		// build setting. Use the value contained in the
		// configuration.
		level := buildSettingOverride.Message.GetLeaf()
		if level == nil {
			return model_core.Message[*model_starlark_pb.Value, TReference]{}, fmt.Errorf("build setting override for label setting %#v is not a valid leaf", visibleBuildSettingLabel)
		}
		return model_core.Nested(buildSettingOverride, level.GetValue()), nil
	}

	// No override present. Obtain the default value
	// of the build setting.
	targetValue := e.GetTargetValue(&model_analysis_pb.Target_Key{
		Label: visibleBuildSettingLabel,
	})
	if !targetValue.IsSet() {
		return model_core.Message[*model_starlark_pb.Value, TReference]{}, evaluation.ErrMissingDependency
	}
	targetValueDefinition := targetValue.Message.GetDefinition()
	if targetValueDefinition == nil {
		return model_core.Message[*model_starlark_pb.Value, TReference]{}, fmt.Errorf("build setting %#v does not exist", visibleBuildSettingLabel)
	}
	switch targetValueDefinition.WhichKind() {
	case model_starlark_pb.Target_Definition_LabelSetting_case:
		// Build setting is a label_setting() or
		// label_flag().
		buildSettingDefault := stringToStarlarkLabelOrNone(targetValueDefinition.GetLabelSetting().GetBuildSettingDefault())
		if targetValueDefinition.GetLabelSetting().GetSingletonList() {
			buildSettingDefault = model_starlark_pb.Value_builder{
				List: model_starlark_pb.List_builder{
					Elements: []*model_starlark_pb.List_Element{model_starlark_pb.List_Element_builder{
						Leaf: proto.ValueOrDefault(buildSettingDefault),
					}.Build()},
				}.Build(),
			}.Build()
		}
		return model_core.NewSimpleMessage[TReference](buildSettingDefault), nil
	case model_starlark_pb.Target_Definition_RuleTarget_case:
		if bs := targetValueDefinition.GetRuleTarget().GetBuildSetting(); bs != nil {
			// Build setting that is written in Starlark.
			return model_core.Nested(targetValue, bs.GetDefault()), nil
		}

		// Not a build setting, but the target may provide
		// FeatureFlagInfo if configured.
		featureFlagInfoProviderIdentifierStr := featureFlagInfoProviderIdentifier.String()
		featureFlagProvider := e.GetTargetProviderValue(
			model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.TargetProvider_Key {
				return &model_analysis_pb.TargetProvider_Key{
					Label:              visibleBuildSettingLabel,
					Variant:            newTargetVariant(model_core.Patch(e, configurationReference).Merge(patcher), nil),
					ProviderIdentifier: featureFlagInfoProviderIdentifierStr,
				}
			}),
		)
		if !featureFlagProvider.IsSet() {
			return model_core.Message[*model_starlark_pb.Value, TReference]{}, evaluation.ErrMissingDependency
		}
		featureFlagProviderFields := featureFlagProvider.Message.GetFields()
		if featureFlagProviderFields == nil {
			return model_core.Message[*model_starlark_pb.Value, TReference]{}, fmt.Errorf("rule %#v used by build setting %#v does not have \"build_setting\" set, nor does it yield provider FeatureFlagInfo", targetValueDefinition.GetRuleTarget().GetRuleIdentifier(), visibleBuildSettingLabel)
		}
		featureFlagValue, err := model_starlark.GetStructFieldValue(
			ctx,
			valueReaders.List,
			model_core.Nested(featureFlagProvider, featureFlagProviderFields),
			"value",
		)
		if err != nil {
			return model_core.Message[*model_starlark_pb.Value, TReference]{}, fmt.Errorf("failed to obtain field \"value\" of FeatureFlagInfo provider of target %#v: %w", visibleBuildSettingLabel, err)
		}
		return featureFlagValue, nil
	default:
		return model_core.Message[*model_starlark_pb.Value, TReference]{}, fmt.Errorf("target %#v is not a build setting or rule target", visibleBuildSettingLabel)
	}
}

type performUserDefinedTransitionEnvironment[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] interface {
	model_core.ObjectManager[TReference, TMetadata]
	FileCreationParametersObjectGetter
	getBuildSettingValueEnvironment[TReference, TMetadata]
	getExpectedTransitionOutputEnvironment[TReference, TMetadata]
	loadBzlGlobalsEnvironment[TReference]
}

func (c *baseComputer[TReference, TMetadata]) performUserDefinedTransition(
	ctx context.Context,
	e performUserDefinedTransitionEnvironment[TReference, TMetadata],
	analysisReaders *Readers[TReference],
	valueReaders *model_starlark.ValueReaders[TReference],
	thread *eval.Thread,
	transition model_core.Message[*model_starlark_pb.Transition_UserDefined, TReference],
	configurationReference model_core.Message[*model_core_pb.DecodableReference, TReference],
	attrParameter eval.Value,
) ([]namedExpectedTransitionOutput[TReference], map[string]map[string]eval.Value, error) {
	allBuiltinsModulesNames := e.GetBuiltinsModuleNamesValue(&model_analysis_pb.BuiltinsModuleNames_Key{})
	if !allBuiltinsModulesNames.IsSet() {
		return nil, nil, evaluation.ErrMissingDependency
	}

	var transitionDefinition model_core.Message[*model_starlark_pb.Transition_UserDefined_Definition, TReference]
	switch transition.Message.WhichKind() {
	case model_starlark_pb.Transition_UserDefined_Identifier_case:
		transitionIdentifier, err := label.NewCanonicalStarlarkIdentifier(transition.Message.GetIdentifier())
		if err != nil {
			return nil, nil, fmt.Errorf("invalid transition identifier: %w", err)
		}

		transitionValue := e.GetCompiledBzlFileGlobalValue(&model_analysis_pb.CompiledBzlFileGlobal_Key{
			Identifier: transitionIdentifier.String(),
		})
		if !transitionValue.IsSet() {
			return nil, nil, evaluation.ErrMissingDependency
		}
		tv := transitionValue.Message.GetGlobal().GetTransition()
		if tv == nil {
			return nil, nil, fmt.Errorf("%#v is not a transition", transitionIdentifier.String())
		}
		udt := tv.GetUserDefined()
		if udt == nil {
			return nil, nil, fmt.Errorf("%#v is not a user-defined transition", transitionIdentifier.String())
		}
		udtd := udt.GetDefinition()
		if udtd == nil {
			return nil, nil, fmt.Errorf("%#v is not a user-defined transition definition", transitionIdentifier.String())
		}
		transitionDefinition = model_core.Nested(transitionValue, udtd)
	case model_starlark_pb.Transition_UserDefined_Definition_case:
		transitionDefinition = model_core.Nested(transition, transition.Message.GetDefinition())
	default:
		return nil, nil, errors.New("user-defined transition has an unknown type")
	}

	transitionPackageStr := transitionDefinition.Message.GetCanonicalPackage()
	transitionPackage, err := label.NewCanonicalPackage(transitionPackageStr)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid canonical package %#v: %w", transitionPackageStr, err)
	}
	transitionRepo := transitionPackage.GetCanonicalRepo()

	// Collect inputs to provide to the implementation function.
	missingDependencies := false
	inputs := eval.NewDict()
	for _, input := range transitionDefinition.Message.GetInputs() {
		// Resolve the actual build setting target corresponding
		// to the string value provided as part of the
		// transition definition.
		apparentBuildSettingLabel, err := getApparentBuildSettingLabel(transitionPackage, input)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid build setting label %#v: %w", input, err)
		}
		canonicalBuildSettingLabel, err := label.Canonicalize(newLabelResolver(e), transitionRepo, apparentBuildSettingLabel)
		if err != nil {
			if errors.Is(err, evaluation.ErrMissingDependency) {
				missingDependencies = true
				continue
			}
			return nil, nil, err
		}

		encodedValue, err := c.getBuildSettingValue(
			ctx,
			e,
			analysisReaders,
			valueReaders,
			// TODO: Is this the right package? Shouldn't we
			// use the package in which the transition is
			// declared?
			canonicalBuildSettingLabel.GetCanonicalPackage(),
			canonicalBuildSettingLabel.String(),
			configurationReference,
		)
		if err != nil {
			if errors.Is(err, evaluation.ErrMissingDependency) {
				missingDependencies = true
				continue
			}
			return nil, nil, err
		}

		v, err := model_starlark.DecodeValue[TReference, TMetadata](
			thread, encodedValue,
			/* currentIdentifier = */ nil,
		)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to decode value for input %#v: %w", input, err)
		}
		if err := inputs.SetKey(thread, eval.String(input), v); err != nil {
			return nil, nil, err
		}
	}

	// Preprocess the outputs that we expect to see.
	definedOutputs := transitionDefinition.Message.GetOutputs()
	expectedOutputs := make([]namedExpectedTransitionOutput[TReference], 0, len(definedOutputs))
	expectedOutputLabels := make(map[string]string, len(definedOutputs))
	for _, output := range definedOutputs {
		apparentBuildSettingLabel, err := getApparentBuildSettingLabel(transitionPackage, output)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid build setting label %#v: %w", output, err)
		}
		expectedOutput, err := getExpectedTransitionOutput[TReference, TMetadata](
			e,
			transitionPackage,
			apparentBuildSettingLabel,
		)
		if err != nil {
			if errors.Is(err, evaluation.ErrMissingDependency) {
				missingDependencies = true
				continue
			}
		}
		if existing, ok := expectedOutputLabels[expectedOutput.label]; ok {
			return nil, nil, fmt.Errorf("outputs %#v and %#v both refer to build setting %#v", existing, output, expectedOutput.label)
		}
		expectedOutputLabels[expectedOutput.label] = output
		expectedOutputs = append(expectedOutputs, namedExpectedTransitionOutput[TReference]{
			dictKey:        output,
			expectedOutput: expectedOutput,
		})
	}
	slices.SortFunc(expectedOutputs, func(a, b namedExpectedTransitionOutput[TReference]) int {
		return strings.Compare(a.expectedOutput.label, b.expectedOutput.label)
	})

	if missingDependencies {
		return nil, nil, evaluation.ErrMissingDependency
	}

	// Invoke transition implementation function.
	outputs, err := eval.Call(thread, model_starlark.DecodeFunction[TReference, TMetadata](model_core.Nested(transitionDefinition, transitionDefinition.Message.GetImplementation())), eval.Args{Positional: /* args = */ []eval.Value{
		inputs,
		attrParameter,
	}, Named:/* kwargs = */ nil})
	if err != nil {
		if errors.Is(err, errTransitionDependsOnAttrs) {
			return nil, nil, err
		}
		return nil, nil, formatStarlarkError(err)
	}

	// Process return value of transition implementation function.
	var outputsDict map[string]map[string]eval.Value
	switch typedOutputs := outputs.(type) {
	case eval.List, eval.Tuple:
		// 1:2+ transition in the form of a list.
		var outputsList []map[string]eval.Value
		if err := unpack.List(unpack.Dict(unpack.String, unpack.Any)).UnpackInto(thread, typedOutputs, &outputsList); err != nil {
			return nil, nil, err
		}
		outputsDict = make(map[string]map[string]eval.Value, len(outputsList))
		for i, outputs := range outputsList {
			outputsDict[strconv.FormatInt(int64(i), 10)] = outputs
		}
	case eval.Dict:
		// If the implementation function returns a dict, this
		// can either be a 1:1 transition or a 1:2+ transition
		// in the form of a dictionary of dictionaries. Check
		// whether the return value is a dict of dicts.
		gotEntries := false
		dictOfDicts := true
		var errIter error
		for _, value := range typedOutputs.Items(thread, &errIter) {
			gotEntries = true
			if _, ok := value.(eval.Dict); !ok {
				dictOfDicts = false
				break
			}
		}
		if errIter != nil {
			return nil, nil, errIter
		}
		if gotEntries && dictOfDicts {
			// 1:2+ transition in the form of a dictionary.
			if err := unpack.Dict(unpack.String, unpack.Dict(unpack.String, unpack.Any)).UnpackInto(thread, typedOutputs, &outputsDict); err != nil {
				return nil, nil, err
			}
		} else {
			// 1:1 transition. These are implicitly converted to a
			// singleton list.
			var outputs map[string]eval.Value
			if err := unpack.Dict(unpack.String, unpack.Any).UnpackInto(thread, typedOutputs, &outputs); err != nil {
				return nil, nil, err
			}
			outputsDict = map[string]map[string]eval.Value{
				"0": outputs,
			}
		}
	default:
		return nil, nil, errors.New("transition did not yield a list or dict")
	}
	return expectedOutputs, outputsDict, nil
}

type performAndApplyUserDefinedTransitionResult[TMetadata model_core.ReferenceMetadata] = model_core.PatchedMessage[*model_analysis_pb.UserDefinedTransition_Value_Success, TMetadata]

func (c *baseComputer[TReference, TMetadata]) performAndApplyUserDefinedTransition(ctx context.Context, e performUserDefinedTransitionEnvironment[TReference, TMetadata], analysisEncoders *Encoders[TReference], starlarkValueEncoders *StarlarkValueEncoders[TReference], transition model_core.Message[*model_starlark_pb.Transition_UserDefined, TReference], configurationReference model_core.Message[*model_core_pb.DecodableReference, TReference], attrParameter eval.Value) (performAndApplyUserDefinedTransitionResult[TMetadata], error) {
	analysisReaders := &analysisEncoders.Readers
	valueEncodingOptionsFactory, err := c.newValueEncodingOptionsFactory(e, starlarkValueEncoders)
	if err != nil {
		return performAndApplyUserDefinedTransitionResult[TMetadata]{}, err
	}

	allBuiltinsModulesNames := e.GetBuiltinsModuleNamesValue(&model_analysis_pb.BuiltinsModuleNames_Key{})
	if !allBuiltinsModulesNames.IsSet() {
		return performAndApplyUserDefinedTransitionResult[TMetadata]{}, evaluation.ErrMissingDependency
	}
	thread, err := c.newStarlarkThread(ctx, e, starlarkValueEncoders, allBuiltinsModulesNames.Message.BuiltinsModuleNames)
	if err != nil {
		return performAndApplyUserDefinedTransitionResult[TMetadata]{}, err
	}

	expectedOutputs, outputsDict, err := c.performUserDefinedTransition(ctx, e, analysisReaders, &starlarkValueEncoders.Readers, thread, transition, configurationReference, attrParameter)
	if err != nil {
		return performAndApplyUserDefinedTransitionResult[TMetadata]{}, err
	}

	patcher := model_core.NewReferenceMessagePatcher[TMetadata]()
	entries := make([]*model_analysis_pb.UserDefinedTransition_Value_Success_Entry, 0, len(outputsDict))
	for i, key := range slices.Sorted(maps.Keys(outputsDict)) {
		buildSettingValuesToApply, err := getCanonicalTransitionOutputValuesFromDict(thread, expectedOutputs, outputsDict[key])
		if err != nil {
			return performAndApplyUserDefinedTransitionResult[TMetadata]{}, fmt.Errorf("key %#v: %w", i, err)
		}
		outputConfigurationReference, err := c.applyTransition(
			ctx,
			thread,
			e,
			analysisEncoders,
			configurationReference,
			buildSettingValuesToApply,
			valueEncodingOptionsFactory.newValueEncodingOptions(ctx, nil),
		)
		if err != nil {
			return performAndApplyUserDefinedTransitionResult[TMetadata]{}, fmt.Errorf("key %#v: %w", i, err)
		}
		entries = append(entries, &model_analysis_pb.UserDefinedTransition_Value_Success_Entry{
			Key:                          key,
			OutputConfigurationReference: outputConfigurationReference.Message,
		})
		patcher.Merge(outputConfigurationReference.Patcher)
	}
	return model_core.NewPatchedMessage(
		&model_analysis_pb.UserDefinedTransition_Value_Success{
			Entries: entries,
		},
		patcher,
	), nil
}

type performUserDefinedTransitionCachedEnvironment[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] interface {
	performUserDefinedTransitionEnvironment[TReference, TMetadata]

	UserDefinedTransitionValueGetter[TReference, TMetadata]
}

func (c *baseComputer[TReference, TMetadata]) performUserDefinedTransitionCached(
	ctx context.Context,
	e performUserDefinedTransitionCachedEnvironment[TReference, TMetadata],
	analysisEncoders *Encoders[TReference],
	starlarkValueEncoders *StarlarkValueEncoders[TReference],
	transition model_core.Message[*model_starlark_pb.Transition_UserDefined, TReference],
	configurationReference model_core.Message[*model_core_pb.DecodableReference, TReference],
	attrParameter eval.Value,
) (performAndApplyUserDefinedTransitionResult[TMetadata], error) {
	switch transition.Message.WhichKind() {
	case model_starlark_pb.Transition_UserDefined_Identifier_case:
		// First attempt to call into the UserDefinedTransition
		// function. This function is capable of computing transitions
		// that don't depend on the "attr" parameter.
		patchedConfigurationReference := model_core.Patch(e, configurationReference)
		transitionValue := e.GetUserDefinedTransitionValue(
			model_core.NewPatchedMessage(
				&model_analysis_pb.UserDefinedTransition_Key{
					TransitionIdentifier:        transition.Message.GetIdentifier(),
					InputConfigurationReference: patchedConfigurationReference.Message,
				},
				patchedConfigurationReference.Patcher,
			),
		)
		if !transitionValue.IsSet() {
			return performAndApplyUserDefinedTransitionResult[TMetadata]{}, evaluation.ErrMissingDependency
		}

		switch transitionValue.Message.GetResult().(type) {
		case *model_analysis_pb.UserDefinedTransition_Value_TransitionDependsOnAttrs:
			// It turns out this user defined transition
			// accesses the "attr" parameter. This prevents
			// it from getting cached.
		case *model_analysis_pb.UserDefinedTransition_Value_Success_:
			return model_core.Patch(e, model_core.Nested(transitionValue, transitionValue.Message.GetSuccess())), nil
		default:
			return performAndApplyUserDefinedTransitionResult[TMetadata]{}, errors.New("unexpected user defined transition result type")
		}
	case model_starlark_pb.Transition_UserDefined_Definition_case:
		// User defined transition was declared inline, meaning
		// it does not have an identifier. This prevents us from
		// calling the UserDefinedTransition function, as it
		// expects an identifier.
	default:
		return performAndApplyUserDefinedTransitionResult[TMetadata]{}, errors.New("user-defined transition has an unknown type")
	}

	return c.performAndApplyUserDefinedTransition(
		ctx,
		e,
		analysisEncoders,
		starlarkValueEncoders,
		transition,
		configurationReference,
		attrParameter,
	)
}

type performTransitionEnvironment[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] interface {
	performUserDefinedTransitionCachedEnvironment[TReference, TMetadata]

	ExecTransitionValueGetter[TReference, TMetadata]
}

func (c *baseComputer[TReference, TMetadata]) performTransition(
	ctx context.Context,
	e performTransitionEnvironment[TReference, TMetadata],
	analysisEncoders *Encoders[TReference],
	starlarkValueEncoders *StarlarkValueEncoders[TReference],
	transition model_core.Message[*model_starlark_pb.Transition, TReference],
	configurationReference model_core.Message[*model_core_pb.DecodableReference, TReference],
	attrParameter eval.Value,
	execGroupPlatformLabels map[string]string,
) (performAndApplyUserDefinedTransitionResult[TMetadata], bool, error) {
	if tr := transition.Message.GetUserDefined(); tr != nil {
		configurationReferences, err := c.performUserDefinedTransitionCached(
			ctx,
			e,
			analysisEncoders,
			starlarkValueEncoders,
			model_core.Nested(transition, tr),
			configurationReference,
			attrParameter,
		)
		return configurationReferences, true, err
	}
	configurationReferences, err := performBuiltinTransition(e, transition, configurationReference, execGroupPlatformLabels)
	return configurationReferences, false, err
}

type performBuiltinTransitionEnvironment[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] interface {
	model_core.ObjectManager[TReference, TMetadata]
	ExecTransitionValueGetter[TReference, TMetadata]
}

// performBuiltinTransition applies a transition that is not user
// defined (e.g., "exec", "target", config.none()). These transitions
// never yield more than one configuration, and don't depend on the
// values of any attrs.
func performBuiltinTransition[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	e performBuiltinTransitionEnvironment[TReference, TMetadata],
	transition model_core.Message[*model_starlark_pb.Transition, TReference],
	configurationReference model_core.Message[*model_core_pb.DecodableReference, TReference],
	execGroupPlatformLabels map[string]string,
) (performAndApplyUserDefinedTransitionResult[TMetadata], error) {
	switch transition.Message.WhichKind() {
	case model_starlark_pb.Transition_ExecGroup_case:
		platformLabel, ok := execGroupPlatformLabels[transition.Message.GetExecGroup()]
		if !ok {
			return performAndApplyUserDefinedTransitionResult[TMetadata]{}, fmt.Errorf("unknown exec group %#v", transition.Message.GetExecGroup())
		}

		inputConfigurationReference := model_core.Patch(e, configurationReference)
		execTransition := e.GetExecTransitionValue(
			model_core.NewPatchedMessage(
				&model_analysis_pb.ExecTransition_Key{
					PlatformLabel:               platformLabel,
					InputConfigurationReference: inputConfigurationReference.Message,
				},
				inputConfigurationReference.Patcher,
			),
		)
		if !execTransition.IsSet() {
			return performAndApplyUserDefinedTransitionResult[TMetadata]{}, evaluation.ErrMissingDependency
		}

		outputConfigurationReference := model_core.Patch(e, model_core.Nested(execTransition, execTransition.Message.GetOutputConfigurationReference()))
		return model_core.NewPatchedMessage(
			&model_analysis_pb.UserDefinedTransition_Value_Success{
				Entries: []*model_analysis_pb.UserDefinedTransition_Value_Success_Entry{{
					OutputConfigurationReference: outputConfigurationReference.Message,
				}},
			},
			outputConfigurationReference.Patcher,
		), nil
	case model_starlark_pb.Transition_None_case:
		// Use the empty configuration.
		return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.UserDefinedTransition_Value_Success{
			Entries: []*model_analysis_pb.UserDefinedTransition_Value_Success_Entry{{}},
		}), nil
	case model_starlark_pb.Transition_Target_case:
		// Don't transition. Use the current target.
		patchedConfigurationReference := model_core.Patch(e, configurationReference)
		return model_core.NewPatchedMessage(
			&model_analysis_pb.UserDefinedTransition_Value_Success{
				Entries: []*model_analysis_pb.UserDefinedTransition_Value_Success_Entry{{
					OutputConfigurationReference: patchedConfigurationReference.Message,
				}},
			},
			patchedConfigurationReference.Patcher,
		), nil
	case model_starlark_pb.Transition_Unconfigured_case:
		// Leave targets unconfigured.
		return model_core.NewSimplePatchedMessage[TMetadata](
			&model_analysis_pb.UserDefinedTransition_Value_Success{},
		), nil
	case model_starlark_pb.Transition_UserDefined_case:
		return performAndApplyUserDefinedTransitionResult[TMetadata]{}, errors.New("user-defined transitions cannot be used in this context")
	default:
		return performAndApplyUserDefinedTransitionResult[TMetadata]{}, errors.New("unknown transition type")
	}
}

func (c *baseComputer[TReference, TMetadata]) ComputeUserDefinedTransitionValue(ctx context.Context, key model_core.Message[*model_analysis_pb.UserDefinedTransition_Key, TReference], e UserDefinedTransitionEnvironment[TReference, TMetadata]) (PatchedUserDefinedTransitionValue[TMetadata], error) {
	analysisEncoders, gotAnalysisEncoders := getAnalysisEncoders(e)
	starlarkValueEncoders, gotStarlarkValueEncoders := getStarlarkValueEncoders(e)

	if !gotAnalysisEncoders || !gotStarlarkValueEncoders {
		return PatchedUserDefinedTransitionValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	entries, err := c.performAndApplyUserDefinedTransition(
		ctx,
		e,
		analysisEncoders,
		starlarkValueEncoders,
		model_core.NewSimpleMessage[TReference](model_starlark_pb.Transition_UserDefined_builder{
			Identifier: proto.String(key.Message.GetTransitionIdentifier()),
		}.Build()),
		model_core.Nested(key, key.Message.GetInputConfigurationReference()),
		stubbedTransitionAttr{},
	)
	if err != nil {
		if errors.Is(err, errTransitionDependsOnAttrs) {
			// Can't compute the transition indepently of
			// the rule in which it is referenced. Return
			// this to the caller, so that it can apply the
			// transition directly.
			return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.UserDefinedTransition_Value{
				Result: &model_analysis_pb.UserDefinedTransition_Value_TransitionDependsOnAttrs{TransitionDependsOnAttrs: &emptypb.Empty{}},
			}), nil
		}
		return PatchedUserDefinedTransitionValue[TMetadata]{}, err
	}

	return model_core.NewPatchedMessage(
		&model_analysis_pb.UserDefinedTransition_Value{
			Result: &model_analysis_pb.UserDefinedTransition_Value_Success_{Success: entries.Message},
		},
		entries.Patcher,
	), nil
}

type stubbedTransitionAttr struct{}

var _ eval.HasAttrs = stubbedTransitionAttr{}

// Repr writes the representation of the value.
func (stubbedTransitionAttr) Repr(_ *eval.Thread, p *eval.Printer) error {
	p.WriteString("<transition_attr>")
	return nil
}

func (stubbedTransitionAttr) Type() string {
	return "transition_attr"
}

func (stubbedTransitionAttr) Truth(*eval.Thread) (bool, error) {
	return true, nil
}

var errTransitionDependsOnAttrs = errors.New("transition depends on rule attrs, which are not available in this context")

func (stubbedTransitionAttr) Attr(*eval.Thread, string) (eval.Value, error) {
	return nil, errTransitionDependsOnAttrs
}

func (stubbedTransitionAttr) AttrNames(*eval.Thread) ([]string, error) {
	return nil, errTransitionDependsOnAttrs
}
