package render_test

import (
	"testing"

	model_core "bonanza.build/pkg/model/core"
	model_format "bonanza.build/pkg/model/format"
	"bonanza.build/pkg/model/render"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_encoding_pb "bonanza.build/pkg/proto/model/encoding"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/compile"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/emptypb"
)

func renderMessage(m proto.Message, pretty bool, options render.LayoutOptions) string {
	r := render.Renderer{Pretty: pretty}
	options.IndentWidth = 4
	return render.PlainText(
		render.Layout(
			r.Message(model_core.NewSimpleMessage[object.LocalReference](m.ProtoReflect())),
			options,
		),
		"    ",
	)
}

func str(s string) *model_starlark_pb.Value {
	return newStringValue(s)
}

func leaf(v *model_starlark_pb.Value) *model_starlark_pb.List_Element {
	return &model_starlark_pb.List_Element{Level: &model_starlark_pb.List_Element_Leaf{Leaf: v}}
}

func function(name string) *model_starlark_pb.Function {
	return &model_starlark_pb.Function{NamePath: []*model_starlark_pb.Function_Name{{Name: name}}, Filename: "@@//foo:defs.bzl"}
}

func TestRendererStarlarkValues(t *testing.T) {
	for _, tc := range []struct {
		name     string
		value    *model_starlark_pb.Value
		expected string
	}{
		{
			name:     "TupleWithSingleElement",
			value:    &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Tuple{Tuple: &model_starlark_pb.Tuple{Elements: []*model_starlark_pb.Value{str("a\n\"b")}}}},
			expected: `("a\n\"b",)`,
		},
		{
			name:     "NegativeInt",
			value:    &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Int{Int: []byte{0xff, 0x00}}},
			expected: "-256",
		},
		{
			name:     "None",
			value:    &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_None{None: &emptypb.Empty{}}},
			expected: "None",
		},
		{
			name: "Bytes",
			value: &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Bytes{Bytes: &model_filesystem_pb.MaybeInlineFileContents{
				Level: &model_filesystem_pb.MaybeInlineFileContents_ContentsInline{ContentsInline: []byte("\x00ab")},
			}}},
			expected: `b"\000ab"`,
		},
		{
			name:     "Label",
			value:    &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Label{Label: "@@foo+//bar:baz"}},
			expected: `Label("@@foo+//bar:baz")`,
		},
		{
			name: "Depset",
			value: &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Depset{Depset: &model_starlark_pb.Depset{
				Order:    model_starlark_pb.Depset_TOPOLOGICAL,
				Elements: []*model_starlark_pb.List_Element{leaf(str("x"))},
			}}},
			expected: `depset(["x"], order = "topological")`,
		},
		{
			name: "Dict",
			value: &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Dict{Dict: &model_starlark_pb.Dict{Entries: []*model_starlark_pb.Dict_Entry{{
				Level: &model_starlark_pb.Dict_Entry_Leaf_{Leaf: &model_starlark_pb.Dict_Entry_Leaf{
					Key:   str("k"),
					Value: &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Bool{Bool: true}},
				}},
			}}}}},
			expected: `{"k": True}`,
		},
		{
			name: "SelectWithPlainValue",
			value: &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Select{Select: &model_starlark_pb.Select{
				ConcatenationOperator: model_starlark_pb.Select_PLUS,
				Groups: []*model_starlark_pb.Select_Group{
					{NoMatch: &model_starlark_pb.Select_Group_NoMatchValue{NoMatchValue: str("p")}},
					{
						Conditions: []*model_starlark_pb.Select_Condition{{ConditionIdentifier: "//c:linux", Value: str("l")}},
						NoMatch:    &model_starlark_pb.Select_Group_NoMatchError{NoMatchError: "oops"},
					},
				},
			}}},
			expected: `"p" + select({"//c:linux": "l"}, no_match_error = "oops")`,
		},
		{
			name: "Attr",
			value: &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Attr{Attr: &model_starlark_pb.Attr{
				Default:      str("d"),
				Type:         &model_starlark_pb.Attr_String_{String_: &model_starlark_pb.Attr_StringType{Values: []string{"d", "e"}}},
				Configurable: true,
			}}},
			expected: `attr.string(default = "d", values = ["d", "e"])`,
		},
		{
			name: "AttrNonConfigurable",
			value: &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Attr{Attr: &model_starlark_pb.Attr{
				Type: &model_starlark_pb.Attr_String_{String_: &model_starlark_pb.Attr_StringType{}},
			}}},
			expected: `attr.string(configurable = False)`,
		},
		{
			name: "AttrOutput",
			value: &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Attr{Attr: &model_starlark_pb.Attr{
				Type: &model_starlark_pb.Attr_Output{Output: &model_starlark_pb.Attr_OutputType{}},
			}}},
			expected: `attr.output()`,
		},
		{
			name:     "ToolchainType",
			value:    &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_ToolchainType{ToolchainType: &model_starlark_pb.ToolchainType{ToolchainType: "//t:type"}}},
			expected: `config_common.toolchain_type("//t:type", mandatory = False)`,
		},
		{
			name:     "RuleReference",
			value:    &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Rule{Rule: &model_starlark_pb.Rule{Kind: &model_starlark_pb.Rule_Reference{Reference: "@@//foo:defs.bzl%foo_rule"}}}},
			expected: "<rule foo_rule>",
		},
		{
			name:     "MacroReference",
			value:    &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Macro{Macro: &model_starlark_pb.Macro{Kind: &model_starlark_pb.Macro_Reference{Reference: "@@//foo:defs.bzl%foo_macro"}}}},
			expected: "<macro foo_macro>",
		},
		{
			name: "MacroDefinition",
			value: &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Macro{Macro: &model_starlark_pb.Macro{Kind: &model_starlark_pb.Macro_Definition_{Definition: &model_starlark_pb.Macro_Definition{
				Implementation: function("_impl"),
				Finalizer:      true,
			}}}}},
			expected: "macro(implementation = <function _impl>, finalizer = True)",
		},
		{
			name: "Provider",
			value: &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Provider{Provider: &model_starlark_pb.Provider{
				InstanceProperties: &model_starlark_pb.Provider_InstanceProperties{ProviderIdentifier: "@@//foo:defs.bzl%FooInfo", TypeName: "Foo"},
				Fields:             []string{"a", "b"},
				InitFunction:       function("_init"),
			}}},
			expected: `provider(fields = ["a", "b"], init = <function _init>, type_name = "Foo")`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, renderMessage(tc.value, true, render.LayoutOptions{Width: 80}))
		})
	}
}

