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

func (c *baseComputer[TReference, TMetadata]) ComputeTargetOutputValue(ctx context.Context, key model_core.Message[*model_analysis_pb.TargetOutput_Key, TReference], e TargetOutputEnvironment[TReference, TMetadata]) (PatchedTargetOutputValue[TMetadata], error) {
	patchedConfigurationReference := model_core.Patch(e, model_core.Nested(key, key.Message.ConfigurationReference))
	configuredTarget := e.GetConfiguredTargetValue(
		model_core.NewPatchedMessage(
			&model_analysis_pb.ConfiguredTarget_Key{
				Label:                  key.Message.Label,
				ConfigurationReference: patchedConfigurationReference.Message,
			},
			patchedConfigurationReference.Patcher,
		),
	)
	if !configuredTarget.IsSet() {
		return PatchedTargetOutputValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	packageRelativePath := key.Message.PackageRelativePath
	output, err := btree.Find(
		ctx,
		c.configuredTargetOutputReader,
		model_core.Nested(configuredTarget, configuredTarget.Message.Outputs),
		func(entry model_core.Message[*model_analysis_pb.ConfiguredTarget_Value_Output, TReference]) (int, *model_core_pb.DecodableReference) {
			switch level := entry.Message.Level.(type) {
			case *model_analysis_pb.ConfiguredTarget_Value_Output_Leaf_:
				return strings.Compare(packageRelativePath, level.Leaf.PackageRelativePath), nil
			case *model_analysis_pb.ConfiguredTarget_Value_Output_Parent_:
				return strings.Compare(packageRelativePath, level.Parent.FirstPackageRelativePath), level.Parent.Reference
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
	outputLevel, ok := output.Message.Level.(*model_analysis_pb.ConfiguredTarget_Value_Output_Leaf_)
	if !ok {
		return PatchedTargetOutputValue[TMetadata]{}, errors.New("output is not a leaf")
	}

	patchedDefinition := model_core.Patch(e, model_core.Nested(output, outputLevel.Leaf.Definition))
	return model_core.NewPatchedMessage(
		&model_analysis_pb.TargetOutput_Value{
			Definition: patchedDefinition.Message,
		},
		patchedDefinition.Patcher,
	), nil
}
