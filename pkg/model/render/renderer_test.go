package render_test

import (
	"testing"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/render"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/storage/object"

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
	return &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Str{Str: s}}
}

func leaf(v *model_starlark_pb.Value) *model_starlark_pb.List_Element {
	return &model_starlark_pb.List_Element{Level: &model_starlark_pb.List_Element_Leaf{Leaf: v}}
}

func function(name string) *model_starlark_pb.Function {
	return &model_starlark_pb.Function{Name: name, Filename: "@@//foo:defs.bzl", Line: 3, Column: 1}
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
			value:    &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Int{Int: &model_starlark_pb.Int{AbsoluteValue: []byte{1, 0}, Negative: true}}},
			expected: "-256",
		},
		{
			name:     "None",
			value:    &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_None{None: &emptypb.Empty{}}},
			expected: "None",
		},
		{
			name:     "Bytes",
			value:    &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Bytes{Bytes: []byte("\x00ab")}},
			expected: `b"\x00ab"`,
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
				Default: str("d"),
				Type:    &model_starlark_pb.Attr_String_{String_: &model_starlark_pb.Attr_StringType{Values: []string{"d", "e"}}},
			}}},
			expected: `attr.string(default = "d", values = ["d", "e"])`,
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
				Attr: &model_starlark_pb.Attr{Type: &model_starlark_pb.Attr_LabelList{LabelList: &model_starlark_pb.Attr_LabelListType{
					ListValueOptions: &model_starlark_pb.Attr_LabelOptions{Providers: []string{"@@rules_cc+//cc:defs.bzl%CcInfo"}},
				}}},
			},
			{
				Name: "_tool",
				Attr: &model_starlark_pb.Attr{
					Default: &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Label{Label: "@@//tools:tool"}},
					Type: &model_starlark_pb.Attr_Label{Label: &model_starlark_pb.Attr_LabelType{
						Executable:   true,
						ValueOptions: &model_starlark_pb.Attr_LabelOptions{Cfg: &model_starlark_pb.Transition{Kind: &model_starlark_pb.Transition_ExecGroup{}}},
					}},
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
			"        \"deps\": attr.label_list(providers = [<provider CcInfo>]),\n"+
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
			`{"tuple": {"elements": [{"str": "a"}, {"bool": true}]}}`,
			renderMessage(value, false, render.LayoutOptions{SingleLine: true}),
		)
	})

	t.Run("MultiLine", func(t *testing.T) {
		// JSON does not permit trailing commas.
		require.Equal(
			t,
			"{\n"+
				"    \"tuple\": {\n"+
				"        \"elements\": [{\"str\": \"a\"}, {\"bool\": true}]\n"+
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
		require.Equal(t, []string{"Value", "Tuple", "Value", "Value"}, observed)
	})
}

func TestRendererDirectory(t *testing.T) {
	directory := &model_filesystem_pb.DirectoryContents{
		Directories: []*model_filesystem_pb.DirectoryNode{{
			Name: "sub",
			Directory: &model_filesystem_pb.Directory{
				Contents: &model_filesystem_pb.Directory_ContentsInline{
					ContentsInline: &model_filesystem_pb.DirectoryContents{
						Leaves: &model_filesystem_pb.DirectoryContents_LeavesInline{
							LeavesInline: &model_filesystem_pb.Leaves{
								Symlinks: []*model_filesystem_pb.SymlinkNode{{Name: "link", Target: "../file"}},
							},
						},
					},
				},
			},
		}},
		Leaves: &model_filesystem_pb.DirectoryContents_LeavesInline{
			LeavesInline: &model_filesystem_pb.Leaves{
				Files: []*model_filesystem_pb.FileNode{
					{
						Name: "file",
						Properties: &model_filesystem_pb.FileProperties{
							IsExecutable: true,
							Contents:     &model_filesystem_pb.FileContents{TotalSizeBytes: 1843200},
						},
					},
					{
						Name: "small",
						Properties: &model_filesystem_pb.FileProperties{
							Contents: &model_filesystem_pb.FileContents{TotalSizeBytes: 412},
						},
					},
				},
			},
		},
	}

	r := render.Renderer{Pretty: true}
	blocks := render.Layout(
		r.Message(model_core.NewSimpleMessage[object.LocalReference](directory.ProtoReflect())),
		render.LayoutOptions{Width: 80},
	)
	require.Len(t, blocks, 1)
	require.Equal(t, []render.TableColumn{
		{Name: "Type"},
		{Name: "Size", AlignRight: true},
		{Name: "Path"},
	}, blocks[0].Table.Columns)
	require.Equal(
		t,
		"-r-x\t1.8 MiB\tfile\n"+
			"-r--\t412   B\tsmall\n"+
			"lr-x\t\tsub/link → ../file",
		render.PlainText(blocks, ""),
	)

	// Rounded sizes should have the exact size as a title.
	rows := blocks[0].Table.Rows
	require.Equal(t, []render.Segment{{Text: "1.8 MiB", Title: "1,843,200 bytes"}}, rows[0][1])
	require.Equal(t, []render.Segment{{Text: "412   B"}}, rows[1][1])
}
