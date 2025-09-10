package analysis

import (
	"context"
	"errors"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/storage/object"
)

func (c *baseComputer[TReference, TMetadata]) ComputeTargetCompletionValue(ctx context.Context, key model_core.Message[*model_analysis_pb.TargetCompletion_Key, TReference], e TargetCompletionEnvironment[TReference, TMetadata]) (PatchedTargetCompletionValue[TMetadata], error) {
	// TODO: This should also respect --output_groups.
	defaultInfo, err := getProviderFromConfiguredTarget(
		e,
		key.Message.Label,
		model_core.Patch(e, model_core.Nested(key, key.Message.ConfigurationReference)),
		defaultInfoProviderIdentifier,
	)
	if err != nil {
		return PatchedTargetCompletionValue[TMetadata]{}, err
	}

	files, err := model_starlark.GetStructFieldValue(ctx, c.valueReaders.List, defaultInfo, "files")
	if err != nil {
		return PatchedTargetCompletionValue[TMetadata]{}, err
	}
	filesDepset, ok := files.Message.Kind.(*model_starlark_pb.Value_Depset)
	if !ok {
		return PatchedTargetCompletionValue[TMetadata]{}, errors.New("\"files\" field of DefaultInfo provider is not a depset")
	}

	var errIter error
	missingDependencies := false
	for element := range model_starlark.AllListLeafElementsSkippingDuplicateParents(
		ctx,
		c.valueReaders.List,
		model_core.Nested(files, filesDepset.Depset.Elements),
		map[model_core.Decodable[object.LocalReference]]struct{}{},
		&errIter,
	) {
		elementFile, ok := element.Message.Kind.(*model_starlark_pb.Value_File)
		if !ok {
			return PatchedTargetCompletionValue[TMetadata]{}, errors.New("\"files\" field of DefaultInfo provider contains an element that is not a File")
		}

		patchedFile := model_core.Patch(e, model_core.Nested(element, elementFile.File))
		targetOutput := e.GetFileRootValue(
			model_core.NewPatchedMessage(
				&model_analysis_pb.FileRoot_Key{
					File:            patchedFile.Message,
					DirectoryLayout: model_analysis_pb.DirectoryLayout_INPUT_ROOT,
				},
				patchedFile.Patcher,
			),
		)
		if !targetOutput.IsSet() {
			missingDependencies = true
			continue
		}
	}
	if missingDependencies {
		return PatchedTargetCompletionValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.TargetCompletion_Value{}), nil
}