func TestRendererStarlarkStruct(t *testing.T) {
	value := &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Struct{Struct: &model_starlark_pb.Struct{
		ProviderInstanceProperties: &model_starlark_pb.Provider_InstanceProperties{
			ProviderIdentifier: "@@rules_cc+//cc:defs.bzl%CcInfo",
		},
		Fields: &model_starlark_pb.Struct_Fields{
			Keys: []string{"a", "b", "c", "long"},
			Values: []*model_starlark_pb.List_Element{
				leaf(str("x")),
				// Fields "b" and "c" are stored in a separate
				// object.
				{Level: &model_starlark_pb.List_Element_Parent_{Parent: &model_starlark_pb.List_Element_Parent{Count: 2}}},
				leaf(&model_starlark_pb.Value{Kind: &model_starlark_pb.Value_List{List: &model_starlark_pb.List{
					Elements: []*model_starlark_pb.List_Element{
						leaf(str("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")),
						leaf(str("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")),
					},
				}}}),
			},
		},
	}}}

	t.Run("SingleLine", func(t *testing.T) {
		require.Equal(
			t,
			`CcInfo(a = "x", <fields b to c>, long = ["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"])`,
			renderMessage(value, true, render.LayoutOptions{SingleLine: true}),
		)
	})

	t.Run("MultiLine", func(t *testing.T) {
		require.Equal(
			t,
			"CcInfo(\n"+
				"    a = \"x\",\n"+
				"    <fields b to c>,\n"+
				"    long = [\n"+
				"        \"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\n"+
				"        \"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\",\n"+
				"    ],\n"+
				")",
			renderMessage(value, true, render.LayoutOptions{Width: 80}),
		)
	})
}

