package starlark

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	pg_label "bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	"bonanza.build/pkg/model/core/inlinedtree"
	model_format "bonanza.build/pkg/model/format"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/compile"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/storage/object"

	"google.golang.org/protobuf/proto"
)

// getGlobalsReferencedByFunctions returns the names of the globals of
// a program that are referenced by the code of functions declared in
// it. The top level code of the program is not considered, as it is
// never executed again after the program is written to storage.
func getGlobalsReferencedByFunctions(program *compile.Program) map[string]struct{} {
	names := map[string]struct{}{}
	var walk func(funcode *compile.Funcode)
	walk = func(funcode *compile.Funcode) {
		for pc := uint32(0); pc < uint32(len(funcode.Code)); {
			op, arg, nextPC := compile.DecodeInstruction(funcode.Code, pc)
			if op == compile.GLOBAL {
				names[funcode.Names[arg]] = struct{}{}
			}
			pc = nextPC
		}
		for _, nestedFuncode := range funcode.Functions {
			walk(nestedFuncode)
		}
	}
	for _, funcode := range program.Toplevel.Functions {
		walk(funcode)
	}
	return names
}

// EncodeCompiledProgram converts the globals of a Starlark program that
// has been executed to a Protobuf message, so that it can be written to
// storage. Only globals that can be accessed by other files, or that
// are referenced by functions declared in the program are encoded.
//
// The imports of the program need to have been canonicalized by
// calling CanonicalizeImports(), so that the code of functions can be
// written to storage.
func EncodeCompiledProgram[TReference any, TMetadata model_core.ReferenceMetadata](thread *eval.Thread, program *compile.Program, globals map[string]eval.Value, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.CompiledProgram, TMetadata], error) {
	referencedGlobals := getGlobalsReferencedByFunctions(program)
	var globalsKeys []string
	globalsValuesBuilder := NewListBuilder[TReference, TMetadata](options)
	defer globalsValuesBuilder.Discard()
	for _, name := range slices.Sorted(maps.Keys(globals)) {
		identifier, err := pg_label.NewStarlarkIdentifier(name)
		if err != nil {
			return model_core.PatchedMessage[*model_starlark_pb.CompiledProgram, TMetadata]{}, err
		}
		var currentIdentifier *pg_label.CanonicalStarlarkIdentifier
		if options.CurrentFilename != nil {
			i := options.CurrentFilename.AppendStarlarkIdentifier(identifier)
			currentIdentifier = &i
		}
		value := globals[name]
		_, isNamedGlobal := value.(NamedGlobal)
		_, isReferenced := referencedGlobals[name]
		if isNamedGlobal || isReferenced || identifier.IsPublic() {
			encodedValue, err := EncodeValue(thread, value, nil, currentIdentifier, options)
			if err != nil {
				return model_core.PatchedMessage[*model_starlark_pb.CompiledProgram, TMetadata]{}, fmt.Errorf("global %#v: %w", name, err)
			}
			globalsKeys = append(globalsKeys, name)
			if err := globalsValuesBuilder.PushChild(model_core.NewPatchedMessage(
				model_starlark_pb.List_Element_builder{
					Leaf: proto.ValueOrDefault(encodedValue.Message),
				}.Build(),
				encodedValue.Patcher,
			)); err != nil {
				return model_core.PatchedMessage[*model_starlark_pb.CompiledProgram, TMetadata]{}, err
			}
		}
	}

	globalsValues, err := globalsValuesBuilder.FinalizeList()
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.CompiledProgram, TMetadata]{}, err
	}
	functions, lines, err := encodeCompiledProgramFunctions(program, options)
	if err != nil {
		globalsValues.Discard()
		return model_core.PatchedMessage[*model_starlark_pb.CompiledProgram, TMetadata]{}, err
	}

	return inlinedtree.Build(
		inlinedtree.CandidateList[*model_starlark_pb.CompiledProgram, TMetadata]{
			// Fields that should always be inlined.
			inlinedtree.AlwaysInline(
				model_core.NewReferenceMessagePatcher[TMetadata](),
				func(compiledProgram model_core.PatchedMessage[*model_starlark_pb.CompiledProgram, TMetadata]) {
					compiledProgram.Message.SetTopLevelFunctionLines(lines)
				},
			),
			// Fields that can be stored externally if needed.
			{
				ExternalMessage: globalsValues.ToMarshaler(model_format.NewProtoList[model_starlark_pb.List_Element]()),
				Encoder:         options.ObjectEncoder,
				ParentAppender: func(
					compiledProgram model_core.PatchedMessage[*model_starlark_pb.CompiledProgram, TMetadata],
					externalObject *model_core.Decodable[model_core.CreatedObject[TMetadata]],
				) error {
					values, err := btree.MaybeMergeNodes(
						globalsValues.Message,
						externalObject,
						compiledProgram.Patcher,
						btree.Capturing(options.Context, options.ObjectCapturer, ComputeListParentNode),
					)
					if err != nil {
						return err
					}
					compiledProgram.Message.SetGlobals(model_starlark_pb.Struct_Fields_builder{
						Keys:   globalsKeys,
						Values: values,
					}.Build())
					return nil
				},
			},
			{
				ExternalMessage: functions.ToMarshaler(model_format.NewProtoList[model_starlark_pb.CompiledProgram_FunctionEntry]()),
				Encoder:         options.ObjectEncoder,
				ParentAppender: func(
					compiledProgram model_core.PatchedMessage[*model_starlark_pb.CompiledProgram, TMetadata],
					externalObject *model_core.Decodable[model_core.CreatedObject[TMetadata]],
				) error {
					entries, err := btree.MaybeMergeNodes(
						functions.Message,
						externalObject,
						compiledProgram.Patcher,
						btree.Capturing(options.Context, options.ObjectCapturer, computeFunctionEntryParentNode),
					)
					if err != nil {
						return err
					}
					compiledProgram.Message.SetFunctions(entries)
					return nil
				},
			},
		},
		options.ObjectChunkingParameters.GetInlinedTreeOptions(),
	)
}

