package analysis

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
)

func (baseComputer[TReference, TMetadata]) ComputeUsedModuleExtensionValue(ctx context.Context, key *model_analysis_pb.UsedModuleExtension_Key, e UsedModuleExtensionEnvironment[TReference, TMetadata]) (PatchedUsedModuleExtensionValue[TMetadata], error) {
	usedModuleExtensions := e.GetUsedModuleExtensionsValue(&model_analysis_pb.UsedModuleExtensions_Key{})
	if !usedModuleExtensions.IsSet() {
		return PatchedUsedModuleExtensionValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	extensions := usedModuleExtensions.Message.GetModuleExtensions()
	if i := sort.Search(
		len(extensions),
		func(i int) bool {
			identifier, err := label.NewCanonicalStarlarkIdentifier(extensions[i].GetIdentifier())
			return err == nil && identifier.ToModuleExtension().String() >= key.GetModuleExtension()
		},
	); i < len(extensions) {
		extension := extensions[i]
		identifier, err := label.NewCanonicalStarlarkIdentifier(extension.GetIdentifier())
		if err != nil {
			return PatchedUsedModuleExtensionValue[TMetadata]{}, fmt.Errorf("invalid module extensions Starlark identifier %#v: %w", extension.GetIdentifier(), err)
		}
		if identifier.ToModuleExtension().String() == key.GetModuleExtension() {
			patchedExtension := model_core.Patch(e, model_core.Nested(usedModuleExtensions, extension))
			return model_core.NewPatchedMessage(
				model_analysis_pb.UsedModuleExtension_Value_builder{
					ModuleExtension: patchedExtension.Message,
				}.Build(),
				patchedExtension.Patcher,
			), nil
		}
	}
	return PatchedUsedModuleExtensionValue[TMetadata]{}, errors.New("module extension not found")
}
