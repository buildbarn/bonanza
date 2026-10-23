package render

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	model_core "bonanza.build/pkg/model/core"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	"bonanza.build/pkg/storage/object"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
)

// Renderer of Protobuf messages. Messages are rendered as JSON. If
// pretty printing is enabled, some types of messages are rendered using
// a syntax that is specific to them.
type Renderer struct {
	// If set, render messages for which a more human-friendly
	// representation exists in that form, as opposed to JSON. For
	// example, Starlark values are rendered using Starlark syntax,
	// and directories are rendered as a table.
	Pretty bool
	// The reference format of the objects in which the messages are
	// stored. This is needed to render weak references.
	ReferenceFormat *object.ReferenceFormat
	// Object format to use for reference fields that lack an
	// object_format option.
	FallbackObjectFormat *model_core_pb.ObjectFormat
	// If set, called for every message that is rendered as JSON.
	// This can be used to inspect field values while rendering.
	OnMessage func(m protoreflect.Message)
}

func errorf(format string, args ...any) Doc {
	return Styled(RoleError, "[ "+fmt.Sprintf(format, args...)+" ]")
}

func jsonValue(v any) Doc {
	marshaled, err := json.Marshal(v)
	if err != nil {
		return errorf("%s", err)
	}
	return Styled(RoleLiteral, string(marshaled))
}

// Reference renders a reference to an object as a quoted string. If
// the format of the object is known, the reference links to the
// object.
func Reference(reference model_core.Decodable[object.LocalReference], objectFormat *model_core_pb.ObjectFormat) Doc {
	text := Styled(RoleReference, fmt.Sprintf("%#v", model_core.DecodableLocalReferenceToString(reference)))
	return referenceLink(reference, objectFormat, text)
}

func referenceLink(reference model_core.Decodable[object.LocalReference], objectFormat *model_core_pb.ObjectFormat, text Doc) Doc {
	if objectFormat == nil {
		// Valid reference, but there is no type information.
		// Display the reference without turning it into a link.
		return text
	}
	if _, ok := model_core.ObjectFormatToPath(objectFormat); !ok {
		return errorf("Reference field with unknown object format type")
	}
	return Link(LinkTarget{Reference: reference, ObjectFormat: objectFormat}, text)
}

func getFieldObjectFormat(fieldDescriptor protoreflect.FieldDescriptor) *model_core_pb.ObjectFormat {
	fieldOptions, ok := fieldDescriptor.Options().(*descriptorpb.FieldOptions)
	if !ok {
		return nil
	}
	return proto.GetExtension(fieldOptions, model_core_pb.E_ObjectFormat).(*model_core_pb.ObjectFormat)
}

// fieldReferenceLink renders text that links to the object referenced
// by a field of a message, using the object format declared in the
// field's options. If the reference is invalid, the text is displayed
// without a link.
func fieldReferenceLink(reference model_core.Message[*model_core_pb.DecodableReference, object.LocalReference], outerMessage proto.Message, fieldName string, text Doc) Doc {
	flattenedReference, err := model_core.FlattenDecodableReference(reference)
	if err != nil {
		return text
	}
	fieldDescriptor := outerMessage.ProtoReflect().Descriptor().Fields().ByTextName(fieldName)
	if fieldDescriptor == nil {
		panic(fmt.Sprintf("message %s has no field named %s", outerMessage.ProtoReflect().Descriptor().FullName(), fieldName))
	}
	return referenceLink(flattenedReference, getFieldObjectFormat(fieldDescriptor), text)
}

func (r *Renderer) renderReferenceField(fieldDescriptor protoreflect.FieldDescriptor, reference model_core.Decodable[object.LocalReference]) Doc {
	objectFormat := getFieldObjectFormat(fieldDescriptor)
	if objectFormat == nil {
		objectFormat = r.FallbackObjectFormat
	}
	return Reference(reference, objectFormat)
}

func (r *Renderer) renderField(fieldDescriptor protoreflect.FieldDescriptor, value model_core.Message[protoreflect.Value, object.LocalReference]) Doc {
	switch fieldDescriptor.Kind() {
	// Simple scalar types for which we can just call json.Marshal().
	case protoreflect.BoolKind:
		return jsonValue(value.Message.Bool())
	case protoreflect.Int32Kind, protoreflect.Int64Kind,
		protoreflect.Sint32Kind, protoreflect.Sint64Kind,
		protoreflect.Sfixed32Kind, protoreflect.Sfixed64Kind:
		return jsonValue(value.Message.Int())
	case protoreflect.Uint32Kind, protoreflect.Uint64Kind,
		protoreflect.Fixed32Kind, protoreflect.Fixed64Kind:
		return jsonValue(value.Message.Uint())
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return jsonValue(value.Message.Float())
	case protoreflect.StringKind:
		return jsonValue(value.Message.String())
	case protoreflect.BytesKind:
		return jsonValue(value.Message.Bytes())

	case protoreflect.GroupKind, protoreflect.MessageKind:
		switch m := value.Message.Message().Interface().(type) {
		case *model_core_pb.DecodableReference:
			if reference, err := model_core.FlattenDecodableReference(model_core.Nested(value, m)); err == nil {
				return r.renderReferenceField(fieldDescriptor, reference)
			}
		case *model_core_pb.WeakDecodableReference:
			if r.ReferenceFormat != nil {
				if reference, err := model_core.NewDecodableLocalReferenceFromWeakProto(*r.ReferenceFormat, m); err == nil {
					return r.renderReferenceField(fieldDescriptor, reference)
				}
			}
		}

		// Recurse into message.
		return r.Message(model_core.Nested(value, value.Message.Message()))

	case protoreflect.EnumKind:
		// Render an enum value as a string or integer,
		// depending on whether it corresponds to a known value.
		number := value.Message.Enum()
		if enumValueDescriptor := fieldDescriptor.Enum().Values().ByNumber(number); enumValueDescriptor != nil {
			return jsonValue(string(enumValueDescriptor.Name()))
		}
		return jsonValue(number)

	default:
		return errorf("Unknown field kind")
	}
}

