package main

import (
	"fmt"
	"log"
	"net/http"
	"path"
	"slices"
	"time"

	model_core "bonanza.build/pkg/model/core"
	browser_pb "bonanza.build/pkg/proto/browser"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	"bonanza.build/pkg/storage/object"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// messagePrettyRenderer renders the decoded payload of an object
// as a pretty-printed object intended for humans. It assumes the
// contents are a Protobuf message that can be converted to JSON.
type messagePrettyRenderer struct{}

var _ payloadRenderer = messagePrettyRenderer{}

func (messagePrettyRenderer) queryParameter() string { return "pretty" }
func (messagePrettyRenderer) name() string           { return "Pretty" }

func (messagePrettyRenderer) render(r *http.Request, o model_core.Decodable[*object.Contents], recentlyObservedEncoders []*browser_pb.RecentlyObservedEncoder) ([]g.Node, int, []*browser_pb.RecentlyObservedEncoder) {
	decodedObject, usedEncoderIndex, err := decodeObject(o, recentlyObservedEncoders)
	if err != nil {
		return renderErrorAlert(err), 0, nil
	}

	messageTypeStr := r.PathValue("message_type")
	messageType, err := protoregistry.GlobalTypes.FindMessageByName(protoreflect.FullName(messageTypeStr))
	if err != nil {
		return renderErrorAlert(fmt.Errorf("invalid message type %#v: %w", messageTypeStr, err)), usedEncoderIndex, nil
	}

	message := messageType.New()
	if err := proto.Unmarshal(decodedObject, message.Interface()); err != nil {
		return renderErrorAlert(fmt.Errorf("failed to unmarshal message: %w", err)), usedEncoderIndex, nil
	}
	referenceFormat := o.Value.GetReferenceFormat()
	d := messageJSONRenderer{
		basePath:        "../..",
		referenceFormat: &referenceFormat,
		now:             time.Now(),
		customRenderer:  renderMessagePretty,
	}
	rendered := d.renderTopLevelMessage(model_core.NewTopLevelMessage(message, o.Value))
	return rendered, usedEncoderIndex, d.observedEncoders
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

	return nil
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

	var link string
	switch format := objectFormat.GetFormat().(type) {
	case *model_core_pb.ObjectFormat_Raw:

		link = path.Join(basePath, rawReference, "raw")

	case *model_core_pb.ObjectFormat_ProtoTypeName:

		link = path.Join(basePath, rawReference, "proto", format.ProtoTypeName)

	case *model_core_pb.ObjectFormat_ProtoListTypeName:

		link = path.Join(basePath, rawReference, "proto_list", format.ProtoListTypeName)

	default:

		return []g.Node{
			h.Span(
				h.Class("text-red-600"),
				g.Text("[ Reference field with unknown object format type ]"),
			),
		}
	}

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
	}

	slices.Sort(names)
	firstPrefix := true
	getPathNodes := func(path string) []g.Node {
		if firstPrefix {
			firstPrefix = false
			return []g.Node{g.Text(path)}
		} else {
			return []g.Node{
				h.Span(
					h.Class("text-gray-500"),
					g.Text(path),
				),
			}
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
				pathNodes = append(pathNodes,
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
				res = append(res, renderDirectoryPretty(r, model_core.Nested(dirMessage, contents.ContentsInline), path+file.Name+"/")...)
			}

		case *model_filesystem_pb.LeavesReference:
			pathNodes := getPathNodes(path)
			pathNodes = append(pathNodes,
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
			switch reference := file.Properties.Contents.Level.(type) {
			case *model_filesystem_pb.FileContents_ChunkReference:
				pathNodes = append(pathNodes,
					renderReferenceLinkPretty(
						r.basePath,
						model_core.Nested(dirMessage, reference.ChunkReference),
						file.Properties.Contents, "chunk_reference",
						file.Name,
					)...,
				)

			case *model_filesystem_pb.FileContents_FileContentsListReference:
				pathNodes = append(pathNodes,
					renderReferenceLinkPretty(
						r.basePath,
						model_core.Nested(dirMessage, reference.FileContentsListReference),
						file.Properties.Contents, "file_contents_list_reference",
						file.Name,
					)...,
				)
			}

			res = append(res, h.Tr(
				h.Td(g.Text(prefix)),
				h.Td(g.Text(prettySize(file.Properties.Contents.TotalSizeBytes))),
				h.Td(pathNodes...),
			))

		case *model_filesystem_pb.SymlinkNode:
			pathNodes := getPathNodes(path)
			pathNodes = append(pathNodes, g.Text(file.Name))
			pathNodes = append(pathNodes,
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
	units := []string{"b", "KiB", "MiB", "GiB", "TiB", "PiB"}
	for bytes >= 1024 && unit+1 < len(units) {
		bytes /= 1024
		unit++
	}
	return fmt.Sprintf("%d %s", bytes, units[unit])
}
