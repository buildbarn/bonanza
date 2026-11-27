package render

import (
	"context"
	"fmt"
	"strings"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	model_parser "bonanza.build/pkg/model/parser"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/syntax"
	"bonanza.build/pkg/storage/object"

	"google.golang.org/protobuf/proto"
)

// starlarkLiteral renders a literal value, such as a string, number or
// boolean.
func starlarkLiteral(s string) Doc {
	return Styled(RoleLiteral, s)
}

// starlarkCallee renders the name of a function that is called, such
// as "struct", "depset" or the name of a provider. If provided, the
// title is shown when hovering the name.
func starlarkCallee(name, title string) Doc {
	callee := Styled(RoleCallee, name)
	if title != "" {
		callee = Title(title, callee)
	}
	return callee
}

// starlarkOpaque renders a value that has no literal representation in
// Starlark, such as a rule or a function. These are displayed in the
// same way as Starlark's repr() would, using angle brackets.
func starlarkOpaque(s string) Doc {
	return Styled(RoleDim, "<"+s+">")
}

func starlarkErrorf(format string, args ...any) Doc {
	return errorf(format, args...)
}

// starlarkKeywordArgument renders a keyword argument of a function
// call, or a field of a struct.
func starlarkKeywordArgument(name string, value Doc) Doc {
	return Concat(
		Styled(RoleFieldName, name),
		Text(" = "),
		value,
	)
}

// starlarkCall renders a function call, such as struct(a = 1).
func starlarkCall(callee Doc, arguments []Doc) Doc {
	return Sequence(Concat(callee, Text("(")), Text(")"), arguments, true, false)
}

func starlarkString(s string) Doc {
	return starlarkLiteral(syntax.Quote(s))
}

func starlarkBool(v bool) Doc {
	if v {
		return starlarkLiteral("True")
	}
	return starlarkLiteral("False")
}

func starlarkStringList(values []string) Doc {
	elements := make([]Doc, 0, len(values))
	for _, value := range values {
		elements = append(elements, starlarkString(value))
	}
	return Sequence(Text("["), Text("]"), elements, true, false)
}

// starlarkIdentifierName extracts the name of a Starlark object from
// its canonical identifier (e.g., "CcInfo" from
// "@@rules_cc+//cc/private:cc_info.bzl%CcInfo").
func starlarkIdentifierName(identifier string) string {
	if i := strings.LastIndexByte(identifier, '%'); i >= 0 {
		return identifier[i+1:]
	}
	return identifier
}

// starlarkOpaqueIdentifier renders a value that is identified by a
// canonical Starlark identifier, such as a rule or a provider. Only the
// name is displayed, while the full identifier is shown on hover.
func starlarkOpaqueIdentifier(kind, identifier string) Doc {
	if identifier == "" {
		return starlarkOpaque(kind)
	}
	return Title(identifier, starlarkOpaque(kind+" "+starlarkIdentifierName(identifier)))
}

// StarlarkRenderer renders messages that are part of the
// pkg/proto/model/starlark package using Starlark syntax.
type StarlarkRenderer struct {
	// If set, lists and dicts whose contents are partially stored
	// in other objects are rendered in full, by reading these
	// objects. Otherwise, links to these objects are rendered.
	Context             context.Context
	ListReader          model_parser.MessageObjectReader[object.LocalReference, []*model_starlark_pb.List_Element]
	DictReader          model_parser.MessageObjectReader[object.LocalReference, []*model_starlark_pb.Dict_Entry]
	TestSuiteTestReader model_parser.MessageObjectReader[object.LocalReference, []*model_starlark_pb.TestSuite_Test]
}

// Value renders a Starlark value.
func (sr *StarlarkRenderer) Value(value model_core.Message[*model_starlark_pb.Value, object.LocalReference]) Doc {
	return sr.renderValue(value)
}

// renderReference renders a link to an object containing additional
// list elements, dict entries or struct fields. As these have no
// Starlark representation, the link is displayed in the same style as
// opaque values.
func (*StarlarkRenderer) renderReference(reference model_core.Message[*model_core_pb.DecodableReference, object.LocalReference], outerMessage proto.Message, description string) Doc {
	return fieldReferenceLink(reference, outerMessage, "reference", Styled(RoleDim, "<"+description+">"))
}

// renderMaybeInlineFileContents renders the contents of a string or
// bytes value. Values that are stored inline are rendered as literals.
// Values that are stored externally are rendered as a link to the
// object containing their contents.
func (sr *StarlarkRenderer) renderMaybeInlineFileContents(contents model_core.Message[*model_filesystem_pb.MaybeInlineFileContents, object.LocalReference], prefix, description string) Doc {
	switch contents.Message.WhichLevel() {
	case 0:
		return starlarkLiteral(prefix + syntax.Quote(""))
	case model_filesystem_pb.MaybeInlineFileContents_ContentsInline_case:
		return starlarkLiteral(prefix + syntax.Quote(string(contents.Message.GetContentsInline())))
	case model_filesystem_pb.MaybeInlineFileContents_ContentsExternal_case:
		description = fmt.Sprintf("%s of %d bytes", description, contents.Message.GetContentsExternal().GetTotalSizeBytes())
		switch contents.Message.GetContentsExternal().WhichLevel() {
		case model_filesystem_pb.FileContents_ChunkReference_case:
			return fieldReferenceLink(model_core.Nested(contents, contents.Message.GetContentsExternal().GetChunkReference()), contents.Message.GetContentsExternal(), "chunk_reference", Styled(RoleDim, "<"+description+">"))
		case model_filesystem_pb.FileContents_List_case:
			return sr.renderReference(model_core.Nested(contents, contents.Message.GetContentsExternal().GetList().GetReference()), contents.Message.GetContentsExternal().GetList(), description)
		default:
			return starlarkOpaque(description)
		}
	default:
		return starlarkErrorf("invalid %s contents", description)
	}
}

