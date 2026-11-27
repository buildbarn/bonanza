package evaluation_test

import (
	"testing"

	model_evaluation "bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_evaluation_pb "bonanza.build/pkg/proto/model/evaluation"
	model_fetch_pb "bonanza.build/pkg/proto/model/fetch"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// newKeyMessageDescriptor creates a message descriptor for a key having
// a given key description template. The message contains a string
// field, a repeated string field, an enumeration field, and a nested
// message containing a string field.
func newKeyMessageDescriptor(t *testing.T, name, template string) protoreflect.MessageDescriptor {
	options := &descriptorpb.MessageOptions{}
	proto.SetExtension(options, model_evaluation_pb.E_KeyDescription, model_evaluation_pb.MessageDescription_builder{
		Template: template,
	}.Build())
	file, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name:    proto.String("key_description_test_" + name + ".proto"),
		Package: proto.String("key_description_test_" + name),
		Syntax:  proto.String("proto3"),
		EnumType: []*descriptorpb.EnumDescriptorProto{{
			Name: proto.String("Color"),
			Value: []*descriptorpb.EnumValueDescriptorProto{
				{Name: proto.String("RED"), Number: proto.Int32(0)},
				{Name: proto.String("GREEN"), Number: proto.Int32(1)},
			},
		}},
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String("Nested"),
				Field: []*descriptorpb.FieldDescriptorProto{{
					Name:     proto.String("value"),
					Number:   proto.Int32(1),
					Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
					JsonName: proto.String("value"),
				}},
			},
			{
				Name:    proto.String("Key"),
				Options: options,
				Field: []*descriptorpb.FieldDescriptorProto{
					{
						Name:     proto.String("label"),
						Number:   proto.Int32(1),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
						JsonName: proto.String("label"),
					},
					{
						Name:     proto.String("names"),
						Number:   proto.Int32(2),
						Label:    descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
						JsonName: proto.String("names"),
					},
					{
						Name:     proto.String("color"),
						Number:   proto.Int32(3),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_ENUM.Enum(),
						TypeName: proto.String(".key_description_test_" + name + ".Color"),
						JsonName: proto.String("color"),
					},
					{
						Name:     proto.String("nested"),
						Number:   proto.Int32(4),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
						TypeName: proto.String(".key_description_test_" + name + ".Nested"),
						JsonName: proto.String("nested"),
					},
					{
						Name:     proto.String("data"),
						Number:   proto.Int32(5),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_BYTES.Enum(),
						JsonName: proto.String("data"),
					},
				},
			},
		},
	}, protoregistry.GlobalFiles)
	require.NoError(t, err)
	return file.Messages().ByName("Key")
}

