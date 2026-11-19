package evaluation

import (
	"fmt"
	"strconv"
	"strings"
	"sync"

	model_evaluation_pb "bonanza.build/pkg/proto/model/evaluation"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// MessageDescriptionSegment is a part of a human readable description
// of a message.
type MessageDescriptionSegment struct {
	Text string
	// Whether the text was obtained from a field of the message, as
	// opposed to being literal text contained in the template. This
	// allows tools to display these parts differently (e.g., in a
	// monospace font).
	IsField bool
}

// MessageDescription is a human readable description of a message,
// such as "Analyzing //foo:bar" for an evaluation key.
type MessageDescription struct {
	Segments []MessageDescriptionSegment
}

// String returns the description of the key as plain text.
func (d MessageDescription) String() string {
	var sb strings.Builder
	for _, segment := range d.Segments {
		sb.WriteString(segment.Text)
	}
	return sb.String()
}

// keyDescriptionTemplatePart is a part of a key description template
// that has been parsed. It either contains literal text, or a path of
// fields whose value needs to be substituted.
type keyDescriptionTemplatePart struct {
	literal   string
	fieldPath []protoreflect.FieldDescriptor
}

// compiledKeyDescription is a key description whose template has been
// parsed and validated against the message descriptor of the key.
type compiledKeyDescription struct {
	parts []keyDescriptionTemplatePart
}

type compiledKeyDescriptionResult struct {
	compiledKeyDescription *compiledKeyDescription
	err                    error
}

// compiledKeyDescriptions caches the results of compiling key
// descriptions, keyed by message descriptor.
var compiledKeyDescriptions sync.Map

// isDescribableScalarKind returns true if fields of a given kind can
// be emitted as part of a key description.
func isDescribableScalarKind(kind protoreflect.Kind) bool {
	switch kind {
	case protoreflect.BytesKind, protoreflect.GroupKind, protoreflect.MessageKind:
		return false
	default:
		return true
	}
}

// compileKeyDescriptionTemplate parses a key description template and
// resolves all placeholders to fields of the provided message.
func compileKeyDescriptionTemplate(messageDescriptor protoreflect.MessageDescriptor, template string) ([]keyDescriptionTemplatePart, error) {
	var parts []keyDescriptionTemplatePart
	var literal strings.Builder
	for len(template) > 0 {
		switch {
		case strings.HasPrefix(template, "{{"):
			literal.WriteByte('{')
			template = template[2:]
		case strings.HasPrefix(template, "}}"):
			literal.WriteByte('}')
			template = template[2:]
		case template[0] == '}':
			return nil, fmt.Errorf("unmatched \"}\" in template")
		case template[0] == '{':
			end := strings.IndexByte(template, '}')
			if end < 0 {
				return nil, fmt.Errorf("unterminated placeholder in template")
			}
			placeholder := template[1:end]
			template = template[end+1:]

			// Resolve the placeholder to a path of fields.
			var fieldPath []protoreflect.FieldDescriptor
			currentMessageDescriptor := messageDescriptor
			fieldNames := strings.Split(placeholder, ".")
			for i, fieldName := range fieldNames {
				if currentMessageDescriptor == nil {
					return nil, fmt.Errorf("placeholder %#v refers to a field contained in a field that is not a singular message", placeholder)
				}
				fieldDescriptor := currentMessageDescriptor.Fields().ByName(protoreflect.Name(fieldName))
				if fieldDescriptor == nil {
					return nil, fmt.Errorf("placeholder %#v refers to field %#v, which does not exist in message %s", placeholder, fieldName, currentMessageDescriptor.FullName())
				}
				fieldPath = append(fieldPath, fieldDescriptor)
				if fieldDescriptor.Kind() == protoreflect.MessageKind && fieldDescriptor.Cardinality() != protoreflect.Repeated {
					currentMessageDescriptor = fieldDescriptor.Message()
				} else {
					currentMessageDescriptor = nil
				}
				if i == len(fieldNames)-1 {
					if fieldDescriptor.IsMap() || !isDescribableScalarKind(fieldDescriptor.Kind()) {
						return nil, fmt.Errorf("placeholder %#v refers to field %#v, which is not of a scalar type", placeholder, fieldName)
					}
				}
			}

			if literal.Len() > 0 {
				parts = append(parts, keyDescriptionTemplatePart{literal: literal.String()})
				literal.Reset()
			}
			parts = append(parts, keyDescriptionTemplatePart{fieldPath: fieldPath})
		default:
			end := strings.IndexAny(template, "{}")
			if end < 0 {
				end = len(template)
			}
			literal.WriteString(template[:end])
			template = template[end:]
		}
	}
	if literal.Len() > 0 {
		parts = append(parts, keyDescriptionTemplatePart{literal: literal.String()})
	}
	return parts, nil
}

// getCompiledKeyDescription returns the compiled key description of a
// given message type. If the message type has no key description,
// nil is returned.
func getCompiledKeyDescription(messageDescriptor protoreflect.MessageDescriptor) (*compiledKeyDescription, error) {
	if result, ok := compiledKeyDescriptions.Load(messageDescriptor); ok {
		r := result.(compiledKeyDescriptionResult)
		return r.compiledKeyDescription, r.err
	}

	var result compiledKeyDescriptionResult
	if options := messageDescriptor.Options(); options != nil && proto.HasExtension(options, model_evaluation_pb.E_KeyDescription) {
		keyDescription := proto.GetExtension(options, model_evaluation_pb.E_KeyDescription).(*model_evaluation_pb.MessageDescription)
		if parts, err := compileKeyDescriptionTemplate(messageDescriptor, keyDescription.Template); err == nil {
			result.compiledKeyDescription = &compiledKeyDescription{
				parts: parts,
			}
		} else {
			result.err = fmt.Errorf("invalid key description of message %s: %w", messageDescriptor.FullName(), err)
		}
	}
	compiledKeyDescriptions.Store(messageDescriptor, result)
	return result.compiledKeyDescription, result.err
}

// ValidateKeyDescription checks whether a message type has a key
// description, and whether its template is valid. This can be used by
// tests to ensure that all keys have valid descriptions.
func ValidateKeyDescription(messageDescriptor protoreflect.MessageDescriptor) (bool, error) {
	compiledKeyDescription, err := getCompiledKeyDescription(messageDescriptor)
	return compiledKeyDescription != nil, err
}

// formatKeyDescriptionScalar converts a single scalar value contained
// in a field of a key to text.
func formatKeyDescriptionScalar(fieldDescriptor protoreflect.FieldDescriptor, value protoreflect.Value) string {
	switch fieldDescriptor.Kind() {
	case protoreflect.EnumKind:
		if enumValue := fieldDescriptor.Enum().Values().ByNumber(value.Enum()); enumValue != nil {
			return string(enumValue.Name())
		}
		return strconv.FormatInt(int64(value.Enum()), 10)
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return strconv.FormatFloat(value.Float(), 'g', -1, 64)
	default:
		return value.String()
	}
}

// DescribeKey returns a human readable description of an evaluation
// key. If the message type of the key has no key description, false
// is returned.
func DescribeKey(key protoreflect.Message) (MessageDescription, bool, error) {
	compiledKeyDescription, err := getCompiledKeyDescription(key.Descriptor())
	if err != nil {
		return MessageDescription{}, false, err
	}
	if compiledKeyDescription == nil {
		return MessageDescription{}, false, nil
	}

	segments := make([]MessageDescriptionSegment, 0, len(compiledKeyDescription.parts))
	for _, part := range compiledKeyDescription.parts {
		if part.fieldPath == nil {
			segments = append(segments, MessageDescriptionSegment{Text: part.literal})
			continue
		}

		// Traverse into nested messages. Unset messages yield
		// default values for all of their fields.
		message := key
		for _, fieldDescriptor := range part.fieldPath[:len(part.fieldPath)-1] {
			message = message.Get(fieldDescriptor).Message()
		}
		fieldDescriptor := part.fieldPath[len(part.fieldPath)-1]
		value := message.Get(fieldDescriptor)

		var text string
		if fieldDescriptor.IsList() {
			list := value.List()
			elements := make([]string, 0, list.Len())
			for i := range list.Len() {
				elements = append(elements, formatKeyDescriptionScalar(fieldDescriptor, list.Get(i)))
			}
			text = strings.Join(elements, ", ")
		} else {
			text = formatKeyDescriptionScalar(fieldDescriptor, value)
		}
		segments = append(segments, MessageDescriptionSegment{
			Text:    text,
			IsField: true,
		})
	}
	return MessageDescription{
		Segments: segments,
	}, true, nil
}
