package analysis

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
		model_core.Nested(depset, depset.Message.GetElements()),
		map[model_core.Decodable[object.LocalReference]]struct{}{},
		&errIter,
	) {
		elementFile := element.Message.GetFile()
		if elementFile == nil {
			return errors.New("depset contains an element that is not a File")
		}

		patchedFile := model_core.Patch(e, model_core.Nested(element, elementFile))
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

// findProviderInstance looks up the instance of a provider in a list of
// provider instances that is sorted by provider identifier.
func findProviderInstance[TReference any](
	providerInstances model_core.Message[[]*model_starlark_pb.Struct, TReference],
	providerIdentifier string,
) (model_core.Message[*model_starlark_pb.Struct, TReference], bool) {
	index, ok := sort.Find(
		len(providerInstances.Message),
		func(i int) int {
			return strings.Compare(providerIdentifier, providerInstances.Message[i].GetProviderInstanceProperties().GetProviderIdentifier())
		},
	)
	if !ok {
		return model_core.Message[*model_starlark_pb.Struct, TReference]{}, false
	}
	return model_core.Nested(providerInstances, providerInstances.Message[index]), true
}

// buildSelectedOutputs builds the selected outputs of a target or
// aspect: the files contained in DefaultInfo.files, and the files
// contained in the fields of OutputGroupInfo. The fields of these
// providers are only needed if the corresponding outputs are selected.
// Providers that are not set and output groups that are not provided
// are ignored. The names of the output groups that are provided are
// returned.
func buildSelectedOutputs[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	ctx context.Context,
	e TargetCompletionEnvironment[TReference, TMetadata],
	valueReaders *model_starlark.ValueReaders[TReference],
	outputs *model_analysis_pb.OutputSelection,
	defaultInfoFields model_core.Message[*model_starlark_pb.Struct_Fields, TReference],
	outputGroupInfoFields model_core.Message[*model_starlark_pb.Struct_Fields, TReference],
) ([]string, error) {
	missingDependencies := false
	buildField := func(fields model_core.Message[*model_starlark_pb.Struct_Fields, TReference], fieldName, description string) (bool, error) {
		if fields.Message == nil {
			return false, nil
		}
		if _, ok := slices.BinarySearch(fields.Message.GetKeys(), fieldName); !ok {
			return false, nil
		}
		value, err := model_starlark.GetStructFieldValue(ctx, valueReaders.List, fields, fieldName)
		if err != nil {
			return false, fmt.Errorf("%s: %w", description, err)
		}
		depset := value.Message.GetDepset()
		if depset == nil {
			return false, fmt.Errorf("%s is not a depset", description)
		}
		if err := buildFilesInDepset(ctx, e, valueReaders, model_core.Nested(value, depset), &missingDependencies); err != nil {
			return false, fmt.Errorf("%s: %w", description, err)
		}
		return true, nil
	}

	if outputs.GetDefaultInfoFiles() {
		if _, err := buildField(defaultInfoFields, "files", "field \"files\" of DefaultInfo"); err != nil {
			return nil, err
		}
	}
	var providedOutputGroups []string
	for _, outputGroup := range outputs.GetOutputGroups() {
		provided, err := buildField(outputGroupInfoFields, outputGroup, fmt.Sprintf("output group %#v", outputGroup))
		if err != nil {
			return nil, err
		}
		if provided {
			providedOutputGroups = append(providedOutputGroups, outputGroup)
		}
	}
	if missingDependencies {
		return nil, evaluation.ErrMissingDependency
	}
	return providedOutputGroups, nil
}

func (*baseComputer[TReference, TMetadata]) ComputeTargetCompletionValue(ctx context.Context, key model_core.Message[*model_analysis_pb.TargetCompletion_Key, TReference], e TargetCompletionEnvironment[TReference, TMetadata]) (PatchedTargetCompletionValue[TMetadata], error) {
	valueReaders, gotValueReaders := getStarlarkValueReaders(e)

	// Obtain the fields of the providers of the target, or of the
	// last aspect applied to it, that are needed to build the
	// selected outputs.
	outputs := key.Message.GetOutputs()
	var defaultInfoFields, outputGroupInfoFields model_core.Message[*model_starlark_pb.Struct_Fields, TReference]
	if aspects := key.Message.GetVariant().GetAspects(); len(aspects) == 0 {
		// Only request the providers that are needed, so that
		// changes to other providers of the target don't cause
		// this computation to be invalidated.
		missingDependencies := !gotValueReaders
		getProviderFields := func(providerIdentifier string) model_core.Message[*model_starlark_pb.Struct_Fields, TReference] {
			targetProvider := e.GetTargetProviderValue(
				model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.TargetProvider_Key {
					return &model_analysis_pb.TargetProvider_Key{
						Label:              key.Message.GetLabel(),
						Variant:            model_core.Patch(e, model_core.Nested(key, key.Message.GetVariant())).Merge(patcher),
						ProviderIdentifier: providerIdentifier,
					}
				}),
			)
			if !targetProvider.IsSet() {
				missingDependencies = true
				return model_core.Message[*model_starlark_pb.Struct_Fields, TReference]{}
			}
			return model_core.Nested(targetProvider, targetProvider.Message.GetFields())
		}
		if outputs.GetDefaultInfoFiles() {
			defaultInfoFields = getProviderFields(defaultInfoProviderIdentifier.String())
		}
		if len(outputs.GetOutputGroups()) > 0 {
			outputGroupInfoFields = getProviderFields(outputGroupInfoProviderIdentifier.String())
		}
		if missingDependencies {
			return PatchedTargetCompletionValue[TMetadata]{}, evaluation.ErrMissingDependency
		}
	} else {
		// Aspects are applied in the configuration yielded by
		// the target's incoming edge transition. Obtain the
		// providers of the last aspect only, as opposed to the
		// merged providers of the target and all aspects.
		incomingEdgeTransition := e.GetTargetIncomingEdgeTransitionValue(
			model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.TargetIncomingEdgeTransition_Key {
				return &model_analysis_pb.TargetIncomingEdgeTransition_Key{
					Label:                  key.Message.GetLabel(),
					ConfigurationReference: model_core.Patch(e, model_core.Nested(key, key.Message.GetVariant().GetConfigurationReference())).Merge(patcher),
				}
			}),
		)
		if !incomingEdgeTransition.IsSet() || !gotValueReaders {
			return PatchedTargetCompletionValue[TMetadata]{}, evaluation.ErrMissingDependency
		}
		configuredTarget := e.GetConfiguredTargetValue(
			model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_analysis_pb.ConfiguredTarget_Key {
				return &model_analysis_pb.ConfiguredTarget_Key{
					Label: key.Message.GetLabel(),
					Variant: newTargetVariant(
						model_core.Patch(e, model_core.Nested(incomingEdgeTransition, incomingEdgeTransition.Message.GetOutputConfigurationReference())).Merge(patcher),
						model_core.PatchList(e, model_core.Nested(key, aspects)).Merge(patcher),
					),
				}
			}),
		)
		if !configuredTarget.IsSet() {
			return PatchedTargetCompletionValue[TMetadata]{}, evaluation.ErrMissingDependency
		}

		// Aspects cannot return DefaultInfo, so only consider
		// OutputGroupInfo.
		providerInstances := model_core.Nested(configuredTarget, configuredTarget.Message.GetProviderInstances())
		if outputGroupInfo, ok := findProviderInstance(providerInstances, outputGroupInfoProviderIdentifier.String()); ok {
			outputGroupInfoFields = model_core.Nested(outputGroupInfo, outputGroupInfo.Message.GetFields())
		}
	}

	providedOutputGroups, err := buildSelectedOutputs(ctx, e, valueReaders, outputs, defaultInfoFields, outputGroupInfoFields)
	if err != nil {
		return PatchedTargetCompletionValue[TMetadata]{}, err
	}
	return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.TargetCompletion_Value{
		ProvidedOutputGroups: providedOutputGroups,
	}), nil
}