func (sr *StarlarkRenderer) renderValue(value model_core.Message[*model_starlark_pb.Value, object.LocalReference]) Doc {
	switch value.Message.WhichKind() {
	case model_starlark_pb.Value_Aspect_case:
		return sr.renderAspect(model_core.Nested(value, value.Message.GetAspect()))
	case model_starlark_pb.Value_Attr_case:
		return sr.renderAttr(model_core.Nested(value, value.Message.GetAttr()))
	case model_starlark_pb.Value_Bool_case:
		return starlarkBool(value.Message.GetBool())
	case model_starlark_pb.Value_Builtin_case:
		return starlarkOpaque("built-in function " + value.Message.GetBuiltin())
	case model_starlark_pb.Value_Bytes_case:
		return sr.renderMaybeInlineFileContents(model_core.Nested(value, value.Message.GetBytes()), "b", "bytes")
	case model_starlark_pb.Value_Depset_case:
		return sr.renderDepset(model_core.Nested(value, value.Message.GetDepset()))
	case model_starlark_pb.Value_Dict_case:
		return sr.renderDictEntries(model_core.Nested(value, value.Message.GetDict().GetEntries()))
	case model_starlark_pb.Value_ExecGroup_case:
		return sr.renderExecGroup(value.Message.GetExecGroup())
	case model_starlark_pb.Value_File_case:
		return sr.renderFile(model_core.Nested(value, value.Message.GetFile()))
	case model_starlark_pb.Value_Function_case:
		return sr.renderFunction(value.Message.GetFunction())
	case model_starlark_pb.Value_Int_case:
		return sr.renderInt(value.Message.GetInt())
	case model_starlark_pb.Value_Label_case:
		return starlarkCall(starlarkCallee("Label", ""), []Doc{starlarkString(value.Message.GetLabel())})
	case model_starlark_pb.Value_List_case:
		return sr.renderListElements(Text("["), Text("]"), model_core.Nested(value, value.Message.GetList().GetElements()), false)
	case model_starlark_pb.Value_Macro_case:
		return sr.renderMacro(model_core.Nested(value, value.Message.GetMacro()))
	case model_starlark_pb.Value_ModuleExtension_case:
		return sr.renderModuleExtension(model_core.Nested(value, value.Message.GetModuleExtension()))
	case model_starlark_pb.Value_None_case:
		return starlarkLiteral("None")
	case model_starlark_pb.Value_Provider_case:
		return sr.renderProvider(model_core.Nested(value, value.Message.GetProvider()))
	case model_starlark_pb.Value_RepositoryRule_case:
		return sr.renderRepositoryRule(model_core.Nested(value, value.Message.GetRepositoryRule()))
	case model_starlark_pb.Value_Rule_case:
		return sr.renderRule(model_core.Nested(value, value.Message.GetRule()))
	case model_starlark_pb.Value_Select_case:
		return sr.renderSelect(model_core.Nested(value, value.Message.GetSelect()))
	case model_starlark_pb.Value_Set_case:
		return starlarkCall(
			starlarkCallee("set", ""),
			[]Doc{
				sr.renderListElements(Text("["), Text("]"), model_core.Nested(value, value.Message.GetSet().GetElements()), false),
			},
		)
	case model_starlark_pb.Value_Str_case:
		return sr.renderMaybeInlineFileContents(model_core.Nested(value, value.Message.GetStr()), "", "string")
	case model_starlark_pb.Value_Struct_case:
		return sr.renderStruct(model_core.Nested(value, value.Message.GetStruct()))
	case model_starlark_pb.Value_Subrule_case:
		return sr.renderSubrule(model_core.Nested(value, value.Message.GetSubrule()))
	case model_starlark_pb.Value_TagClass_case:
		return sr.renderTagClass(model_core.Nested(value, value.Message.GetTagClass()))
	case model_starlark_pb.Value_TargetReference_case:
		return sr.renderTargetReference(model_core.Nested(value, value.Message.GetTargetReference()))
	case model_starlark_pb.Value_ToolchainType_case:
		return sr.renderToolchainType(value.Message.GetToolchainType())
	case model_starlark_pb.Value_Transition_case:
		return sr.renderTransition(value.Message.GetTransition())
	case model_starlark_pb.Value_Tuple_case:
		tupleElements := value.Message.GetTuple().GetElements()
		elements := make([]Doc, 0, len(tupleElements))
		for _, element := range tupleElements {
			elements = append(elements, sr.renderValue(model_core.Nested(value, element)))
		}
		return Sequence(Text("("), Text(")"), elements, true, true)
	default:
		return starlarkErrorf("Unknown value kind")
	}
}

