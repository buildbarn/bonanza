package main

import (
	"fmt"
	"log"
	"math/big"
	"net/http"
	"path"
	"slices"
	"strings"

	"bonanza.build/pkg/model/core"
	model_core "bonanza.build/pkg/model/core"
	browser_pb "bonanza.build/pkg/proto/browser"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/storage/object"

	"go.starlark.net/syntax"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// messagePrettyRenderer renders the decoded payload of an object
// as a pretty-printed object intended for humans. It assumes the
// contents are a Protobuf message that can be converted to JSON.
type messagePrettyRenderer struct {
	jsonRenderer messageJSONPayloadRenderer
}

var _ payloadRenderer = messagePrettyRenderer{}

func (messagePrettyRenderer) queryParameter() string { return "pretty" }
func (messagePrettyRenderer) name() string           { return "Pretty" }

func (s messagePrettyRenderer) render(r *http.Request, o model_core.Decodable[*object.Contents], recentlyObservedEncoders []*browser_pb.RecentlyObservedEncoder) ([]g.Node, int, []*browser_pb.RecentlyObservedEncoder) {
	return s.jsonRenderer.render(r, o, recentlyObservedEncoders)
}

// messagePrettyListRenderer renders the decoded payload of an object as a
// pretty-printed object intended for humans. It assumes the contents are a
// varint separated list of Protobuf messages that can be converted to JSON.
type messageListPrettyRenderer struct {
	jsonRenderer messageListJSONPayloadRenderer
}

var _ payloadRenderer = messageListPrettyRenderer{}

func (messageListPrettyRenderer) queryParameter() string { return "pretty" }
func (messageListPrettyRenderer) name() string           { return "Pretty" }

func (s messageListPrettyRenderer) render(r *http.Request, o model_core.Decodable[*object.Contents], recentlyObservedEncoders []*browser_pb.RecentlyObservedEncoder) ([]g.Node, int, []*browser_pb.RecentlyObservedEncoder) {
	return s.jsonRenderer.render(r, o, recentlyObservedEncoders)
}

func renderMessagePretty(r *messageJSONRenderer, m model_core.Message[protoreflect.Message, object.LocalReference], fields map[string][]g.Node) []g.Node {
	switch v := m.Message.Interface().(type) {
	case *model_filesystem_pb.DirectoryContents:
		return []g.Node{
			h.Table(
				h.THead(
					h.Tr(
						h.Th(g.Text("Type")),
						h.Th(g.Text("Size")),
						h.Th(g.Text("Path")),
					),
				),
				h.TBody(renderDirectoryPretty(r, model_core.Nested(m, v), "")...),
			),
		}
	}

	return renderStarlarkPretty(r, model_core.Nested(m, m.Message.Interface()))
}

func directoryErrorf(fmt string, args ...any) g.Node {
	return h.Tr(
		h.Td(
			h.ColSpan("3"),
			h.Class("text-red-600"),
			g.Textf(fmt, args...),
		),
	)
}

func renderReferenceLinkPretty(basePath string, message model_core.Message[*model_core_pb.DecodableReference, object.LocalReference], outerMessage proto.Message, fieldName, fileName string) []g.Node {
	reference, err := model_core.FlattenDecodableReference(message)
	if err != nil {
		return []g.Node{
			h.Span(
				h.Class("whitespace-nowrap"),
				g.Text(fileName),
			),
		}
	}

	fieldDescriptor := outerMessage.ProtoReflect().Descriptor().Fields().ByTextName(fieldName)
	if fieldDescriptor == nil {
		for i := range outerMessage.ProtoReflect().Descriptor().Fields().Len() {
			log.Printf("have: %s", outerMessage.ProtoReflect().Descriptor().Fields().Get(i).TextName())
		}
		log.Panicf("no field descriptor in message by name %s", fieldName)
	}
	fieldOptions := fieldDescriptor.Options().(*descriptorpb.FieldOptions)
	objectFormat := proto.GetExtension(fieldOptions, model_core_pb.E_ObjectFormat).(*model_core_pb.ObjectFormat)

	rawReference := model_core.DecodableLocalReferenceToString(reference)

	if objectFormat == nil {
		// Field is a valid reference, but there is no type
		// information. Just show the reference without turning
		// it into a link.
		return []g.Node{
			h.Span(
				h.Class("whitespace-nowrap"),
				g.Text(fileName),
			),
		}
	}

	segments, ok := core.ObjectFormatToPath(objectFormat)
	if !ok {
		return []g.Node{
			h.Span(
				h.Class("text-red-600"),
				g.Text("[ Reference field with unknown object format type ]"),
			),
		}
	}

	link := path.Join(append([]string{basePath, rawReference}, segments...)...)
	return []g.Node{
		h.A(
			h.Class("link link-accent whitespace-nowrap"),
			h.Href(link+"?format=pretty"),
			g.Text(fileName),
		),
	}
}

func renderDirectoryPretty(r *messageJSONRenderer, dirMessage model_core.Message[*model_filesystem_pb.DirectoryContents, object.LocalReference], path string) []g.Node {
	var res []g.Node

	var names []string
	contents := map[string]any{}
	addEntry := func(name string, value any) {
		if _, ok := contents[name]; ok {
			res = append(res, directoryErrorf("duplicate entry %s", name))
		} else {
			contents[name] = value
			names = append(names, name)
		}
	}

	directory := dirMessage.Message

	for _, subdir := range directory.Directories {
		addEntry(subdir.Name, subdir)
	}
	switch leaves := directory.Leaves.(type) {
	case *model_filesystem_pb.DirectoryContents_LeavesExternal:
		addEntry("...", leaves)
	case *model_filesystem_pb.DirectoryContents_LeavesInline:
		for _, file := range leaves.LeavesInline.Files {
			addEntry(file.Name, file)
		}
		for _, symlink := range leaves.LeavesInline.Symlinks {
			addEntry(symlink.Name, symlink)
		}
	}

	if len(names) == 0 {
		// Directory has no contents - render it as just itself
		res = append(res, h.Tr(
			h.Td(g.Text("dr-x")),
			h.Td(),
			h.Td(g.Text(path)),
		))
		return res
	}

	slices.Sort(names)
	firstPrefix := true
	getPathNodes := func(path string) []g.Node {
		if firstPrefix {
			firstPrefix = false
			return []g.Node{g.Text(path)}
		}
		return []g.Node{
			h.Span(
				h.Class("text-gray-500"),
				g.Text(path),
			),
		}
	}

	for _, name := range names {
		file := contents[name]
		switch file := file.(type) {
		case *model_filesystem_pb.DirectoryNode:
			switch contents := file.Directory.Contents.(type) {
			case *model_filesystem_pb.Directory_ContentsExternal:
				pathNodes := getPathNodes(path)
				pathNodes = append(pathNodes, g.Text(name+"/"))
				pathNodes = append(
					pathNodes,
					renderReferenceLinkPretty(
						r.basePath,
						model_core.Nested(dirMessage, contents.ContentsExternal.Reference),
						contents.ContentsExternal, "reference",
						"...",
					)...,
				)

				res = append(res, h.Tr(
					h.Td(g.Text("dr-x")),
					h.Td(),
					h.Td(pathNodes...),
				))

			case *model_filesystem_pb.Directory_ContentsInline:
				children := renderDirectoryPretty(r, model_core.Nested(dirMessage, contents.ContentsInline), path+file.Name+"/")
				if len(children) > 0 {
					firstPrefix = true
				}
				res = append(res, children...)
			}

		case *model_filesystem_pb.LeavesReference:
			pathNodes := getPathNodes(path)
			pathNodes = append(
				pathNodes,
				renderReferenceLinkPretty(
					r.basePath,
					model_core.Nested(dirMessage, file.Reference),
					file, "reference",
					name,
				)...,
			)

			res = append(res, h.Tr(
				h.Td(g.Text("dr-x")),
				h.Td(),
				h.Td(pathNodes...),
			))

		case *model_filesystem_pb.FileNode:
			prefix := "-r--"
			if file.Properties.IsExecutable {
				prefix = "-r-x"
			}

			pathNodes := getPathNodes(path)
			totalSizeBytes := uint64(0)
			if contents := file.Properties.Contents; contents != nil {
				switch reference := contents.Level.(type) {
				case *model_filesystem_pb.FileContents_ChunkReference:
					pathNodes = append(
						pathNodes,
						renderReferenceLinkPretty(
							r.basePath,
							model_core.Nested(dirMessage, reference.ChunkReference),
							contents, "chunk_reference",
							file.Name,
						)...,
					)
				case *model_filesystem_pb.FileContents_List_:
					pathNodes = append(
						pathNodes,
						renderReferenceLinkPretty(
							r.basePath,
							model_core.Nested(dirMessage, reference.List.Reference),
							reference.List, "reference",
							file.Name,
						)...,
					)
				}
				totalSizeBytes = contents.TotalSizeBytes
			} else {
				pathNodes = append(pathNodes, g.Text(file.Name))
			}
			res = append(res, h.Tr(
				h.Td(g.Text(prefix)),
				h.Td(g.Text(prettySize(totalSizeBytes))),
				h.Td(pathNodes...),
			))

		case *model_filesystem_pb.SymlinkNode:
			pathNodes := getPathNodes(path)
			pathNodes = append(pathNodes, g.Text(file.Name))
			pathNodes = append(
				pathNodes,
				g.Text(" → "+file.Target),
			)

			res = append(res, h.Tr(
				h.Td(g.Text("lr-x")),
				h.Td(),
				h.Td(pathNodes...),
			))

		}
	}

	return res
}

func prettySize(bytes uint64) string {
	unit := 0
	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}
	for bytes >= 1024 && unit+1 < len(units) {
		bytes /= 1024
		unit++
	}
	return fmt.Sprintf("%d %s", bytes, units[unit])
}

