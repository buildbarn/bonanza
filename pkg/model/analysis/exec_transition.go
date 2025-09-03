package analysis

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"bonanza.build/pkg/evaluation"
	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_build_pb "bonanza.build/pkg/proto/model/build"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"

	"go.starlark.net/starlark"
)

type createInitialConfigurationEnvironment[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	model_core.ObjectCapturer[TReference, TMetadata]
	labelResolverEnvironment[TReference]
	getExpectedTransitionOutputEnvironment[TReference, TMetadata]
}

// initialConfigurationBuildSetting contains all of the values that were
// provided for a single build setting. Depending on whether the build
// setting type is repeatable, either the last or all values are used to
// determine the build setting's definitive value.
type initialConfigurationBuildSetting[TReference any] struct {
	expectedOutput expectedTransitionOutput[TReference]
	values         []string
}

func (c *baseComputer[TReference, TMetadata]) createInitialConfiguration(
	ctx context.Context,
	e createInitialConfigurationEnvironment[TReference, TMetadata],
	thread *starlark.Thread,
	rootPackage label.CanonicalPackage,
	configuration *model_build_pb.Configuration,
) (model_core.PatchedMessage[*model_core_pb.DecodableReference, TMetadata], error) {
	// Walk over all provided build setting overrides and group all
	// values belonging to the same build setting together.
	var buildSettings []initialConfigurationBuildSetting[TReference]
	originalBuildSettingLabelIndices := map[string]int{}
	visibleBuildSettingLabelIndices := map[string]int{}
	missingDependencies := false
	for _, buildSettingOverride := range configuration.BuildSettingOverrides {
		index, ok := originalBuildSettingLabelIndices[buildSettingOverride.Label]
		if !ok {
			apparentBuildSettingLabel, err := rootPackage.AppendLabel(buildSettingOverride.Label)
			if err != nil {
				return model_core.PatchedMessage[*model_core_pb.DecodableReference, TMetadata]{}, fmt.Errorf("invalid build setting label %#v: %w", buildSettingOverride.Label, err)
			}
			expectedOutput, err := getExpectedTransitionOutput[TReference, TMetadata](e, rootPackage, apparentBuildSettingLabel)
			if err != nil {
				if errors.Is(err, evaluation.ErrMissingDependency) {
					missingDependencies = true
					continue
				}
			}
			if !expectedOutput.isFlag {
				return model_core.PatchedMessage[*model_core_pb.DecodableReference, TMetadata]{}, fmt.Errorf("build setting with label %#v is not callable on the command line", expectedOutput.label)
			}
			index, ok = visibleBuildSettingLabelIndices[expectedOutput.label]
			if !ok {
				index = len(buildSettings)
				buildSettings = append(buildSettings, initialConfigurationBuildSetting[TReference]{
					expectedOutput: expectedOutput,
				})
				visibleBuildSettingLabelIndices[expectedOutput.label] = index
			}
			originalBuildSettingLabelIndices[buildSettingOverride.Label] = index
		}
		buildSettings[index].values = append(buildSettings[index].values, buildSettingOverride.Value)
	}
	if missingDependencies {
		return model_core.PatchedMessage[*model_core_pb.DecodableReference, TMetadata]{}, evaluation.ErrMissingDependency
	}

	// Sort all build settings by label, as that's the order in
	// which applyTransition expects them to be to insert them into
	// the resulting configuration.
	slices.SortFunc(buildSettings, func(a, b initialConfigurationBuildSetting[TReference]) int {
		return strings.Compare(a.expectedOutput.label, b.expectedOutput.label)
	})

	labelResolver := newLabelResolver(e)
	buildSettingValuesToApply := make([]buildSettingValueToApply[TReference], 0, len(buildSettings))
	for _, buildSetting := range buildSettings {
		expectedOutput := &buildSetting.expectedOutput
		canonicalizedValue, err := expectedOutput.canonicalizer.CanonicalizeStringList(buildSetting.values, labelResolver)
		if err != nil {
			return model_core.PatchedMessage[*model_core_pb.DecodableReference, TMetadata]{}, fmt.Errorf("invalid values for build setting label %#v: %w", expectedOutput.label, err)
		}
		buildSettingValuesToApply = append(buildSettingValuesToApply, buildSettingValueToApply[TReference]{
			label:              expectedOutput.label,
			canonicalizedValue: canonicalizedValue,
			defaultValue:       expectedOutput.defaultValue,
		})
	}

	return c.applyTransition(
		ctx,
		e,
		model_core.NewSimpleMessage[TReference]((*model_core_pb.DecodableReference)(nil)),
		buildSettingValuesToApply,
		c.getValueEncodingOptions(e, nil),
	)
}