func (sr *StarlarkRenderer) renderAttr(attr model_core.Message[*model_starlark_pb.Attr, object.LocalReference]) Doc {
	typeField := attr.Message.ProtoReflect().WhichOneof(
		(&model_starlark_pb.Attr{}).ProtoReflect().Descriptor().Oneofs().ByName("type"),
	)
	if typeField == nil {
		return starlarkErrorf("Attribute has unknown type")
	}

	var arguments []Doc
	if defaultValue := attr.Message.GetDefault(); defaultValue != nil {
		arguments = append(arguments, starlarkKeywordArgument("default", sr.renderValue(model_core.Nested(attr, defaultValue))))
	}
	if !attr.Message.GetConfigurable() {
		switch attr.Message.WhichType() {
		case model_starlark_pb.Attr_Output_case, model_starlark_pb.Attr_OutputList_case:
			// Outputs are never configurable.
		default:
			arguments = append(arguments, starlarkKeywordArgument("configurable", starlarkBool(false)))
		}
	}
	addLabelOptions := func(labelOptions *model_starlark_pb.Attr_LabelOptions, allowFilesKeyword string) {
		// Omit allow_files if file targets are not permitted, as
		// that is the default. The original list of file
		// extensions cannot be recovered from the pattern.
		if allowFiles := labelOptions.GetAllowFiles(); allowFiles != nil {
			if len(allowFiles.GetFilenamePattern()) == 0 {
				arguments = append(arguments, starlarkKeywordArgument(allowFilesKeyword, starlarkBool(true)))
			} else {
				arguments = append(arguments, starlarkKeywordArgument(allowFilesKeyword, starlarkOpaque("filename pattern")))
			}
		}
		// Omit providers if they are set to the default value,
		// being a single empty set that is satisfied by any target.
		if providerSets := labelOptions.GetProviders(); !isAnyProviderSets(providerSets) {
			arguments = append(arguments, starlarkKeywordArgument("providers", renderProviderSets(providerSets)))
		}
		if aspects := labelOptions.GetAspects(); len(aspects) > 0 {
			elements := make([]Doc, 0, len(aspects))
			for _, aspect := range aspects {
				elements = append(elements, starlarkOpaqueIdentifier("aspect", aspect))
			}
			arguments = append(arguments, starlarkKeywordArgument("aspects", Sequence(Text("["), Text("]"), elements, true, false)))
		}
		if cfg := labelOptions.GetCfg(); cfg != nil {
			arguments = append(arguments, starlarkKeywordArgument("cfg", sr.renderTransition(cfg)))
		}
	}
	// Omit allow_empty if empty values are permitted, as that is
	// the default.
	if compositeOptions, ok := model_starlark.GetAttrCompositeOptions(attr.Message); ok && !compositeOptions.GetAllowEmpty() {
		arguments = append(arguments, starlarkKeywordArgument("allow_empty", starlarkBool(false)))
	}
	switch attr.Message.WhichType() {
	case model_starlark_pb.Attr_Int_case:
		if values := attr.Message.GetInt().GetValues(); len(values) > 0 {
			elements := make([]Doc, 0, len(values))
			for _, value := range values {
				elements = append(elements, starlarkLiteral(fmt.Sprintf("%d", value)))
			}
			arguments = append(arguments, starlarkKeywordArgument("values", Sequence(Text("["), Text("]"), elements, true, false)))
		}
	case model_starlark_pb.Attr_Label_case:
		if attr.Message.GetLabel().GetAllowSingleFile() {
			addLabelOptions(attr.Message.GetLabel().GetValueOptions(), "allow_single_file")
		} else {
			addLabelOptions(attr.Message.GetLabel().GetValueOptions(), "allow_files")
		}
		if attr.Message.GetLabel().GetExecutable() {
			arguments = append(arguments, starlarkKeywordArgument("executable", starlarkBool(true)))
		}
	case model_starlark_pb.Attr_LabelKeyedStringDict_case:
		addLabelOptions(attr.Message.GetLabelKeyedStringDict().GetDictKeyOptions(), "allow_files")
	case model_starlark_pb.Attr_LabelList_case:
		addLabelOptions(attr.Message.GetLabelList().GetListValueOptions(), "allow_files")
	case model_starlark_pb.Attr_String__case:
		if values := attr.Message.GetString().GetValues(); len(values) > 0 {
			arguments = append(arguments, starlarkKeywordArgument("values", starlarkStringList(values)))
		}
	case model_starlark_pb.Attr_StringKeyedLabelDict_case:
		addLabelOptions(attr.Message.GetStringKeyedLabelDict().GetDictValueOptions(), "allow_files")
	}

	return starlarkCall(starlarkCallee("attr."+string(typeField.Name()), ""), arguments)
}

func (sr *StarlarkRenderer) renderDepset(depset model_core.Message[*model_starlark_pb.Depset, object.LocalReference]) Doc {
	arguments := []Doc{
		sr.renderListElements(Text("["), Text("]"), model_core.Nested(depset, depset.Message.GetElements()), false),
	}
	if order := depset.Message.GetOrder(); order != model_starlark_pb.Depset_DEFAULT {
		arguments = append(arguments, starlarkKeywordArgument("order", starlarkString(strings.ToLower(order.String()))))
	}
	return starlarkCall(starlarkCallee("depset", ""), arguments)
}

func (sr *StarlarkRenderer) renderDictEntry(entry model_core.Message[*model_starlark_pb.Dict_Entry, object.LocalReference]) Doc {
	switch entry.Message.WhichLevel() {
	case model_starlark_pb.Dict_Entry_Leaf_case:
		return Concat(
			sr.renderValue(model_core.Nested(entry, entry.Message.GetLeaf().GetKey())),
			Text(": "),
			sr.renderValue(model_core.Nested(entry, entry.Message.GetLeaf().GetValue())),
		)
	case model_starlark_pb.Dict_Entry_Parent_case:
		return sr.renderReference(model_core.Nested(entry, entry.Message.GetParent().GetReference()), entry.Message.GetParent(), "more entries")
	default:
		return starlarkErrorf("Unknown dict entry level")
	}
}

func (sr *StarlarkRenderer) renderDictEntries(entries model_core.Message[[]*model_starlark_pb.Dict_Entry, object.LocalReference]) Doc {
	elements := make([]Doc, 0, len(entries.Message))
	if sr.DictReader != nil {
		// Load entries that are stored in other objects.
		var errIter error
		for entry := range btree.AllLeaves(
			sr.Context,
			sr.DictReader,
			entries,
			func(entry model_core.Message[*model_starlark_pb.Dict_Entry, object.LocalReference]) (*model_core_pb.DecodableReference, error) {
				return entry.Message.GetParent().GetReference(), nil
			},
			&errIter,
		) {
			elements = append(elements, sr.renderDictEntry(entry))
		}
		if errIter != nil {
			elements = append(elements, starlarkErrorf("Failed to read dict entries: %s", errIter))
		}
	} else {
		for _, entry := range entries.Message {
			elements = append(elements, sr.renderDictEntry(model_core.Nested(entries, entry)))
		}
	}
	return Sequence(Text("{"), Text("}"), elements, true, false)
}

func (sr *StarlarkRenderer) renderExecGroup(execGroup *model_starlark_pb.ExecGroup) Doc {
	var arguments []Doc
	if execCompatibleWith := execGroup.GetExecCompatibleWith(); len(execCompatibleWith) > 0 {
		arguments = append(arguments, starlarkKeywordArgument("exec_compatible_with", starlarkStringList(execCompatibleWith)))
	}
	if toolchainTypes := execGroup.GetToolchains(); len(toolchainTypes) > 0 {
		toolchains := make([]Doc, 0, len(toolchainTypes))
		for _, toolchain := range toolchainTypes {
			toolchains = append(toolchains, sr.renderToolchainType(toolchain))
		}
		arguments = append(arguments, starlarkKeywordArgument("toolchains", Sequence(Text("["), Text("]"), toolchains, true, false)))
	}
	return starlarkCall(starlarkCallee("exec_group", ""), arguments)
}

func (*StarlarkRenderer) renderFile(file model_core.Message[*model_starlark_pb.File, object.LocalReference]) Doc {
	owner := file.Message.GetOwner()
	if owner == nil {
		return starlarkOpaque("source file " + file.Message.GetLabel())
	}

	var kind string
	switch owner.GetType() {
	case model_starlark_pb.File_Owner_DIRECTORY:
		kind = "generated directory "
	case model_starlark_pb.File_Owner_SYMLINK:
		kind = "generated symlink "
	default:
		kind = "generated file "
	}
	description := Styled(RoleDim, "<"+kind+file.Message.GetLabel())
	if variant := owner.GetVariant(); variant.GetConfigurationReference() != nil {
		// Provide a link to the configuration in which the
		// file is built.
		description = Concat(
			description,
			Styled(RoleDim, " in configuration "),
			fieldReferenceLink(model_core.Nested(file, variant.GetConfigurationReference()), variant, "configuration_reference", Text("...")),
		)
	}
	return Concat(description, Styled(RoleDim, ">"))
}