// starlarkMaximumInlineWidth is the maximum number of characters a
// list, dict, function call, etc. may take up for it to be rendered on
// a single line. Anything longer is spread out across multiple lines,
// using one line per element.
const starlarkMaximumInlineWidth = 80

// starlarkNodes contains a fragment of a Starlark value that has been
// rendered as HTML.
type starlarkNodes struct {
	nodes []g.Node
	// The number of characters the fragment takes up if it can be
	// displayed on a single line, or -1 if it spans multiple lines.
	width int
}

func (n starlarkNodes) isInline() bool {
	return n.width >= 0
}

func starlarkText(s string) starlarkNodes {
	return starlarkNodes{
		nodes: []g.Node{g.Text(s)},
		width: len(s),
	}
}

func starlarkStyled(class, s string) starlarkNodes {
	return starlarkNodes{
		nodes: []g.Node{h.Span(h.Class(class), g.Text(s))},
		width: len(s),
	}
}

// starlarkLiteral renders a literal value, such as a string, number or
// boolean.
func starlarkLiteral(s string) starlarkNodes {
	return starlarkStyled("text-fuchsia-300", s)
}

// starlarkCallee renders the name of a function that is called, such
// as "struct", "depset" or the name of a provider.
func starlarkCallee(name, title string) starlarkNodes {
	attrs := []g.Node{h.Class("text-sky-300")}
	if title != "" {
		attrs = append(attrs, h.Title(title))
	}
	return starlarkNodes{
		nodes: []g.Node{h.Span(append(attrs, g.Text(name))...)},
		width: len(name),
	}
}

