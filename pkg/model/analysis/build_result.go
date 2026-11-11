package analysis

import (
	"context"
	"errors"
	"fmt"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
)

func (c *baseComputer[TReference, TMetadata]) ComputeBuildResultValue(ctx context.Context, key *model_analysis_pb.BuildResult_Key, e BuildResultEnvironment[TReference, TMetadata]) (PatchedBuildResultValue[TMetadata], error) {
	starlarkValueEncoders, gotStarlarkValueEncoders := getStarlarkValueEncoders(e)

	buildSpecificationMessage := e.GetBuildSpecificationValue(&model_analysis_pb.BuildSpecification_Key{})
	if !buildSpecificationMessage.IsSet() || !gotStarlarkValueEncoders {
		return PatchedBuildResultValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	buildSpecification := buildSpecificationMessage.Message

	rootModuleName := buildSpecification.RootModuleName
	rootModule, err := label.NewModule(rootModuleName)
	if err != nil {
		return PatchedBuildResultValue[TMetadata]{}, fmt.Errorf("invalid root module name %#v: %w", rootModuleName, err)
	}
	rootRepo := rootModule.ToModuleInstance(nil).GetBareCanonicalRepo()
	rootPackage := rootRepo.GetRootPackage()

	thread, err := c.newStarlarkThread(ctx, e, starlarkValueEncoders, buildSpecification.BuiltinsModuleNames)
	if err != nil {
		return PatchedBuildResultValue[TMetadata]{}, err
	}
	missingDependencies := false

	// Determine which targets to build. The query is only expanded
	// after creating the configurations, so that the dependencies
	// of both are requested together.
	canonicalQuery, err := parseRootModuleQuery(e, key.Query)
	gotCanonicalQuery := err == nil
	if !gotCanonicalQuery {
		if !errors.Is(err, evaluation.ErrMissingDependency) {
			return PatchedBuildResultValue[TMetadata]{}, err
		}
		missingDependencies = true
	}

	for i, configuration := range key.Configurations {
		targetPlatformConfigurationReference, err := c.createInitialConfiguration(ctx, e, thread, rootPackage, configuration)
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
			canonicalQuery,
			&iterErr,
		) {
			visibleTargetValue := e.GetVisibleTargetValue(
				model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.VisibleTarget_Key {
					return &model_analysis_pb.VisibleTarget_Key{
						FromPackage:            canonicalTargetLabel.GetCanonicalPackage().String(),
						ToLabel:                canonicalTargetLabel.String(),
						ConfigurationReference: model_core.Patch(e, clonedConfigurationReference).Merge(patcher),
					}
				}),
			)
			if !visibleTargetValue.IsSet() {
				missingDependencies = true
				continue
			}

			targetCompletionValue := e.GetTargetCompletionValue(
				model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.TargetCompletion_Key {
					return &model_analysis_pb.TargetCompletion_Key{
						Label:                  visibleTargetValue.Message.Label,
						ConfigurationReference: model_core.Patch(e, clonedConfigurationReference).Merge(patcher),
					}
				}),
			)
			if !targetCompletionValue.IsSet() {
				missingDependencies = true
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

	return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.BuildResult_Value{}), nil
}