func (*StarlarkRenderer) renderFunction(function *model_starlark_pb.Function) Doc {
	return Title(
		function.GetFilename(),
		starlarkOpaque("function "+model_starlark.GetFunctionDisplayName(function)),
	)
}

func (*StarlarkRenderer) renderInt(v []byte) Doc {
	return starlarkLiteral(model_starlark.DecodeInt(v).String())
}

func (sr *StarlarkRenderer) renderListElement(element model_core.Message[*model_starlark_pb.List_Element, object.LocalReference]) Doc {
	switch element.Message.WhichLevel() {
	case model_starlark_pb.List_Element_Leaf_case:
		return sr.renderValue(model_core.Nested(element, element.Message.GetLeaf()))
	case model_starlark_pb.List_Element_Parent_case:
		return sr.renderListElementParent(model_core.Nested(element, element.Message.GetParent()))
	default:
		return starlarkErrorf("Unknown list element level")
	}
}

func (sr *StarlarkRenderer) renderListElementParent(parent model_core.Message[*model_starlark_pb.List_Element_Parent, object.LocalReference]) Doc {
	description := fmt.Sprintf("%d more elements", parent.Message.GetCount())
	if parent.Message.GetCount() == 1 {
		description = "1 more element"
	}
	return sr.renderReference(model_core.Nested(parent, parent.Message.GetReference()), parent.Message, description)
}

func (sr *StarlarkRenderer) renderListElements(open, close Doc, elements model_core.Message[[]*model_starlark_pb.List_Element, object.LocalReference], singleElementNeedsComma bool) Doc {
	renderedElements := make([]Doc, 0, len(elements.Message))
	if sr.ListReader != nil {
		// Load elements that are stored in other objects.
		var errIter error
		for element := range btree.AllLeaves(
			sr.Context,
			sr.ListReader,
			elements,
			func(element model_core.Message[*model_starlark_pb.List_Element, object.LocalReference]) (*model_core_pb.DecodableReference, error) {
				return element.Message.GetParent().GetReference(), nil
			},
			&errIter,
		) {
			renderedElements = append(renderedElements, sr.renderListElement(element))
		}
		if errIter != nil {
			renderedElements = append(renderedElements, starlarkErrorf("Failed to read list elements: %s", errIter))
		}
	} else {
		for _, element := range elements.Message {
			renderedElements = append(renderedElements, sr.renderListElement(model_core.Nested(elements, element)))
		}
	}
	return Sequence(open, close, renderedElements, true, singleElementNeedsComma)
}

func (sr *StarlarkRenderer) renderTestSuiteTest(test model_core.Message[*model_starlark_pb.TestSuite_Test, object.LocalReference]) Doc {
	switch test.Message.WhichLevel() {
	case model_starlark_pb.TestSuite_Test_Leaf_case:
		return starlarkString(test.Message.GetLeaf())
	case model_starlark_pb.TestSuite_Test_Parent_case:
		return sr.renderReference(model_core.Nested(test, test.Message.GetParent().GetReference()), test.Message.GetParent(), "more tests")
	default:
		return starlarkErrorf("Unknown test level")
	}
}

// renderTestSuiteTests renders the B-tree containing the labels of the
// tests that are part of a test suite as a list of strings.
func (sr *StarlarkRenderer) renderTestSuiteTests(tests model_core.Message[[]*model_starlark_pb.TestSuite_Test, object.LocalReference]) Doc {
	renderedTests := make([]Doc, 0, len(tests.Message))
	if sr.TestSuiteTestReader != nil {
		// Load labels that are stored in other objects.
		var errIter error
		for test := range btree.AllLeaves(
			sr.Context,
			sr.TestSuiteTestReader,
			tests,
			func(test model_core.Message[*model_starlark_pb.TestSuite_Test, object.LocalReference]) (*model_core_pb.DecodableReference, error) {
				return test.Message.GetParent().GetReference(), nil
			},
			&errIter,
		) {
			renderedTests = append(renderedTests, sr.renderTestSuiteTest(test))
		}
		if errIter != nil {
			renderedTests = append(renderedTests, starlarkErrorf("Failed to read tests: %s", errIter))
		}
	} else {
		for _, test := range tests.Message {
			renderedTests = append(renderedTests, sr.renderTestSuiteTest(model_core.Nested(tests, test)))
		}
	}
	return Sequence(Text("["), Text("]"), renderedTests, true, false)
}

func (sr *StarlarkRenderer) renderSelectGroup(group model_core.Message[*model_starlark_pb.Select_Group, object.LocalReference]) Doc {
	conditions := group.Message.GetConditions()
	if len(conditions) == 0 {
		if noMatchValue := group.Message.GetNoMatchValue(); noMatchValue != nil {
			// Group consisting of a single value that is not
			// wrapped in a select().
			return sr.renderValue(model_core.Nested(group, noMatchValue))
		}
	}

	entries := make([]Doc, 0, len(conditions)+1)
	for _, condition := range conditions {
		entries = append(
			entries,
			Concat(
				starlarkString(condition.GetConditionIdentifier()),
				Text(": "),
				sr.renderValue(model_core.Nested(group, condition.GetValue())),
			),
		)
	}
	arguments := make([]Doc, 0, 2)
	switch group.Message.WhichNoMatch() {
	case model_starlark_pb.Select_Group_NoMatchValue_case:
		entries = append(
			entries,
			Concat(
				starlarkString("//conditions:default"),
				Text(": "),
				sr.renderValue(model_core.Nested(group, group.Message.GetNoMatchValue())),
			),
		)
		arguments = append(arguments, Sequence(Text("{"), Text("}"), entries, true, false))
	case model_starlark_pb.Select_Group_NoMatchError_case:
		arguments = append(
			arguments,
			Sequence(Text("{"), Text("}"), entries, true, false),
			starlarkKeywordArgument("no_match_error", starlarkString(group.Message.GetNoMatchError())),
		)
	default:
		arguments = append(arguments, Sequence(Text("{"), Text("}"), entries, true, false))
	}
	return starlarkCall(starlarkCallee("select", ""), arguments)
}

