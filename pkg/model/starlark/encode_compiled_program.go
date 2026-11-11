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
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/compile"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/storage/object"
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
				&model_starlark_pb.List_Element{
					Level: &model_starlark_pb.List_Element_Leaf{
						Leaf: encodedValue.Message,
					},
				},
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
					compiledProgram.Message.TopLevelFunctionLines = lines
				},
			),
			// Fields that can be stored externally if needed.
			{
				ExternalMessage: model_core.ProtoListToBinaryMarshaler(globalsValues),
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
					compiledProgram.Message.Globals = &model_starlark_pb.Struct_Fields{
						Keys:   globalsKeys,
						Values: values,
					}
					return nil
				},
			},
			{
				ExternalMessage: model_core.ProtoListToBinaryMarshaler(functions),
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
					compiledProgram.Message.Functions = entries
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
	switch level := childNodes.Message[0].Level.(type) {
	case *model_starlark_pb.CompiledProgram_FunctionEntry_Leaf_:
		firstName = level.Leaf.Name
	case *model_starlark_pb.CompiledProgram_FunctionEntry_Parent_:
		firstName = level.Parent.FirstName
	}
	functionCount := uint32(0)
	for _, childNode := range childNodes.Message {
		functionCount += uint32(getFunctionEntryCount(childNode))
	}
	return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_starlark_pb.CompiledProgram_FunctionEntry {
		return &model_starlark_pb.CompiledProgram_FunctionEntry{
			Level: &model_starlark_pb.CompiledProgram_FunctionEntry_Parent_{
				Parent: &model_starlark_pb.CompiledProgram_FunctionEntry_Parent{
					Reference:     patcher.AddDecodableReference(createdObject),
					FirstName:     firstName,
					FunctionCount: functionCount,
				},
			},
		}
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
		if err := treeBuilder.PushChild(model_core.NewSimplePatchedMessage[TMetadata](
			&model_starlark_pb.CompiledProgram_FunctionEntry{
				Level: &model_starlark_pb.CompiledProgram_FunctionEntry_Leaf_{
					Leaf: &model_starlark_pb.CompiledProgram_FunctionEntry_Leaf{
						Name: encodeFunctionName(f.name),
						Code: encodedFuncode,
					},
				},
			},
		)); err != nil {
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
	switch attrTypeInfo := attr.Message.Type.(type) {
	case *model_starlark_pb.Attr_Bool:
		return NewBoolAttrType[TReference, TMetadata](), nil
	case *model_starlark_pb.Attr_Int:
		return NewIntAttrType[TReference, TMetadata](attrTypeInfo.Int.Values), nil
	case *model_starlark_pb.Attr_IntList:
		return NewIntListAttrType[TReference, TMetadata](), nil
	case *model_starlark_pb.Attr_Label:
		if attrTypeInfo.Label.ValueOptions == nil || attrTypeInfo.Label.ValueOptions.Cfg == nil {
			return nil, errors.New("missing value options")
		}
		return NewLabelAttrType[TReference, TMetadata](
			attrTypeInfo.Label.AllowNone,
			attrTypeInfo.Label.AllowSingleFile,
			attrTypeInfo.Label.Executable,
			attrTypeInfo.Label.ValueOptions.AllowFiles,
			NewProtoTransitionDefinition[TReference, TMetadata](
				model_core.Nested(attr, attrTypeInfo.Label.ValueOptions.Cfg),
			),
		), nil
	case *model_starlark_pb.Attr_LabelKeyedStringDict:
		if attrTypeInfo.LabelKeyedStringDict.DictKeyOptions == nil || attrTypeInfo.LabelKeyedStringDict.DictKeyOptions.Cfg == nil {
			return nil, errors.New("missing dict key options")
		}
		return NewLabelKeyedStringDictAttrType[TReference, TMetadata](
			attrTypeInfo.LabelKeyedStringDict.DictKeyOptions.AllowFiles,
			NewProtoTransitionDefinition[TReference, TMetadata](
				model_core.Nested(attr, attrTypeInfo.LabelKeyedStringDict.DictKeyOptions.Cfg),
			),
		), nil
	case *model_starlark_pb.Attr_LabelList:
		if attrTypeInfo.LabelList.ListValueOptions == nil || attrTypeInfo.LabelList.ListValueOptions.Cfg == nil {
			return nil, errors.New("missing list value options")
		}
		return NewLabelListAttrType[TReference, TMetadata](
			attrTypeInfo.LabelList.ListValueOptions.AllowFiles,
			NewProtoTransitionDefinition[TReference, TMetadata](
				model_core.Nested(attr, attrTypeInfo.LabelList.ListValueOptions.Cfg),
			),
		), nil
	case *model_starlark_pb.Attr_Output:
		return NewOutputAttrType[TReference, TMetadata](attrTypeInfo.Output.FilenameTemplate), nil
	case *model_starlark_pb.Attr_OutputList:
		return NewOutputListAttrType[TReference, TMetadata](), nil
	case *model_starlark_pb.Attr_String_:
		return NewStringAttrType[TReference, TMetadata](attrTypeInfo.String_.Values), nil
	case *model_starlark_pb.Attr_StringDict:
		return NewStringDictAttrType[TReference, TMetadata](), nil
	case *model_starlark_pb.Attr_StringKeyedLabelDict:
		if attrTypeInfo.StringKeyedLabelDict.DictValueOptions == nil || attrTypeInfo.StringKeyedLabelDict.DictValueOptions.Cfg == nil {
			return nil, errors.New("missing dict value options")
		}
		return NewStringKeyedLabelDictAttrType[TReference, TMetadata](
			attrTypeInfo.StringKeyedLabelDict.DictValueOptions.AllowFiles,
			NewProtoTransitionDefinition[TReference, TMetadata](
				model_core.Nested(attr, attrTypeInfo.StringKeyedLabelDict.DictValueOptions.Cfg),
			),
		), nil
	case *model_starlark_pb.Attr_StringList:
		return NewStringListAttrType[TReference, TMetadata](), nil
	case *model_starlark_pb.Attr_StringListDict:
		return NewStringListDictAttrType[TReference, TMetadata](), nil
	default:
		return nil, errors.New("unknown attribute type")
	}
}

// DecodeBuildSettingType extracts the type of a build setting from a
// build setting's Protobuf message.
func DecodeBuildSettingType[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](buildSetting *model_starlark_pb.BuildSetting) (BuildSettingType[TReference, TMetadata], error) {
	switch buildSettingTypeInfo := buildSetting.Type.(type) {
	case *model_starlark_pb.BuildSetting_Bool:
		return NewBoolBuildSettingType[TReference, TMetadata](), nil
	case *model_starlark_pb.BuildSetting_Int:
		return NewIntBuildSettingType[TReference, TMetadata](), nil
	case *model_starlark_pb.BuildSetting_LabelList:
		return NewLabelListBuildSettingType[TReference, TMetadata](buildSettingTypeInfo.LabelList.Repeatable), nil
	case *model_starlark_pb.BuildSetting_String_:
		return NewStringBuildSettingType[TReference, TMetadata](), nil
	case *model_starlark_pb.BuildSetting_StringList:
		return NewStringListBuildSettingType[TReference, TMetadata](buildSettingTypeInfo.StringList.Repeatable), nil
	default:
		return nil, errors.New("unknown build setting type")
	}
}

func decodeBuildSetting[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](buildSetting *model_starlark_pb.BuildSetting) (*BuildSetting[TReference, TMetadata], error) {
	buildSettingType, err := DecodeBuildSettingType[TReference, TMetadata](buildSetting)
	if err != nil {
		return nil, err
	}
	return NewBuildSetting(buildSettingType, buildSetting.Flag), nil
}
