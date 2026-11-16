package analysis

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_evaluation "bonanza.build/pkg/model/evaluation"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"
)

var outputGroupInfoProviderIdentifier = util.Must(label.NewCanonicalStarlarkIdentifier("@@builtins_core+//:exports.bzl%OutputGroupInfo"))

// providerInstanceSource is a set of provider instances that need to be
// merged into the provider instances of a target. These are either the
// provider instances of the target itself, or the ones of an aspect
// applied to it.
type providerInstanceSource[TReference any] struct {
	description       string
	providerInstances model_core.Message[[]*model_starlark_pb.Struct, TReference]
}

// mergeProviderInstances merges the provider instances of a target with
// those of the aspects that are applied to it. Instances of
// OutputGroupInfo are merged by combining their fields. It is an error
// for the target and the aspects to provide instances of any other
// provider.
func mergeProviderInstances[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	ctx context.Context,
	e model_core.ObjectManager[TReference, TMetadata],
	valueReaders *model_starlark.ValueReaders[TReference],
	valueEncodingOptions *model_starlark.ValueEncodingOptions[TReference, TMetadata],
	sources []providerInstanceSource[TReference],
	patcher *model_core.ReferenceMessagePatcher[TMetadata],
) ([]*model_starlark_pb.Struct, error) {
	type providerInstance struct {
		source   *providerInstanceSource[TReference]
		instance model_core.Message[*model_starlark_pb.Struct, TReference]
	}
	var allInstances []providerInstance
	for i := range sources {
		source := &sources[i]
		for _, instance := range source.providerInstances.Message {
			allInstances = append(allInstances, providerInstance{
				source:   source,
				instance: model_core.Nested(source.providerInstances, instance),
			})
		}
	}
	getProviderIdentifier := func(instance providerInstance) string {
		return instance.instance.Message.ProviderInstanceProperties.GetProviderIdentifier()
	}
	slices.SortStableFunc(allInstances, func(a, b providerInstance) int {
		return strings.Compare(getProviderIdentifier(a), getProviderIdentifier(b))
	})

	outputGroupInfoProviderIdentifierStr := outputGroupInfoProviderIdentifier.String()
	mergedInstances := make([]*model_starlark_pb.Struct, 0, len(allInstances))
	for len(allInstances) > 0 {
		providerIdentifier := getProviderIdentifier(allInstances[0])
		groupLength := 1
		for groupLength < len(allInstances) && getProviderIdentifier(allInstances[groupLength]) == providerIdentifier {
			groupLength++
		}
		group := allInstances[:groupLength]
		allInstances = allInstances[groupLength:]

		if len(group) == 1 {
			mergedInstances = append(mergedInstances, model_core.Patch(e, group[0].instance).Merge(patcher))
			continue
		}
		if providerIdentifier != outputGroupInfoProviderIdentifierStr {
			return nil, fmt.Errorf("both %s and %s provide an instance of provider %#v", group[0].source.description, group[1].source.description, providerIdentifier)
		}

		// Merge the fields of all instances of OutputGroupInfo.
		// The values of the fields are stored in B-trees, so
		// gather all fields and construct a new struct.
		fieldSources := map[string]*providerInstanceSource[TReference]{}
		fieldValues := map[string]any{}
		for _, instance := range group {
			var errIter error
			for key, value := range model_starlark.AllStructFields(
				ctx,
				valueReaders.List,
				model_core.Nested(instance.instance, instance.instance.Message.Fields),
				&errIter,
			) {
				if existingSource, ok := fieldSources[key]; ok {
					return nil, fmt.Errorf("both %s and %s provide output group %#v", existingSource.description, instance.source.description, key)
				}
				fieldSources[key] = instance.source
				fieldValues[key] = value
			}
			if errIter != nil {
				return nil, fmt.Errorf("instance of provider %#v provided by %s: %w", providerIdentifier, instance.source.description, errIter)
			}
		}
		mergedFields, err := model_starlark.NewStructFromDict[TReference, TMetadata](nil, fieldValues).
			EncodeStructFields(eval.NewThread(ctx, nil), nil, valueEncodingOptions)
		if err != nil {
			return nil, fmt.Errorf("failed to encode merged instance of provider %#v: %w", providerIdentifier, err)
		}
		mergedInstances = append(mergedInstances, &model_starlark_pb.Struct{
			Fields: mergedFields.Merge(patcher),
			ProviderInstanceProperties: model_core.Patch(
				e,
				model_core.Nested(group[0].instance, group[0].instance.Message.ProviderInstanceProperties),
			).Merge(patcher),
		})
	}
	return mergedInstances, nil
}

