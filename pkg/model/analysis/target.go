package analysis

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"bonanza.build/pkg/evaluation"
	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
)

func (c *baseComputer[TReference, TMetadata]) lookupTargetDefinitionInTargetList(ctx context.Context, targetList model_core.Message[[]*model_analysis_pb.Package_Value_Target, TReference], targetName label.TargetName) (model_core.Message[*model_starlark_pb.Target_Definition, TReference], error) {
	targetNameStr := targetName.String()
	target, err := btree.Find(
		ctx,
		c.packageValueTargetReader,
		targetList,
		func(entry model_core.Message[*model_analysis_pb.Package_Value_Target, TReference]) (int, *model_core_pb.DecodableReference) {
			switch level := entry.Message.Level.(type) {
			case *model_analysis_pb.Package_Value_Target_Leaf:
				return strings.Compare(targetNameStr, level.Leaf.Name), nil
			case *model_analysis_pb.Package_Value_Target_Parent_:
				return strings.Compare(targetNameStr, level.Parent.FirstName), level.Parent.Reference
			default:
				return 0, nil
			}
		},
	)
	if err != nil {
		return model_core.Message[*model_starlark_pb.Target_Definition, TReference]{}, err
	}
	if !target.IsSet() {
		return model_core.Message[*model_starlark_pb.Target_Definition, TReference]{}, nil
	}

	level, ok := target.Message.Level.(*model_analysis_pb.Package_Value_Target_Leaf)
	if !ok {
		return model_core.Message[*model_starlark_pb.Target_Definition, TReference]{}, errors.New("target list has an unknown level type")
	}
	definition := level.Leaf.Definition
	if definition == nil {
		return model_core.Message[*model_starlark_pb.Target_Definition, TReference]{}, errors.New("target does not have a definition")
	}
	return model_core.Nested(target, definition), nil
}

func (c *baseComputer[TReference, TMetadata]) ComputeTargetValue(ctx context.Context, key *model_analysis_pb.Target_Key, e TargetEnvironment[TReference, TMetadata]) (PatchedTargetValue[TMetadata], error) {
	targetLabel, err := label.NewCanonicalLabel(key.Label)
	if err != nil {
		return PatchedTargetValue[TMetadata]{}, fmt.Errorf("invalid target label: %w", err)
	}
	packageValue := e.GetPackageValue(&model_analysis_pb.Package_Key{
		Label: targetLabel.GetCanonicalPackage().String(),
	})
	if !packageValue.IsSet() {
		return PatchedTargetValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	definition, err := c.lookupTargetDefinitionInTargetList(
		ctx,
		model_core.Nested(packageValue, packageValue.Message.Targets),
		targetLabel.GetTargetName(),
	)
	if err != nil {
		return PatchedTargetValue[TMetadata]{}, err
	}
	if !definition.IsSet() {
		return PatchedTargetValue[TMetadata]{}, errors.New("target does not exist")
	}

	patchedDefinition := model_core.Patch(e, definition)
	return model_core.NewPatchedMessage(
		&model_analysis_pb.Target_Value{
			Definition: patchedDefinition.Message,
		},
		patchedDefinition.Patcher,
	), nil
}