func (sr *StarlarkRenderer) renderSelect(selectValue model_core.Message[*model_starlark_pb.Select, object.LocalReference]) Doc {
	operator := "+"
	if selectValue.Message.GetConcatenationOperator() == model_starlark_pb.Select_PIPE {
		operator = "|"
	}

	groups := selectValue.Message.GetGroups()
	renderedGroups := make([]Doc, 0, len(groups))
	for _, group := range groups {
		renderedGroups = append(renderedGroups, sr.renderSelectGroup(model_core.Nested(selectValue, group)))
	}
	if len(renderedGroups) == 1 {
		return renderedGroups[0]
	}

	// If the operands don't fit on a single line, place each of
	// them on a separate line, wrapped in parentheses.
	parts := []Doc{SoftLine()}
	for i, renderedGroup := range renderedGroups {
		if i > 0 {
			parts = append(parts, Line(), Text(operator+" "))
		}
		parts = append(parts, renderedGroup)
	}
	return Group(
		Concat(
			IfBroken(Text("("), Text("")),
			Nest(Concat(parts...)),
			SoftLine(),
			IfBroken(Text(")"), Text("")),
		),
	)
}

func (sr *StarlarkRenderer) renderStructFields(callee Doc, fields model_core.Message[*model_starlark_pb.Struct_Fields, object.LocalReference]) Doc {
	keys := fields.Message.GetKeys()
	values := fields.Message.GetValues()
	arguments := make([]Doc, 0, len(values))
	for _, value := range values {
		switch value.WhichLevel() {
		case model_starlark_pb.List_Element_Leaf_case:
			if len(keys) == 0 {
				return starlarkErrorf("Struct has fewer keys than values")
			}
			arguments = append(arguments, starlarkKeywordArgument(keys[0], sr.renderValue(model_core.Nested(fields, value.GetLeaf()))))
			keys = keys[1:]
		case model_starlark_pb.List_Element_Parent_case:
			// Values are stored in a separate object. Display
			// the range of keys whose values are stored there.
			count := value.GetParent().GetCount()
			if count == 0 || uint64(len(keys)) < count {
				return starlarkErrorf("Struct has fewer keys than values")
			}
			description := "field " + keys[0]
			if count > 1 {
				description = fmt.Sprintf("fields %s to %s", keys[0], keys[count-1])
			}
			arguments = append(
				arguments,
				sr.renderReference(model_core.Nested(fields, value.GetParent().GetReference()), value.GetParent(), description),
			)
			keys = keys[count:]
		default:
			return starlarkErrorf("Unknown list element level")
		}
	}
	if len(keys) > 0 {
		return starlarkErrorf("Struct has more keys than values")
	}
	return starlarkCall(callee, arguments)
}

func (sr *StarlarkRenderer) renderStruct(structValue model_core.Message[*model_starlark_pb.Struct, object.LocalReference]) Doc {
	callee := starlarkCallee("struct", "")
	if properties := structValue.Message.GetProviderInstanceProperties(); properties != nil {
		// Instance of a provider. Use the name of the provider
		// as the name of the function that is called.
		callee = starlarkCallee(starlarkIdentifierName(properties.GetProviderIdentifier()), properties.GetProviderIdentifier())
	}
	return sr.renderStructFields(callee, model_core.Nested(structValue, structValue.Message.GetFields()))
}

func (sr *StarlarkRenderer) renderTargetReference(targetReference model_core.Message[*model_starlark_pb.TargetReference, object.LocalReference]) Doc {
	configured := targetReference.Message.GetConfigured()
	if configured == nil {
		return starlarkOpaque("unconfigured target " + targetReference.Message.GetOriginalLabel())
	}

	open := Styled(RoleDim, "<target "+configured.GetLabel())
	close := Styled(RoleDim, ">")
	configuredProviders := configured.GetProviders()
	if len(configuredProviders) == 0 {
		return Concat(open, close)
	}
	providers := make([]Doc, 0, len(configuredProviders))
	for _, provider := range configuredProviders {
		providers = append(providers, sr.renderStruct(model_core.Nested(targetReference, provider)))
	}
	return Sequence(
		Concat(open, Text(", providers = [")),
		Concat(Text("]"), close),
		providers,
		true,
		false,
	)
}

func (*StarlarkRenderer) renderToolchainType(toolchainType *model_starlark_pb.ToolchainType) Doc {
	arguments := []Doc{starlarkString(toolchainType.GetToolchainType())}
	if !toolchainType.GetMandatory() {
		arguments = append(arguments, starlarkKeywordArgument("mandatory", starlarkBool(false)))
	}
	return starlarkCall(starlarkCallee("config_common.toolchain_type", ""), arguments)
}

func (sr *StarlarkRenderer) renderTransition(transition *model_starlark_pb.Transition) Doc {
	switch transition.WhichKind() {
	case model_starlark_pb.Transition_ExecGroup_case:
		var arguments []Doc
		if execGroup := transition.GetExecGroup(); execGroup != "" {
			arguments = append(arguments, starlarkString(execGroup))
		}
		return starlarkCall(starlarkCallee("config.exec", ""), arguments)
	case model_starlark_pb.Transition_None_case:
		return starlarkCall(starlarkCallee("config.none", ""), nil)
	case model_starlark_pb.Transition_Target_case:
		return starlarkCall(starlarkCallee("config.target", ""), nil)
	case model_starlark_pb.Transition_Unconfigured_case:
		return starlarkOpaque("unconfigured transition")
	case model_starlark_pb.Transition_UserDefined_case:
		return sr.renderUserDefinedTransition(transition.GetUserDefined())
	default:
		return starlarkErrorf("Unknown transition kind")
	}
}

// starlarkIdentifierList renders a list of objects that are referenced
// by canonical Starlark identifier, such as the providers that a rule
// provides.
func starlarkIdentifierList(kind string, identifiers []string) Doc {
	elements := make([]Doc, 0, len(identifiers))
	for _, identifier := range identifiers {
		elements = append(elements, starlarkOpaqueIdentifier(kind, identifier))
	}
	return Sequence(Text("["), Text("]"), elements, true, false)
}

