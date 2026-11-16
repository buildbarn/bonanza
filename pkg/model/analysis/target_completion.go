package analysis

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/storage/object"
)

// buildFilesInDepset requests the file roots of all files contained in
// a depset, causing them to be built. If any of the files have not been
// built yet, missingDependencies is set.
func buildFilesInDepset[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	ctx context.Context,
	e TargetCompletionEnvironment[TReference, TMetadata],
	valueReaders *model_starlark.ValueReaders[TReference],
	depset model_core.Message[*model_starlark_pb.Depset, TReference],
	missingDependencies *bool,
) error {
	var errIter error
	for element := range model_starlark.AllListLeafElementsSkippingDuplicateParents(
		ctx,
		valueReaders.List,
		model_core.Nested(depset, depset.Message.Elements),
		map[model_core.Decodable[object.LocalReference]]struct{}{},
		&errIter,
	) {
		elementFile, ok := element.Message.Kind.(*model_starlark_pb.Value_File)
		if !ok {
			return errors.New("depset contains an element that is not a File")
		}

		patchedFile := model_core.Patch(e, model_core.Nested(element, elementFile.File))
		fileRoot := e.GetFileRootValue(
			model_core.NewPatchedMessage(
				&model_analysis_pb.FileRoot_Key{
					File:            patchedFile.Message,
					DirectoryLayout: model_analysis_pb.DirectoryLayout_INPUT_ROOT,
				},
				patchedFile.Patcher,
			),
		)
		if !fileRoot.IsSet() {
			*missingDependencies = true
		}
	}
	return errIter
}

func (c *baseComputer[TReference, TMetadata]) ComputeTargetCompletionValue(ctx context.Context, key model_core.Message[*model_analysis_pb.TargetCompletion_Key, TReference], e TargetCompletionEnvironment[TReference, TMetadata]) (PatchedTargetCompletionValue[TMetadata], error) {
	if len(key.Message.Variant.GetAspects()) > 0 {
		return c.computeAspectTargetCompletionValue(ctx, key, e)
	}

	valueReaders, gotValueReaders := getStarlarkValueReaders(e)

	// TODO: This should also respect --output_groups.
	defaultInfo, err := getProviderFromConfiguredTarget(
		e,
		key.Message.Label,
		model_core.Patch(e, model_core.Nested(key, key.Message.Variant.GetConfigurationReference())),
		defaultInfoProviderIdentifier,
	)
	if err != nil {
		return PatchedTargetCompletionValue[TMetadata]{}, err
	}

	if !gotValueReaders {
		return PatchedTargetCompletionValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	files, err := model_starlark.GetStructFieldValue(ctx, valueReaders.List, defaultInfo, "files")
	if err != nil {
		return PatchedTargetCompletionValue[TMetadata]{}, err
	}
	filesDepset, ok := files.Message.Kind.(*model_starlark_pb.Value_Depset)
	if !ok {
		return PatchedTargetCompletionValue[TMetadata]{}, errors.New("\"files\" field of DefaultInfo provider is not a depset")
	}

	missingDependencies := false
	if err := buildFilesInDepset(ctx, e, valueReaders, model_core.Nested(files, filesDepset.Depset), &missingDependencies); err != nil {
		return PatchedTargetCompletionValue[TMetadata]{}, fmt.Errorf("\"files\" field of DefaultInfo provider: %w", err)
	}
	if missingDependencies {
		return PatchedTargetCompletionValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.TargetCompletion_Value{}), nil
}

// computeAspectTargetCompletionValue builds the outputs of the last
// aspect in the list of aspects provided in the key, applied to the
// target. As aspects cannot return DefaultInfo, these are the outputs
// contained in the aspect's instance of OutputGroupInfo.
//
// TODO: Only build the output groups requested through --output_groups.
// Until that is supported, all output groups are built.
func (*baseComputer[TReference, TMetadata]) computeAspectTargetCompletionValue(ctx context.Context, key model_core.Message[*model_analysis_pb.TargetCompletion_Key, TReference], e TargetCompletionEnvironment[TReference, TMetadata]) (PatchedTargetCompletionValue[TMetadata], error) {
	valueReaders, gotValueReaders := getStarlarkValueReaders(e)

	// Aspects are applied in the configuration yielded by the
	// target's incoming edge transition. Obtain the providers of
	// the last aspect only, as opposed to the merged providers of
	// the target and all aspects.
	incomingEdgeTransition := e.GetTargetIncomingEdgeTransitionValue(
		model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.TargetIncomingEdgeTransition_Key {
			return &model_analysis_pb.TargetIncomingEdgeTransition_Key{
				Label:                  key.Message.Label,
				ConfigurationReference: model_core.Patch(e, model_core.Nested(key, key.Message.Variant.GetConfigurationReference())).Merge(patcher),
			}
		}),
	)
	if !incomingEdgeTransition.IsSet() || !gotValueReaders {
		return PatchedTargetCompletionValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	configuredTarget := e.GetConfiguredTargetValue(
		model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.ConfiguredTarget_Key {
			return &model_analysis_pb.ConfiguredTarget_Key{
				Label: key.Message.Label,
				Variant: newTargetVariant(
					model_core.Patch(e, model_core.Nested(incomingEdgeTransition, incomingEdgeTransition.Message.OutputConfigurationReference)).Merge(patcher),
					model_core.PatchList(e, model_core.Nested(key, key.Message.Variant.GetAspects())).Merge(patcher),
				),
			}
		}),
	)
	if !configuredTarget.IsSet() {
		return PatchedTargetCompletionValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	providerInstances := configuredTarget.Message.ProviderInstances
	outputGroupInfoProviderIdentifierStr := outputGroupInfoProviderIdentifier.String()
	outputGroupInfoIndex, ok := sort.Find(
		len(providerInstances),
		func(i int) int {
			return strings.Compare(outputGroupInfoProviderIdentifierStr, providerInstances[i].ProviderInstanceProperties.GetProviderIdentifier())
		},
	)
	if !ok {
		// Aspect did not yield any output groups.
		return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.TargetCompletion_Value{}), nil
	}

	missingDependencies := false
	var errIter error
	for outputGroupName, outputGroup := range model_starlark.AllStructFields(
		ctx,
		valueReaders.List,
		model_core.Nested(configuredTarget, providerInstances[outputGroupInfoIndex].Fields),
		&errIter,
	) {
		outputGroupDepset, ok := outputGroup.Message.Kind.(*model_starlark_pb.Value_Depset)
		if !ok {
			return PatchedTargetCompletionValue[TMetadata]{}, fmt.Errorf("output group %#v is not a depset", outputGroupName)
		}
		if err := buildFilesInDepset(ctx, e, valueReaders, model_core.Nested(outputGroup, outputGroupDepset.Depset), &missingDependencies); err != nil {
			return PatchedTargetCompletionValue[TMetadata]{}, fmt.Errorf("output group %#v: %w", outputGroupName, err)
		}
	}
	if errIter != nil {
		return PatchedTargetCompletionValue[TMetadata]{}, errIter
	}
	if missingDependencies {
		return PatchedTargetCompletionValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.TargetCompletion_Value{}), nil
}
