package analysis

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	"bonanza.build/pkg/model/evaluation"
	model_starlark "bonanza.build/pkg/model/starlark"
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
	canonicalPackage := targetLabel.GetCanonicalPackage().String()
	targetName := targetLabel.GetTargetName()
	targetNameStr := targetName.String()

	// Look up the target, either at the top level of the package or
	// within one of the macro instances in the package.
	//
	// Callers of macros may declare targets in the namespace of the
	// macro instance. The macro instance may then not declare
	// targets having the same name. This means that the macro
	// instance only needs to be expanded if the caller does not
	// declare the target.
	//
	// The namespaces of the macro expansion contexts yielded by a
	// package or macro instance are disjoint and contiguous. The
	// only macro instance whose namespace may contain the target's
	// name is therefore the one whose name suffix precedes it.
	//
	// As the names of these macro instances are prefixes of the
	// target's name, the elements of the macro instance name can be
	// obtained by slicing the target's name.
	var macroInstanceName []string
	macroInstanceNameLength := 0
	for {
		packageValue := e.GetPackageValue(&model_analysis_pb.Package_Key{
			Label:             canonicalPackage,
			MacroInstanceName: macroInstanceName,
		})
		if !packageValue.IsSet() {
			return PatchedTargetValue[TMetadata]{}, evaluation.ErrMissingDependency
		}
		definition, err := c.lookupTargetDefinitionInTargetList(ctx, model_core.Nested(packageValue, packageValue.Message.Targets), targetName)
		if err != nil {
			return PatchedTargetValue[TMetadata]{}, err
		}
		if definition.IsSet() {
			patchedDefinition := model_core.Patch(e, definition)
			return model_core.NewPatchedMessage(
				&model_analysis_pb.Target_Value{
					Definition: patchedDefinition.Message,
				},
				patchedDefinition.Patcher,
			), nil
		}

		nameSuffix := targetNameStr[macroInstanceNameLength:]
		var precedingNameSuffix *string
		if _, err := btree.Find(
			ctx,
			c.macroExpansionContextEntryReader,
			model_core.Nested(packageValue, packageValue.Message.MacroExpansionContexts),
			func(entry model_core.Message[*model_starlark_pb.MacroExpansionContext_Entry, TReference]) (int, *model_core_pb.DecodableReference) {
				switch level := entry.Message.Level.(type) {
				case *model_starlark_pb.MacroExpansionContext_Entry_Leaf:
					r := model_starlark.CompareMacroInstanceNames(nameSuffix, level.Leaf.NameSuffix)
					if r >= 0 {
						precedingNameSuffix = &level.Leaf.NameSuffix
					}
					return r, nil
				case *model_starlark_pb.MacroExpansionContext_Entry_Parent_:
					return model_starlark.CompareMacroInstanceNames(nameSuffix, level.Parent.FirstNameSuffix), level.Parent.Reference
				default:
					return 0, nil
				}
			},
		); err != nil {
			return PatchedTargetValue[TMetadata]{}, err
		}
		if precedingNameSuffix == nil || !model_starlark.IsValidMacroDeclarationName(*precedingNameSuffix, nameSuffix) {
			return PatchedTargetValue[TMetadata]{}, errors.New("target does not exist")
		}
		ownerNameLength := macroInstanceNameLength + len(*precedingNameSuffix)
		macroInstanceName = append(macroInstanceName, targetNameStr[macroInstanceNameLength:ownerNameLength])
		macroInstanceNameLength = ownerNameLength
	}
}

// TargetEnvironmentForTesting is used to generate mocks for testing
// ComputeTargetValue().
type TargetEnvironmentForTesting TargetEnvironment[model_core.CreatedObjectTree, model_core.CreatedObjectTree]
