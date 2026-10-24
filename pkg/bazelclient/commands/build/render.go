package build

import (
	"net/url"
	"strings"

	"bonanza.build/pkg/bazelclient/formatted"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/render"
	"bonanza.build/pkg/storage/object"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// messageFormatter converts Protobuf messages to formatted text that
// can be written to the terminal, either as JSON or pretty printed.
type messageFormatter struct {
	renderer render.Renderer
	// URL of the object page of the Bonanza Browser. If set,
	// references to objects are emitted as clickable links.
	objectBaseURL string
}

func (mf *messageFormatter) message(m model_core.Message[protoreflect.Message, object.LocalReference]) render.Doc {
	return mf.renderer.Message(m)
}

// formatSingleLine lays out a document on a single line.
func (mf *messageFormatter) formatSingleLine(doc render.Doc) formatted.Node {
	blocks := render.Layout(doc, render.LayoutOptions{SingleLine: true})
	parts := make([]formatted.Node, 0, len(blocks))
	for i, block := range blocks {
		if i > 0 {
			parts = append(parts, formatted.Text(" "))
		}
		if block.Table == nil {
			parts = append(parts, mf.formatSegments(block.Segments))
		} else {
			// Tables cannot be displayed on a single line.
			// Display all rows, separated by semicolons.
			for j, row := range block.Table.Rows {
				if j > 0 {
					parts = append(parts, formatted.Text("; "))
				}
				for k, cell := range row {
					if k > 0 {
						parts = append(parts, formatted.Text(" "))
					}
					parts = append(parts, mf.formatSegments(cell))
				}
			}
		}
	}
	return formatted.Join(parts...)
}

// FormatDoc lays out a document across multiple lines of a given
// width, and converts it to formatted text that can be written to the
// terminal. Syntax highlighting is applied based on the roles of the
// segments of text. Each line is terminated with a newline character.
func FormatDoc(doc render.Doc, width int, indentation string) formatted.Node {
	var mf messageFormatter
	blocks := render.Layout(doc, render.LayoutOptions{
		Width:       width,
		IndentWidth: len(indentation),
	})
	var parts []formatted.Node
	writeIndentation := func(indent int) {
		parts = append(parts, formatted.Text(strings.Repeat(indentation, indent)))
	}
	for _, block := range blocks {
		if block.Table == nil {
			if len(block.Segments) > 0 {
				writeIndentation(block.Indent)
			}
			parts = append(parts, mf.formatSegments(block.Segments), formatted.Text("\n"))
		} else {
			// Display tables with tab separated columns.
			for _, row := range block.Table.Rows {
				writeIndentation(block.Indent)
				for k, cell := range row {
					if k > 0 {
						parts = append(parts, formatted.Text("\t"))
					}
					parts = append(parts, mf.formatSegments(cell))
				}
				parts = append(parts, formatted.Text("\n"))
			}
		}
	}
	return formatted.Join(parts...)
}

func (mf *messageFormatter) formatSegments(segments []render.Segment) formatted.Node {
	parts := make([]formatted.Node, 0, len(segments))
	for _, segment := range segments {
		var node formatted.Node
		switch segment.Role {
		case render.RoleFieldName:
			node = formatted.Yellow(formatted.Text(segment.Text))
		case render.RoleLiteral:
			node = formatted.Magenta(formatted.Text(segment.Text))
		case render.RoleCallee:
			node = formatted.Blue(formatted.Text(segment.Text))
		case render.RoleDim:
			node = formatted.Gray(formatted.Text(segment.Text))
		case render.RoleReference:
			node = formatted.Cyan(formatted.Text(abbreviateReference(segment.Text)))
		case render.RoleError:
			node = formatted.Bold(formatted.Red(formatted.Text(segment.Text)))
		default:
			node = formatted.Text(segment.Text)
		}

		if link := segment.Link; link != nil && mf.objectBaseURL != "" {
			if pathSegments, ok := model_core.ObjectFormatToPath(link.ObjectFormat); ok {
				if linkURL, err := url.JoinPath(
					mf.objectBaseURL,
					append(
						[]string{model_core.DecodableLocalReferenceToString(link.Reference)},
						pathSegments...,
					)...,
				); err == nil {
					node = formatted.Link(linkURL, node)
				}
			}
		}
		parts = append(parts, node)
	}
	return formatted.Join(parts...)
}

// abbreviateReference shortens a quoted reference to an object. As
// references are long and look random, displaying them in full adds
// little value. If the terminal supports it, the full reference can
// still be accessed by following the link.
func abbreviateReference(s string) string {
	const maximumLength = 8
	if inner, ok := strings.CutPrefix(s, "\""); ok {
		if inner, ok := strings.CutSuffix(inner, "\""); ok && len(inner) > maximumLength+3 {
			return "\"" + inner[:maximumLength] + "...\""
		}
	}
	return s
}