func (c *baseComputer[TReference, TMetadata]) ComputeTargetProvidersValue(ctx context.Context, key model_core.Message[*model_analysis_pb.TargetProviders_Key, TReference], e TargetProvidersEnvironment[TReference, TMetadata]) (PatchedTargetProvidersValue[TMetadata], error) {
	// Targets are only analyzed in the configuration yielded by
	// their incoming edge transition. This ensures that targets that
	// are requested in different configurations that transition to
	// the same configuration are only analyzed once.
	incomingEdgeTransition := e.GetTargetIncomingEdgeTransitionValue(
		model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.TargetIncomingEdgeTransition_Key {
			return &model_analysis_pb.TargetIncomingEdgeTransition_Key{
				Label:                  key.Message.Label,
				ConfigurationReference: model_core.Patch(e, model_core.Nested(key, key.Message.ConfigurationReference)).Merge(patcher),
			}
		}),
	)
	if !incomingEdgeTransition.IsSet() {
		return PatchedTargetProvidersValue[TMetadata]{}, model_evaluation.ErrMissingDependency
	}

	configurationReference := model_core.Nested(incomingEdgeTransition, incomingEdgeTransition.Message.OutputConfigurationReference)
	configuredTarget := e.GetConfiguredTargetValue(
		model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.ConfiguredTarget_Key {
			return &model_analysis_pb.ConfiguredTarget_Key{
				Label:                  key.Message.Label,
				ConfigurationReference: model_core.Patch(e, configurationReference).Merge(patcher),
			}
		}),
	)
	if !configuredTarget.IsSet() {
		return PatchedTargetProvidersValue[TMetadata]{}, model_evaluation.ErrMissingDependency
	}

	if len(key.Message.Aspects) == 0 {
		return newTargetProvidersValueWithoutAspects(e, configuredTarget), nil
	}
	return c.computeTargetProvidersValueWithAspects(ctx, key, e, configurationReference, configuredTarget)
}

// newTargetProvidersValueWithoutAspects returns the provider instances
// of a target to which no aspects are applied. These are identical to
// the ones yielded by ConfiguredTarget.
func newTargetProvidersValueWithoutAspects[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	e model_core.ObjectManager[TReference, TMetadata],
	configuredTarget model_core.Message[*model_analysis_pb.ConfiguredTarget_Value, TReference],
) PatchedTargetProvidersValue[TMetadata] {
	return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.TargetProviders_Value {
		return &model_analysis_pb.TargetProviders_Value{
			ProviderInstances: model_core.PatchList(e, model_core.Nested(configuredTarget, configuredTarget.Message.ProviderInstances)).Merge(patcher),
			IsFileTarget:      configuredTarget.Message.IsFileTarget,
		}
	})
}

// computeTargetProvidersValueWithAspects applies aspects to a target,
// and merges the provider instances of the aspects with those of the
// target.
func (c *baseComputer[TReference, TMetadata]) computeTargetProvidersValueWithAspects(
	ctx context.Context,
	key model_core.Message[*model_analysis_pb.TargetProviders_Key, TReference],
	e TargetProvidersEnvironment[TReference, TMetadata],
	configurationReference model_core.Message[*model_core_pb.DecodableReference, TReference],
	configuredTarget model_core.Message[*model_analysis_pb.ConfiguredTarget_Value, TReference],
) (PatchedTargetProvidersValue[TMetadata], error) {
	starlarkValueEncoders, gotStarlarkValueEncoders := getStarlarkValueEncoders(e)
	if !gotStarlarkValueEncoders {
		return PatchedTargetProvidersValue[TMetadata]{}, model_evaluation.ErrMissingDependency
	}
	valueReaders := &starlarkValueEncoders.Readers

	// Apply all aspects. Each aspect sees the providers of the
	// aspects preceding it, meaning that aspects are applied to
	// every prefix of the list. Aspects are applied in the same
	// configuration as the target itself.
	aspects := key.Message.Aspects
	sources := make([]providerInstanceSource[TReference], 0, 1+len(aspects))
	sources = append(sources, providerInstanceSource[TReference]{
		description:       fmt.Sprintf("target %#v", key.Message.Label),
		providerInstances: model_core.Nested(configuredTarget, configuredTarget.Message.ProviderInstances),
	})
	missingDependencies := false
	for i, aspect := range aspects {
		aspectConfiguredTarget := e.GetConfiguredTargetValue(
			model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.ConfiguredTarget_Key {
				return &model_analysis_pb.ConfiguredTarget_Key{
					Label:                  key.Message.Label,
					ConfigurationReference: model_core.Patch(e, configurationReference).Merge(patcher),
					Aspects:                model_core.PatchList(e, model_core.Nested(key, aspects[:i+1])).Merge(patcher),
				}
			}),
		)
		if !aspectConfiguredTarget.IsSet() {
			missingDependencies = true
			continue
		}
		sources = append(sources, providerInstanceSource[TReference]{
			description:       fmt.Sprintf("aspect %#v", aspect.Identifier),
			providerInstances: model_core.Nested(aspectConfiguredTarget, aspectConfiguredTarget.Message.ProviderInstances),
		})
	}
	if missingDependencies {
		return PatchedTargetProvidersValue[TMetadata]{}, model_evaluation.ErrMissingDependency
	}

	return model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (*model_analysis_pb.TargetProviders_Value, error) {
		providerInstances, err := mergeProviderInstances(
			ctx,
			e,
			valueReaders,
			&model_starlark.ValueEncodingOptions[TReference, TMetadata]{
				Context:                  ctx,
				ObjectEncoder:            starlarkValueEncoders.Encoder,
				ObjectReferenceFormat:    c.referenceFormat,
				ObjectCapturer:           e,
				ObjectChunkingParameters: c.chunkingParameters,
			},
			sources,
			patcher,
		)
		if err != nil {
			return nil, err
		}
		return &model_analysis_pb.TargetProviders_Value{
			ProviderInstances: providerInstances,
			IsFileTarget:      configuredTarget.Message.IsFileTarget,
		}, nil
	})
}

// TargetProvidersEnvironmentForTesting is used to generate mocks for
// testing ComputeTargetProvidersValue().
type TargetProvidersEnvironmentForTesting TargetProvidersEnvironment[model_core.CreatedObjectTree, model_core.CreatedObjectTree]