// starlarkDefinitionCall renders a call to a function that declares a
// rule, provider, etc. If the canonical identifier under which the
// object is declared is known, it is shown when hovering the name of
// the function.
func starlarkDefinitionCall(function, identifier string, arguments []Doc) Doc {
	return starlarkCall(starlarkCallee(function, identifier), arguments)
}

// renderNamedAttrs renders the attributes of a rule, repository rule,
// subrule or tag class as a dict, as would be passed to the attrs
// argument of rule(), repository_rule(), etc.
func (sr *StarlarkRenderer) renderNamedAttrs(attrs model_core.Message[[]*model_starlark_pb.NamedAttr, object.LocalReference]) Doc {
	entries := make([]Doc, 0, len(attrs.Message))
	for _, namedAttr := range attrs.Message {
		entries = append(
			entries,
			Concat(
				starlarkString(namedAttr.GetName()),
				Text(": "),
				sr.renderAttr(model_core.Nested(attrs, namedAttr.GetAttr())),
			),
		)
	}
	return Sequence(Text("{"), Text("}"), entries, true, false)
}

func (*StarlarkRenderer) renderBuildSetting(buildSetting *model_starlark_pb.BuildSetting) Doc {
	typeField := buildSetting.ProtoReflect().WhichOneof(
		(&model_starlark_pb.BuildSetting{}).ProtoReflect().Descriptor().Oneofs().ByName("type"),
	)
	if typeField == nil {
		return starlarkErrorf("Build setting has unknown type")
	}
	var arguments []Doc
	if buildSetting.GetFlag() {
		arguments = append(arguments, starlarkKeywordArgument("flag", starlarkBool(true)))
	}
	switch buildSetting.WhichType() {
	case model_starlark_pb.BuildSetting_LabelList_case:
		if buildSetting.GetLabelList().GetRepeatable() {
			arguments = append(arguments, starlarkKeywordArgument("repeatable", starlarkBool(true)))
		}
	case model_starlark_pb.BuildSetting_StringList_case:
		if buildSetting.GetStringList().GetRepeatable() {
			arguments = append(arguments, starlarkKeywordArgument("repeatable", starlarkBool(true)))
		}
	}
	return starlarkCall(starlarkCallee("config."+string(typeField.Name()), ""), arguments)
}

func (sr *StarlarkRenderer) renderModuleExtension(moduleExtension model_core.Message[*model_starlark_pb.ModuleExtension, object.LocalReference]) Doc {
	var arguments []Doc
	if implementation := moduleExtension.Message.GetImplementation(); implementation != nil {
		arguments = append(arguments, starlarkKeywordArgument("implementation", sr.renderFunction(implementation)))
	}
	if tagClasses := moduleExtension.Message.GetTagClasses(); len(tagClasses) > 0 {
		entries := make([]Doc, 0, len(tagClasses))
		for _, namedTagClass := range tagClasses {
			entries = append(
				entries,
				Concat(
					starlarkString(namedTagClass.GetName()),
					Text(": "),
					sr.renderTagClass(model_core.Nested(moduleExtension, namedTagClass.GetTagClass())),
				),
			)
		}
		arguments = append(arguments, starlarkKeywordArgument("tag_classes", Sequence(Text("{"), Text("}"), entries, true, false)))
	}
	return starlarkDefinitionCall("module_extension", "", arguments)
}

func (sr *StarlarkRenderer) renderProvider(provider model_core.Message[*model_starlark_pb.Provider, object.LocalReference]) Doc {
	properties := provider.Message.GetInstanceProperties()
	var arguments []Doc
	if fields := provider.Message.GetFields(); len(fields) > 0 {
		arguments = append(arguments, starlarkKeywordArgument("fields", starlarkStringList(fields)))
	}
	if initFunction := provider.Message.GetInitFunction(); initFunction != nil {
		arguments = append(arguments, starlarkKeywordArgument("init", sr.renderFunction(initFunction)))
	}

	// The following properties cannot be set through provider() in
	// Bazel. In Bonanza they are used to implement types that Bazel
	// implements natively.
	if computedFields := properties.GetComputedFields(); len(computedFields) > 0 {
		entries := make([]Doc, 0, len(computedFields))
		for _, computedField := range computedFields {
			entries = append(
				entries,
				Concat(
					starlarkString(computedField.GetName()),
					Text(": "),
					sr.renderFunction(computedField.GetFunction()),
				),
			)
		}
		arguments = append(arguments, starlarkKeywordArgument("computed_fields", Sequence(Text("{"), Text("}"), entries, true, false)))
	}
	if properties.GetDictLike() {
		arguments = append(arguments, starlarkKeywordArgument("dict_like", starlarkBool(true)))
	}
	if typeName := properties.GetTypeName(); typeName != "" {
		arguments = append(arguments, starlarkKeywordArgument("type_name", starlarkString(typeName)))
	}
	return starlarkDefinitionCall("provider", properties.GetProviderIdentifier(), arguments)
}

func (sr *StarlarkRenderer) renderRepositoryRule(repositoryRule model_core.Message[*model_starlark_pb.RepositoryRule, object.LocalReference]) Doc {
	switch repositoryRule.Message.WhichKind() {
	case model_starlark_pb.RepositoryRule_Reference_case:
		return starlarkOpaqueIdentifier("repository_rule", repositoryRule.Message.GetReference())
	case model_starlark_pb.RepositoryRule_Definition_case:
		definition := repositoryRule.Message.GetDefinition()
		var arguments []Doc
		if implementation := definition.GetImplementation(); implementation != nil {
			arguments = append(arguments, starlarkKeywordArgument("implementation", sr.renderFunction(implementation)))
		}
		if attrs := definition.GetAttrs(); len(attrs) > 0 {
			arguments = append(arguments, starlarkKeywordArgument("attrs", sr.renderNamedAttrs(model_core.Nested(repositoryRule, attrs))))
		}
		return starlarkDefinitionCall("repository_rule", "", arguments)
	default:
		return starlarkOpaque("repository_rule")
	}
}