func TestDescribeKey(t *testing.T) {
	t.Run("NoDescription", func(t *testing.T) {
		// Messages without a key description cannot be
		// described.
		_, ok, err := model_evaluation.DescribeKey((&wrapperspb.StringValue{Value: "hello"}).ProtoReflect())
		require.NoError(t, err)
		require.False(t, ok)

		ok, err = model_evaluation.ValidateKeyDescription((&wrapperspb.StringValue{}).ProtoReflect().Descriptor())
		require.NoError(t, err)
		require.False(t, ok)
	})

	t.Run("ConfiguredTarget", func(t *testing.T) {
		description, ok, err := model_evaluation.DescribeKey((&model_analysis_pb.ConfiguredTarget_Key{
			Label: "@@foo+//bar:baz",
		}).ProtoReflect())
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, model_evaluation.MessageDescription{
			Segments: []model_evaluation.MessageDescriptionSegment{
				{Text: "Analyzing "},
				{Text: "@@foo+//bar:baz", IsField: true},
			},
		}, description)
		require.Equal(t, "Analyzing @@foo+//bar:baz", description.String())
	})

	t.Run("NestedRepeatedField", func(t *testing.T) {
		description, ok, err := model_evaluation.DescribeKey((&model_analysis_pb.HttpFileContents_Key{
			FetchOptions: &model_analysis_pb.HttpFetchOptions{
				Target: &model_fetch_pb.Target{
					Urls: []string{"https://example.com/a", "https://example.org/a"},
				},
			},
		}).ProtoReflect())
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, "Downloading https://example.com/a, https://example.org/a", description.String())
	})

	t.Run("UnsetNestedMessage", func(t *testing.T) {
		// Placeholders referring to fields of unset messages
		// should yield default values.
		description, ok, err := model_evaluation.DescribeKey((&model_analysis_pb.TargetActionResult_Key{}).ProtoReflect())
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, "Running an action of ", description.String())
	})

	t.Run("MultipleFields", func(t *testing.T) {
		description, ok, err := model_evaluation.DescribeKey((&model_analysis_pb.FileProperties_Key{
			CanonicalRepo: "foo+",
			Path:          "bar/BUILD.bazel",
		}).ProtoReflect())
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, "Obtaining properties of bar/BUILD.bazel in @@foo+", description.String())
	})

	t.Run("AllFieldKinds", func(t *testing.T) {
		messageDescriptor := newKeyMessageDescriptor(t, "all_field_kinds", "{{{label}}} [{names}] {color} {nested.value}")
		key := dynamicpb.NewMessage(messageDescriptor)
		fields := messageDescriptor.Fields()
		key.Set(fields.ByName("label"), protoreflect.ValueOfString("a"))
		names := key.Mutable(fields.ByName("names")).List()
		names.Append(protoreflect.ValueOfString("b"))
		names.Append(protoreflect.ValueOfString("c"))
		key.Set(fields.ByName("color"), protoreflect.ValueOfEnum(1))
		nested := key.Mutable(fields.ByName("nested")).Message()
		nested.Set(nested.Descriptor().Fields().ByName("value"), protoreflect.ValueOfString("d"))

		description, ok, err := model_evaluation.DescribeKey(key)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, model_evaluation.MessageDescription{
			Segments: []model_evaluation.MessageDescriptionSegment{
				{Text: "{"},
				{Text: "a", IsField: true},
				{Text: "} ["},
				{Text: "b, c", IsField: true},
				{Text: "] "},
				{Text: "GREEN", IsField: true},
				{Text: " "},
				{Text: "d", IsField: true},
			},
		}, description)
	})

	t.Run("InvalidTemplates", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			template string
			err      string
		}{
			{"UnknownField", "{nonexistent}", "does not exist"},
			{"UnknownNestedField", "{nested.nonexistent}", "does not exist"},
			{"MessageField", "{nested}", "not of a scalar type"},
			{"BytesField", "{data}", "not of a scalar type"},
			{"FieldOfScalar", "{label.length}", "not a singular message"},
			{"Unterminated", "Hello {label", "unterminated placeholder"},
			{"UnmatchedClosingBrace", "Hello }", "unmatched"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				messageDescriptor := newKeyMessageDescriptor(t, "invalid_"+tc.name, tc.template)
				ok, err := model_evaluation.ValidateKeyDescription(messageDescriptor)
				require.False(t, ok)
				require.ErrorContains(t, err, tc.err)

				_, ok, err = model_evaluation.DescribeKey(dynamicpb.NewMessage(messageDescriptor))
				require.False(t, ok)
				require.ErrorContains(t, err, tc.err)
			})
		}
	})
}

// newProgressMessageDescriptor creates a message descriptor for a
// progress message having the given fields and progress description
// template.
func newProgressMessageDescriptor(t *testing.T, name, template string, fields []*descriptorpb.FieldDescriptorProto) protoreflect.MessageDescriptor {
	options := &descriptorpb.MessageOptions{}
	proto.SetExtension(options, model_evaluation_pb.E_ProgressDescription, model_evaluation_pb.MessageDescription_builder{
		Template: template,
	}.Build())
	file, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name:    proto.String("progress_description_test_" + name + ".proto"),
		Package: proto.String("progress_description_test_" + name),
		Syntax:  proto.String("proto3"),
		Dependency: []string{
			"bonanza.build/pkg/proto/model/core/core.proto",
			"bonanza.build/pkg/proto/model/evaluation/evaluation.proto",
		},
		MessageType: []*descriptorpb.DescriptorProto{{
			Name:    proto.String("Progress"),
			Options: options,
			Field:   fields,
		}},
	}, protoregistry.GlobalFiles)
	require.NoError(t, err)
	return file.Messages().ByName("Progress")
}

func messageField(name string, number int32, typeName string, repeated bool) *descriptorpb.FieldDescriptorProto {
	field := &descriptorpb.FieldDescriptorProto{
		Name:     proto.String(name),
		Number:   proto.Int32(number),
		Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
		TypeName: proto.String(typeName),
		JsonName: proto.String(name),
	}
	if repeated {
		field.Label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
	}
	return field
}