func TestRendererStarlarkRule(t *testing.T) {
	value := &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Rule{Rule: &model_starlark_pb.Rule{Kind: &model_starlark_pb.Rule_Definition_{Definition: &model_starlark_pb.Rule_Definition{
		Implementation: function("_impl"),
		Attrs: []*model_starlark_pb.NamedAttr{
			{
				Name: "deps",
				Attr: &model_starlark_pb.Attr{
					Type: &model_starlark_pb.Attr_LabelList{LabelList: &model_starlark_pb.Attr_LabelListType{
						ListOptions:      &model_starlark_pb.Attr_CompositeOptions{AllowEmpty: true},
						ListValueOptions: &model_starlark_pb.Attr_LabelOptions{Providers: []*model_starlark_pb.ProviderSet{{ProviderIdentifiers: []string{"@@rules_cc+//cc:defs.bzl%CcInfo"}}}},
					}},
					Configurable: true,
				},
			},
			{
				Name: "_tool",
				Attr: &model_starlark_pb.Attr{
					Default: &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Label{Label: "@@//tools:tool"}},
					Type: &model_starlark_pb.Attr_Label{Label: &model_starlark_pb.Attr_LabelType{
						Executable: true,
						ValueOptions: &model_starlark_pb.Attr_LabelOptions{
							Cfg:       &model_starlark_pb.Transition{Kind: &model_starlark_pb.Transition_ExecGroup{}},
							Providers: []*model_starlark_pb.ProviderSet{{}},
						},
					}},
					Configurable: true,
				},
			},
		},
		BuildSetting: &model_starlark_pb.BuildSetting{
			Flag: true,
			Type: &model_starlark_pb.BuildSetting_StringList{StringList: &model_starlark_pb.BuildSetting_ListType{Repeatable: true}},
		},
		CfgTransition: &model_starlark_pb.Transition_UserDefined{Kind: &model_starlark_pb.Transition_UserDefined_Definition_{
			Definition: &model_starlark_pb.Transition_UserDefined_Definition{
				Implementation: function("_transition"),
				Inputs:         []string{"//command_line_option:cpu"},
			},
		}},
		Provides: []string{"@@//foo:defs.bzl%FooInfo"},
		Type:     model_starlark_pb.Rule_Definition_TEST,
	}}}}}

	require.Equal(
		t,
		"rule(\n"+
			"    implementation = <function _impl>,\n"+
			"    test = True,\n"+
			"    attrs = {\n"+
			"        \"deps\": attr.label_list(providers = [[<provider CcInfo>]]),\n"+
			"        \"_tool\": attr.label(\n"+
			"            default = Label(\"@@//tools:tool\"),\n"+
			"            cfg = config.exec(),\n"+
			"            executable = True,\n"+
			"        ),\n"+
			"    },\n"+
			"    build_setting = config.string_list(flag = True, repeatable = True),\n"+
			"    cfg = transition(\n"+
			"        implementation = <function _transition>,\n"+
			"        inputs = [\"//command_line_option:cpu\"],\n"+
			"        outputs = [],\n"+
			"    ),\n"+
			"    provides = [<provider FooInfo>],\n"+
			")",
		renderMessage(value, true, render.LayoutOptions{Width: 100}),
	)
}

func TestRendererStarlarkAspect(t *testing.T) {
	value := &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Aspect{Aspect: &model_starlark_pb.Aspect{Kind: &model_starlark_pb.Aspect_Definition_{Definition: &model_starlark_pb.Aspect_Definition{
		Implementation:         function("_impl"),
		ApplyToGeneratingRules: true,
		AttrAspects:            &model_starlark_pb.Aspect_PropagationEdges{Kind: &model_starlark_pb.Aspect_PropagationEdges_Fixed_{Fixed: &model_starlark_pb.Aspect_PropagationEdges_Fixed{Names: []string{"deps"}}}},
		Attrs: []*model_starlark_pb.NamedAttr{
			{
				Name: "_srcs",
				Attr: &model_starlark_pb.Attr{
					Default: &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_List{List: &model_starlark_pb.List{}}},
					Type: &model_starlark_pb.Attr_LabelList{LabelList: &model_starlark_pb.Attr_LabelListType{
						ListValueOptions: &model_starlark_pb.Attr_LabelOptions{
							AllowFiles: &model_starlark_pb.Attr_AllowFiles{},
							Aspects:    []string{"@@//foo:defs.bzl%other_aspect"},
							Providers:  []*model_starlark_pb.ProviderSet{{}},
						},
					}},
					Configurable: true,
				},
			},
		},
		Provides:                []string{"@@//foo:defs.bzl%FooInfo"},
		RequiredAspectProviders: []*model_starlark_pb.ProviderSet{{ProviderIdentifiers: []string{"@@//foo:defs.bzl%BarInfo"}}},
		RequiredProviders:       []*model_starlark_pb.ProviderSet{{}},
		Requires:                []string{"@@//foo:defs.bzl%other_aspect"},
		ToolchainsAspects:       &model_starlark_pb.Aspect_PropagationEdges{Kind: &model_starlark_pb.Aspect_PropagationEdges_All{All: &emptypb.Empty{}}},
	}}}}}

	require.Equal(
		t,
		"aspect(\n"+
			"    implementation = <function _impl>,\n"+
			"    apply_to_generating_rules = True,\n"+
			"    attr_aspects = [\"deps\"],\n"+
			"    attrs = {\n"+
			"        \"_srcs\": attr.label_list(\n"+
			"            default = [],\n"+
			"            allow_empty = False,\n"+
			"            allow_files = True,\n"+
			"            aspects = [<aspect other_aspect>],\n"+
			"        ),\n"+
			"    },\n"+
			"    provides = [<provider FooInfo>],\n"+
			"    required_aspect_providers = [[<provider BarInfo>]],\n"+
			"    requires = [<aspect other_aspect>],\n"+
			"    toolchains_aspects = [\"*\"],\n"+
			")",
		renderMessage(value, true, render.LayoutOptions{Width: 100}),
	)
}