// starlarkOpaque renders a value that has no literal representation in
// Starlark, such as a rule or a function. These are displayed in the
// same way as Starlark's repr() would, using angle brackets.
func starlarkOpaque(s string) starlarkNodes {
	return starlarkStyled("text-gray-500", "<"+s+">")
}

func starlarkErrorf(format string, args ...any) starlarkNodes {
	return starlarkStyled("text-red-600", "[ "+fmt.Sprintf(format, args...)+" ]")
}

func starlarkConcat(parts ...starlarkNodes) starlarkNodes {
	var result starlarkNodes
	for _, part := range parts {
		result.nodes = append(result.nodes, part.nodes...)
		if result.width >= 0 {
			if part.width >= 0 {
				result.width += part.width
			} else {
				result.width = -1
			}
		}
	}
	return result
}

// starlarkKeywordArgument renders a keyword argument of a function
// call, or a field of a struct.
func starlarkKeywordArgument(name string, value starlarkNodes) starlarkNodes {
	return starlarkConcat(
		starlarkStyled("text-amber-200", name),
		starlarkText(" = "),
		value,
	)
}

// starlarkSequence renders a sequence of elements that are enclosed by
// delimiters, such as a list, a dict, or the arguments of a function
// call. Short sequences are rendered on a single line. Longer ones are
// rendered with one element per line, using trailing commas.
func starlarkSequence(open, close starlarkNodes, elements []starlarkNodes, singleElementNeedsComma bool) starlarkNodes {
	if len(elements) == 0 {
		return starlarkConcat(open, close)
	}

	width := open.width + close.width + 2*(len(elements)-1)
	if singleElementNeedsComma && len(elements) == 1 {
		width++
	}
	inline := open.isInline() && close.isInline()
	for _, element := range elements {
		if !element.isInline() {
			inline = false
			break
		}
		width += element.width
	}

	if inline && width <= starlarkMaximumInlineWidth {
		result := open
		result.nodes = append([]g.Node(nil), open.nodes...)
		for i, element := range elements {
			if i > 0 {
				result.nodes = append(result.nodes, g.Text(", "))
			}
			result.nodes = append(result.nodes, element.nodes...)
		}
		if singleElementNeedsComma && len(elements) == 1 {
			result.nodes = append(result.nodes, g.Text(","))
		}
		result.nodes = append(result.nodes, close.nodes...)
		result.width = width
		return result
	}

	listItems := make([]g.Node, 0, len(elements))
	for _, element := range elements {
		listItems = append(listItems, h.Li(slices.Concat(element.nodes, []g.Node{g.Text(",")})...))
	}
	result := starlarkNodes{width: -1}
	result.nodes = append(result.nodes, open.nodes...)
	result.nodes = append(result.nodes, h.Ul(listItems...))
	result.nodes = append(result.nodes, close.nodes...)
	return result
}

// starlarkCall renders a function call, such as struct(a = 1).
func starlarkCall(callee starlarkNodes, arguments []starlarkNodes) starlarkNodes {
	return starlarkSequence(starlarkConcat(callee, starlarkText("(")), starlarkText(")"), arguments, false)
}

func starlarkString(s string) starlarkNodes {
	return starlarkLiteral(syntax.Quote(s, false))
}

func starlarkBool(v bool) starlarkNodes {
	if v {
		return starlarkLiteral("True")
	}
	return starlarkLiteral("False")
}

