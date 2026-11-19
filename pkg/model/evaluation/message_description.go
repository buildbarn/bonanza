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
	// If the message contains a field of type ProgressCounter, the
	// value of that field. This can be used by tools to display a
	// progress bar. This field is only set for progress messages.
	Counter *model_evaluation_pb.ProgressCounter
}

// String returns the description of the message as plain text.
func (d MessageDescription) String() string {
	var sb strings.Builder
	for _, segment := range d.Segments {
		sb.WriteString(segment.Text)
	}
	return sb.String()
}

// messageDescriptionTemplatePart is a part of a message description
// template that has been parsed. It either contains literal text, or a
// path of fields whose value needs to be substituted.
type messageDescriptionTemplatePart struct {
	literal   string
	fieldPath []protoreflect.FieldDescriptor
}

// compiledMessageDescription is a message description whose template
// has been parsed and validated against the message descriptor.
type compiledMessageDescription struct {
	parts []messageDescriptionTemplatePart
	// Path of fields leading to the ProgressCounter contained in the
	// message, if any.
	counterPath []protoreflect.FieldDescriptor
}

type compiledMessageDescriptionKey struct {
	messageDescriptor protoreflect.MessageDescriptor
	extensionType     protoreflect.ExtensionType
}

type compiledMessageDescriptionResult struct {
	compiledMessageDescription *compiledMessageDescription
	err                        error
}

// compiledMessageDescriptions caches the results of compiling message
// descriptions, keyed by message descriptor and the option from which
// the template was obtained.
var compiledMessageDescriptions sync.Map

var progressCounterFullName = (*model_evaluation_pb.ProgressCounter)(nil).ProtoReflect().Descriptor().FullName()

// isProgressCounterField returns true if a field is a singular field of
// type ProgressCounter.
func isProgressCounterField(fieldDescriptor protoreflect.FieldDescriptor) bool {
	return fieldDescriptor.Kind() == protoreflect.MessageKind &&
		fieldDescriptor.Cardinality() != protoreflect.Repeated &&
		fieldDescriptor.Message().FullName() == progressCounterFullName
}

// isDescribableField returns true if the value of a field can be
// emitted as part of a message description.
func isDescribableField(fieldDescriptor protoreflect.FieldDescriptor) bool {
	if fieldDescriptor.IsMap() {
		return false
	}
	switch fieldDescriptor.Kind() {
	case protoreflect.BytesKind, protoreflect.GroupKind:
		return false
	case protoreflect.MessageKind:
		return isProgressCounterField(fieldDescriptor)
	default:
		return true
	}
}

// compileMessageDescriptionTemplate parses a message description
// template and resolves all placeholders to fields of the provided
// message.
func compileMessageDescriptionTemplate(messageDescriptor protoreflect.MessageDescriptor, template string) ([]messageDescriptionTemplatePart, error) {
	var parts []messageDescriptionTemplatePart
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
				if i == len(fieldNames)-1 && !isDescribableField(fieldDescriptor) {
					return nil, fmt.Errorf("placeholder %#v refers to field %#v, which is not of a scalar type", placeholder, fieldName)
				}
			}

			if literal.Len() > 0 {
				parts = append(parts, messageDescriptionTemplatePart{literal: literal.String()})
				literal.Reset()
			}
			parts = append(parts, messageDescriptionTemplatePart{fieldPath: fieldPath})
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
		parts = append(parts, messageDescriptionTemplatePart{literal: literal.String()})
	}
	return parts, nil
}