// computeFunctionEntryParentNode generates a parent node of the B-tree
// containing the code of the functions in a program.
func computeFunctionEntryParentNode[TMetadata model_core.ReferenceMetadata](
	createdObject model_core.Decodable[model_core.MetadataEntry[TMetadata]],
	childNodes model_core.Message[[]*model_starlark_pb.CompiledProgram_FunctionEntry, object.LocalReference],
) model_core.PatchedMessage[*model_starlark_pb.CompiledProgram_FunctionEntry, TMetadata] {
	var firstName *model_starlark_pb.Function_Name
	switch childNodes.Message[0].WhichLevel() {
	case model_starlark_pb.CompiledProgram_FunctionEntry_Leaf_case:
		firstName = childNodes.Message[0].GetLeaf().GetName()
	case model_starlark_pb.CompiledProgram_FunctionEntry_Parent_case:
		firstName = childNodes.Message[0].GetParent().GetFirstName()
	}
	functionCount := uint32(0)
	for _, childNode := range childNodes.Message {
		functionCount += uint32(getFunctionEntryCount(childNode))
	}
	return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_starlark_pb.CompiledProgram_FunctionEntry {
		return model_starlark_pb.CompiledProgram_FunctionEntry_builder{
			Parent: model_starlark_pb.CompiledProgram_FunctionEntry_Parent_builder{
				Reference:     patcher.AddDecodableReference(createdObject),
				FirstName:     firstName,
				FunctionCount: functionCount,
			}.Build(),
		}.Build()
	})
}