func starlarkStringList(values []string) starlarkNodes {
	elements := make([]starlarkNodes, 0, len(values))
	for _, value := range values {
		elements = append(elements, starlarkString(value))
	}
	return starlarkSequence(starlarkText("["), starlarkText("]"), elements, false)
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
func starlarkOpaqueIdentifier(kind, identifier string) starlarkNodes {
	if identifier == "" {
		return starlarkOpaque(kind)
	}
	s := "<" + kind + " " + starlarkIdentifierName(identifier) + ">"
	return starlarkNodes{
		nodes: []g.Node{
			h.Span(
				h.Class("text-gray-500"),
				h.Title(identifier),
				g.Text(s),
			),
		},
		width: len(s),
	}
}

// starlarkPrettyRenderer renders messages that are part of the
// pkg/proto/model/starlark package using Starlark syntax.
type starlarkPrettyRenderer struct {
	basePath string
}

// renderReference renders a link to an object containing additional
// list elements, dict entries or struct fields. As these have no
// Starlark representation, the link is displayed in the same style as
// opaque values.
func (sr *starlarkPrettyRenderer) renderReference(reference model_core.Message[*model_core_pb.DecodableReference, object.LocalReference], outerMessage proto.Message, description string) starlarkNodes {
	text := "<" + description + ">"
	return starlarkNodes{
		nodes: renderReferenceLinkPretty(sr.basePath, reference, outerMessage, "reference", text),
		width: len(text),
	}
}

func (sr *starlarkPrettyRenderer) renderValue(value model_core.Message[*model_starlark_pb.Value, object.LocalReference]) starlarkNodes {
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
		return starlarkCall(starlarkCallee("Label", ""), []starlarkNodes{starlarkString(kind.Label)})
	case *model_starlark_pb.Value_List:
		return sr.renderListElements(starlarkText("["), starlarkText("]"), model_core.Nested(value, kind.List.GetElements()), false)
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
			[]starlarkNodes{
				sr.renderListElements(starlarkText("["), starlarkText("]"), model_core.Nested(value, kind.Set.GetElements()), false),
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
		elements := make([]starlarkNodes, 0, len(kind.Tuple.GetElements()))
		for _, element := range kind.Tuple.GetElements() {
			elements = append(elements, sr.renderValue(model_core.Nested(value, element)))
		}
		return starlarkSequence(starlarkText("("), starlarkText(")"), elements, true)
	default:
		return starlarkErrorf("Unknown value kind")
	}
}

func (sr *starlarkPrettyRenderer) renderAttr(attr model_core.Message[*model_starlark_pb.Attr, object.LocalReference]) starlarkNodes {
	typeField := attr.Message.ProtoReflect().WhichOneof(
		(&model_starlark_pb.Attr{}).ProtoReflect().Descriptor().Oneofs().ByName("type"),
	)
	if typeField == nil {
		return starlarkErrorf("Attribute has unknown type")
	}

	var arguments []starlarkNodes
	if defaultValue := attr.Message.GetDefault(); defaultValue != nil {
		arguments = append(arguments, starlarkKeywordArgument("default", sr.renderValue(model_core.Nested(attr, defaultValue))))
	}
	addLabelOptions := func(labelOptions *model_starlark_pb.Attr_LabelOptions) {
		if providers := labelOptions.GetProviders(); len(providers) > 0 {
			elements := make([]starlarkNodes, 0, len(providers))
			for _, provider := range providers {
				elements = append(elements, starlarkOpaqueIdentifier("provider", provider))
			}
			arguments = append(arguments, starlarkKeywordArgument("providers", starlarkSequence(starlarkText("["), starlarkText("]"), elements, false)))
		}
		if aspects := labelOptions.GetAspects(); len(aspects) > 0 {
			elements := make([]starlarkNodes, 0, len(aspects))
			for _, aspect := range aspects {
				elements = append(elements, starlarkOpaqueIdentifier("aspect", aspect))
			}
			arguments = append(arguments, starlarkKeywordArgument("aspects", starlarkSequence(starlarkText("["), starlarkText("]"), elements, false)))
		}
		if cfg := labelOptions.GetCfg(); cfg != nil {
			arguments = append(arguments, starlarkKeywordArgument("cfg", sr.renderTransition(cfg)))
		}
	}
	switch attrType := attr.Message.GetType().(type) {
	case *model_starlark_pb.Attr_Int:
		if values := attrType.Int.Values; len(values) > 0 {
			elements := make([]starlarkNodes, 0, len(values))
			for _, value := range values {
				elements = append(elements, starlarkLiteral(fmt.Sprintf("%d", value)))
			}
			arguments = append(arguments, starlarkKeywordArgument("values", starlarkSequence(starlarkText("["), starlarkText("]"), elements, false)))
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

func (sr *starlarkPrettyRenderer) renderDepset(depset model_core.Message[*model_starlark_pb.Depset, object.LocalReference]) starlarkNodes {
	arguments := []starlarkNodes{
		sr.renderListElements(starlarkText("["), starlarkText("]"), model_core.Nested(depset, depset.Message.GetElements()), false),
	}
	if order := depset.Message.GetOrder(); order != model_starlark_pb.Depset_DEFAULT {
		arguments = append(arguments, starlarkKeywordArgument("order", starlarkString(strings.ToLower(order.String()))))
	}
	return starlarkCall(starlarkCallee("depset", ""), arguments)
}

func (sr *starlarkPrettyRenderer) renderDictEntry(entry model_core.Message[*model_starlark_pb.Dict_Entry, object.LocalReference]) starlarkNodes {
	switch level := entry.Message.GetLevel().(type) {
	case *model_starlark_pb.Dict_Entry_Leaf_:
		return starlarkConcat(
			sr.renderValue(model_core.Nested(entry, level.Leaf.GetKey())),
			starlarkText(": "),
			sr.renderValue(model_core.Nested(entry, level.Leaf.GetValue())),
		)
	case *model_starlark_pb.Dict_Entry_Parent_:
		return sr.renderReference(model_core.Nested(entry, level.Parent.Reference), level.Parent, "more entries")
	default:
		return starlarkErrorf("Unknown dict entry level")
	}
}

func (sr *starlarkPrettyRenderer) renderDictEntries(entries model_core.Message[[]*model_starlark_pb.Dict_Entry, object.LocalReference]) starlarkNodes {
	elements := make([]starlarkNodes, 0, len(entries.Message))
	for _, entry := range entries.Message {
		elements = append(elements, sr.renderDictEntry(model_core.Nested(entries, entry)))
	}
	return starlarkSequence(starlarkText("{"), starlarkText("}"), elements, false)
}

func (sr *starlarkPrettyRenderer) renderExecGroup(execGroup *model_starlark_pb.ExecGroup) starlarkNodes {
	var arguments []starlarkNodes
	if len(execGroup.GetExecCompatibleWith()) > 0 {
		arguments = append(arguments, starlarkKeywordArgument("exec_compatible_with", starlarkStringList(execGroup.GetExecCompatibleWith())))
	}
	if len(execGroup.GetToolchains()) > 0 {
		toolchains := make([]starlarkNodes, 0, len(execGroup.GetToolchains()))
		for _, toolchain := range execGroup.GetToolchains() {
			toolchains = append(toolchains, sr.renderToolchainType(toolchain))
		}
		arguments = append(arguments, starlarkKeywordArgument("toolchains", starlarkSequence(starlarkText("["), starlarkText("]"), toolchains, false)))
	}
	return starlarkCall(starlarkCallee("exec_group", ""), arguments)
}

func (sr *starlarkPrettyRenderer) renderFile(file model_core.Message[*model_starlark_pb.File, object.LocalReference]) starlarkNodes {
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
	description := starlarkStyled("text-gray-500", "<"+kind+file.Message.GetLabel())
	if configurationReference := owner.ConfigurationReference; configurationReference != nil {
		// Provide a link to the configuration in which the
		// file is built.
		description = starlarkConcat(
			description,
			starlarkStyled("text-gray-500", " in configuration "),
			starlarkNodes{
				nodes: renderReferenceLinkPretty(sr.basePath, model_core.Nested(file, configurationReference), owner, "configuration_reference", "..."),
				width: len("..."),
			},
		)
	}
	return starlarkConcat(description, starlarkStyled("text-gray-500", ">"))
}

func (*starlarkPrettyRenderer) renderFunction(function *model_starlark_pb.Function) starlarkNodes {
	s := fmt.Sprintf("<function %s>", function.GetName())
	return starlarkNodes{
		nodes: []g.Node{
			h.Span(
				h.Class("text-gray-500"),
				h.Title(fmt.Sprintf("%s:%d:%d", function.GetFilename(), function.GetLine(), function.GetColumn())),
				g.Text(s),
			),
		},
		width: len(s),
	}
}

func (*starlarkPrettyRenderer) renderInt(v *model_starlark_pb.Int) starlarkNodes {
	var i big.Int
	i.SetBytes(v.GetAbsoluteValue())
	if v.GetNegative() {
		i.Neg(&i)
	}
	return starlarkLiteral(i.String())
}

func (sr *starlarkPrettyRenderer) renderListElement(element model_core.Message[*model_starlark_pb.List_Element, object.LocalReference]) starlarkNodes {
	switch level := element.Message.GetLevel().(type) {
	case *model_starlark_pb.List_Element_Leaf:
		return sr.renderValue(model_core.Nested(element, level.Leaf))
	case *model_starlark_pb.List_Element_Parent_:
		return sr.renderListElementParent(model_core.Nested(element, level.Parent))
	default:
		return starlarkErrorf("Unknown list element level")
	}
}

func (sr *starlarkPrettyRenderer) renderListElementParent(parent model_core.Message[*model_starlark_pb.List_Element_Parent, object.LocalReference]) starlarkNodes {
	description := fmt.Sprintf("%d more elements", parent.Message.Count)
	if parent.Message.Count == 1 {
		description = "1 more element"
	}
	return sr.renderReference(model_core.Nested(parent, parent.Message.Reference), parent.Message, description)
}

func (sr *starlarkPrettyRenderer) renderListElements(open, close starlarkNodes, elements model_core.Message[[]*model_starlark_pb.List_Element, object.LocalReference], singleElementNeedsComma bool) starlarkNodes {
	renderedElements := make([]starlarkNodes, 0, len(elements.Message))
	for _, element := range elements.Message {
		renderedElements = append(renderedElements, sr.renderListElement(model_core.Nested(elements, element)))
	}
	return starlarkSequence(open, close, renderedElements, singleElementNeedsComma)
}

func (sr *starlarkPrettyRenderer) renderSelectGroup(group model_core.Message[*model_starlark_pb.Select_Group, object.LocalReference]) starlarkNodes {
	conditions := group.Message.GetConditions()
	if len(conditions) == 0 {
		if noMatchValue, ok := group.Message.GetNoMatch().(*model_starlark_pb.Select_Group_NoMatchValue); ok {
			// Group consisting of a single value that is not
			// wrapped in a select().
			return sr.renderValue(model_core.Nested(group, noMatchValue.NoMatchValue))
		}
	}

	entries := make([]starlarkNodes, 0, len(conditions)+1)
	for _, condition := range conditions {
		entries = append(
			entries,
			starlarkConcat(
				starlarkString(condition.GetConditionIdentifier()),
				starlarkText(": "),
				sr.renderValue(model_core.Nested(group, condition.GetValue())),
			),
		)
	}
	arguments := make([]starlarkNodes, 0, 2)
	switch noMatch := group.Message.GetNoMatch().(type) {
	case *model_starlark_pb.Select_Group_NoMatchValue:
		entries = append(
			entries,
			starlarkConcat(
				starlarkString("//conditions:default"),
				starlarkText(": "),
				sr.renderValue(model_core.Nested(group, noMatch.NoMatchValue)),
			),
		)
		arguments = append(arguments, starlarkSequence(starlarkText("{"), starlarkText("}"), entries, false))
	case *model_starlark_pb.Select_Group_NoMatchError:
		arguments = append(
			arguments,
			starlarkSequence(starlarkText("{"), starlarkText("}"), entries, false),
			starlarkKeywordArgument("no_match_error", starlarkString(noMatch.NoMatchError)),
		)
	default:
		arguments = append(arguments, starlarkSequence(starlarkText("{"), starlarkText("}"), entries, false))
	}
	return starlarkCall(starlarkCallee("select", ""), arguments)
}

func (sr *starlarkPrettyRenderer) renderSelect(selectValue model_core.Message[*model_starlark_pb.Select, object.LocalReference]) starlarkNodes {
	operator := " + "
	if selectValue.Message.GetConcatenationOperator() == model_starlark_pb.Select_PIPE {
		operator = " | "
	}

	groups := selectValue.Message.GetGroups()
	renderedGroups := make([]starlarkNodes, 0, len(groups))
	inline := true
	for _, group := range groups {
		renderedGroup := sr.renderSelectGroup(model_core.Nested(selectValue, group))
		inline = inline && renderedGroup.isInline()
		renderedGroups = append(renderedGroups, renderedGroup)
	}
	if len(renderedGroups) == 1 {
		return renderedGroups[0]
	}

	if inline {
		result := starlarkNodes{}
		for i, renderedGroup := range renderedGroups {
			if i > 0 {
				result = starlarkConcat(result, starlarkText(operator))
			}
			result = starlarkConcat(result, renderedGroup)
		}
		if result.width <= starlarkMaximumInlineWidth {
			return result
		}
	}

	// Place each operand on a separate line.
	listItems := make([]g.Node, 0, len(renderedGroups))
	for i, renderedGroup := range renderedGroups {
		nodes := renderedGroup.nodes
		if i > 0 {
			nodes = slices.Concat([]g.Node{g.Text(strings.TrimLeft(operator, " "))}, nodes)
		}
		listItems = append(listItems, h.Li(nodes...))
	}
	return starlarkNodes{
		nodes: []g.Node{g.Text("("), h.Ul(listItems...), g.Text(")")},
		width: -1,
	}
}

func (sr *starlarkPrettyRenderer) renderStructFields(callee starlarkNodes, fields model_core.Message[*model_starlark_pb.Struct_Fields, object.LocalReference]) starlarkNodes {
	keys := fields.Message.GetKeys()
	values := fields.Message.GetValues()
	arguments := make([]starlarkNodes, 0, len(values))
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

func (sr *starlarkPrettyRenderer) renderStruct(structValue model_core.Message[*model_starlark_pb.Struct, object.LocalReference]) starlarkNodes {
	callee := starlarkCallee("struct", "")
	if properties := structValue.Message.GetProviderInstanceProperties(); properties != nil {
		// Instance of a provider. Use the name of the provider
		// as the name of the function that is called.
		callee = starlarkCallee(starlarkIdentifierName(properties.ProviderIdentifier), properties.ProviderIdentifier)
	}
	return sr.renderStructFields(callee, model_core.Nested(structValue, structValue.Message.GetFields()))
}

func (sr *starlarkPrettyRenderer) renderTargetReference(targetReference model_core.Message[*model_starlark_pb.TargetReference, object.LocalReference]) starlarkNodes {
	configured := targetReference.Message.GetConfigured()
	if configured == nil {
		return starlarkOpaque("unconfigured target " + targetReference.Message.GetOriginalLabel())
	}

	open := starlarkStyled("text-gray-500", "<target "+configured.Label)
	close := starlarkStyled("text-gray-500", ">")
	if len(configured.Providers) == 0 {
		return starlarkConcat(open, close)
	}
	providers := make([]starlarkNodes, 0, len(configured.Providers))
	for _, provider := range configured.Providers {
		providers = append(providers, sr.renderStruct(model_core.Nested(targetReference, provider)))
	}
	return starlarkSequence(
		starlarkConcat(open, starlarkText(", providers = [")),
		starlarkConcat(starlarkText("]"), close),
		providers,
		false,
	)
}

func (*starlarkPrettyRenderer) renderToolchainType(toolchainType *model_starlark_pb.ToolchainType) starlarkNodes {
	arguments := []starlarkNodes{starlarkString(toolchainType.GetToolchainType())}
	if !toolchainType.GetMandatory() {
		arguments = append(arguments, starlarkKeywordArgument("mandatory", starlarkBool(false)))
	}
	return starlarkCall(starlarkCallee("config_common.toolchain_type", ""), arguments)
}

func (sr *starlarkPrettyRenderer) renderTransition(transition *model_starlark_pb.Transition) starlarkNodes {
	switch kind := transition.GetKind().(type) {
	case *model_starlark_pb.Transition_ExecGroup:
		var arguments []starlarkNodes
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
func starlarkIdentifierList(kind string, identifiers []string) starlarkNodes {
	elements := make([]starlarkNodes, 0, len(identifiers))
	for _, identifier := range identifiers {
		elements = append(elements, starlarkOpaqueIdentifier(kind, identifier))
	}
	return starlarkSequence(starlarkText("["), starlarkText("]"), elements, false)
}

// starlarkDefinitionCall renders a call to a function that declares a
// rule, provider, etc. If the canonical identifier under which the
// object is declared is known, it is shown when hovering the name of
// the function.
func starlarkDefinitionCall(function, identifier string, arguments []starlarkNodes) starlarkNodes {
	return starlarkCall(starlarkCallee(function, identifier), arguments)
}

// renderNamedAttrs renders the attributes of a rule, repository rule,
// subrule or tag class as a dict, as would be passed to the attrs
// argument of rule(), repository_rule(), etc.
func (sr *starlarkPrettyRenderer) renderNamedAttrs(attrs model_core.Message[[]*model_starlark_pb.NamedAttr, object.LocalReference]) starlarkNodes {
	entries := make([]starlarkNodes, 0, len(attrs.Message))
	for _, namedAttr := range attrs.Message {
		entries = append(
			entries,
			starlarkConcat(
				starlarkString(namedAttr.GetName()),
				starlarkText(": "),
				sr.renderAttr(model_core.Nested(attrs, namedAttr.GetAttr())),
			),
		)
	}
	return starlarkSequence(starlarkText("{"), starlarkText("}"), entries, false)
}

func (*starlarkPrettyRenderer) renderBuildSetting(buildSetting *model_starlark_pb.BuildSetting) starlarkNodes {
	typeField := buildSetting.ProtoReflect().WhichOneof(
		(&model_starlark_pb.BuildSetting{}).ProtoReflect().Descriptor().Oneofs().ByName("type"),
	)
	if typeField == nil {
		return starlarkErrorf("Build setting has unknown type")
	}
	var arguments []starlarkNodes
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

func (sr *starlarkPrettyRenderer) renderModuleExtension(moduleExtension model_core.Message[*model_starlark_pb.ModuleExtension, object.LocalReference]) starlarkNodes {
	var arguments []starlarkNodes
	if implementation := moduleExtension.Message.GetImplementation(); implementation != nil {
		arguments = append(arguments, starlarkKeywordArgument("implementation", sr.renderFunction(implementation)))
	}
	if tagClasses := moduleExtension.Message.GetTagClasses(); len(tagClasses) > 0 {
		entries := make([]starlarkNodes, 0, len(tagClasses))
		for _, namedTagClass := range tagClasses {
			entries = append(
				entries,
				starlarkConcat(
					starlarkString(namedTagClass.Name),
					starlarkText(": "),
					sr.renderTagClass(model_core.Nested(moduleExtension, namedTagClass.TagClass)),
				),
			)
		}
		arguments = append(arguments, starlarkKeywordArgument("tag_classes", starlarkSequence(starlarkText("{"), starlarkText("}"), entries, false)))
	}
	return starlarkDefinitionCall("module_extension", "", arguments)
}

func (sr *starlarkPrettyRenderer) renderProvider(provider model_core.Message[*model_starlark_pb.Provider, object.LocalReference]) starlarkNodes {
	properties := provider.Message.GetInstanceProperties()
	var arguments []starlarkNodes
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
		entries := make([]starlarkNodes, 0, len(computedFields))
		for _, computedField := range computedFields {
			entries = append(
				entries,
				starlarkConcat(
					starlarkString(computedField.Name),
					starlarkText(": "),
					sr.renderFunction(computedField.Function),
				),
			)
		}
		arguments = append(arguments, starlarkKeywordArgument("computed_fields", starlarkSequence(starlarkText("{"), starlarkText("}"), entries, false)))
	}
	if properties.GetDictLike() {
		arguments = append(arguments, starlarkKeywordArgument("dict_like", starlarkBool(true)))
	}
	if typeName := properties.GetTypeName(); typeName != "" {
		arguments = append(arguments, starlarkKeywordArgument("type_name", starlarkString(typeName)))
	}
	return starlarkDefinitionCall("provider", properties.GetProviderIdentifier(), arguments)
}

func (sr *starlarkPrettyRenderer) renderRepositoryRule(repositoryRule model_core.Message[*model_starlark_pb.RepositoryRule, object.LocalReference]) starlarkNodes {
	switch kind := repositoryRule.Message.GetKind().(type) {
	case *model_starlark_pb.RepositoryRule_Reference:
		return starlarkOpaqueIdentifier("repository_rule", kind.Reference)
	case *model_starlark_pb.RepositoryRule_Definition_:
		definition := kind.Definition
		var arguments []starlarkNodes
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

func (sr *starlarkPrettyRenderer) renderRule(rule model_core.Message[*model_starlark_pb.Rule, object.LocalReference]) starlarkNodes {
	switch kind := rule.Message.GetKind().(type) {
	case *model_starlark_pb.Rule_Reference:
		return starlarkOpaqueIdentifier("rule", kind.Reference)
	case *model_starlark_pb.Rule_Definition_:
		definition := kind.Definition
		var arguments []starlarkNodes
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
			entries := make([]starlarkNodes, 0, len(definition.ExecGroups))
			for _, namedExecGroup := range definition.ExecGroups {
				entries = append(
					entries,
					starlarkConcat(
						starlarkString(namedExecGroup.Name),
						starlarkText(": "),
						sr.renderExecGroup(namedExecGroup.ExecGroup),
					),
				)
			}
			arguments = append(arguments, starlarkKeywordArgument("exec_groups", starlarkSequence(starlarkText("{"), starlarkText("}"), entries, false)))
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

func (sr *starlarkPrettyRenderer) renderSubrule(subrule model_core.Message[*model_starlark_pb.Subrule, object.LocalReference]) starlarkNodes {
	switch kind := subrule.Message.GetKind().(type) {
	case *model_starlark_pb.Subrule_Reference:
		return starlarkOpaqueIdentifier("subrule", kind.Reference)
	case *model_starlark_pb.Subrule_Definition_:
		definition := kind.Definition
		var arguments []starlarkNodes
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

func (sr *starlarkPrettyRenderer) renderTagClass(tagClass model_core.Message[*model_starlark_pb.TagClass, object.LocalReference]) starlarkNodes {
	var arguments []starlarkNodes
	if attrs := tagClass.Message.GetAttrs(); len(attrs) > 0 {
		arguments = append(arguments, starlarkKeywordArgument("attrs", sr.renderNamedAttrs(model_core.Nested(tagClass, attrs))))
	}
	return starlarkDefinitionCall("tag_class", "", arguments)
}

func (sr *starlarkPrettyRenderer) renderUserDefinedTransition(transition *model_starlark_pb.Transition_UserDefined) starlarkNodes {
	switch kind := transition.GetKind().(type) {
	case *model_starlark_pb.Transition_UserDefined_Identifier:
		return starlarkOpaqueIdentifier("transition", kind.Identifier)
	case *model_starlark_pb.Transition_UserDefined_Definition_:
		definition := kind.Definition
		var arguments []starlarkNodes
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

// renderStarlarkPretty renders messages declared in
// pkg/proto/model/starlark using Starlark syntax. It returns nil if the
// message is not one that has a sensible Starlark representation.
func renderStarlarkPretty(r *messageJSONRenderer, m model_core.Message[proto.Message, object.LocalReference]) []g.Node {
	sr := starlarkPrettyRenderer{basePath: r.basePath}
	var result starlarkNodes
	switch v := m.Message.(type) {
	case *model_starlark_pb.Attr:
		result = sr.renderAttr(model_core.Nested(m, v))
	case *model_starlark_pb.Dict_Entry:
		result = sr.renderDictEntry(model_core.Nested(m, v))
	case *model_starlark_pb.File:
		result = sr.renderFile(model_core.Nested(m, v))
	case *model_starlark_pb.List_Element:
		result = sr.renderListElement(model_core.Nested(m, v))
	case *model_starlark_pb.Select_Group:
		result = sr.renderSelectGroup(model_core.Nested(m, v))
	case *model_starlark_pb.Struct:
		result = sr.renderStruct(model_core.Nested(m, v))
	case *model_starlark_pb.Struct_Fields:
		result = sr.renderStructFields(starlarkCallee("struct", ""), model_core.Nested(m, v))
	case *model_starlark_pb.ToolchainType:
		result = sr.renderToolchainType(v)
	case *model_starlark_pb.Transition:
		result = sr.renderTransition(v)
	case *model_starlark_pb.Value:
		result = sr.renderValue(model_core.Nested(m, v))
	default:
		return nil
	}
	return result.nodes
}
