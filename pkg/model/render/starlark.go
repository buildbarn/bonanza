package render

import (
	"fmt"
	"math/big"
	"strings"

	model_core "bonanza.build/pkg/model/core"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/storage/object"

	"go.starlark.net/syntax"
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
	return starlarkLiteral(syntax.Quote(s, false))
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

// starlarkRenderer renders messages that are part of the
// pkg/proto/model/starlark package using Starlark syntax.
type starlarkRenderer struct{}

// renderReference renders a link to an object containing additional
// list elements, dict entries or struct fields. As these have no
// Starlark representation, the link is displayed in the same style as
// opaque values.
func (*starlarkRenderer) renderReference(reference model_core.Message[*model_core_pb.DecodableReference, object.LocalReference], outerMessage proto.Message, description string) Doc {
	return fieldReferenceLink(reference, outerMessage, "reference", Styled(RoleDim, "<"+description+">"))
}

func (sr *starlarkRenderer) renderValue(value model_core.Message[*model_starlark_pb.Value, object.LocalReference]) Doc {
	switch kind := value.Message.GetKind().(type) {
	case *model_starlark_pb.Value_Aspect:
		return starlarkOpaqueIdentifier("aspect", kind.Aspect.GetReference())
	case *model_starlark_pb.Value_Attr:
		return sr.renderAttr(model_core.Nested(value, kind.Attr))
	case *model_starlark_pb.Value_Bool:
		return starlarkBool(kind.Bool)
	case *model_starlark_pb.Value_Builtin:
		return starlarkOpaque("built-in function " + kind.Builtin)
	case *model_starlark_pb.Value_Bytes:
		return starlarkLiteral(syntax.Quote(string(kind.Bytes), true))
	case *model_starlark_pb.Value_Depset:
		return sr.renderDepset(model_core.Nested(value, kind.Depset))
	case *model_starlark_pb.Value_Dict:
		return sr.renderDictEntries(model_core.Nested(value, kind.Dict.GetEntries()))
	case *model_starlark_pb.Value_ExecGroup:
		return sr.renderExecGroup(kind.ExecGroup)
	case *model_starlark_pb.Value_File:
		return sr.renderFile(model_core.Nested(value, kind.File))
	case *model_starlark_pb.Value_Function:
		return sr.renderFunction(kind.Function)
	case *model_starlark_pb.Value_Int:
		return sr.renderInt(kind.Int)
	case *model_starlark_pb.Value_Label:
		return starlarkCall(starlarkCallee("Label", ""), []Doc{starlarkString(kind.Label)})
	case *model_starlark_pb.Value_List:
		return sr.renderListElements(Text("["), Text("]"), model_core.Nested(value, kind.List.GetElements()), false)
	case *model_starlark_pb.Value_Macro:
		return starlarkOpaque("macro")
	case *model_starlark_pb.Value_ModuleExtension:
		return sr.renderModuleExtension(model_core.Nested(value, kind.ModuleExtension))
	case *model_starlark_pb.Value_None:
		return starlarkLiteral("None")
	case *model_starlark_pb.Value_Provider:
		return sr.renderProvider(model_core.Nested(value, kind.Provider))
	case *model_starlark_pb.Value_RepositoryRule:
		return sr.renderRepositoryRule(model_core.Nested(value, kind.RepositoryRule))
	case *model_starlark_pb.Value_Rule:
		return sr.renderRule(model_core.Nested(value, kind.Rule))
	case *model_starlark_pb.Value_Select:
		return sr.renderSelect(model_core.Nested(value, kind.Select))
	case *model_starlark_pb.Value_Set:
		return starlarkCall(
			starlarkCallee("set", ""),
			[]Doc{
				sr.renderListElements(Text("["), Text("]"), model_core.Nested(value, kind.Set.GetElements()), false),
			},
		)
	case *model_starlark_pb.Value_Str:
		return starlarkString(kind.Str)
	case *model_starlark_pb.Value_Struct:
		return sr.renderStruct(model_core.Nested(value, kind.Struct))
	case *model_starlark_pb.Value_Subrule:
		return sr.renderSubrule(model_core.Nested(value, kind.Subrule))
	case *model_starlark_pb.Value_TagClass:
		return sr.renderTagClass(model_core.Nested(value, kind.TagClass))
	case *model_starlark_pb.Value_TargetReference:
		return sr.renderTargetReference(model_core.Nested(value, kind.TargetReference))
	case *model_starlark_pb.Value_ToolchainType:
		return sr.renderToolchainType(kind.ToolchainType)
	case *model_starlark_pb.Value_Transition:
		return sr.renderTransition(kind.Transition)
	case *model_starlark_pb.Value_Tuple:
		elements := make([]Doc, 0, len(kind.Tuple.GetElements()))
		for _, element := range kind.Tuple.GetElements() {
			elements = append(elements, sr.renderValue(model_core.Nested(value, element)))
		}
		return Sequence(Text("("), Text(")"), elements, true, true)
	default:
		return starlarkErrorf("Unknown value kind")
	}
}

func (sr *starlarkRenderer) renderAttr(attr model_core.Message[*model_starlark_pb.Attr, object.LocalReference]) Doc {
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
	addLabelOptions := func(labelOptions *model_starlark_pb.Attr_LabelOptions) {
		if providers := labelOptions.GetProviders(); len(providers) > 0 {
			elements := make([]Doc, 0, len(providers))
			for _, provider := range providers {
				elements = append(elements, starlarkOpaqueIdentifier("provider", provider))
			}
			arguments = append(arguments, starlarkKeywordArgument("providers", Sequence(Text("["), Text("]"), elements, true, false)))
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
	switch attrType := attr.Message.GetType().(type) {
	case *model_starlark_pb.Attr_Int:
		if values := attrType.Int.Values; len(values) > 0 {
			elements := make([]Doc, 0, len(values))
			for _, value := range values {
				elements = append(elements, starlarkLiteral(fmt.Sprintf("%d", value)))
			}
			arguments = append(arguments, starlarkKeywordArgument("values", Sequence(Text("["), Text("]"), elements, true, false)))
		}
	case *model_starlark_pb.Attr_Label:
		addLabelOptions(attrType.Label.ValueOptions)
		if attrType.Label.Executable {
			arguments = append(arguments, starlarkKeywordArgument("executable", starlarkBool(true)))
		}
		if attrType.Label.AllowSingleFile {
			arguments = append(arguments, starlarkKeywordArgument("allow_single_file", starlarkBool(true)))
		}
	case *model_starlark_pb.Attr_LabelKeyedStringDict:
		addLabelOptions(attrType.LabelKeyedStringDict.DictKeyOptions)
	case *model_starlark_pb.Attr_LabelList:
		addLabelOptions(attrType.LabelList.ListValueOptions)
	case *model_starlark_pb.Attr_String_:
		if values := attrType.String_.Values; len(values) > 0 {
			arguments = append(arguments, starlarkKeywordArgument("values", starlarkStringList(values)))
		}
	case *model_starlark_pb.Attr_StringKeyedLabelDict:
		addLabelOptions(attrType.StringKeyedLabelDict.DictValueOptions)
	}

	return starlarkCall(starlarkCallee("attr."+string(typeField.Name()), ""), arguments)
}

func (sr *starlarkRenderer) renderDepset(depset model_core.Message[*model_starlark_pb.Depset, object.LocalReference]) Doc {
	arguments := []Doc{
		sr.renderListElements(Text("["), Text("]"), model_core.Nested(depset, depset.Message.GetElements()), false),
	}
	if order := depset.Message.GetOrder(); order != model_starlark_pb.Depset_DEFAULT {
		arguments = append(arguments, starlarkKeywordArgument("order", starlarkString(strings.ToLower(order.String()))))
	}
	return starlarkCall(starlarkCallee("depset", ""), arguments)
}

func (sr *starlarkRenderer) renderDictEntry(entry model_core.Message[*model_starlark_pb.Dict_Entry, object.LocalReference]) Doc {
	switch level := entry.Message.GetLevel().(type) {
	case *model_starlark_pb.Dict_Entry_Leaf_:
		return Concat(
			sr.renderValue(model_core.Nested(entry, level.Leaf.GetKey())),
			Text(": "),
			sr.renderValue(model_core.Nested(entry, level.Leaf.GetValue())),
		)
	case *model_starlark_pb.Dict_Entry_Parent_:
		return sr.renderReference(model_core.Nested(entry, level.Parent.Reference), level.Parent, "more entries")
	default:
		return starlarkErrorf("Unknown dict entry level")
	}
}

func (sr *starlarkRenderer) renderDictEntries(entries model_core.Message[[]*model_starlark_pb.Dict_Entry, object.LocalReference]) Doc {
	elements := make([]Doc, 0, len(entries.Message))
	for _, entry := range entries.Message {
		elements = append(elements, sr.renderDictEntry(model_core.Nested(entries, entry)))
	}
	return Sequence(Text("{"), Text("}"), elements, true, false)
}

func (sr *starlarkRenderer) renderExecGroup(execGroup *model_starlark_pb.ExecGroup) Doc {
	var arguments []Doc
	if len(execGroup.GetExecCompatibleWith()) > 0 {
		arguments = append(arguments, starlarkKeywordArgument("exec_compatible_with", starlarkStringList(execGroup.GetExecCompatibleWith())))
	}
	if len(execGroup.GetToolchains()) > 0 {
		toolchains := make([]Doc, 0, len(execGroup.GetToolchains()))
		for _, toolchain := range execGroup.GetToolchains() {
			toolchains = append(toolchains, sr.renderToolchainType(toolchain))
		}
		arguments = append(arguments, starlarkKeywordArgument("toolchains", Sequence(Text("["), Text("]"), toolchains, true, false)))
	}
	return starlarkCall(starlarkCallee("exec_group", ""), arguments)
}

func (*starlarkRenderer) renderFile(file model_core.Message[*model_starlark_pb.File, object.LocalReference]) Doc {
	owner := file.Message.GetOwner()
	if owner == nil {
		return starlarkOpaque("source file " + file.Message.GetLabel())
	}

	var kind string
	switch owner.Type {
	case model_starlark_pb.File_Owner_DIRECTORY:
		kind = "generated directory "
	case model_starlark_pb.File_Owner_SYMLINK:
		kind = "generated symlink "
	default:
		kind = "generated file "
	}
	description := Styled(RoleDim, "<"+kind+file.Message.GetLabel())
	if configurationReference := owner.ConfigurationReference; configurationReference != nil {
		// Provide a link to the configuration in which the
		// file is built.
		description = Concat(
			description,
			Styled(RoleDim, " in configuration "),
			fieldReferenceLink(model_core.Nested(file, configurationReference), owner, "configuration_reference", Text("...")),
		)
	}
	return Concat(description, Styled(RoleDim, ">"))
}

func (*starlarkRenderer) renderFunction(function *model_starlark_pb.Function) Doc {
	return Title(
		fmt.Sprintf("%s:%d:%d", function.GetFilename(), function.GetLine(), function.GetColumn()),
		starlarkOpaque("function "+function.GetName()),
	)
}

func (*starlarkRenderer) renderInt(v *model_starlark_pb.Int) Doc {
	var i big.Int
	i.SetBytes(v.GetAbsoluteValue())
	if v.GetNegative() {
		i.Neg(&i)
	}
	return starlarkLiteral(i.String())
}

func (sr *starlarkRenderer) renderListElement(element model_core.Message[*model_starlark_pb.List_Element, object.LocalReference]) Doc {
	switch level := element.Message.GetLevel().(type) {
	case *model_starlark_pb.List_Element_Leaf:
		return sr.renderValue(model_core.Nested(element, level.Leaf))
	case *model_starlark_pb.List_Element_Parent_:
		return sr.renderListElementParent(model_core.Nested(element, level.Parent))
	default:
		return starlarkErrorf("Unknown list element level")
	}
}

func (sr *starlarkRenderer) renderListElementParent(parent model_core.Message[*model_starlark_pb.List_Element_Parent, object.LocalReference]) Doc {
	description := fmt.Sprintf("%d more elements", parent.Message.Count)
	if parent.Message.Count == 1 {
		description = "1 more element"
	}
	return sr.renderReference(model_core.Nested(parent, parent.Message.Reference), parent.Message, description)
}

func (sr *starlarkRenderer) renderListElements(open, close Doc, elements model_core.Message[[]*model_starlark_pb.List_Element, object.LocalReference], singleElementNeedsComma bool) Doc {
	renderedElements := make([]Doc, 0, len(elements.Message))
	for _, element := range elements.Message {
		renderedElements = append(renderedElements, sr.renderListElement(model_core.Nested(elements, element)))
	}
	return Sequence(open, close, renderedElements, true, singleElementNeedsComma)
}

func (sr *starlarkRenderer) renderSelectGroup(group model_core.Message[*model_starlark_pb.Select_Group, object.LocalReference]) Doc {
	conditions := group.Message.GetConditions()
	if len(conditions) == 0 {
		if noMatchValue, ok := group.Message.GetNoMatch().(*model_starlark_pb.Select_Group_NoMatchValue); ok {
			// Group consisting of a single value that is not
			// wrapped in a select().
			return sr.renderValue(model_core.Nested(group, noMatchValue.NoMatchValue))
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
	switch noMatch := group.Message.GetNoMatch().(type) {
	case *model_starlark_pb.Select_Group_NoMatchValue:
		entries = append(
			entries,
			Concat(
				starlarkString("//conditions:default"),
				Text(": "),
				sr.renderValue(model_core.Nested(group, noMatch.NoMatchValue)),
			),
		)
		arguments = append(arguments, Sequence(Text("{"), Text("}"), entries, true, false))
	case *model_starlark_pb.Select_Group_NoMatchError:
		arguments = append(
			arguments,
			Sequence(Text("{"), Text("}"), entries, true, false),
			starlarkKeywordArgument("no_match_error", starlarkString(noMatch.NoMatchError)),
		)
	default:
		arguments = append(arguments, Sequence(Text("{"), Text("}"), entries, true, false))
	}
	return starlarkCall(starlarkCallee("select", ""), arguments)
}

func (sr *starlarkRenderer) renderSelect(selectValue model_core.Message[*model_starlark_pb.Select, object.LocalReference]) Doc {
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

func (sr *starlarkRenderer) renderStructFields(callee Doc, fields model_core.Message[*model_starlark_pb.Struct_Fields, object.LocalReference]) Doc {
	keys := fields.Message.GetKeys()
	values := fields.Message.GetValues()
	arguments := make([]Doc, 0, len(values))
	for _, value := range values {
		switch level := value.Level.(type) {
		case *model_starlark_pb.List_Element_Leaf:
			if len(keys) == 0 {
				return starlarkErrorf("Struct has fewer keys than values")
			}
			arguments = append(arguments, starlarkKeywordArgument(keys[0], sr.renderValue(model_core.Nested(fields, level.Leaf))))
			keys = keys[1:]
		case *model_starlark_pb.List_Element_Parent_:
			// Values are stored in a separate object. Display
			// the range of keys whose values are stored there.
			count := level.Parent.Count
			if count == 0 || uint64(len(keys)) < count {
				return starlarkErrorf("Struct has fewer keys than values")
			}
			description := "field " + keys[0]
			if count > 1 {
				description = fmt.Sprintf("fields %s to %s", keys[0], keys[count-1])
			}
			arguments = append(
				arguments,
				sr.renderReference(model_core.Nested(fields, level.Parent.Reference), level.Parent, description),
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

func (sr *starlarkRenderer) renderStruct(structValue model_core.Message[*model_starlark_pb.Struct, object.LocalReference]) Doc {
	callee := starlarkCallee("struct", "")
	if properties := structValue.Message.GetProviderInstanceProperties(); properties != nil {
		// Instance of a provider. Use the name of the provider
		// as the name of the function that is called.
		callee = starlarkCallee(starlarkIdentifierName(properties.ProviderIdentifier), properties.ProviderIdentifier)
	}
	return sr.renderStructFields(callee, model_core.Nested(structValue, structValue.Message.GetFields()))
}

func (sr *starlarkRenderer) renderTargetReference(targetReference model_core.Message[*model_starlark_pb.TargetReference, object.LocalReference]) Doc {
	configured := targetReference.Message.GetConfigured()
	if configured == nil {
		return starlarkOpaque("unconfigured target " + targetReference.Message.GetOriginalLabel())
	}

	open := Styled(RoleDim, "<target "+configured.Label)
	close := Styled(RoleDim, ">")
	if len(configured.Providers) == 0 {
		return Concat(open, close)
	}
	providers := make([]Doc, 0, len(configured.Providers))
	for _, provider := range configured.Providers {
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

func (*starlarkRenderer) renderToolchainType(toolchainType *model_starlark_pb.ToolchainType) Doc {
	arguments := []Doc{starlarkString(toolchainType.GetToolchainType())}
	if !toolchainType.GetMandatory() {
		arguments = append(arguments, starlarkKeywordArgument("mandatory", starlarkBool(false)))
	}
	return starlarkCall(starlarkCallee("config_common.toolchain_type", ""), arguments)
}

func (sr *starlarkRenderer) renderTransition(transition *model_starlark_pb.Transition) Doc {
	switch kind := transition.GetKind().(type) {
	case *model_starlark_pb.Transition_ExecGroup:
		var arguments []Doc
		if kind.ExecGroup != "" {
			arguments = append(arguments, starlarkString(kind.ExecGroup))
		}
		return starlarkCall(starlarkCallee("config.exec", ""), arguments)
	case *model_starlark_pb.Transition_None:
		return starlarkCall(starlarkCallee("config.none", ""), nil)
	case *model_starlark_pb.Transition_Target:
		return starlarkCall(starlarkCallee("config.target", ""), nil)
	case *model_starlark_pb.Transition_Unconfigured:
		return starlarkOpaque("unconfigured transition")
	case *model_starlark_pb.Transition_UserDefined_:
		return sr.renderUserDefinedTransition(kind.UserDefined)
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
func (sr *starlarkRenderer) renderNamedAttrs(attrs model_core.Message[[]*model_starlark_pb.NamedAttr, object.LocalReference]) Doc {
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

func (*starlarkRenderer) renderBuildSetting(buildSetting *model_starlark_pb.BuildSetting) Doc {
	typeField := buildSetting.ProtoReflect().WhichOneof(
		(&model_starlark_pb.BuildSetting{}).ProtoReflect().Descriptor().Oneofs().ByName("type"),
	)
	if typeField == nil {
		return starlarkErrorf("Build setting has unknown type")
	}
	var arguments []Doc
	if buildSetting.Flag {
		arguments = append(arguments, starlarkKeywordArgument("flag", starlarkBool(true)))
	}
	switch buildSettingType := buildSetting.Type.(type) {
	case *model_starlark_pb.BuildSetting_LabelList:
		if buildSettingType.LabelList.GetRepeatable() {
			arguments = append(arguments, starlarkKeywordArgument("repeatable", starlarkBool(true)))
		}
	case *model_starlark_pb.BuildSetting_StringList:
		if buildSettingType.StringList.GetRepeatable() {
			arguments = append(arguments, starlarkKeywordArgument("repeatable", starlarkBool(true)))
		}
	}
	return starlarkCall(starlarkCallee("config."+string(typeField.Name()), ""), arguments)
}

func (sr *starlarkRenderer) renderModuleExtension(moduleExtension model_core.Message[*model_starlark_pb.ModuleExtension, object.LocalReference]) Doc {
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
					starlarkString(namedTagClass.Name),
					Text(": "),
					sr.renderTagClass(model_core.Nested(moduleExtension, namedTagClass.TagClass)),
				),
			)
		}
		arguments = append(arguments, starlarkKeywordArgument("tag_classes", Sequence(Text("{"), Text("}"), entries, true, false)))
	}
	return starlarkDefinitionCall("module_extension", "", arguments)
}

func (sr *starlarkRenderer) renderProvider(provider model_core.Message[*model_starlark_pb.Provider, object.LocalReference]) Doc {
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
					starlarkString(computedField.Name),
					Text(": "),
					sr.renderFunction(computedField.Function),
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

func (sr *starlarkRenderer) renderRepositoryRule(repositoryRule model_core.Message[*model_starlark_pb.RepositoryRule, object.LocalReference]) Doc {
	switch kind := repositoryRule.Message.GetKind().(type) {
	case *model_starlark_pb.RepositoryRule_Reference:
		return starlarkOpaqueIdentifier("repository_rule", kind.Reference)
	case *model_starlark_pb.RepositoryRule_Definition_:
		definition := kind.Definition
		var arguments []Doc
		if implementation := definition.Implementation; implementation != nil {
			arguments = append(arguments, starlarkKeywordArgument("implementation", sr.renderFunction(implementation)))
		}
		if len(definition.Attrs) > 0 {
			arguments = append(arguments, starlarkKeywordArgument("attrs", sr.renderNamedAttrs(model_core.Nested(repositoryRule, definition.Attrs))))
		}
		return starlarkDefinitionCall("repository_rule", "", arguments)
	default:
		return starlarkOpaque("repository_rule")
	}
}

func (sr *starlarkRenderer) renderRule(rule model_core.Message[*model_starlark_pb.Rule, object.LocalReference]) Doc {
	switch kind := rule.Message.GetKind().(type) {
	case *model_starlark_pb.Rule_Reference:
		return starlarkOpaqueIdentifier("rule", kind.Reference)
	case *model_starlark_pb.Rule_Definition_:
		definition := kind.Definition
		var arguments []Doc
		if implementation := definition.Implementation; implementation != nil {
			arguments = append(arguments, starlarkKeywordArgument("implementation", sr.renderFunction(implementation)))
		}
		if definition.Test {
			arguments = append(arguments, starlarkKeywordArgument("test", starlarkBool(true)))
		}
		if len(definition.Attrs) > 0 {
			arguments = append(arguments, starlarkKeywordArgument("attrs", sr.renderNamedAttrs(model_core.Nested(rule, definition.Attrs))))
		}
		if buildSetting := definition.BuildSetting; buildSetting != nil {
			arguments = append(arguments, starlarkKeywordArgument("build_setting", sr.renderBuildSetting(buildSetting)))
		}
		if cfg := definition.CfgTransition; cfg != nil {
			arguments = append(arguments, starlarkKeywordArgument("cfg", sr.renderUserDefinedTransition(cfg)))
		}
		if len(definition.ExecGroups) > 0 {
			entries := make([]Doc, 0, len(definition.ExecGroups))
			for _, namedExecGroup := range definition.ExecGroups {
				entries = append(
					entries,
					Concat(
						starlarkString(namedExecGroup.Name),
						Text(": "),
						sr.renderExecGroup(namedExecGroup.ExecGroup),
					),
				)
			}
			arguments = append(arguments, starlarkKeywordArgument("exec_groups", Sequence(Text("{"), Text("}"), entries, true, false)))
		}
		if initializer := definition.Initializer; initializer != nil {
			arguments = append(arguments, starlarkKeywordArgument("initializer", sr.renderFunction(initializer)))
		}
		if len(definition.Provides) > 0 {
			arguments = append(arguments, starlarkKeywordArgument("provides", starlarkIdentifierList("provider", definition.Provides)))
		}
		if len(definition.SubruleIdentifiers) > 0 {
			arguments = append(arguments, starlarkKeywordArgument("subrules", starlarkIdentifierList("subrule", definition.SubruleIdentifiers)))
		}
		return starlarkDefinitionCall("rule", "", arguments)
	default:
		return starlarkOpaque("rule")
	}
}

func (sr *starlarkRenderer) renderSubrule(subrule model_core.Message[*model_starlark_pb.Subrule, object.LocalReference]) Doc {
	switch kind := subrule.Message.GetKind().(type) {
	case *model_starlark_pb.Subrule_Reference:
		return starlarkOpaqueIdentifier("subrule", kind.Reference)
	case *model_starlark_pb.Subrule_Definition_:
		definition := kind.Definition
		var arguments []Doc
		if implementation := definition.Implementation; implementation != nil {
			arguments = append(arguments, starlarkKeywordArgument("implementation", sr.renderFunction(implementation)))
		}
		if len(definition.Attrs) > 0 {
			arguments = append(arguments, starlarkKeywordArgument("attrs", sr.renderNamedAttrs(model_core.Nested(subrule, definition.Attrs))))
		}
		if len(definition.SubruleIdentifiers) > 0 {
			arguments = append(arguments, starlarkKeywordArgument("subrules", starlarkIdentifierList("subrule", definition.SubruleIdentifiers)))
		}
		return starlarkDefinitionCall("subrule", "", arguments)
	default:
		return starlarkOpaque("subrule")
	}
}

func (sr *starlarkRenderer) renderTagClass(tagClass model_core.Message[*model_starlark_pb.TagClass, object.LocalReference]) Doc {
	var arguments []Doc
	if attrs := tagClass.Message.GetAttrs(); len(attrs) > 0 {
		arguments = append(arguments, starlarkKeywordArgument("attrs", sr.renderNamedAttrs(model_core.Nested(tagClass, attrs))))
	}
	return starlarkDefinitionCall("tag_class", "", arguments)
}

func (sr *starlarkRenderer) renderUserDefinedTransition(transition *model_starlark_pb.Transition_UserDefined) Doc {
	switch kind := transition.GetKind().(type) {
	case *model_starlark_pb.Transition_UserDefined_Identifier:
		return starlarkOpaqueIdentifier("transition", kind.Identifier)
	case *model_starlark_pb.Transition_UserDefined_Definition_:
		definition := kind.Definition
		var arguments []Doc
		if implementation := definition.Implementation; implementation != nil {
			arguments = append(arguments, starlarkKeywordArgument("implementation", sr.renderFunction(implementation)))
		}
		arguments = append(
			arguments,
			starlarkKeywordArgument("inputs", starlarkStringList(definition.Inputs)),
			starlarkKeywordArgument("outputs", starlarkStringList(definition.Outputs)),
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
	var sr starlarkRenderer
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