func (c *baseComputer[TReference, TMetadata]) ComputeExecTransitionValue(ctx context.Context, key model_core.Message[*model_analysis_pb.ExecTransition_Key, TReference], e ExecTransitionEnvironment[TReference, TMetadata]) (PatchedExecTransitionValue[TMetadata], error) {
	allBuiltinsModulesNames := e.GetBuiltinsModuleNamesValue(&model_analysis_pb.BuiltinsModuleNames_Key{})
	rootModuleValue := e.GetRootModuleValue(&model_analysis_pb.RootModule_Key{})
	if !allBuiltinsModulesNames.IsSet() || !rootModuleValue.IsSet() {
		return PatchedExecTransitionValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	rootModuleName, err := label.NewModule(rootModuleValue.Message.RootModuleName)
	if err != nil {
		return PatchedExecTransitionValue[TMetadata]{}, fmt.Errorf("invalid root module name: %w", err)
	}
	rootPackage := rootModuleName.ToModuleInstance(nil).GetBareCanonicalRepo().GetRootPackage()

	// Determine the new target platform.
	apparentTargetPlatform, err := label.NewApparentLabel(key.Message.PlatformLabel)
	if err != nil {
		return PatchedExecTransitionValue[TMetadata]{}, fmt.Errorf("invalid target platform: %w", err)
	}
	canonicalTargetPlatform, err := label.Canonicalize(
		newLabelResolver(e),
		rootPackage.GetCanonicalRepo(),
		apparentTargetPlatform,
	)
	if err != nil {
		return PatchedExecTransitionValue[TMetadata]{}, fmt.Errorf("failed to resolve target platform %#v: %w", err)
	}

	// Obtain the identifier of the Starlark transition that is used
	// for exec transitions.
	inputConfigurationReference := model_core.Nested(key, key.Message.InputConfigurationReference)
	execConfigCommandLineOption := "@@bazel_tools+//command_line_option:experimental_exec_config"
	execConfigValue, err := c.getBuildSettingValue(
		ctx,
		e,
		rootPackage,
		execConfigCommandLineOption,
		inputConfigurationReference,
	)
	if err != nil {
		return PatchedExecTransitionValue[TMetadata]{}, err
	}
	execConfigStr, ok := execConfigValue.Message.Kind.(*model_starlark_pb.Value_Str)
	if !ok {
		return PatchedExecTransitionValue[TMetadata]{}, fmt.Errorf("value of build setting %s is not a string", execConfigCommandLineOption)
	}

	// Invoke the exec transition.
	thread := c.newStarlarkThread(ctx, e, allBuiltinsModulesNames.Message.BuiltinsModuleNames)
	expectedOutputs, outputsDict, err := c.performUserDefinedTransition(
		ctx,
		e,
		thread,
		model_core.NewSimpleMessage[TReference](
			&model_starlark_pb.Transition_UserDefined{
				Kind: &model_starlark_pb.Transition_UserDefined_Identifier{
					Identifier: execConfigStr.Str,
				},
			},
		),
		inputConfigurationReference,
		stubbedTransitionAttr{},
	)
	if err != nil {
		return PatchedExecTransitionValue[TMetadata]{}, err
	}

	if l := len(outputsDict); l > 1 {
		return PatchedExecTransitionValue[TMetadata]{}, fmt.Errorf("exec transition yielded %d configurations, while exactly 1 was expected", l)
	}
	buildSettingValuesToApply, err := getCanonicalTransitionOutputValuesFromDict(thread, expectedOutputs, slices.Collect(maps.Values(outputsDict))[0])
	if err != nil {
		return PatchedExecTransitionValue[TMetadata]{}, err
	}

	// Inject --platforms into build setting values, as the exec
	// transition does not set it for us.
	commandLineOptionPlatformsLabelStr := commandLineOptionPlatformsLabel.String()
	platformsInsertionIndex, hasExistingPlatforms := sort.Find(
		len(buildSettingValuesToApply),
		func(i int) int {
			return strings.Compare(commandLineOptionPlatformsLabelStr, buildSettingValuesToApply[i].label)
		},
	)
	if hasExistingPlatforms {
		return PatchedExecTransitionValue[TMetadata]{}, fmt.Errorf("exec transition is overriding build setting %#v, which is not expected", commandLineOptionPlatformsLabelStr)
	}
	buildSettingValuesToApply = append(
		append(
			buildSettingValuesToApply[:platformsInsertionIndex:platformsInsertionIndex],
			buildSettingValueToApply[TReference]{
				label: commandLineOptionPlatformsLabelStr,
				canonicalizedValue: starlark.NewList([]starlark.Value{
					model_starlark.NewLabel[TReference, TMetadata](canonicalTargetPlatform.AsResolved()),
				}),
				// TODO: This is not correct, but likely not harmful.
				defaultValue: model_core.NewSimpleMessage[TReference](&model_starlark_pb.Value{}),
			},
		),
	)

	// Create new configuration to include any build settings
	// yielded by the exec transition.
	outputConfigurationReference, err := c.applyTransition(
		ctx,
		e,
		model_core.NewSimpleMessage[TReference]((*model_core_pb.DecodableReference)(nil)),
		buildSettingValuesToApply,
		c.getValueEncodingOptions(e, nil),
	)
	if err != nil {
		return PatchedExecTransitionValue[TMetadata]{}, err
	}

	return model_core.NewPatchedMessage(
		&model_analysis_pb.ExecTransition_Value{
			OutputConfigurationReference: outputConfigurationReference.Message,
		},
		outputConfigurationReference.Patcher,
	), nil
}