// findProgressCounter traverses a progress message type and returns the
// path of fields leading to the ProgressCounter contained in it, if
// any. As progress messages may not contain any references to objects,
// this function also rejects message types defined in package
// bonanza.model.core, as those are used to store references.
func findProgressCounter(messageDescriptor protoreflect.MessageDescriptor, path []protoreflect.FieldDescriptor, visited map[protoreflect.FullName]struct{}, counterPath *[]protoreflect.FieldDescriptor) error {
	if _, ok := visited[messageDescriptor.FullName()]; ok {
		return nil
	}
	visited[messageDescriptor.FullName()] = struct{}{}
	defer delete(visited, messageDescriptor.FullName())

	fields := messageDescriptor.Fields()
	for i := range fields.Len() {
		fieldDescriptor := fields.Get(i)
		if fieldDescriptor.Kind() != protoreflect.MessageKind && fieldDescriptor.Kind() != protoreflect.GroupKind {
			continue
		}
		fieldPath := append(append([]protoreflect.FieldDescriptor(nil), path...), fieldDescriptor)
		fieldMessageDescriptor := fieldDescriptor.Message()
		if fieldMessageDescriptor.FullName() == progressCounterFullName {
			if fieldDescriptor.Cardinality() == protoreflect.Repeated {
				return fmt.Errorf("field %s is a repeated field of type %s", fieldDescriptor.FullName(), progressCounterFullName)
			}
			if *counterPath != nil {
				return fmt.Errorf("fields %s and %s are both of type %s", (*counterPath)[len(*counterPath)-1].FullName(), fieldDescriptor.FullName(), progressCounterFullName)
			}
			*counterPath = fieldPath
			continue
		}
		if fieldMessageDescriptor.ParentFile().Package() == "bonanza.model.core" {
			return fmt.Errorf("field %s is of type %s, which may contain references to objects", fieldDescriptor.FullName(), fieldMessageDescriptor.FullName())
		}
		if fieldDescriptor.Cardinality() == protoreflect.Repeated {
			// Counters contained in repeated fields cannot
			// be traversed, but references still need to
			// be rejected.
			var nestedCounterPath []protoreflect.FieldDescriptor
			if err := findProgressCounter(fieldMessageDescriptor, nil, visited, &nestedCounterPath); err != nil {
				return err
			}
			if nestedCounterPath != nil {
				return fmt.Errorf("field %s is a repeated field containing a field of type %s", fieldDescriptor.FullName(), progressCounterFullName)
			}
			continue
		}
		if err := findProgressCounter(fieldMessageDescriptor, fieldPath, visited, counterPath); err != nil {
			return err
		}
	}
	return nil
}

// getCompiledMessageDescription returns the compiled message
// description of a given message type, obtained from a given message
// option. If the message type does not have the option set, nil is
// returned.
func getCompiledMessageDescription(messageDescriptor protoreflect.MessageDescriptor, extensionType protoreflect.ExtensionType) (*compiledMessageDescription, error) {
	cacheKey := compiledMessageDescriptionKey{
		messageDescriptor: messageDescriptor,
		extensionType:     extensionType,
	}
	if result, ok := compiledMessageDescriptions.Load(cacheKey); ok {
		r := result.(compiledMessageDescriptionResult)
		return r.compiledMessageDescription, r.err
	}

	var result compiledMessageDescriptionResult
	if options := messageDescriptor.Options(); options != nil && proto.HasExtension(options, extensionType) {
		messageDescription := proto.GetExtension(options, extensionType).(*model_evaluation_pb.MessageDescription)
		optionName := extensionType.TypeDescriptor().FullName()
		if parts, err := compileMessageDescriptionTemplate(messageDescriptor, messageDescription.Template); err != nil {
			result.err = fmt.Errorf("invalid %s of message %s: %w", optionName, messageDescriptor.FullName(), err)
		} else {
			compiled := &compiledMessageDescription{parts: parts}
			if extensionType == model_evaluation_pb.E_ProgressDescription {
				if err := findProgressCounter(messageDescriptor, nil, map[protoreflect.FullName]struct{}{}, &compiled.counterPath); err != nil {
					result.err = fmt.Errorf("invalid progress message %s: %w", messageDescriptor.FullName(), err)
				}
			}
			if result.err == nil {
				result.compiledMessageDescription = compiled
			}
		}
	}
	compiledMessageDescriptions.Store(cacheKey, result)
	return result.compiledMessageDescription, result.err
}

// ValidateKeyDescription checks whether a message type has a key
// description, and whether its template is valid. This can be used by
// tests to ensure that all keys have valid descriptions.
func ValidateKeyDescription(messageDescriptor protoreflect.MessageDescriptor) (bool, error) {
	compiledMessageDescription, err := getCompiledMessageDescription(messageDescriptor, model_evaluation_pb.E_KeyDescription)
	return compiledMessageDescription != nil, err
}

// ValidateProgressDescription checks whether a message type has a
// progress description, whether its template is valid, and whether the
// message type is suitable for reporting progress. Messages used to
// report progress may not contain references to objects, and may
// contain at most one ProgressCounter.
func ValidateProgressDescription(messageDescriptor protoreflect.MessageDescriptor) (bool, error) {
	compiledMessageDescription, err := getCompiledMessageDescription(messageDescriptor, model_evaluation_pb.E_ProgressDescription)
	return compiledMessageDescription != nil, err
}

// formatSize formats a size in bytes using binary prefixes (e.g.,
// "12.3 MiB").
func formatSize(sizeBytes uint64) string {
	if sizeBytes < 1024 {
		return strconv.FormatUint(sizeBytes, 10) + " B"
	}
	size := float64(sizeBytes)
	for _, unit := range []string{"KiB", "MiB", "GiB", "TiB", "PiB"} {
		size /= 1024
		if size < 1024 || unit == "PiB" {
			return strconv.FormatFloat(size, 'f', 1, 64) + " " + unit
		}
	}
	panic("unreachable")
}