// TopLevelMessage renders a message that is stored in an object of
// its own. If the message is an Any, its payload is rendered with an
// added "@type" field, similar to how protojson renders an anypb.Any.
func (r *Renderer) TopLevelMessage(m model_core.TopLevelMessage[protoreflect.Message, object.LocalReference]) Doc {
	if message, ok := m.Message.Interface().(*anypb.Any); ok {
		anyValue, err := model_core.UnmarshalTopLevelAnyNew(model_core.NewTopLevelMessage(message, m.OutgoingReferences))
		if err != nil {
			return errorf("%s", err)
		}
		return r.renderMessageCommon(
			model_core.Nested(anyValue.Decay(), anyValue.Message.ProtoReflect()),
			map[string]Doc{"@type": jsonValue(message.TypeUrl)},
		)
	}
	return r.Message(m.Decay())
}

// Message renders a message that is embedded in another message, or
// stored in an object of its own. If the message is a
// model_core_pb.Any, its payload is rendered with an added "@type"
// field, similar to how protojson renders an anypb.Any.
func (r *Renderer) Message(m model_core.Message[protoreflect.Message, object.LocalReference]) Doc {
	if message, ok := m.Message.Interface().(*model_core_pb.Any); ok {
		anyValue, err := model_core.UnmarshalAnyNew(model_core.Nested(m, message))
		if err != nil {
			return errorf("%s", err)
		}
		return r.renderMessageCommon(
			model_core.Nested(anyValue.Decay(), anyValue.Message.ProtoReflect()),
			map[string]Doc{"@type": jsonValue(message.Value.GetTypeUrl())},
		)
	}
	return r.renderMessageCommon(m, map[string]Doc{})
}

// MessageList renders a list of messages, such as the contents of an
// object containing a varint separated list of messages.
func (r *Renderer) MessageList(list model_core.Message[[]protoreflect.Message, object.LocalReference]) Doc {
	elements := make([]Doc, 0, len(list.Message))
	for _, element := range list.Message {
		elements = append(elements, r.Message(model_core.Nested(list, element)))
	}
	return Sequence(Text("["), Text("]"), elements, false, false)
}

func (r *Renderer) renderMessageCommon(m model_core.Message[protoreflect.Message, object.LocalReference], fields map[string]Doc) Doc {
	if r.Pretty {
		if d := r.renderPretty(m); d != nil {
			return d
		}
	}

	if v, ok := m.Message.Interface().(*durationpb.Duration); ok {
		if marshaled, err := protojson.Marshal(v); err == nil {
			return Styled(RoleLiteral, string(marshaled))
		}
	}

	if r.OnMessage != nil {
		r.OnMessage(m.Message)
	}

	// Iterate over all message fields and render their values.
	m.Message.Range(func(fieldDescriptor protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		var valueDoc Doc
		if fieldDescriptor.IsList() {
			// Repeated fields should be rendered as JSON lists.
			list := value.List()
			elements := make([]Doc, 0, list.Len())
			for i := range list.Len() {
				elements = append(elements, r.renderField(fieldDescriptor, model_core.Nested(m, list.Get(i))))
			}
			valueDoc = Sequence(Text("["), Text("]"), elements, false, false)
		} else if fieldDescriptor.IsMap() {
			mapField := value.Map()
			entries := make([]Doc, 0, mapField.Len())
			mapField.Range(func(entryKey protoreflect.MapKey, entryValue protoreflect.Value) bool {
				entries = append(
					entries,
					Concat(
						r.renderField(fieldDescriptor.MapKey(), model_core.Nested(m, entryKey.Value())),
						Text(": "),
						r.renderField(fieldDescriptor.MapValue(), model_core.Nested(m, entryValue)),
					),
				)
				return true
			})
			valueDoc = Sequence(Text("{"), Text("}"), entries, false, false)
		} else {
			valueDoc = r.renderField(fieldDescriptor, model_core.Nested(m, value))
		}
		fields[fieldDescriptor.JSONName()] = valueDoc
		return true
	})

	// Sort fields by name and join them together in a single JSON
	// object.
	entries := make([]Doc, 0, len(fields))
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		marshaledName, err := json.Marshal(name)
		if err != nil {
			panic(err)
		}
		entries = append(
			entries,
			Concat(
				Styled(RoleFieldName, string(marshaledName)),
				Text(": "),
				fields[name],
			),
		)
	}
	return Sequence(Text("{"), Text("}"), entries, false, false)
}

// renderPretty renders a message using a representation that is
// specific to its type. It returns nil if no such representation
// exists, meaning the message should be rendered as JSON.
func (*Renderer) renderPretty(m model_core.Message[protoreflect.Message, object.LocalReference]) Doc {
	switch v := m.Message.Interface().(type) {
	case *model_filesystem_pb.DirectoryContents:
		return renderDirectory(model_core.Nested(m, v))
	}
	return renderStarlark(model_core.Nested(m, m.Message.Interface()))
}