func TestRendererStarlarkExecutableRule(t *testing.T) {
	value := &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Rule{Rule: &model_starlark_pb.Rule{Kind: &model_starlark_pb.Rule_Definition_{Definition: &model_starlark_pb.Rule_Definition{
		Implementation: function("_impl"),
		Type:           model_starlark_pb.Rule_Definition_EXECUTABLE,
	}}}}}

	require.Equal(
		t,
		"rule(implementation = <function _impl>, executable = True)",
		renderMessage(value, true, render.LayoutOptions{Width: 100}),
	)
}

func TestRendererJSON(t *testing.T) {
	// When pretty printing is disabled, Starlark values should be
	// rendered as JSON.
	value := &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Tuple{Tuple: &model_starlark_pb.Tuple{
		Elements: []*model_starlark_pb.Value{
			str("a"),
			{Kind: &model_starlark_pb.Value_Bool{Bool: true}},
		},
	}}}

	t.Run("SingleLine", func(t *testing.T) {
		require.Equal(
			t,
			`{"tuple": {"elements": [{"str": {"contentsInline": "YQ=="}}, {"bool": true}]}}`,
			renderMessage(value, false, render.LayoutOptions{SingleLine: true}),
		)
	})

	t.Run("MultiLine", func(t *testing.T) {
		// JSON does not permit trailing commas.
		require.Equal(
			t,
			"{\n"+
				"    \"tuple\": {\n"+
				"        \"elements\": [\n"+
				"            {\"str\": {\"contentsInline\": \"YQ==\"}},\n"+
				"            {\"bool\": true}\n"+
				"        ]\n"+
				"    }\n"+
				"}",
			renderMessage(value, false, render.LayoutOptions{Width: 50}),
		)
	})

	t.Run("OnMessage", func(t *testing.T) {
		var observed []string
		r := render.Renderer{
			OnMessage: func(m protoreflect.Message) {
				observed = append(observed, string(m.Descriptor().Name()))
			},
		}
		r.Message(model_core.NewSimpleMessage[object.LocalReference](value.ProtoReflect()))
		require.Equal(t, []string{"Value", "Tuple", "Value", "MaybeInlineFileContents", "Value"}, observed)
	})
}