func (sr *StarlarkRenderer) renderMacro(macro model_core.Message[*model_starlark_pb.Macro, object.LocalReference]) Doc {
	switch macro.Message.WhichKind() {
	case model_starlark_pb.Macro_Reference_case:
		return starlarkOpaqueIdentifier("macro", macro.Message.GetReference())
	case model_starlark_pb.Macro_Definition_case:
		definition := macro.Message.GetDefinition()
		var arguments []Doc
		if implementation := definition.GetImplementation(); implementation != nil {
			arguments = append(arguments, starlarkKeywordArgument("implementation", sr.renderFunction(implementation)))
		}
		if attrs := definition.GetAttrs(); len(attrs) > 0 {
			arguments = append(arguments, starlarkKeywordArgument("attrs", sr.renderNamedAttrs(model_core.Nested(macro, attrs))))
		}
		if definition.GetFinalizer() {
			arguments = append(arguments, starlarkKeywordArgument("finalizer", starlarkBool(true)))
		}
		return starlarkCall(starlarkCallee("macro", ""), arguments)
	default:
		return starlarkErrorf("Unknown macro kind")
	}
}

// isAnyProviderSets returns true if a list of provider sets only
// contains a single empty set, meaning it is satisfied by any target.
func isAnyProviderSets(providerSets []*model_starlark_pb.ProviderSet) bool {
	return len(providerSets) == 1 && len(providerSets[0].GetProviderIdentifiers()) == 0
}

// renderProviderSets renders a list of provider sets, as accepted by
// attr.label(providers = ...) and aspect(required_providers = ...), in
// normal form.
func renderProviderSets(providerSets []*model_starlark_pb.ProviderSet) Doc {
	elements := make([]Doc, 0, len(providerSets))
	for _, providerSet := range providerSets {
		elements = append(elements, starlarkIdentifierList("provider", providerSet.GetProviderIdentifiers()))
	}
	return Sequence(Text("["), Text("]"), elements, true, false)
}

func (sr *StarlarkRenderer) renderAspect(aspect model_core.Message[*model_starlark_pb.Aspect, object.LocalReference]) Doc {
	switch aspect.Message.WhichKind() {
	case model_starlark_pb.Aspect_Reference_case:
		return starlarkOpaqueIdentifier("aspect", aspect.Message.GetReference())
	case model_starlark_pb.Aspect_Definition_case:
		definition := aspect.Message.GetDefinition()
		var arguments []Doc
		if implementation := definition.GetImplementation(); implementation != nil {
			arguments = append(arguments, starlarkKeywordArgument("implementation", sr.renderFunction(implementation)))
		}
		if definition.GetApplyToGeneratingRules() {
			arguments = append(arguments, starlarkKeywordArgument("apply_to_generating_rules", starlarkBool(true)))
		}
		if attrAspects := definition.GetAttrAspects(); attrAspects != nil {
			arguments = append(arguments, starlarkKeywordArgument("attr_aspects", sr.renderAspectPropagationEdges(attrAspects)))
		}
		if attrs := definition.GetAttrs(); len(attrs) > 0 {
			arguments = append(arguments, starlarkKeywordArgument("attrs", sr.renderNamedAttrs(model_core.Nested(aspect, attrs))))
		}
		if execGroups := definition.GetExecGroups(); len(execGroups) > 0 {
			arguments = append(arguments, starlarkKeywordArgument("exec_groups", sr.renderNamedExecGroups(execGroups)))
		}
		if propagationPredicate := definition.GetPropagationPredicate(); propagationPredicate != nil {
			arguments = append(arguments, starlarkKeywordArgument("propagation_predicate", sr.renderFunction(propagationPredicate)))
		}
		if provides := definition.GetProvides(); len(provides) > 0 {
			arguments = append(arguments, starlarkKeywordArgument("provides", starlarkIdentifierList("provider", provides)))
		}
		if providerSets := definition.GetRequiredAspectProviders(); len(providerSets) > 0 {
			arguments = append(arguments, starlarkKeywordArgument("required_aspect_providers", renderProviderSets(providerSets)))
		}
		if providerSets := definition.GetRequiredProviders(); !isAnyProviderSets(providerSets) {
			arguments = append(arguments, starlarkKeywordArgument("required_providers", renderProviderSets(providerSets)))
		}
		if requires := definition.GetRequires(); len(requires) > 0 {
			arguments = append(arguments, starlarkKeywordArgument("requires", starlarkIdentifierList("aspect", requires)))
		}
		if subruleIdentifiers := definition.GetSubruleIdentifiers(); len(subruleIdentifiers) > 0 {
			arguments = append(arguments, starlarkKeywordArgument("subrules", starlarkIdentifierList("subrule", subruleIdentifiers)))
		}
		if toolchainsAspects := definition.GetToolchainsAspects(); toolchainsAspects != nil {
			arguments = append(arguments, starlarkKeywordArgument("toolchains_aspects", sr.renderAspectPropagationEdges(toolchainsAspects)))
		}
		return starlarkDefinitionCall("aspect", "", arguments)
	default:
		return starlarkOpaque("aspect")
	}
}

// renderAspectPropagationEdges renders the edges along which an aspect
// propagates in the form in which they are provided to aspect().
func (sr *StarlarkRenderer) renderAspectPropagationEdges(propagationEdges *model_starlark_pb.Aspect_PropagationEdges) Doc {
	switch propagationEdges.WhichKind() {
	case model_starlark_pb.Aspect_PropagationEdges_Fixed_case:
		return starlarkStringList(propagationEdges.GetFixed().GetNames())
	case model_starlark_pb.Aspect_PropagationEdges_All_case:
		return starlarkStringList([]string{"*"})
	case model_starlark_pb.Aspect_PropagationEdges_Function_case:
		return sr.renderFunction(propagationEdges.GetFunction())
	default:
		return starlarkOpaque("propagation_edges")
	}
}

// renderNamedExecGroups renders the exec groups of a rule or aspect as
// a dict.
func (sr *StarlarkRenderer) renderNamedExecGroups(namedExecGroups []*model_starlark_pb.NamedExecGroup) Doc {
	entries := make([]Doc, 0, len(namedExecGroups))
	for _, namedExecGroup := range namedExecGroups {
		entries = append(
			entries,
			Concat(
				starlarkString(namedExecGroup.GetName()),
				Text(": "),
				sr.renderExecGroup(namedExecGroup.GetExecGroup()),
			),
		)
	}
	return Sequence(Text("{"), Text("}"), entries, true, false)
}

