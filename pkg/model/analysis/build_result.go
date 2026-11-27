package analysis

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
)

func (c *baseComputer[TReference, TMetadata]) ComputeBuildResultValue(ctx context.Context, key *model_analysis_pb.BuildResult_Key, e BuildResultEnvironment[TReference, TMetadata]) (PatchedBuildResultValue[TMetadata], error) {
	analysisEncoders, gotAnalysisEncoders := getAnalysisEncoders(e)
	starlarkValueEncoders, gotStarlarkValueEncoders := getStarlarkValueEncoders(e)

	rootModuleValue := e.GetRootModuleValue(&model_analysis_pb.RootModule_Key{})
	allBuiltinsModulesNames := e.GetBuiltinsModuleNamesValue(&model_analysis_pb.BuiltinsModuleNames_Key{})
	if !rootModuleValue.IsSet() || !allBuiltinsModulesNames.IsSet() || !gotAnalysisEncoders || !gotStarlarkValueEncoders {
		return PatchedBuildResultValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	analysisReaders := &analysisEncoders.Readers

	rootModuleName := rootModuleValue.Message.GetRootModuleName()
	rootModule, err := label.NewModule(rootModuleName)
	if err != nil {
		return PatchedBuildResultValue[TMetadata]{}, fmt.Errorf("invalid root module name %#v: %w", rootModuleName, err)
	}
	rootRepo := rootModule.ToModuleInstance(nil).GetBareCanonicalRepo()
	rootPackage := rootRepo.GetRootPackage()

	thread, err := c.newStarlarkThread(ctx, e, starlarkValueEncoders, allBuiltinsModulesNames.Message.GetBuiltinsModuleNames())
	if err != nil {
		return PatchedBuildResultValue[TMetadata]{}, err
	}
	missingDependencies := false
	providedOutputGroups := map[string]struct{}{}

	// Determine which aspects to apply to the targets that are
	// built, as provided through --aspects.
	var topLevelAspects model_core.Message[[]*model_starlark_pb.AppliedAspect, TReference]
	if patchedTopLevelAspects, err := c.getTopLevelAspects(ctx, e, thread, analysisEncoders, starlarkValueEncoders, rootRepo, key); err == nil {
		topLevelAspects = model_core.Unpatch(e, patchedTopLevelAspects).Decay()
	} else {
		if !errors.Is(err, evaluation.ErrMissingDependency) {
			return PatchedBuildResultValue[TMetadata]{}, err
		}
		missingDependencies = true
	}

	// Determine which targets to build. The query is only expanded
	// after creating the configurations, so that the dependencies
	// of both are requested together.
	canonicalQuery, err := parseRootModuleQuery(e, key.GetQuery())
	gotCanonicalQuery := err == nil
	if !gotCanonicalQuery {
		if !errors.Is(err, evaluation.ErrMissingDependency) {
			return PatchedBuildResultValue[TMetadata]{}, err
		}
		missingDependencies = true
	}

	for i, configuration := range key.GetConfigurations() {
		targetPlatformConfigurationReference, err := c.createInitialConfiguration(ctx, e, analysisEncoders, starlarkValueEncoders, thread, rootPackage, configuration)
		if err != nil {
			if !errors.Is(err, evaluation.ErrMissingDependency) {
				return PatchedBuildResultValue[TMetadata]{}, fmt.Errorf("failed to create initial configuration for configuration at index %d: %w", i, err)
			}
			missingDependencies = true
			continue
		}
		clonedConfigurationReference := model_core.Unpatch(
			e,
			targetPlatformConfigurationReference,
		).Decay()

		if !gotCanonicalQuery {
			continue
		}
		var iterErr error
		for canonicalTargetLabel := range c.allQueryLabels(
			ctx,
			e,
			analysisReaders,
			canonicalQuery,
			&iterErr,
		) {
			visibleTargetValue := e.GetVisibleTargetValue(
				model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.VisibleTarget_Key {
					return model_analysis_pb.VisibleTarget_Key_builder{
						FromPackage:            canonicalTargetLabel.GetCanonicalPackage().String(),
						ToLabel:                canonicalTargetLabel.String(),
						ConfigurationReference: model_core.Patch(e, clonedConfigurationReference).Merge(patcher),
					}.Build()
				}),
			)
			if !visibleTargetValue.IsSet() {
				missingDependencies = true
				continue
			}

			targetCompletionValue := e.GetTargetCompletionValue(
				model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.TargetCompletion_Key {
					return model_analysis_pb.TargetCompletion_Key_builder{
						Label: visibleTargetValue.Message.GetLabel(),
						Variant: newTargetVariant(
							model_core.Patch(e, clonedConfigurationReference).Merge(patcher),
							/* aspects = */ nil,
						),
						Outputs: key.GetOutputs(),
					}.Build()
				}),
			)
			if targetCompletionValue.IsSet() {
				for _, outputGroup := range targetCompletionValue.Message.GetProvidedOutputGroups() {
					providedOutputGroups[outputGroup] = struct{}{}
				}
			} else {
				missingDependencies = true
			}

			// Build the outputs of the aspects that are applied
			// to the target. Each aspect is applied to the target
			// after the aspects preceding it.
			for i := range topLevelAspects.Message {
				aspectCompletionValue := e.GetTargetCompletionValue(
					model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.TargetCompletion_Key {
						return model_analysis_pb.TargetCompletion_Key_builder{
							Label: visibleTargetValue.Message.GetLabel(),
							Variant: newTargetVariant(
								model_core.Patch(e, clonedConfigurationReference).Merge(patcher),
								model_core.PatchList(e, model_core.Nested(topLevelAspects, topLevelAspects.Message[:i+1])).Merge(patcher),
							),
							Outputs: key.GetOutputs(),
						}.Build()
					}),
				)
				if aspectCompletionValue.IsSet() {
					for _, outputGroup := range aspectCompletionValue.Message.GetProvidedOutputGroups() {
						providedOutputGroups[outputGroup] = struct{}{}
					}
				} else {
					missingDependencies = true
				}
			}
		}
		if iterErr != nil {
			if !errors.Is(iterErr, evaluation.ErrMissingDependency) {
				return PatchedBuildResultValue[TMetadata]{}, fmt.Errorf("failed to expand query: %w", iterErr)
			}
			missingDependencies = true
		}
	}
	if missingDependencies {
		return PatchedBuildResultValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	// Like Bazel, optionally fail if output groups were requested
	// explicitly that are not provided by any of the targets or
	// aspects.
	var unknownOutputGroups []string
	for _, outputGroup := range key.GetRequiredOutputGroups() {
		if _, ok := providedOutputGroups[outputGroup]; !ok {
			unknownOutputGroups = append(unknownOutputGroups, fmt.Sprintf("%#v", outputGroup))
		}
	}
	if len(unknownOutputGroups) > 0 {
		return PatchedBuildResultValue[TMetadata]{}, fmt.Errorf("requested output groups %s are not provided by any of the top-level targets or aspects", strings.Join(unknownOutputGroups, ", "))
	}

	return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.BuildResult_Value{}), nil
}