func TestRendererOpaque(t *testing.T) {
	// Instead of rendering the indices contained in the reference
	// set, the references themselves should be rendered, linking to
	// the referenced objects in raw form.
	reference1 := object.MustNewSHA256V1LocalReference("8b1a9953c4611296a827abf8c47804d7e6c49c6b2c4e7e3f5d6a7b8c9d0e1f20", 123, 1, 2, 0)
	reference2 := object.MustNewSHA256V1LocalReference("0b1a9953c4611296a827abf8c47804d7e6c49c6b2c4e7e3f5d6a7b8c9d0e1f20", 456, 0, 0, 0)
	reference3 := object.MustNewSHA256V1LocalReference("1b1a9953c4611296a827abf8c47804d7e6c49c6b2c4e7e3f5d6a7b8c9d0e1f20", 789, 0, 0, 0)

	r := render.Renderer{}
	blocks := render.Layout(
		r.Message(model_core.NewMessage(
			(&model_core_pb.Opaque{
				Value: []byte("Hello"),
				References: &model_core_pb.ReferenceSet{
					Indices: []uint32{1, 3},
				},
			}).ProtoReflect(),
			object.OutgoingReferencesList[object.LocalReference]{reference1, reference2, reference3},
		)),
		render.LayoutOptions{SingleLine: true},
	)
	require.Equal(
		t,
		`{"references": [`+
			`"`+model_core.DecodableLocalReferenceToString(model_core.Decodable[object.LocalReference]{Value: reference1})+`", `+
			`"`+model_core.DecodableLocalReferenceToString(model_core.Decodable[object.LocalReference]{Value: reference3})+`"`+
			`], "value": "SGVsbG8="}`,
		render.PlainText(blocks, ""),
	)

	var links []*render.LinkTarget
	for _, block := range blocks {
		for _, segment := range block.Segments {
			if segment.Link != nil {
				links = append(links, segment.Link)
			}
		}
	}
	require.Len(t, links, 2)
	require.Equal(t, reference1, links[0].Reference.Value)
	require.Equal(t, reference3, links[1].Reference.Value)
	require.Empty(t, links[0].Reference.GetDecodingParameters())
	testutil.RequireEqualProto(t, model_format.Raw{}.ToProto(), links[0].ObjectFormat)

	t.Run("InvalidReferenceSet", func(t *testing.T) {
		require.Equal(
			t,
			"[ rpc error: code = InvalidArgument desc = Reference message set contains indices in range [1, 4], which is outside expected range [1, 3] ]",
			render.PlainText(
				render.Layout(
					r.Message(model_core.NewMessage(
						(&model_core_pb.Opaque{
							References: &model_core_pb.ReferenceSet{
								Indices: []uint32{1, 4},
							},
						}).ProtoReflect(),
						object.OutgoingReferencesList[object.LocalReference]{reference1, reference2, reference3},
					)),
					render.LayoutOptions{SingleLine: true},
				),
				"",
			),
		)
	})
}

// fileEntry creates a DirectoryEntry for a regular file.
func fileEntry(name string, properties *model_filesystem_pb.FileProperties) *model_filesystem_pb.DirectoryEntry {
	return &model_filesystem_pb.DirectoryEntry{
		Level: &model_filesystem_pb.DirectoryEntry_Node_{
			Node: &model_filesystem_pb.DirectoryEntry_Node{
				Name: name,
				Kind: &model_filesystem_pb.DirectoryEntry_Node_File{File: properties},
			},
		},
	}
}

// symlinkEntry creates a DirectoryEntry for a symbolic link.
func symlinkEntry(name, target string) *model_filesystem_pb.DirectoryEntry {
	return &model_filesystem_pb.DirectoryEntry{
		Level: &model_filesystem_pb.DirectoryEntry_Node_{
			Node: &model_filesystem_pb.DirectoryEntry_Node{
				Name: name,
				Kind: &model_filesystem_pb.DirectoryEntry_Node_Symlink{
					Symlink: &model_filesystem_pb.SymlinkProperties{Target: target},
				},
			},
		},
	}
}

// directoryNodeEntry creates a DirectoryEntry for a subdirectory node.
func directoryNodeEntry(name string) *model_filesystem_pb.DirectoryEntry {
	return &model_filesystem_pb.DirectoryEntry{
		Level: &model_filesystem_pb.DirectoryEntry_Node_{
			Node: &model_filesystem_pb.DirectoryEntry_Node{
				Name: name,
				Kind: &model_filesystem_pb.DirectoryEntry_Node_Directory{
					Directory: &model_filesystem_pb.DirectoryProperties{},
				},
			},
		},
	}
}

// directoryContentsEntry creates a DirectoryEntry containing the
// contents of a subdirectory.
func directoryContentsEntry(entries ...*model_filesystem_pb.DirectoryEntry) *model_filesystem_pb.DirectoryEntry {
	return &model_filesystem_pb.DirectoryEntry{
		Level: &model_filesystem_pb.DirectoryEntry_DirectoryContents_{
			DirectoryContents: &model_filesystem_pb.DirectoryEntry_DirectoryContents{Entries: entries},
		},
	}
}

