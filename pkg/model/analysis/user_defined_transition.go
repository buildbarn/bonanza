package analysis

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	"bonanza.build/pkg/model/evaluation"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/unpack"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/protobuf/types/known/emptypb"

	"go.starlark.net/starlark"
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

	GetCompiledBzlFileGlobalValue(*model_analysis_pb.CompiledBzlFileGlobal_Key) model_core.Message[*model_analysis_pb.CompiledBzlFileGlobal_Value, TReference]
	GetTargetValue(*model_analysis_pb.Target_Key) model_core.Message[*model_analysis_pb.Target_Value, TReference]
	GetVisibleTargetValue(model_core.PatchedMessage[*model_analysis_pb.VisibleTarget_Key, TMetadata]) model_core.Message[*model_analysis_pb.VisibleTarget_Value, TReference]
}

// stringToStarlarkLabelOrNone converts a string containing a resolved
// label value to a Protobuf message of a Starlark Label object. If the
// string is empty, it is converted to None.
//
// This function can, for example, be used to convert a label setting's
// build_setting_default to a Starlark value.
func stringToStarlarkLabelOrNone(v string) *model_starlark_pb.Value {
	if v == "" {
		return &model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_None{
				None: &emptypb.Empty{},
			},
		}
	}
	return &model_starlark_pb.Value{
		Kind: &model_starlark_pb.Value_Label{
			Label: v,
		},
	}
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
		model_core.NewSimplePatchedMessage[TMetadata](
			&model_analysis_pb.VisibleTarget_Key{
				FromPackage:        canonicalBuildSettingLabel.GetCanonicalPackage().String(),
				ToLabel:            canonicalBuildSettingLabel.String(),
				StopAtLabelSetting: true,
			},
		),
	)
	if !visibleTargetValue.IsSet() {
		return expectedTransitionOutput[TReference]{}, evaluation.ErrMissingDependency
	}
	visibleBuildSettingLabel := visibleTargetValue.Message.Label

	// Determine how values associated with this build
	// setting need to be canonicalized.
	targetValue := e.GetTargetValue(&model_analysis_pb.Target_Key{
		Label: visibleBuildSettingLabel,
	})
	if !targetValue.IsSet() {
		return expectedTransitionOutput[TReference]{}, evaluation.ErrMissingDependency
	}
	switch targetKind := targetValue.Message.Definition.GetKind().(type) {
	case *model_starlark_pb.Target_Definition_LabelSetting:
		// Build setting is a label_setting() or label_flag().
		return expectedTransitionOutput[TReference]{
			label: visibleBuildSettingLabel,
			canonicalizer: model_starlark.NewLabelBuildSettingCanonicalizer[TReference, TMetadata](
				transitionPackage,
				targetKind.LabelSetting.SingletonList,
			),
			defaultValue: model_core.NewSimpleMessage[TReference](
				stringToStarlarkLabelOrNone(targetKind.LabelSetting.BuildSettingDefault),
			),
			isFlag: targetKind.LabelSetting.Flag,
		}, nil
	case *model_starlark_pb.Target_Definition_RuleTarget:
		// Build setting is written in Starlark.
		if targetKind.RuleTarget.BuildSettingDefault == nil {
			return expectedTransitionOutput[TReference]{}, fmt.Errorf("rule %#v used by label setting %#v does not have \"build_setting\" set", targetKind.RuleTarget.RuleIdentifier, visibleBuildSettingLabel)
		}
		ruleValue := e.GetCompiledBzlFileGlobalValue(&model_analysis_pb.CompiledBzlFileGlobal_Key{
			Identifier: targetKind.RuleTarget.RuleIdentifier,
		})
		if !ruleValue.IsSet() {
			return expectedTransitionOutput[TReference]{}, evaluation.ErrMissingDependency
		}
		rule, ok := ruleValue.Message.Global.Kind.(*model_starlark_pb.Value_Rule)
		if !ok {
			return expectedTransitionOutput[TReference]{}, fmt.Errorf("identifier %#v used by build setting %#v is not a rule", targetKind.RuleTarget.RuleIdentifier, visibleBuildSettingLabel)
		}
		ruleDefinition, ok := rule.Rule.Kind.(*model_starlark_pb.Rule_Definition_)
		if !ok {
			return expectedTransitionOutput[TReference]{}, fmt.Errorf("rule %#v used by build setting %#v does not have a definition", targetKind.RuleTarget.RuleIdentifier, visibleBuildSettingLabel)
		}
		buildSetting := ruleDefinition.Definition.BuildSetting
		if buildSetting == nil {
			return expectedTransitionOutput[TReference]{}, fmt.Errorf("rule %#v used by build setting %#v does not have \"build_setting\" set", targetKind.RuleTarget.RuleIdentifier, visibleBuildSettingLabel)
		}
		buildSettingType, err := model_starlark.DecodeBuildSettingType[TReference, TMetadata](buildSetting)
		if err != nil {
			return expectedTransitionOutput[TReference]{}, fmt.Errorf("failed to decode build setting type for rule %#v used by build setting %#v: %w", targetKind.RuleTarget.RuleIdentifier, visibleBuildSettingLabel, err)
		}
		return expectedTransitionOutput[TReference]{
			label:         visibleBuildSettingLabel,
			canonicalizer: buildSettingType.GetCanonicalizer(transitionPackage),
			defaultValue:  model_core.Nested(targetValue, targetKind.RuleTarget.BuildSettingDefault),
			isFlag:        buildSetting.Flag,
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

func getCanonicalTransitionOutputValuesFromDict[TReference any](thread *starlark.Thread, namedExpectedOutputs []namedExpectedTransitionOutput[TReference], outputs map[string]starlark.Value) ([]buildSettingValueToApply[TReference], error) {
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
	canonicalizedValue starlark.Value
	defaultValue       model_core.Message[*model_starlark_pb.Value, TReference]
}

func (c *baseComputer[TReference, TMetadata]) applyTransition(
	ctx context.Context,
	e model_core.ObjectCapturer[TReference, TMetadata],
	configurationReference model_core.Message[*model_core_pb.DecodableReference, TReference],
	buildSettingValuesToApply []buildSettingValueToApply[TReference],
	valueEncodingOptions *model_starlark.ValueEncodingOptions[TReference, TMetadata],
) (model_core.PatchedMessage[*model_core_pb.DecodableReference, TMetadata], error) {
	var errIter error
	existingIter, existingIterStop := iter.Pull(btree.AllLeaves(
		ctx,
		c.buildSettingOverrideReader,
		getBuildSettingOverridesFromReference(configurationReference),
		func(override model_core.Message[*model_analysis_pb.BuildSettingOverride, TReference]) (*model_core_pb.DecodableReference, error) {
			return override.Message.GetParent().GetReference(), nil
		},
		&errIter,
	))
	defer existingIterStop()

	// TODO: Use a proper encoder!
	treeBuilder := btree.NewSplitProllyBuilder(
		/* minimumSizeBytes = */ 32*1024,
		/* maximumSizeBytes = */ 128*1024,
		btree.NewObjectCreatingNodeMerger(
			c.getValueObjectEncoder(),
			c.getReferenceFormat(),
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
			encodedValue, _, err := model_starlark.EncodeValue(
				buildSettingValueToApply.canonicalizedValue,
				/* path = */ map[starlark.Value]struct{}{},
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

	createdConfiguration, err := model_core.MarshalAndEncode(
		model_core.ProtoListToMarshalable(buildSettingOverrides),
		c.getReferenceFormat(),
		c.getValueObjectEncoder(),
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
	GetConfiguredTargetValue(model_core.PatchedMessage[*model_analysis_pb.ConfiguredTarget_Key, TMetadata]) model_core.Message[*model_analysis_pb.ConfiguredTarget_Value, TReference]
	GetTargetValue(*model_analysis_pb.Target_Key) model_core.Message[*model_analysis_pb.Target_Value, TReference]
	GetVisibleTargetValue(model_core.PatchedMessage[*model_analysis_pb.VisibleTarget_Key, TMetadata]) model_core.Message[*model_analysis_pb.VisibleTarget_Value, TReference]
}

var featureFlagInfoProviderIdentifier = util.Must(label.NewCanonicalStarlarkIdentifier("@@builtins_core+//:exports.bzl%FeatureFlagInfo"))

func (c *baseComputer[TReference, TMetadata]) getBuildSettingValue(ctx context.Context, e getBuildSettingValueEnvironment[TReference, TMetadata], fromPackage label.CanonicalPackage, buildSettingLabel string, configurationReference model_core.Message[*model_core_pb.DecodableReference, TReference]) (model_core.Message[*model_starlark_pb.Value, TReference], error) {
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
	visibleBuildSettingLabel := visibleTargetValue.Message.Label

	// Determine the current value of the build setting.
	if buildSettingOverride, err := btree.Find(
		ctx,
		c.buildSettingOverrideReader,
		getBuildSettingOverridesFromReference(configurationReference),
		func(entry model_core.Message[*model_analysis_pb.BuildSettingOverride, TReference]) (int, *model_core_pb.DecodableReference) {
			switch level := entry.Message.Level.(type) {
			case *model_analysis_pb.BuildSettingOverride_Leaf_:
				return strings.Compare(visibleBuildSettingLabel, level.Leaf.Label), nil
			case *model_analysis_pb.BuildSettingOverride_Parent_:
				return strings.Compare(visibleBuildSettingLabel, level.Parent.FirstLabel), level.Parent.Reference
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
		level, ok := buildSettingOverride.Message.Level.(*model_analysis_pb.BuildSettingOverride_Leaf_)
		if !ok {
			return model_core.Message[*model_starlark_pb.Value, TReference]{}, fmt.Errorf("build setting override for label setting %#v is not a valid leaf", visibleBuildSettingLabel)
		}
		return model_core.Nested(buildSettingOverride, level.Leaf.Value), nil
	}

	// No override present. Obtain the default value
	// of the build setting.
	targetValue := e.GetTargetValue(&model_analysis_pb.Target_Key{
		Label: visibleBuildSettingLabel,
	})
	if !targetValue.IsSet() {
		return model_core.Message[*model_starlark_pb.Value, TReference]{}, evaluation.ErrMissingDependency
	}
	switch targetKind := targetValue.Message.Definition.GetKind().(type) {
	case *model_starlark_pb.Target_Definition_LabelSetting:
		// Build setting is a label_setting() or
		// label_flag().
		buildSettingDefault := stringToStarlarkLabelOrNone(targetKind.LabelSetting.BuildSettingDefault)
		if targetKind.LabelSetting.SingletonList {
			buildSettingDefault = &model_starlark_pb.Value{
				Kind: &model_starlark_pb.Value_List{
					List: &model_starlark_pb.List{
						Elements: []*model_starlark_pb.List_Element{{
							Level: &model_starlark_pb.List_Element_Leaf{
								Leaf: buildSettingDefault,
							},
						}},
					},
				},
			}
		}
		return model_core.NewSimpleMessage[TReference](buildSettingDefault), nil
	case *model_starlark_pb.Target_Definition_RuleTarget:
		if d := targetKind.RuleTarget.BuildSettingDefault; d != nil {
			// Build setting that is written in Starlark.
			return model_core.Nested(targetValue, d), nil
		}

		// Not a build setting, but the target may provide
		// FeatureFlagInfo if configured.
		patchedConfigurationReference := model_core.Patch(e, configurationReference)
		configuredTarget := e.GetConfiguredTargetValue(
			model_core.NewPatchedMessage(
				&model_analysis_pb.ConfiguredTarget_Key{
					Label:                  visibleBuildSettingLabel,
					ConfigurationReference: patchedConfigurationReference.Message,
				},
				patchedConfigurationReference.Patcher,
			),
		)
		if !configuredTarget.IsSet() {
			return model_core.Message[*model_starlark_pb.Value, TReference]{}, evaluation.ErrMissingDependency
		}
		featureFlagInfoProviderIdentifierStr := featureFlagInfoProviderIdentifier.String()
		providerInstances := configuredTarget.Message.ProviderInstances
		featureFlagInfoIndex, ok := sort.Find(
			len(providerInstances),
			func(i int) int {
				return strings.Compare(featureFlagInfoProviderIdentifierStr, providerInstances[i].ProviderInstanceProperties.GetProviderIdentifier())
			},
		)
		if !ok {
			return model_core.Message[*model_starlark_pb.Value, TReference]{}, fmt.Errorf("rule %#v used by build setting %#v does not have \"build_setting\" set, nor does it yield provider FeatureFlagInfo", targetKind.RuleTarget.RuleIdentifier, visibleBuildSettingLabel)
		}
		featureFlagValue, err := model_starlark.GetStructFieldValue(
			ctx,
			c.valueReaders.List,
			model_core.Nested(configuredTarget, providerInstances[featureFlagInfoIndex].Fields),
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

type performUserDefinedTransitionEnvironment[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	model_core.ObjectManager[TReference, TMetadata]
	getBuildSettingValueEnvironment[TReference, TMetadata]
	getExpectedTransitionOutputEnvironment[TReference, TMetadata]
	starlarkThreadEnvironment[TReference]
}

func (c *baseComputer[TReference, TMetadata]) performUserDefinedTransition(
	ctx context.Context,
	e performUserDefinedTransitionEnvironment[TReference, TMetadata],
	thread *starlark.Thread,
	transition model_core.Message[*model_starlark_pb.Transition_UserDefined, TReference],
	configurationReference model_core.Message[*model_core_pb.DecodableReference, TReference],
	attrParameter starlark.Value,
) ([]namedExpectedTransitionOutput[TReference], map[string]map[string]starlark.Value, error) {
	allBuiltinsModulesNames := e.GetBuiltinsModuleNamesValue(&model_analysis_pb.BuiltinsModuleNames_Key{})
	if !allBuiltinsModulesNames.IsSet() {
		return nil, nil, evaluation.ErrMissingDependency
	}

	var transitionDefinition model_core.Message[*model_starlark_pb.Transition_UserDefined_Definition, TReference]
	switch t := transition.Message.Kind.(type) {
	case *model_starlark_pb.Transition_UserDefined_Identifier:
		transitionIdentifier, err := label.NewCanonicalStarlarkIdentifier(t.Identifier)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid transition identifier: %w", err)
		}

		transitionValue := e.GetCompiledBzlFileGlobalValue(&model_analysis_pb.CompiledBzlFileGlobal_Key{
			Identifier: transitionIdentifier.String(),
		})
		if !transitionValue.IsSet() {
			return nil, nil, evaluation.ErrMissingDependency
		}
		tv, ok := transitionValue.Message.Global.GetKind().(*model_starlark_pb.Value_Transition)
		if !ok {
			return nil, nil, fmt.Errorf("%#v is not a transition", transitionIdentifier.String())
		}
		udt, ok := tv.Transition.Kind.(*model_starlark_pb.Transition_UserDefined_)
		if !ok {
			return nil, nil, fmt.Errorf("%#v is not a user-defined transition", transitionIdentifier.String())
		}
		udtd, ok := udt.UserDefined.Kind.(*model_starlark_pb.Transition_UserDefined_Definition_)
		if !ok {
			return nil, nil, fmt.Errorf("%#v is not a user-defined transition definition", transitionIdentifier.String())
		}
		transitionDefinition = model_core.Nested(transitionValue, udtd.Definition)
	case *model_starlark_pb.Transition_UserDefined_Definition_:
		transitionDefinition = model_core.Nested(transition, t.Definition)
	default:
		return nil, nil, errors.New("user-defined transition has an unknown type")
	}

	transitionPackageStr := transitionDefinition.Message.CanonicalPackage
	transitionPackage, err := label.NewCanonicalPackage(transitionPackageStr)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid canonical package %#v: %w", transitionPackageStr, err)
	}
	transitionRepo := transitionPackage.GetCanonicalRepo()

	// Collect inputs to provide to the implementation function.
	missingDependencies := false
	inputs := starlark.NewDict(len(transitionDefinition.Message.Inputs))
	for _, input := range transitionDefinition.Message.Inputs {
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
			encodedValue,
			/* currentIdentifier = */ nil,
			c.getValueDecodingOptions(ctx, func(resolvedLabel label.ResolvedLabel) (starlark.Value, error) {
				return model_starlark.NewLabel[TReference, TMetadata](resolvedLabel), nil
			}),
		)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to decode value for input %#v: %w", input, err)
		}
		if err := inputs.SetKey(thread, starlark.String(input), v); err != nil {
			return nil, nil, err
		}
	}
	inputs.Freeze()

	// Preprocess the outputs that we expect to see.
	expectedOutputs := make([]namedExpectedTransitionOutput[TReference], 0, len(transitionDefinition.Message.Outputs))
	expectedOutputLabels := make(map[string]string, len(transitionDefinition.Message.Outputs))
	for _, output := range transitionDefinition.Message.Outputs {
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
	outputs, err := starlark.Call(
		thread,
		model_starlark.NewNamedFunction(
			model_starlark.NewProtoNamedFunctionDefinition[TReference, TMetadata](
				model_core.Nested(transitionDefinition, transitionDefinition.Message.Implementation),
			),
		),
		/* args = */ starlark.Tuple{
			inputs,
			attrParameter,
		},
		/* kwargs = */ nil,
	)
	if err != nil {
		if !errors.Is(err, evaluation.ErrMissingDependency) && !errors.Is(err, errTransitionDependsOnAttrs) {
			var evalErr *starlark.EvalError
			if errors.As(err, &evalErr) {
				return nil, nil, errors.New(evalErr.Backtrace())
			}
		}
		return nil, nil, err
	}

	// Process return value of transition implementation function.
	var outputsDict map[string]map[string]starlark.Value
	switch typedOutputs := outputs.(type) {
	case starlark.Indexable:
		// 1:2+ transition in the form of a list.
		var outputsList []map[string]starlark.Value
		if err := unpack.List(unpack.Dict(unpack.String, unpack.Any)).UnpackInto(thread, typedOutputs, &outputsList); err != nil {
			return nil, nil, err
		}
		outputsDict = make(map[string]map[string]starlark.Value, len(outputsList))
		for i, outputs := range outputsList {
			outputsDict[strconv.FormatInt(int64(i), 10)] = outputs
		}
	case starlark.IterableMapping:
		// If the implementation function returns a dict, this
		// can either be a 1:1 transition or a 1:2+ transition
		// in the form of a dictionary of dictionaries. Check
		// whether the return value is a dict of dicts.
		gotEntries := false
		dictOfDicts := true
		for _, value := range starlark.Entries(thread, typedOutputs) {
			gotEntries = true
			if _, ok := value.(starlark.Mapping); !ok {
				dictOfDicts = false
				break
			}
		}
		if gotEntries && dictOfDicts {
			// 1:2+ transition in the form of a dictionary.
			if err := unpack.Dict(unpack.String, unpack.Dict(unpack.String, unpack.Any)).UnpackInto(thread, typedOutputs, &outputsDict); err != nil {
				return nil, nil, err
			}
		} else {
			// 1:1 transition. These are implicitly converted to a
			// singleton list.
			var outputs map[string]starlark.Value
			if err := unpack.Dict(unpack.String, unpack.Any).UnpackInto(thread, typedOutputs, &outputs); err != nil {
				return nil, nil, err
			}
			outputsDict = map[string]map[string]starlark.Value{
				"0": outputs,
			}
		}
	default:
		return nil, nil, errors.New("transition did not yield a list or dict")
	}
	return expectedOutputs, outputsDict, nil
}

type performAndApplyUserDefinedTransitionResult[TMetadata model_core.ReferenceMetadata] = model_core.PatchedMessage[*model_analysis_pb.UserDefinedTransition_Value_Success, TMetadata]

func (c *baseComputer[TReference, TMetadata]) performAndApplyUserDefinedTransition(ctx context.Context, e performUserDefinedTransitionEnvironment[TReference, TMetadata], transition model_core.Message[*model_starlark_pb.Transition_UserDefined, TReference], configurationReference model_core.Message[*model_core_pb.DecodableReference, TReference], attrParameter starlark.Value) (performAndApplyUserDefinedTransitionResult[TMetadata], error) {
	allBuiltinsModulesNames := e.GetBuiltinsModuleNamesValue(&model_analysis_pb.BuiltinsModuleNames_Key{})
	if !allBuiltinsModulesNames.IsSet() {
		return performAndApplyUserDefinedTransitionResult[TMetadata]{}, evaluation.ErrMissingDependency
	}
	thread := c.newStarlarkThread(ctx, e, allBuiltinsModulesNames.Message.BuiltinsModuleNames)

	expectedOutputs, outputsDict, err := c.performUserDefinedTransition(ctx, e, thread, transition, configurationReference, attrParameter)
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
			e,
			configurationReference,
			buildSettingValuesToApply,
			c.getValueEncodingOptions(ctx, e, nil),
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

type performUserDefinedTransitionCachedEnvironment[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	performUserDefinedTransitionEnvironment[TReference, TMetadata]

	GetUserDefinedTransitionValue(model_core.PatchedMessage[*model_analysis_pb.UserDefinedTransition_Key, TMetadata]) model_core.Message[*model_analysis_pb.UserDefinedTransition_Value, TReference]
}

func (c *baseComputer[TReference, TMetadata]) performUserDefinedTransitionCached(
	ctx context.Context,
	e performUserDefinedTransitionCachedEnvironment[TReference, TMetadata],
	transition model_core.Message[*model_starlark_pb.Transition_UserDefined, TReference],
	configurationReference model_core.Message[*model_core_pb.DecodableReference, TReference],
	attrParameter starlark.Value,
) (performAndApplyUserDefinedTransitionResult[TMetadata], error) {
	switch t := transition.Message.Kind.(type) {
	case *model_starlark_pb.Transition_UserDefined_Identifier:
		// First attempt to call into the UserDefinedTransition
		// function. This function is capable of computing transitions
		// that don't depend on the "attr" parameter.
		patchedConfigurationReference := model_core.Patch(e, configurationReference)
		transitionValue := e.GetUserDefinedTransitionValue(
			model_core.NewPatchedMessage(
				&model_analysis_pb.UserDefinedTransition_Key{
					TransitionIdentifier:        t.Identifier,
					InputConfigurationReference: patchedConfigurationReference.Message,
				},
				patchedConfigurationReference.Patcher,
			),
		)
		if !transitionValue.IsSet() {
			return performAndApplyUserDefinedTransitionResult[TMetadata]{}, evaluation.ErrMissingDependency
		}

		switch result := transitionValue.Message.Result.(type) {
		case *model_analysis_pb.UserDefinedTransition_Value_TransitionDependsOnAttrs:
			// It turns out this user defined transition
			// accesses the "attr" parameter. This prevents
			// it from getting cached.
		case *model_analysis_pb.UserDefinedTransition_Value_Success_:
			return model_core.Patch(e, model_core.Nested(transitionValue, result.Success)), nil
		default:
			return performAndApplyUserDefinedTransitionResult[TMetadata]{}, errors.New("unexpected user defined transition result type")
		}
	case *model_starlark_pb.Transition_UserDefined_Definition_:
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
		transition,
		configurationReference,
		attrParameter,
	)
}

type performTransitionEnvironment[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	performUserDefinedTransitionCachedEnvironment[TReference, TMetadata]

	GetExecTransitionValue(model_core.PatchedMessage[*model_analysis_pb.ExecTransition_Key, TMetadata]) model_core.Message[*model_analysis_pb.ExecTransition_Value, TReference]
}

func (c *baseComputer[TReference, TMetadata]) performTransition(
	ctx context.Context,
	e performTransitionEnvironment[TReference, TMetadata],
	transition model_core.Message[*model_starlark_pb.Transition, TReference],
	configurationReference model_core.Message[*model_core_pb.DecodableReference, TReference],
	attrParameter starlark.Value,
	execGroupPlatformLabels map[string]string,
) (performAndApplyUserDefinedTransitionResult[TMetadata], bool, error) {
	// See if any transitions need to be applied.
	switch tr := transition.Message.GetKind().(type) {
	case *model_starlark_pb.Transition_ExecGroup:
		platformLabel, ok := execGroupPlatformLabels[tr.ExecGroup]
		if !ok {
			return performAndApplyUserDefinedTransitionResult[TMetadata]{}, false, fmt.Errorf("unknown exec group %#v", tr.ExecGroup)
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
			return performAndApplyUserDefinedTransitionResult[TMetadata]{}, false, evaluation.ErrMissingDependency
		}

		outputConfigurationReference := model_core.Patch(e, model_core.Nested(execTransition, execTransition.Message.OutputConfigurationReference))
		return model_core.NewPatchedMessage(
			&model_analysis_pb.UserDefinedTransition_Value_Success{
				Entries: []*model_analysis_pb.UserDefinedTransition_Value_Success_Entry{{
					OutputConfigurationReference: outputConfigurationReference.Message,
				}},
			},
			outputConfigurationReference.Patcher,
		), false, nil
	case *model_starlark_pb.Transition_None:
		// Use the empty configuration.
		return model_core.NewSimplePatchedMessage[TMetadata](
			&model_analysis_pb.UserDefinedTransition_Value_Success{
				Entries: []*model_analysis_pb.UserDefinedTransition_Value_Success_Entry{{}},
			},
		), false, nil
	case *model_starlark_pb.Transition_Target:
		// Don't transition. Use the current target.
		patchedConfigurationReference := model_core.Patch(e, configurationReference)
		return model_core.NewPatchedMessage(
			&model_analysis_pb.UserDefinedTransition_Value_Success{
				Entries: []*model_analysis_pb.UserDefinedTransition_Value_Success_Entry{{
					OutputConfigurationReference: patchedConfigurationReference.Message,
				}},
			},
			patchedConfigurationReference.Patcher,
		), false, nil
	case *model_starlark_pb.Transition_Unconfigured:
		// Leave targets unconfigured.
		return model_core.NewSimplePatchedMessage[TMetadata](
			&model_analysis_pb.UserDefinedTransition_Value_Success{},
		), false, nil
	case *model_starlark_pb.Transition_UserDefined_:
		configurationReferences, err := c.performUserDefinedTransitionCached(
			ctx,
			e,
			model_core.Nested(transition, tr.UserDefined),
			configurationReference,
			attrParameter,
		)
		return configurationReferences, true, err
	default:
		return performAndApplyUserDefinedTransitionResult[TMetadata]{}, false, errors.New("unknown transition type")
	}
}

func (c *baseComputer[TReference, TMetadata]) ComputeUserDefinedTransitionValue(ctx context.Context, key model_core.Message[*model_analysis_pb.UserDefinedTransition_Key, TReference], e UserDefinedTransitionEnvironment[TReference, TMetadata]) (PatchedUserDefinedTransitionValue[TMetadata], error) {
	entries, err := c.performAndApplyUserDefinedTransition(
		ctx,
		e,
		model_core.NewSimpleMessage[TReference](
			&model_starlark_pb.Transition_UserDefined{
				Kind: &model_starlark_pb.Transition_UserDefined_Identifier{
					Identifier: key.Message.TransitionIdentifier,
				},
			},
		),
		model_core.Nested(key, key.Message.InputConfigurationReference),
		stubbedTransitionAttr{},
	)
	if err != nil {
		if errors.Is(err, errTransitionDependsOnAttrs) {
			// Can't compute the transition indepently of
			// the rule in which it is referenced. Return
			// this to the caller, so that it can apply the
			// transition directly.
			return model_core.NewSimplePatchedMessage[TMetadata](
				&model_analysis_pb.UserDefinedTransition_Value{
					Result: &model_analysis_pb.UserDefinedTransition_Value_TransitionDependsOnAttrs{
						TransitionDependsOnAttrs: &emptypb.Empty{},
					},
				},
			), nil
		}
		return PatchedUserDefinedTransitionValue[TMetadata]{}, err
	}

	return model_core.NewPatchedMessage(
		&model_analysis_pb.UserDefinedTransition_Value{
			Result: &model_analysis_pb.UserDefinedTransition_Value_Success_{
				Success: entries.Message,
			},
		},
		entries.Patcher,
	), nil
}

type stubbedTransitionAttr struct{}

var _ starlark.HasAttrs = stubbedTransitionAttr{}

func (stubbedTransitionAttr) String() string {
	return "<transition_attr>"
}

func (stubbedTransitionAttr) Type() string {
	return "transition_attr"
}

func (stubbedTransitionAttr) Freeze() {}

func (stubbedTransitionAttr) Truth() starlark.Bool {
	return starlark.True
}

func (stubbedTransitionAttr) Hash(thread *starlark.Thread) (uint32, error) {
	return 0, errors.New("transition_attr cannot be hashed")
}

var errTransitionDependsOnAttrs = errors.New("transition depends on rule attrs, which are not available in this context")

func (stubbedTransitionAttr) Attr(*starlark.Thread, string) (starlark.Value, error) {
	return nil, errTransitionDependsOnAttrs
}

func (stubbedTransitionAttr) AttrNames() []string {
	// TODO: This should also be able to return an error.
	return nil
}