// formatProgressCounter converts a ProgressCounter to text (e.g.,
// "12.3 MiB of 40.1 MiB").
func formatProgressCounter(counter *model_evaluation_pb.ProgressCounter) string {
	format := func(v uint64) string { return strconv.FormatUint(v, 10) }
	if counter.GetUnit() == model_evaluation_pb.ProgressCounter_BYTES {
		format = formatSize
	}
	if counter.GetTotal() == 0 {
		return format(counter.GetCurrent())
	}
	return format(counter.GetCurrent()) + " of " + format(counter.GetTotal())
}

// getProgressCounter converts a ProgressCounter message that may be
// backed by a dynamic message to its native type. If the message is
// not set, nil is returned.
func getProgressCounter(message protoreflect.Message) *model_evaluation_pb.ProgressCounter {
	if !message.IsValid() {
		return nil
	}
	if counter, ok := message.Interface().(*model_evaluation_pb.ProgressCounter); ok {
		return counter
	}
	fields := message.Descriptor().Fields()
	return &model_evaluation_pb.ProgressCounter{
		Current: message.Get(fields.ByNumber(1)).Uint(),
		Total:   message.Get(fields.ByNumber(2)).Uint(),
		Unit:    model_evaluation_pb.ProgressCounter_Unit(message.Get(fields.ByNumber(3)).Enum()),
	}
}

// formatMessageDescriptionScalar converts a single scalar value
// contained in a field of a message to text.
func formatMessageDescriptionScalar(fieldDescriptor protoreflect.FieldDescriptor, value protoreflect.Value) string {
	switch fieldDescriptor.Kind() {
	case protoreflect.EnumKind:
		if enumValue := fieldDescriptor.Enum().Values().ByNumber(value.Enum()); enumValue != nil {
			return string(enumValue.Name())
		}
		return strconv.FormatInt(int64(value.Enum()), 10)
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return strconv.FormatFloat(value.Float(), 'g', -1, 64)
	case protoreflect.MessageKind:
		return formatProgressCounter(getProgressCounter(value.Message()))
	default:
		return value.String()
	}
}

// getFieldPathValue traverses into nested messages to obtain the value
// of a field. Unset messages yield default values for all of their
// fields.
func getFieldPathValue(message protoreflect.Message, fieldPath []protoreflect.FieldDescriptor) protoreflect.Value {
	for _, fieldDescriptor := range fieldPath[:len(fieldPath)-1] {
		message = message.Get(fieldDescriptor).Message()
	}
	return message.Get(fieldPath[len(fieldPath)-1])
}

func describeMessage(message protoreflect.Message, extensionType protoreflect.ExtensionType) (MessageDescription, bool, error) {
	compiledMessageDescription, err := getCompiledMessageDescription(message.Descriptor(), extensionType)
	if err != nil {
		return MessageDescription{}, false, err
	}
	if compiledMessageDescription == nil {
		return MessageDescription{}, false, nil
	}

	segments := make([]MessageDescriptionSegment, 0, len(compiledMessageDescription.parts))
	for _, part := range compiledMessageDescription.parts {
		if part.fieldPath == nil {
			segments = append(segments, MessageDescriptionSegment{Text: part.literal})
			continue
		}

		fieldDescriptor := part.fieldPath[len(part.fieldPath)-1]
		value := getFieldPathValue(message, part.fieldPath)
		var text string
		if fieldDescriptor.IsList() {
			list := value.List()
			elements := make([]string, 0, list.Len())
			for i := range list.Len() {
				elements = append(elements, formatMessageDescriptionScalar(fieldDescriptor, list.Get(i)))
			}
			text = strings.Join(elements, ", ")
		} else {
			text = formatMessageDescriptionScalar(fieldDescriptor, value)
		}
		segments = append(segments, MessageDescriptionSegment{
			Text:    text,
			IsField: true,
		})
	}

	description := MessageDescription{Segments: segments}
	if counterPath := compiledMessageDescription.counterPath; counterPath != nil {
		description.Counter = getProgressCounter(getFieldPathValue(message, counterPath).Message())
	}
	return description, true, nil
}

// DescribeKey returns a human readable description of an evaluation
// key. If the message type of the key has no key description, false
// is returned.
func DescribeKey(key protoreflect.Message) (MessageDescription, bool, error) {
	return describeMessage(key, model_evaluation_pb.E_KeyDescription)
}

// DescribeProgress returns a human readable description of a message
// that was reported by an evaluation function to indicate the progress
// of evaluating a key. If the message type has no progress
// description, false is returned.
func DescribeProgress(progress protoreflect.Message) (MessageDescription, bool, error) {
	return describeMessage(progress, model_evaluation_pb.E_ProgressDescription)
}