// parentEntry creates a DirectoryEntry referring to entries stored in
// another object.
func parentEntry(parent *model_filesystem_pb.DirectoryEntry_Parent) *model_filesystem_pb.DirectoryEntry {
	parent.Reference = &model_core_pb.DecodableReference{
		Reference: &model_core_pb.Reference{Index: 1},
	}
	return &model_filesystem_pb.DirectoryEntry{
		Level: &model_filesystem_pb.DirectoryEntry_Parent_{Parent: parent},
	}
}

// directoryEntriesToProtoReflect converts a list of directory entries
// to a form that can be provided to Renderer.MessageList().
func directoryEntriesToProtoReflect(entries []*model_filesystem_pb.DirectoryEntry) []protoreflect.Message {
	list := make([]protoreflect.Message, 0, len(entries))
	for _, entry := range entries {
		list = append(list, entry.ProtoReflect())
	}
	return list
}

func TestRendererDirectory(t *testing.T) {
	directory := []*model_filesystem_pb.DirectoryEntry{
		directoryContentsEntry(symlinkEntry("link", "../file")),
		fileEntry("empty", &model_filesystem_pb.FileProperties{}),
		fileEntry("file", &model_filesystem_pb.FileProperties{
			IsExecutable: true,
			Contents:     &model_filesystem_pb.FileContents{TotalSizeBytes: 1843200},
		}),
		fileEntry("small", &model_filesystem_pb.FileProperties{
			Contents: &model_filesystem_pb.FileContents{
				TotalSizeBytes: 412,
				// 1,233 line breaks, ending with a newline.
				Utf8Stat: 1233<<2 | 1<<1 | 1,
			},
		}),
		directoryNodeEntry("sub"),
	}

	r := render.Renderer{Pretty: true}
	blocks := render.Layout(
		r.MessageList(model_core.NewSimpleMessage[object.LocalReference](directoryEntriesToProtoReflect(directory))),
		render.LayoutOptions{Width: 80},
	)
	require.Len(t, blocks, 1)
	require.Equal(t, []render.TableColumn{
		{Name: "Type"},
		{Name: "Size", AlignRight: true},
		{Name: "Lines", AlignRight: true},
		{Name: "Path"},
	}, blocks[0].Table.Columns)
	require.Equal(
		t,
		"-r--\t0   B\t0\tempty\n"+
			"-r-x\t1.8 MiB\t\tfile\n"+
			"-r--\t412   B\t1,234\tsmall\n"+
			"lr-x\t\t\tsub/link → ../file",
		render.PlainText(blocks, ""),
	)

	// Rounded sizes should have the exact size as a title.
	rows := blocks[0].Table.Rows
	require.Equal(t, []render.Segment{{Text: "1.8 MiB", Title: "1,843,200 bytes"}}, rows[1][1])
	require.Equal(t, []render.Segment{{Text: "412   B"}}, rows[2][1])
}

func TestRendererDirectoryEntriesField(t *testing.T) {
	// Fields of type repeated DirectoryEntry should be rendered as
	// a directory, as opposed to a JSON list.
	contents := &model_filesystem_pb.DirectoryEntry_DirectoryContents{
		Entries: []*model_filesystem_pb.DirectoryEntry{
			symlinkEntry("link", "target"),
		},
	}

	r := render.Renderer{Pretty: true}
	blocks := render.Layout(
		r.Message(model_core.NewSimpleMessage[object.LocalReference](contents.ProtoReflect())),
		render.LayoutOptions{Width: 80},
	)
	require.Equal(
		t,
		"{\n\"entries\": \nlr-x\t\t\tlink → target\n}",
		render.PlainText(blocks, ""),
	)
}