// encodeCompiledProgramFunctions creates a B-tree containing the code
// of all functions and lambdas declared at the top level of a program.
// Nested functions are stored as part of the top-level function
// containing them. It also returns the lines at which the functions
// are declared, in the same order as the B-tree.
func encodeCompiledProgramFunctions[TReference any, TMetadata model_core.ReferenceMetadata](program *compile.Program, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[[]*model_starlark_pb.CompiledProgram_FunctionEntry, TMetadata], []uint32, error) {
	treeBuilder := newBTreeBuilder(
		options,
		/* isParent = */ func(entry *model_starlark_pb.CompiledProgram_FunctionEntry) bool {
			return entry.GetParent() != nil
		},
		/* parentNodeComputer = */ btree.Capturing(options.Context, options.ObjectCapturer, computeFunctionEntryParentNode),
	)
	defer treeBuilder.Discard()

	type namedFuncode struct {
		name    compile.FunctionName
		funcode *compile.Funcode
	}
	funcodes := make([]namedFuncode, 0, len(program.Toplevel.Functions))
	for i, funcode := range program.Toplevel.Functions {
		funcodes = append(funcodes, namedFuncode{
			name:    program.Toplevel.GetNestedFunctionName(i),
			funcode: funcode,
		})
	}
	slices.SortFunc(funcodes, func(a, b namedFuncode) int {
		return a.name.Compare(b.name)
	})

	lines := make([]uint32, 0, len(funcodes))
	for _, f := range funcodes {
		lines = append(lines, uint32(f.funcode.Pos.Line))
		encodedFuncode, err := encodeFuncode(f.funcode)
		if err != nil {
			return model_core.PatchedMessage[[]*model_starlark_pb.CompiledProgram_FunctionEntry, TMetadata]{}, nil, fmt.Errorf("function %#v: %w", f.funcode.Name, err)
		}
		if err := treeBuilder.PushChild(model_core.NewSimplePatchedMessage[TMetadata](model_starlark_pb.CompiledProgram_FunctionEntry_builder{
			Leaf: model_starlark_pb.CompiledProgram_FunctionEntry_Leaf_builder{
				Name: encodeFunctionName(f.name),
				Code: encodedFuncode,
			}.Build(),
		}.Build())); err != nil {
			return model_core.PatchedMessage[[]*model_starlark_pb.CompiledProgram_FunctionEntry, TMetadata]{}, nil, err
		}
	}
	functions, err := treeBuilder.FinalizeList()
	if err != nil {
		return model_core.PatchedMessage[[]*model_starlark_pb.CompiledProgram_FunctionEntry, TMetadata]{}, nil, err
	}
	return functions, lines, nil
}

// DecodeGlobals decodes all of the globals declared in a .bzl file that
// was previously compiled and written to storage. As decoding is lazy,
// this does not read the contents of the globals.
func DecodeGlobals[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](thread *eval.Thread, encodedGlobals model_core.Message[*model_starlark_pb.Struct_Fields, TReference], currentFilename pg_label.CanonicalLabel) (map[string]eval.Value, error) {
	options, err := getValueDecodingOptions[TReference](thread)
	if err != nil {
		return nil, err
	}
	globals := map[string]eval.Value{}
	var errIter error
	for key, encodedValue := range AllStructFields(
		thread.Context(),
		options.Readers.List,
		encodedGlobals,
		&errIter,
	) {
		identifier, err := pg_label.NewStarlarkIdentifier(key)
		if err != nil {
			return nil, err
		}
		currentIdentifier := currentFilename.AppendStarlarkIdentifier(identifier)
		value, err := DecodeValue[TReference, TMetadata](thread, encodedValue, &currentIdentifier)
		if err != nil {
			return nil, err
		}
		globals[key] = value
	}
	if errIter != nil {
		return nil, errIter
	}
	return globals, nil
}

