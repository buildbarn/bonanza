package main

import (
	"path"
	"strconv"
	"time"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/render"
	browser_pb "bonanza.build/pkg/proto/browser"
	model_encoding_pb "bonanza.build/pkg/proto/model/encoding"
	"bonanza.build/pkg/storage/object"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"

	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// messageRenderWidth is the maximum number of characters that lines of
// rendered messages may contain. Lists, objects, function calls, etc.
// that are longer are spread out across multiple lines.
const messageRenderWidth = 100

var binaryEncoderDescriptor = (&model_encoding_pb.BinaryEncoder{}).ProtoReflect().Descriptor()

// messageRenderer renders Protobuf messages as HTML, either as JSON or
// pretty printed. Any references to other objects contained in these
// messages are rendered as clickable links.
type messageRenderer struct {
	renderer render.Renderer
	// Path relative to which links to objects are emitted. If
	// empty, references are not turned into links.
	basePath string
	// Format of the payload to request when following links.
	linkFormat string

	now              time.Time
	observedEncoders []*browser_pb.RecentlyObservedEncoder
}

func newMessageRenderer(basePath string, referenceFormat *object.ReferenceFormat, pretty bool) *messageRenderer {
	mr := &messageRenderer{
		renderer: render.Renderer{
			Pretty:          pretty,
			ReferenceFormat: referenceFormat,
		},
		basePath:   basePath,
		linkFormat: "json",
		now:        time.Now(),
	}
	if pretty {
		mr.linkFormat = "pretty"
	}
	mr.renderer.OnMessage = mr.observeEncoders
	return mr
}

// observeEncoders is called into for every message that is rendered.
// If the message contains any BinaryEncoder configurations, they are
// recorded, so that they can be offered in the encoder selector.
func (mr *messageRenderer) observeEncoders(m protoreflect.Message) {
	m.Range(func(fieldDescriptor protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		if fieldDescriptor.IsList() && fieldDescriptor.Message() == binaryEncoderDescriptor {
			list := value.List()
			configuration := make([]*model_encoding_pb.BinaryEncoder, 0, list.Len())
			for i := range list.Len() {
				configuration = append(
					configuration,
					list.Get(i).Message().Interface().(*model_encoding_pb.BinaryEncoder),
				)
			}
			mr.observedEncoders = append(
				mr.observedEncoders,
				&browser_pb.RecentlyObservedEncoder{
					Configuration: configuration,
					LastObservation: &browser_pb.RecentlyObservedEncoder_LastObservation{
						Time:        timestamppb.New(mr.now),
						MessageType: string(m.Descriptor().Name()),
						FieldName:   fieldDescriptor.TextName(),
					},
				},
			)
		}
		return true
	})
}

func (mr *messageRenderer) renderTopLevelMessage(m model_core.TopLevelMessage[protoreflect.Message, object.LocalReference]) []g.Node {
	return mr.renderDoc(mr.renderer.TopLevelMessage(m))
}

func (mr *messageRenderer) renderMessage(m model_core.Message[protoreflect.Message, object.LocalReference]) []g.Node {
	return mr.renderDoc(mr.renderer.Message(m))
}

func (mr *messageRenderer) renderMessageList(list model_core.Message[[]protoreflect.Message, object.LocalReference]) []g.Node {
	return mr.renderDoc(mr.renderer.MessageList(list))
}

// renderDoc lays out a document and converts it to HTML. Indented lines
// are rendered as nested lists, which are styled to have guides that
// show the level of indentation.
func (mr *messageRenderer) renderDoc(doc render.Doc) []g.Node {
	blocks := render.Layout(doc, render.LayoutOptions{
		Width:       messageRenderWidth,
		IndentWidth: 4,
	})
	var nodes []g.Node
	for _, item := range mr.renderBlocks(blocks, 0) {
		nodes = append(nodes, h.Div(item...))
	}
	return nodes
}

// renderBlocks converts all blocks having an indentation level of at
// least the provided level to HTML. Each block with an indentation
// level equal to the provided level yields a list of nodes. Any blocks
// with a higher indentation level that follow it are added to it as a
// nested list.
func (mr *messageRenderer) renderBlocks(blocks []render.Block, level int) [][]g.Node {
	var items [][]g.Node
	for len(blocks) > 0 && blocks[0].Indent >= level {
		item := mr.renderBlock(blocks[0])
		blocks = blocks[1:]

		end := 0
		for end < len(blocks) && blocks[end].Indent > level {
			end++
		}
		if end > 0 {
			children := mr.renderBlocks(blocks[:end], level+1)
			listItems := make([]g.Node, 0, len(children))
			for _, child := range children {
				listItems = append(listItems, h.Li(child...))
			}
			item = append(item, h.Ul(listItems...))
			blocks = blocks[end:]
		}
		items = append(items, item)
	}
	return items
}

func (mr *messageRenderer) renderBlock(block render.Block) []g.Node {
	table := block.Table
	if table == nil {
		return mr.renderSegments(block.Segments)
	}

	// Use inline styles for alignment, as the stylesheet sets the
	// alignment of table headers with a more specific selector than
	// that of utility classes. Preserve whitespace, as values in
	// right aligned columns may be padded (e.g., "412   B").
	alignRight := h.Style("text-align: right; white-space: pre")
	headerCells := make([]g.Node, 0, len(table.Columns))
	for _, column := range table.Columns {
		headerNodes := []g.Node{g.Text(column.Name)}
		if column.AlignRight {
			headerNodes = append(headerNodes, alignRight)
		}
		headerCells = append(headerCells, h.Th(headerNodes...))
	}
	rows := make([]g.Node, 0, len(table.Rows))
	for _, row := range table.Rows {
		cells := make([]g.Node, 0, len(row))
		for i, cell := range row {
			cellNodes := mr.renderSegments(cell)
			if i == len(row)-1 && len(row) < len(table.Columns) {
				// Let the last cell span the remaining
				// columns.
				cellNodes = append(cellNodes, h.ColSpan(strconv.Itoa(len(table.Columns)-i)))
			} else if i < len(table.Columns) && table.Columns[i].AlignRight {
				cellNodes = append(cellNodes, alignRight)
			}
			cells = append(cells, h.Td(cellNodes...))
		}
		rows = append(rows, h.Tr(cells...))
	}
	return []g.Node{
		h.Table(
			h.THead(h.Tr(headerCells...)),
			h.TBody(rows...),
		),
	}
}

var roleClasses = map[render.Role]string{
	render.RoleFieldName: "text-amber-200",
	render.RoleLiteral:   "text-fuchsia-300",
	render.RoleCallee:    "text-sky-300",
	render.RoleDim:       "text-gray-500",
	render.RoleError:     "text-red-600",
}

func (mr *messageRenderer) renderSegments(segments []render.Segment) []g.Node {
	nodes := make([]g.Node, 0, len(segments))
	for _, segment := range segments {
		var attributes []g.Node
		if segment.Title != "" {
			attributes = append(attributes, h.Title(segment.Title))
		}

		if link := segment.Link; link != nil && mr.basePath != "" {
			if pathSegments, ok := model_core.ObjectFormatToPath(link.ObjectFormat); ok {
				// Links are styled uniformly, regardless
				// of the role of the text.
				href := path.Join(
					append(
						[]string{
							mr.basePath,
							model_core.DecodableLocalReferenceToString(link.Reference),
						},
						pathSegments...,
					)...,
				) + "?format=" + mr.linkFormat
				nodes = append(nodes, h.A(
					append(
						attributes,
						h.Class("link link-accent"),
						h.Href(href),
						g.Text(segment.Text),
					)...,
				))
				continue
			}
		}

		if class, ok := roleClasses[segment.Role]; ok {
			attributes = append(attributes, h.Class(class))
		}
		if len(attributes) == 0 {
			nodes = append(nodes, g.Text(segment.Text))
		} else {
			nodes = append(nodes, h.Span(append(attributes, g.Text(segment.Text))...))
		}
	}
	return nodes
}