func TestRendererDirectoryExternalChildren(t *testing.T) {
	// The contents of directories "b" and "c" are stored in the
	// same external object. Both should be listed, linking to the
	// same object.
	directory := []*model_filesystem_pb.DirectoryEntry{
		directoryContentsEntry(),
		parentEntry(&model_filesystem_pb.DirectoryEntry_Parent{
			DirectoryContentsCount: 2,
		}),
		directoryNodeEntry("a"),
		directoryNodeEntry("b"),
		directoryNodeEntry("c"),
	}
	reference := object.MustNewSHA256V1LocalReference("8b1a9953c4611296a827abf8c47804d7e6c49c6b2c4e7e3f5d6a7b8c9d0e1f20", 123, 1, 2, 0)

	r := render.Renderer{Pretty: true}
	blocks := render.Layout(
		r.MessageList(model_core.NewMessage(
			directoryEntriesToProtoReflect(directory),
			object.OutgoingReferencesList[object.LocalReference]{reference},
		)),
		render.LayoutOptions{Width: 80},
	)
	require.Len(t, blocks, 1)
	require.Equal(
		t,
		"dr-x\t\t\ta/\n"+
			"dr-x\t\t\tb/ (index 0)\n"+
			"dr-x\t\t\tc/ (index 1)",
		render.PlainText(blocks, ""),
	)

	rows := blocks[0].Table.Rows
	require.Len(t, rows, 3)
	for _, row := range rows[1:] {
		var link *render.LinkTarget
		for _, segment := range row[3] {
			if segment.Link != nil {
				link = segment.Link
			}
		}
		require.NotNil(t, link)
		require.Equal(t, reference, link.Reference.Value)

		// The index should be displayed in grey.
		lastSegment := row[3][len(row[3])-1]
		require.Equal(t, render.RoleDim, lastSegment.Role)
	}
}

func TestRendererDirectoryExternalNodes(t *testing.T) {
	// The nodes of the directory are stored in an external
	// object, while the contents of its only subdirectory are
	// stored inline. The name of the subdirectory is unknown.
	directory := []*model_filesystem_pb.DirectoryEntry{
		directoryContentsEntry(symlinkEntry("link", "target")),
		parentEntry(&model_filesystem_pb.DirectoryEntry_Parent{
			FirstName:           "a",
			LeafNodesCount:      3,
			DirectoryNodesCount: 1,
		}),
	}
	reference := object.MustNewSHA256V1LocalReference("8b1a9953c4611296a827abf8c47804d7e6c49c6b2c4e7e3f5d6a7b8c9d0e1f20", 123, 1, 2, 0)

	r := render.Renderer{Pretty: true}
	blocks := render.Layout(
		r.MessageList(model_core.NewMessage(
			directoryEntriesToProtoReflect(directory),
			object.OutgoingReferencesList[object.LocalReference]{reference},
		)),
		render.LayoutOptions{Width: 80},
	)
	require.Len(t, blocks, 1)
	require.Equal(
		t,
		"\t\t\ta… (4 entries)\n"+
			"lr-x\t\t\t#0/link → target",
		render.PlainText(blocks, ""),
	)
}

func TestRendererDirectoryEntryList(t *testing.T) {
	// An object referenced by a DirectoryEntry.Parent contains a
	// part of a directory's B-tree. It contains two subdirectory
	// nodes, but the contents of only one subdirectory. As it's
	// not known which subdirectory these contents belong to, the
	// contents should be displayed separately.
	entries := []*model_filesystem_pb.DirectoryEntry{
		directoryContentsEntry(symlinkEntry("link", "../a")),
		symlinkEntry("a", "target"),
		directoryNodeEntry("b"),
		directoryNodeEntry("c"),
	}
	list := make([]protoreflect.Message, 0, len(entries))
	for _, entry := range entries {
		list = append(list, entry.ProtoReflect())
	}

	r := render.Renderer{Pretty: true}
	blocks := render.Layout(
		r.MessageList(model_core.NewSimpleMessage[object.LocalReference](list)),
		render.LayoutOptions{Width: 80},
	)
	require.Len(t, blocks, 1)
	require.Equal(
		t,
		"lr-x\t\t\ta → target\n"+
			"dr-x\t\t\tb/\n"+
			"dr-x\t\t\tc/\n"+
			"lr-x\t\t\t#0/link → ../a",
		render.PlainText(blocks, ""),
	)

	t.Run("CompleteDirectory", func(t *testing.T) {
		// If the counts match, the list is assumed to be a
		// complete directory (e.g., an object referenced by a
		// DirectoryReference), and names are matched.
		list := []protoreflect.Message{
			directoryContentsEntry(symlinkEntry("link", "../a")).ProtoReflect(),
			directoryNodeEntry("b").ProtoReflect(),
		}
		blocks := render.Layout(
			r.MessageList(model_core.NewSimpleMessage[object.LocalReference](list)),
			render.LayoutOptions{Width: 80},
		)
		require.Equal(t, "lr-x\t\t\tb/link → ../a", render.PlainText(blocks, ""))
	})
}