// DecodeAttrType extracts the type of a rule attribute from a rule
// attribute's Protobuf message.
func DecodeAttrType[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](attr model_core.Message[*model_starlark_pb.Attr, TReference]) (AttrType[TReference, TMetadata], error) {
	switch attr.Message.WhichType() {
	case model_starlark_pb.Attr_Bool_case:
		return NewBoolAttrType[TReference, TMetadata](), nil
	case model_starlark_pb.Attr_Int_case:
		return NewIntAttrType[TReference, TMetadata](attr.Message.GetInt().GetValues()), nil
	case model_starlark_pb.Attr_IntList_case:
		return NewIntListAttrType[TReference, TMetadata](attr.Message.GetIntList().GetListOptions().GetAllowEmpty()), nil
	case model_starlark_pb.Attr_Label_case:
		valueOptions, err := DecodeLabelOptions[TReference, TMetadata](model_core.Nested(attr, attr.Message.GetLabel().GetValueOptions()))
		if err != nil {
			return nil, fmt.Errorf("value options: %w", err)
		}
		return NewLabelAttrType[TReference, TMetadata](
			attr.Message.GetLabel().GetAllowNone(),
			attr.Message.GetLabel().GetAllowSingleFile(),
			attr.Message.GetLabel().GetExecutable(),
			valueOptions,
		), nil
	case model_starlark_pb.Attr_LabelKeyedStringDict_case:
		dictKeyOptions, err := DecodeLabelOptions[TReference, TMetadata](model_core.Nested(attr, attr.Message.GetLabelKeyedStringDict().GetDictKeyOptions()))
		if err != nil {
			return nil, fmt.Errorf("dict key options: %w", err)
		}
		return NewLabelKeyedStringDictAttrType[TReference, TMetadata](attr.Message.GetLabelKeyedStringDict().GetDictOptions().GetAllowEmpty(), dictKeyOptions), nil
	case model_starlark_pb.Attr_LabelList_case:
		listValueOptions, err := DecodeLabelOptions[TReference, TMetadata](model_core.Nested(attr, attr.Message.GetLabelList().GetListValueOptions()))
		if err != nil {
			return nil, fmt.Errorf("list value options: %w", err)
		}
		return NewLabelListAttrType[TReference, TMetadata](attr.Message.GetLabelList().GetListOptions().GetAllowEmpty(), listValueOptions), nil
	case model_starlark_pb.Attr_Output_case:
		return NewOutputAttrType[TReference, TMetadata](attr.Message.GetOutput().GetFilenameTemplate()), nil
	case model_starlark_pb.Attr_OutputList_case:
		return NewOutputListAttrType[TReference, TMetadata](attr.Message.GetOutputList().GetListOptions().GetAllowEmpty()), nil
	case model_starlark_pb.Attr_String__case:
		return NewStringAttrType[TReference, TMetadata](attr.Message.GetString().GetValues()), nil
	case model_starlark_pb.Attr_StringDict_case:
		return NewStringDictAttrType[TReference, TMetadata](attr.Message.GetStringDict().GetDictOptions().GetAllowEmpty()), nil
	case model_starlark_pb.Attr_StringKeyedLabelDict_case:
		dictValueOptions, err := DecodeLabelOptions[TReference, TMetadata](model_core.Nested(attr, attr.Message.GetStringKeyedLabelDict().GetDictValueOptions()))
		if err != nil {
			return nil, fmt.Errorf("dict value options: %w", err)
		}
		return NewStringKeyedLabelDictAttrType[TReference, TMetadata](attr.Message.GetStringKeyedLabelDict().GetDictOptions().GetAllowEmpty(), dictValueOptions), nil
	case model_starlark_pb.Attr_StringList_case:
		return NewStringListAttrType[TReference, TMetadata](attr.Message.GetStringList().GetListOptions().GetAllowEmpty()), nil
	case model_starlark_pb.Attr_StringListDict_case:
		return NewStringListDictAttrType[TReference, TMetadata](attr.Message.GetStringListDict().GetDictOptions().GetAllowEmpty()), nil
	default:
		return nil, errors.New("unknown attribute type")
	}
}

// DecodeBuildSettingType extracts the type of a build setting from a
// build setting's Protobuf message.
func DecodeBuildSettingType[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](buildSetting *model_starlark_pb.BuildSetting) (BuildSettingType[TReference, TMetadata], error) {
	switch buildSetting.WhichType() {
	case model_starlark_pb.BuildSetting_Bool_case:
		return NewBoolBuildSettingType[TReference, TMetadata](), nil
	case model_starlark_pb.BuildSetting_Int_case:
		return NewIntBuildSettingType[TReference, TMetadata](), nil
	case model_starlark_pb.BuildSetting_LabelList_case:
		return NewLabelListBuildSettingType[TReference, TMetadata](buildSetting.GetLabelList().GetRepeatable()), nil
	case model_starlark_pb.BuildSetting_String__case:
		return NewStringBuildSettingType[TReference, TMetadata](), nil
	case model_starlark_pb.BuildSetting_StringList_case:
		return NewStringListBuildSettingType[TReference, TMetadata](buildSetting.GetStringList().GetRepeatable()), nil
	default:
		return nil, errors.New("unknown build setting type")
	}
}

func decodeBuildSetting[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](buildSetting *model_starlark_pb.BuildSetting) (*BuildSetting[TReference, TMetadata], error) {
	buildSettingType, err := DecodeBuildSettingType[TReference, TMetadata](buildSetting)
	if err != nil {
		return nil, err
	}
	return NewBuildSetting(buildSettingType, buildSetting.GetFlag()), nil
}