func TestDescribeProgress(t *testing.T) {
	t.Run("NoDescription", func(t *testing.T) {
		// Key descriptions should not be used to describe
		// progress messages, and vice versa.
		_, ok, err := model_evaluation.DescribeProgress((&model_analysis_pb.ConfiguredTarget_Key{}).ProtoReflect())
		require.NoError(t, err)
		require.False(t, ok)

		_, ok, err = model_evaluation.DescribeKey((&model_analysis_pb.RawActionResult_Progress{}).ProtoReflect())
		require.NoError(t, err)
		require.False(t, ok)
	})

	t.Run("Enum", func(t *testing.T) {
		description, ok, err := model_evaluation.DescribeProgress((&model_analysis_pb.RawActionResult_Progress{
			Stage: model_analysis_pb.RawActionResult_Progress_EXECUTING,
		}).ProtoReflect())
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, "EXECUTING", description.String())
		require.Nil(t, description.Counter)
	})

	t.Run("CounterBytes", func(t *testing.T) {
		for _, tc := range []struct {
			current, total uint64
			text           string
		}{
			{0, 0, "0 B"},
			{1023, 0, "1023 B"},
			{1024, 4096, "1.0 KiB of 4.0 KiB"},
			{12897485, 42047652, "12.3 MiB of 40.1 MiB"},
			{3 << 40, 0, "3.0 TiB"},
		} {
			counter := model_evaluation_pb.ProgressCounter_builder{
				Current: tc.current,
				Total:   tc.total,
				Unit:    model_evaluation_pb.ProgressCounter_BYTES,
			}.Build()
			description, ok, err := model_evaluation.DescribeProgress((&model_analysis_pb.HttpFileContents_Progress{
				Downloaded: counter,
			}).ProtoReflect())
			require.NoError(t, err)
			require.True(t, ok)
			require.Equal(t, []model_evaluation.MessageDescriptionSegment{
				{Text: tc.text, IsField: true},
			}, description.Segments)
			require.Same(t, counter, description.Counter)
		}
	})

	t.Run("UnsetCounter", func(t *testing.T) {
		description, ok, err := model_evaluation.DescribeProgress((&model_analysis_pb.HttpFileContents_Progress{}).ProtoReflect())
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, "0", description.String())
		require.Nil(t, description.Counter)
	})

	t.Run("CounterItemsInDynamicMessage", func(t *testing.T) {
		messageDescriptor := newProgressMessageDescriptor(t, "items", "Extracted {extracted} files", []*descriptorpb.FieldDescriptorProto{
			messageField("extracted", 1, ".bonanza.model.evaluation.ProgressCounter", false),
		})
		progress := dynamicpb.NewMessage(messageDescriptor)
		counter := progress.Mutable(messageDescriptor.Fields().ByName("extracted")).Message()
		counterFields := counter.Descriptor().Fields()
		counter.Set(counterFields.ByName("current"), protoreflect.ValueOfUint64(3))
		counter.Set(counterFields.ByName("total"), protoreflect.ValueOfUint64(10))

		description, ok, err := model_evaluation.DescribeProgress(progress)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, "Extracted 3 of 10 files", description.String())
		testutil.RequireEqualProto(t, model_evaluation_pb.ProgressCounter_builder{
			Current: 3,
			Total:   10,
		}.Build(), description.Counter)
	})

	t.Run("InvalidProgressMessages", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			fields []*descriptorpb.FieldDescriptorProto
			err    string
		}{
			{
				"MultipleCounters",
				[]*descriptorpb.FieldDescriptorProto{
					messageField("a", 1, ".bonanza.model.evaluation.ProgressCounter", false),
					messageField("b", 2, ".bonanza.model.evaluation.ProgressCounter", false),
				},
				"are both of type",
			},
			{
				"RepeatedCounter",
				[]*descriptorpb.FieldDescriptorProto{
					messageField("a", 1, ".bonanza.model.evaluation.ProgressCounter", true),
				},
				"is a repeated field",
			},
			{
				"Reference",
				[]*descriptorpb.FieldDescriptorProto{
					messageField("a", 1, ".bonanza.model.core.DecodableReference", false),
				},
				"may contain references to objects",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				messageDescriptor := newProgressMessageDescriptor(t, "invalid_"+tc.name, "Hello", tc.fields)
				ok, err := model_evaluation.ValidateProgressDescription(messageDescriptor)
				require.False(t, ok)
				require.ErrorContains(t, err, tc.err)
			})
		}
	})
}