// newStringValue creates a Value message containing a string that is
// stored inline.
func newStringValue(s string) *model_starlark_pb.Value {
	contents := &model_filesystem_pb.MaybeInlineFileContents{}
	if s != "" {
		contents.Level = &model_filesystem_pb.MaybeInlineFileContents_ContentsInline{
			ContentsInline: []byte(s),
		}
	}
	return &model_starlark_pb.Value{
		Kind: &model_starlark_pb.Value_Str{Str: contents},
	}
}

func TestRendererFunctionEntryLeaf(t *testing.T) {
	newLeaf := func(code []byte) *model_starlark_pb.CompiledProgram_FunctionEntry_Leaf {
		return &model_starlark_pb.CompiledProgram_FunctionEntry_Leaf{
			Name: &model_starlark_pb.Function_Name{Name: "greet"},
			Code: &model_starlark_pb.Funcode{
				Column: 1,
				Code:   code,
				// Position of the instruction at offset 4, as
				// accessing parameters cannot fail.
				LineTable:       []byte{0x83, 0x85},
				StringConstants: [][]byte{[]byte("hello\n")},
				Locals:          []string{"name"},
				NumParams:       1,
				MaxStack:        2,
				Functions: []*model_starlark_pb.Funcode_NestedFunction{{
					RelativeLine: 2,
					Funcode: &model_starlark_pb.Funcode{
						Code:          []byte{byte(compile.FREECELL), 0x80, byte(compile.RETURN)},
						FreeVariables: []string{"captured"},
						Column:        5,
						LineTable:     []byte{0x80},
						MaxStack:      1,
					},
				}},
			},
		}
	}
	code := []byte{
		byte(compile.STR), 0x80,
		byte(compile.LOCAL), 0x80,
		byte(compile.PLUS),
		byte(compile.JMP), 7,
		byte(compile.RETURN),
	}

	t.Run("Pretty", func(t *testing.T) {
		require.Equal(
			t,
			"function greet(name) stack=2\n"+
				"0\t\tstr\t0\t\"hello\\n\"\n"+
				"2\t\tlocal\t0\tname\n"+
				"4\t2:5\tplus\t\t\n"+
				"5\t\tjmp\t7\t\n"+
				"7\t\treturn\t\t\n"+
				"\n"+
				"function lambda() stack=1\n"+
				"freevars: captured\n"+
				"0\t3:5\tfreecell\t0\tcaptured\n"+
				"2\t\treturn\t\t",
			renderMessage(newLeaf(code), true, render.LayoutOptions{Width: 80}),
		)
	})

	t.Run("JSON", func(t *testing.T) {
		// The disassembly should only be displayed when pretty
		// printing is enabled.
		require.Contains(
			t,
			renderMessage(newLeaf(code), false, render.LayoutOptions{SingleLine: true}),
			`"lineTable"`,
		)
	})

	t.Run("Malformed", func(t *testing.T) {
		// If the code cannot be disassembled, its raw contents
		// should be displayed instead.
		require.Contains(
			t,
			renderMessage(newLeaf([]byte{byte(compile.STR)}), true, render.LayoutOptions{SingleLine: true}),
			`"lineTable"`,
		)
	})
}

func TestRendererSecret(t *testing.T) {
	// Fields annotated with [debug_redact = true] should have
	// their values marked as secret, while the field name itself
	// should remain visible.
	r := render.Renderer{}
	blocks := render.Layout(
		r.Message(model_core.NewSimpleMessage[object.LocalReference](
			(&model_encoding_pb.BinaryEncoder{
				Encoder: &model_encoding_pb.BinaryEncoder_Encrypting{
					Encrypting: &model_encoding_pb.EncryptingBinaryEncoder{
						EncryptionKey: []byte("key"),
					},
				},
			}).ProtoReflect(),
		)),
		render.LayoutOptions{SingleLine: true},
	)
	require.Equal(t, `{"encrypting": {"encryptionKey": "a2V5"}}`, render.PlainText(blocks, ""))

	var secrets []string
	for _, segment := range blocks[0].Segments {
		if segment.Secret {
			secrets = append(secrets, segment.Text)
		}
	}
	require.Equal(t, []string{`"a2V5"`}, secrets)
}
