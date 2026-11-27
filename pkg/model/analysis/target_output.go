package analysis

import (
	"context"
	"errors"
	"strings"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	"bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_core_pb "bonanza.build/pkg/proto/model/core"
)

func (*baseComputer[TReference, TMetadata]) ComputeTargetOutputValue(ctx context.Context, key model_core.Message[*model_analysis_pb.TargetOutput_Key, TReference], e TargetOutputEnvironment[TReference, TMetadata]) (PatchedTargetOutputValue[TMetadata], error) {
	analysisReaders, gotAnalysisReaders := getAnalysisReaders(e)

	patchedVariant := model_core.Patch(e, model_core.Nested(key, key.Message.GetVariant()))
	configuredTarget := e.GetConfiguredTargetValue(
		model_core.NewPatchedMessage(
			model_analysis_pb.ConfiguredTarget_Key_builder{
				Label:   key.Message.GetLabel(),
				Variant: patchedVariant.Message,
			}.Build(),
			patchedVariant.Patcher,
		),
	)
	if !configuredTarget.IsSet() || !gotAnalysisReaders {
		return PatchedTargetOutputValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	packageRelativePath := key.Message.GetPackageRelativePath()
	output, err := btree.Find(
		ctx,
		analysisReaders.ConfiguredTargetOutput,
		model_core.Nested(configuredTarget, configuredTarget.Message.GetOutputs()),
		func(entry model_core.Message[*model_analysis_pb.ConfiguredTarget_Value_Output, TReference]) (int, *model_core_pb.DecodableReference) {
			switch entry.Message.WhichLevel() {
			case model_analysis_pb.ConfiguredTarget_Value_Output_Leaf_case:
				return strings.Compare(packageRelativePath, entry.Message.GetLeaf().GetPackageRelativePath()), nil
			case model_analysis_pb.ConfiguredTarget_Value_Output_Parent_case:
				return strings.Compare(packageRelativePath, entry.Message.GetParent().GetFirstPackageRelativePath()), entry.Message.GetParent().GetReference()
			default:
				return 0, nil
			}
		},
	)
	if err != nil {
		return PatchedTargetOutputValue[TMetadata]{}, err
	}
	if !output.IsSet() {
		return PatchedTargetOutputValue[TMetadata]{}, errors.New("target does not yield an output with the provided package relative path")
	}
	outputLevel := output.Message.GetLeaf()
	if outputLevel == nil {
		return PatchedTargetOutputValue[TMetadata]{}, errors.New("output is not a leaf")
	}

	patchedDefinition := model_core.Patch(e, model_core.Nested(output, outputLevel.GetDefinition()))
	return model_core.NewPatchedMessage(
		model_analysis_pb.TargetOutput_Value_builder{
			Definition: patchedDefinition.Message,
		}.Build(),
		patchedDefinition.Patcher,
	), nil
}