func (sr *StarlarkRenderer) renderRule(rule model_core.Message[*model_starlark_pb.Rule, object.LocalReference]) Doc {
	switch rule.Message.WhichKind() {
	case model_starlark_pb.Rule_Reference_case:
		return starlarkOpaqueIdentifier("rule", rule.Message.GetReference())
	case model_starlark_pb.Rule_Definition_case:
		definition := rule.Message.GetDefinition()
		var arguments []Doc
		if implementation := definition.GetImplementation(); implementation != nil {
			arguments = append(arguments, starlarkKeywordArgument("implementation", sr.renderFunction(implementation)))
		}
		switch definition.GetType() {
		case model_starlark_pb.Rule_Definition_EXECUTABLE:
			arguments = append(arguments, starlarkKeywordArgument("executable", starlarkBool(true)))
		case model_starlark_pb.Rule_Definition_TEST:
			arguments = append(arguments, starlarkKeywordArgument("test", starlarkBool(true)))
		}
		if attrs := definition.GetAttrs(); len(attrs) > 0 {
			arguments = append(arguments, starlarkKeywordArgument("attrs", sr.renderNamedAttrs(model_core.Nested(rule, attrs))))
		}
		if buildSetting := definition.GetBuildSetting(); buildSetting != nil {
			arguments = append(arguments, starlarkKeywordArgument("build_setting", sr.renderBuildSetting(buildSetting)))
		}
		if cfg := definition.GetCfgTransition(); cfg != nil {
			arguments = append(arguments, starlarkKeywordArgument("cfg", sr.renderUserDefinedTransition(cfg)))
		}
		if execGroups := definition.GetExecGroups(); len(execGroups) > 0 {
			arguments = append(arguments, starlarkKeywordArgument("exec_groups", sr.renderNamedExecGroups(execGroups)))
		}
		if initializer := definition.GetInitializer(); initializer != nil {
			arguments = append(arguments, starlarkKeywordArgument("initializer", sr.renderFunction(initializer)))
		}
		if provides := definition.GetProvides(); len(provides) > 0 {
			arguments = append(arguments, starlarkKeywordArgument("provides", starlarkIdentifierList("provider", provides)))
		}
		if subruleIdentifiers := definition.GetSubruleIdentifiers(); len(subruleIdentifiers) > 0 {
			arguments = append(arguments, starlarkKeywordArgument("subrules", starlarkIdentifierList("subrule", subruleIdentifiers)))
		}
		return starlarkDefinitionCall("rule", "", arguments)
	default:
		return starlarkOpaque("rule")
	}
}

func (sr *StarlarkRenderer) renderSubrule(subrule model_core.Message[*model_starlark_pb.Subrule, object.LocalReference]) Doc {
	switch subrule.Message.WhichKind() {
	case model_starlark_pb.Subrule_Reference_case:
		return starlarkOpaqueIdentifier("subrule", subrule.Message.GetReference())
	case model_starlark_pb.Subrule_Definition_case:
		definition := subrule.Message.GetDefinition()
		var arguments []Doc
		if implementation := definition.GetImplementation(); implementation != nil {
			arguments = append(arguments, starlarkKeywordArgument("implementation", sr.renderFunction(implementation)))
		}
		if attrs := definition.GetAttrs(); len(attrs) > 0 {
			arguments = append(arguments, starlarkKeywordArgument("attrs", sr.renderNamedAttrs(model_core.Nested(subrule, attrs))))
		}
		if subruleIdentifiers := definition.GetSubruleIdentifiers(); len(subruleIdentifiers) > 0 {
			arguments = append(arguments, starlarkKeywordArgument("subrules", starlarkIdentifierList("subrule", subruleIdentifiers)))
		}
		return starlarkDefinitionCall("subrule", "", arguments)
	default:
		return starlarkOpaque("subrule")
	}
}

func (sr *StarlarkRenderer) renderTagClass(tagClass model_core.Message[*model_starlark_pb.TagClass, object.LocalReference]) Doc {
	var arguments []Doc
	if attrs := tagClass.Message.GetAttrs(); len(attrs) > 0 {
		arguments = append(arguments, starlarkKeywordArgument("attrs", sr.renderNamedAttrs(model_core.Nested(tagClass, attrs))))
	}
	return starlarkDefinitionCall("tag_class", "", arguments)
}

func (sr *StarlarkRenderer) renderUserDefinedTransition(transition *model_starlark_pb.Transition_UserDefined) Doc {
	switch transition.WhichKind() {
	case model_starlark_pb.Transition_UserDefined_Identifier_case:
		return starlarkOpaqueIdentifier("transition", transition.GetIdentifier())
	case model_starlark_pb.Transition_UserDefined_Definition_case:
		definition := transition.GetDefinition()
		var arguments []Doc
		if implementation := definition.GetImplementation(); implementation != nil {
			arguments = append(arguments, starlarkKeywordArgument("implementation", sr.renderFunction(implementation)))
		}
		arguments = append(
			arguments,
			starlarkKeywordArgument("inputs", starlarkStringList(definition.GetInputs())),
			starlarkKeywordArgument("outputs", starlarkStringList(definition.GetOutputs())),
		)
		return starlarkDefinitionCall("transition", "", arguments)
	default:
		return starlarkOpaque("transition")
	}
}

// renderStarlark renders messages declared in pkg/proto/model/starlark
// using Starlark syntax. It returns nil if the message is not one that
// has a sensible Starlark representation.
func renderStarlark(m model_core.Message[proto.Message, object.LocalReference]) Doc {
	var sr StarlarkRenderer
	switch v := m.Message.(type) {
	case *model_starlark_pb.Attr:
		return sr.renderAttr(model_core.Nested(m, v))
	case *model_starlark_pb.Dict_Entry:
		return sr.renderDictEntry(model_core.Nested(m, v))
	case *model_starlark_pb.File:
		return sr.renderFile(model_core.Nested(m, v))
	case *model_starlark_pb.List_Element:
		return sr.renderListElement(model_core.Nested(m, v))
	case *model_starlark_pb.Select_Group:
		return sr.renderSelectGroup(model_core.Nested(m, v))
	case *model_starlark_pb.Struct:
		return sr.renderStruct(model_core.Nested(m, v))
	case *model_starlark_pb.Struct_Fields:
		return sr.renderStructFields(starlarkCallee("struct", ""), model_core.Nested(m, v))
	case *model_starlark_pb.ToolchainType:
		return sr.renderToolchainType(v)
	case *model_starlark_pb.Transition:
		return sr.renderTransition(v)
	case *model_starlark_pb.Value:
		return sr.renderValue(model_core.Nested(m, v))
	default:
		return nil
	}
}
