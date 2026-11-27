package analysis

import (
	"context"
	"errors"
	"fmt"
	"strings"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	"bonanza.build/pkg/model/evaluation"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
)

func (*baseComputer[TReference, TMetadata]) ComputeMacroExpansionContextValue(ctx context.Context, key *model_analysis_pb.MacroExpansionContext_Key, e MacroExpansionContextEnvironment[TReference, TMetadata]) (PatchedMacroExpansionContextValue[TMetadata], error) {
	analysisReaders, gotAnalysisReaders := getAnalysisReaders(e)

	macroInstanceName := key.GetMacroInstanceName()
	if len(macroInstanceName) == 0 {
		return PatchedMacroExpansionContextValue[TMetadata]{}, errors.New("no macro instance name provided")
	}

	// Look up the macro expansion context in the declarations of
	// the package or macro instance whose expansion yielded it.
	// Macro expansion contexts are stored by name suffix, meaning
	// only the last element of the macro instance name needs to be
	// compared.
	parentValue := e.GetPackageValue(&model_analysis_pb.Package_Key{
		Label:             key.GetPackage(),
		MacroInstanceName: macroInstanceName[:len(macroInstanceName)-1],
	})
	if !parentValue.IsSet() || !gotAnalysisReaders {
		return PatchedMacroExpansionContextValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	nameSuffix := macroInstanceName[len(macroInstanceName)-1]
	macroExpansionContext, err := btree.Find(
		ctx,
		analysisReaders.MacroExpansionContextEntry,
		model_core.Nested(parentValue, parentValue.Message.GetMacroExpansionContexts()),
		func(entry model_core.Message[*model_starlark_pb.MacroExpansionContext_Entry, TReference]) (int, *model_core_pb.DecodableReference) {
			switch entry.Message.WhichLevel() {
			case model_starlark_pb.MacroExpansionContext_Entry_Leaf_case:
				return model_starlark.CompareMacroInstanceNames(nameSuffix, entry.Message.GetLeaf().GetNameSuffix()), nil
			case model_starlark_pb.MacroExpansionContext_Entry_Parent_case:
				return model_starlark.CompareMacroInstanceNames(nameSuffix, entry.Message.GetParent().GetFirstNameSuffix()), entry.Message.GetParent().GetReference()
			default:
				return 0, nil
			}
		},
	)
	if err != nil {
		return PatchedMacroExpansionContextValue[TMetadata]{}, err
	}
	level := macroExpansionContext.Message.GetLeaf()
	if level == nil {
		return PatchedMacroExpansionContextValue[TMetadata]{}, fmt.Errorf("macro instance %#v does not exist", strings.Join(macroInstanceName, ""))
	}
	return model_core.Patch(
		e,
		model_core.Nested(macroExpansionContext, &model_analysis_pb.MacroExpansionContext_Value{
			Definition: level.GetDefinition(),
		}),
	), nil
}

// MacroExpansionContextEnvironmentForTesting is used to generate mocks
// for testing ComputeMacroExpansionContextValue().
type MacroExpansionContextEnvironmentForTesting MacroExpansionContextEnvironment[model_core.CreatedObjectTree, model_core.CreatedObjectTree]
