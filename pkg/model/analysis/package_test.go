package analysis_test

import (
	"context"
	"testing"

	"bonanza.build/pkg/label"
	"bonanza.build/pkg/model/chunking"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/inlinedtree"
	model_encoding "bonanza.build/pkg/model/encoding"
	"bonanza.build/pkg/model/evaluation"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_chunking_pb "bonanza.build/pkg/proto/model/chunking"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/compile"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/resolve"
	"bonanza.build/pkg/starlark/syntax"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"

	"go.uber.org/mock/gomock"
)

func TestPackage(t *testing.T) {
	ctrl, ctx := gomock.WithContext(t.Context(), t)
	bct := newBaseComputerTester(ctrl)

	// newImplementation compiles a .bzl file containing the
	// implementation function of a macro, and returns the encoded
	// function named "_impl".
	newImplementation := func(t *testing.T, source string) (model_core.Message[*model_starlark_pb.Function, model_core.CreatedObjectTree], model_core.Message[*model_starlark_pb.CompiledProgram, model_core.CreatedObjectTree]) {
		filename := util.Must(label.NewCanonicalLabel("@@rules+//:defs.bzl"))
		bzlFileBuiltins, _ := model_starlark.GetBuiltins[model_core.CreatedObjectTree, model_core.CreatedObjectTree]()
		f, err := syntax.Parse(filename.String(), []byte(source), syntax.Options{})
		require.NoError(t, err)
		universe := eval.Universe()
		m, err := resolve.File(
			f,
			&resolve.BzlDialect,
			func(name string) bool { _, ok := bzlFileBuiltins[name]; return ok },
			func(name string) bool { _, ok := universe[name]; return ok },
		)
		require.NoError(t, err)
		thread := eval.NewThread(ctx, universe)
		module := eval.NewInMemoryModule(filename.String(), bzlFileBuiltins, nil)
		program := compile.File(f, m)
		require.NoError(t, eval.ExecFile(thread, program, module))

		capturer := createdObjectTreeCapturer{}
		encodingOptions := &model_starlark.ValueEncodingOptions[model_core.CreatedObjectTree, model_core.CreatedObjectTree]{
			CurrentFilename:       &filename,
			Context:               ctx,
			ObjectEncoder:         model_encoding.NewChainedDeterministicBinaryEncoder(nil),
			ObjectReferenceFormat: object.SHA256V1ReferenceFormat,
			ObjectCapturer:        capturer,
			ObjectChunkingParameters: util.Must(chunking.NewParametersFromProto(&model_chunking_pb.Parameters{
				MinimumSizeBytes: 32 * 1024,
				HorizonSizeBytes: 8 * 32 * 1024,
			}, object.SHA256V1ReferenceFormat)),
		}
		encodedFunction, err := model_starlark.EncodeFunction(
			thread,
			module.Globals()["_impl"].(eval.Function),
			nil,
			encodingOptions,
		)
		require.NoError(t, err)
		encodedProgram, err := model_starlark.EncodeCompiledProgram(thread, program, module.Globals(), encodingOptions)
		require.NoError(t, err)
		return model_core.Unpatch(capturer, encodedFunction).Decay(), model_core.Unpatch(capturer, encodedProgram).Decay()
	}
	stringAttr := func(name string, defaultValue *model_starlark_pb.Value) *model_starlark_pb.NamedAttr {
		return &model_starlark_pb.NamedAttr{
			Name: name,
			Attr: &model_starlark_pb.Attr{
				Default: defaultValue,
				Type: &model_starlark_pb.Attr_String_{
					String_: &model_starlark_pb.Attr_StringType{},
				},
				Configurable: true,
			},
		}
	}
	// newVisibility constructs the package group that is expected
	// to be used as the visibility of a target.
	newVisibility := func(labels ...string) *model_starlark_pb.PackageGroup {
		resolvedLabels := make([]label.ResolvedLabel, 0, len(labels))
		for _, l := range labels {
			resolvedLabels = append(resolvedLabels, util.Must(label.NewResolvedLabel(l)))
		}
		packageGroup, err := model_starlark.NewPackageGroupFromVisibility[model_core.CreatedObjectTree](
			ctx,
			resolvedLabels,
			model_encoding.NewChainedDeterministicBinaryEncoder(nil),
			&inlinedtree.Options{
				ReferenceFormat:  object.SHA256V1ReferenceFormat,
				MaximumSizeBytes: 32 * 1024,
			},
			NewMockPackageEnvironmentForTesting(ctrl),
		)
		require.NoError(t, err)
		return packageGroup.Message
	}

	// newMacroExpansionContextDefinition creates the context for
	// expanding a macro instance of macro
	// "@@rules+//:defs.bzl%my_macro".
	newMacroExpansionContextDefinition := func(publicAttrValues ...*model_starlark_pb.Value) *model_starlark_pb.MacroExpansionContext_Definition {
		definition := &model_starlark_pb.MacroInstance_Definition{
			MacroIdentifier: "@@rules+//:defs.bzl%my_macro",
			InheritableAttrs: &model_starlark_pb.InheritableAttrs{
				Testonly:   true,
				Visibility: newVisibility("@@main+//bar:__pkg__", "@@main+//foo:__pkg__"),
			},
		}
		for _, v := range publicAttrValues {
			definition.PublicAttrValues = append(definition.PublicAttrValues, &model_starlark_pb.RuleTarget_PublicAttrValue{
				ValueParts: []*model_starlark_pb.Select_Group{valueSelectGroup(v)},
			})
		}
		return &model_starlark_pb.MacroExpansionContext_Definition{
			RootMacroInstanceDefinition: definition,
		}
	}
	// expectMacroExpansionContext sets up the expectation for
	// obtaining the context for expanding macro instance "m"
	// declared at the top level of package "@@main+//foo".
	expectMacroExpansionContext := func(e *MockPackageEnvironmentForTesting, definition *model_starlark_pb.MacroExpansionContext_Definition) {
		e.EXPECT().GetMacroExpansionContextValue(
			testutil.EqProto(t, &model_analysis_pb.MacroExpansionContext_Key{Package: "@@main+//foo", MacroInstanceName: []string{"m"}}),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](&model_analysis_pb.MacroExpansionContext_Value{
			Definition: definition,
		}))
	}
	// expectMacroGlobal sets up the expectations for loading the
	// definition of macro "@@rules+//:defs.bzl%my_macro", without
	// loading its implementation function.
	expectMacroGlobal := func(e *MockPackageEnvironmentForTesting, attrs []*model_starlark_pb.NamedAttr, implementation model_core.Message[*model_starlark_pb.Function, model_core.CreatedObjectTree]) {
		if !implementation.IsSet() {
			implementation = model_core.NewSimpleMessage[model_core.CreatedObjectTree](&model_starlark_pb.Function{
				Filename: "@@rules+//:defs.bzl",
				NamePath: []*model_starlark_pb.Function_Name{{Name: "_impl"}},
			})
		}
		e.EXPECT().GetBuiltinsModuleNamesValue(gomock.Any()).
			Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](&model_analysis_pb.BuiltinsModuleNames_Value{})).
			AnyTimes()
		e.EXPECT().GetCompiledBzlFileGlobalValue(
			testutil.EqProto(t, &model_analysis_pb.CompiledBzlFileGlobal_Key{Identifier: "@@rules+//:defs.bzl%my_macro"}),
		).Return(model_core.Nested(implementation, &model_analysis_pb.CompiledBzlFileGlobal_Value{
			Global: &model_starlark_pb.Value{
				Kind: &model_starlark_pb.Value_Macro{
					Macro: &model_starlark_pb.Macro{
						Kind: &model_starlark_pb.Macro_Definition_{
							Definition: &model_starlark_pb.Macro_Definition{
								Attrs:          attrs,
								Implementation: implementation.Message,
							},
						},
					},
				},
			},
		}))
	}
	// expectMacroDefinition sets up the expectations for loading
	// macro "@@rules+//:defs.bzl%my_macro" with a given
	// implementation function.
	expectMacroDefinition := func(e *MockPackageEnvironmentForTesting, source string, attrs []*model_starlark_pb.NamedAttr) {
		implementation, program := newImplementation(t, source)
		e.EXPECT().GetCompiledBzlFileValue(
			testutil.EqProto(t, &model_analysis_pb.CompiledBzlFile_Key{Label: "@@rules+//:defs.bzl"}),
		).Return(model_core.Nested(program, &model_analysis_pb.CompiledBzlFile_Value{
			CompiledProgram: program.Message,
		})).AnyTimes()
		expectMacroGlobal(e, attrs, implementation)
	}
	// expectMacro sets up the expectations for expanding macro
	// instance "@@main+//foo:m", declared at the top level of the
	// package, with a given implementation function and attribute
	// values.
	expectMacro := func(e *MockPackageEnvironmentForTesting, source string, attrs []*model_starlark_pb.NamedAttr, publicAttrValues ...*model_starlark_pb.Value) {
		expectMacroExpansionContext(e, newMacroExpansionContextDefinition(publicAttrValues...))
		expectMacroDefinition(e, source, attrs)
	}
	computeMacroInstance := func(e *MockPackageEnvironmentForTesting, macroInstanceNameSuffixes ...string) (*model_analysis_pb.Package_Value, error) {
		value, err := bct.computer.ComputePackageValue(ctx, &model_analysis_pb.Package_Key{Label: "@@main+//foo", MacroInstanceName: macroInstanceNameSuffixes}, e)
		if err != nil {
			return nil, err
		}
		return value.Message, nil
	}
	t.Run("Success", func(t *testing.T) {
		// The implementation function should receive the
		// macro instance's name and visibility, the values of
		// public attributes wrapped in select(), and the default
		// values of private attributes. Targets that are not
		// given an explicit visibility should only be visible
		// to the package containing the macro's definition.
		e := NewMockPackageEnvironmentForTesting(ctrl)
		e.EXPECT().GetFileCreationParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		expectMacro(
			e,
			`
def _impl(name, visibility, actual, _private):
    if type(visibility) != "visibility":
        fail("visibility is not opaque")
    if type(actual) != "select":
        fail("actual is not a select()")
    native.alias(name = name, actual = actual, visibility = visibility)
    native.alias(name = name + "_private", actual = _private)
    native.test_suite(name = name + "_tests")
`,
			[]*model_starlark_pb.NamedAttr{
				stringAttr("_private", stringValue("//baz:private")),
				stringAttr("actual", nil),
			},
			stringValue("//qux:actual"),
		)

		value, err := computeMacroInstance(e, "m")
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_analysis_pb.Package_Value{
			Targets: []*model_analysis_pb.Package_Value_Target{
				{
					Level: &model_analysis_pb.Package_Value_Target_Leaf{
						Leaf: &model_starlark_pb.Target{
							Name: "m",
							Definition: &model_starlark_pb.Target_Definition{
								Kind: &model_starlark_pb.Target_Definition_Alias{
									Alias: &model_starlark_pb.Alias{
										Actual:     valueSelectGroup(labelValue("@@main+//qux:actual")),
										Visibility: newVisibility("@@main+//bar:__pkg__", "@@main+//foo:__pkg__", "@@rules+//:__pkg__"),
									},
								},
							},
						},
					},
				},
				{
					Level: &model_analysis_pb.Package_Value_Target_Leaf{
						Leaf: &model_starlark_pb.Target{
							Name: "m_private",
							Definition: &model_starlark_pb.Target_Definition{
								Kind: &model_starlark_pb.Target_Definition_Alias{
									Alias: &model_starlark_pb.Alias{
										Actual:     valueSelectGroup(labelValue("@@main+//baz:private")),
										Visibility: newVisibility("@@rules+//:__pkg__"),
									},
								},
							},
						},
					},
				},
				{
					Level: &model_analysis_pb.Package_Value_Target_Leaf{
						Leaf: &model_starlark_pb.Target{
							Name: "m_tests",
							Definition: &model_starlark_pb.Target_Definition{
								Kind: &model_starlark_pb.Target_Definition_TestSuite{
									TestSuite: &model_starlark_pb.TestSuite{
										Sizes: []model_starlark_pb.TestSize{
											model_starlark_pb.TestSize_SMALL,
											model_starlark_pb.TestSize_MEDIUM,
											model_starlark_pb.TestSize_LARGE,
											model_starlark_pb.TestSize_ENORMOUS,
										},
										Visibility: newVisibility("@@rules+//:__pkg__"),
									},
								},
							},
						},
					},
				},
			},
		}, value)
	})

	t.Run("NoneUsesDefault", func(t *testing.T) {
		// If no value was provided for an attribute, the
		// implementation function should receive the default
		// value, wrapped in a select(). If the default value is
		// None, None should be passed in without wrapping it.
		// The same holds for mandatory attributes for which None
		// was provided explicitly.
		e := NewMockPackageEnvironmentForTesting(ctrl)
		e.EXPECT().GetFileCreationParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		expectMacro(
			e,
			`
def _impl(name, visibility, mandatory, with_default, without_default):
    if mandatory != None:
        fail("mandatory is not None")
    if type(with_default) != "select":
        fail("with_default is not a select()")
    if without_default != None:
        fail("without_default is not None")
    native.alias(name = name, actual = with_default)
`,
			[]*model_starlark_pb.NamedAttr{
				stringAttr("mandatory", nil),
				stringAttr("with_default", stringValue("//baz:default")),
				stringAttr("without_default", &model_starlark_pb.Value{
					Kind: &model_starlark_pb.Value_None{},
				}),
			},
			&model_starlark_pb.Value{Kind: &model_starlark_pb.Value_None{}},
			&model_starlark_pb.Value{Kind: &model_starlark_pb.Value_None{}},
			&model_starlark_pb.Value{Kind: &model_starlark_pb.Value_None{}},
		)

		value, err := computeMacroInstance(e, "m")
		require.NoError(t, err)
		require.Len(t, value.Targets, 1)
		testutil.RequireEqualProto(t, valueSelectGroup(labelValue("@@main+//baz:default")), value.Targets[0].GetLeaf().GetDefinition().GetAlias().GetActual())
	})

	t.Run("NonConfigurable", func(t *testing.T) {
		// Values of attributes that are not configurable
		// should be provided as is, so that they can be
		// inspected. This also applies to default values.
		nonConfigurableAttr := func(name string, defaultValue *model_starlark_pb.Value) *model_starlark_pb.NamedAttr {
			attr := stringAttr(name, defaultValue)
			attr.Attr.Configurable = false
			return attr
		}
		e := NewMockPackageEnvironmentForTesting(ctrl)
		e.EXPECT().GetFileCreationParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		expectMacro(
			e,
			`
def _impl(name, visibility, explicit, with_default):
    native.alias(name = name + "_" + explicit, actual = "//baz:actual")
    native.alias(name = name + "_" + with_default, actual = "//baz:actual")
`,
			[]*model_starlark_pb.NamedAttr{
				nonConfigurableAttr("explicit", nil),
				nonConfigurableAttr("with_default", stringValue("default")),
			},
			stringValue("explicit"),
			&model_starlark_pb.Value{Kind: &model_starlark_pb.Value_None{}},
		)

		value, err := computeMacroInstance(e, "m")
		require.NoError(t, err)
		require.Len(t, value.Targets, 2)
		require.Equal(t, "m_default", value.Targets[0].GetLeaf().GetName())
		require.Equal(t, "m_explicit", value.Targets[1].GetLeaf().GetName())
	})

	t.Run("NonConfigurableWithSelect", func(t *testing.T) {
		// Values of attributes that are not configurable
		// cannot depend on select().
		unconditional := func(value *model_starlark_pb.Value) *model_starlark_pb.Select_Group {
			return &model_starlark_pb.Select_Group{
				NoMatch: &model_starlark_pb.Select_Group_NoMatchValue{NoMatchValue: value},
			}
		}
		for name, valueParts := range map[string][]*model_starlark_pb.Select_Group{
			"Conditions": {{
				Conditions: []*model_starlark_pb.Select_Condition{{
					ConditionIdentifier: "@@main+//:opt",
					Value:               stringValue("opt"),
				}},
				NoMatch: &model_starlark_pb.Select_Group_NoMatchValue{NoMatchValue: stringValue("default")},
			}},
			"MultipleGroups": {
				unconditional(stringValue("a")),
				unconditional(stringValue("b")),
			},
			"NoMatchError": {{
				NoMatch: &model_starlark_pb.Select_Group_NoMatchError{NoMatchError: "oops"},
			}},
		} {
			t.Run(name, func(t *testing.T) {
				e := NewMockPackageEnvironmentForTesting(ctrl)
				e.EXPECT().GetFileCreationParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
				e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
				attr := stringAttr("explicit", nil)
				attr.Attr.Configurable = false
				expectMacroGlobal(e, []*model_starlark_pb.NamedAttr{attr}, model_core.Message[*model_starlark_pb.Function, model_core.CreatedObjectTree]{})
				definition := newMacroExpansionContextDefinition()
				definition.RootMacroInstanceDefinition.PublicAttrValues = []*model_starlark_pb.RuleTarget_PublicAttrValue{{
					ValueParts: valueParts,
				}}
				expectMacroExpansionContext(e, definition)

				_, err := computeMacroInstance(e, "m")
				require.EqualError(t, err, "macro instance \"m\": attr \"explicit\": value of attribute that is not configurable depends on select()")
			})
		}
	})

	t.Run("InvalidDeclarationName", func(t *testing.T) {
		// Targets declared by macros need to be prefixed by the
		// name of the macro instance.
		e := NewMockPackageEnvironmentForTesting(ctrl)
		e.EXPECT().GetFileCreationParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		expectMacro(
			e,
			`
def _impl(name, visibility):
    native.alias(name = "other", actual = "//baz:actual")
`,
			nil,
		)

		_, err := computeMacroInstance(e, "m")
		require.ErrorContains(t, err, "name \"other\" is not equal to the name of macro instance \"m\", nor prefixed by it followed by \"_\", \"-\" or \".\"")
	})

	t.Run("ReturnValue", func(t *testing.T) {
		e := NewMockPackageEnvironmentForTesting(ctrl)
		e.EXPECT().GetFileCreationParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		expectMacro(
			e,
			`
def _impl(name, visibility):
    return 42
`,
			nil,
		)

		_, err := computeMacroInstance(e, "m")
		require.EqualError(t, err, "macro instance \"m\": implementation function of macro \"@@rules+//:defs.bzl%my_macro\" returned a value of type int, while macros may not return values")
	})

	t.Run("MissingVisibility", func(t *testing.T) {
		e := NewMockPackageEnvironmentForTesting(ctrl)
		e.EXPECT().GetFileCreationParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		definition := newMacroExpansionContextDefinition()
		definition.RootMacroInstanceDefinition.InheritableAttrs.Visibility = nil
		expectMacroExpansionContext(e, definition)

		_, err := computeMacroInstance(e, "m")
		require.EqualError(t, err, "macro instance \"m\": does not have a visibility")
	})

	t.Run("ReferencedTargetNameSuffixes", func(t *testing.T) {
		// Targets in the namespace of the macro instance that
		// are referenced by the caller should be declared as
		// source files, unless the macro declares them.
		e := NewMockPackageEnvironmentForTesting(ctrl)
		e.EXPECT().GetFileCreationParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		definition := newMacroExpansionContextDefinition()
		definition.ReferencedTargetNameSuffixes = []string{"_gen.h", "_src.c"}
		expectMacroExpansionContext(e, definition)
		expectMacroDefinition(e, `
def _impl(name, visibility):
    native.alias(name = name + "_gen.h", actual = "//baz:actual")
`, nil)

		value, err := computeMacroInstance(e, "m")
		require.NoError(t, err)
		require.Len(t, value.Targets, 2)
		require.Equal(t, "m_gen.h", value.Targets[0].GetLeaf().GetName())
		require.NotNil(t, value.Targets[0].GetLeaf().GetDefinition().GetAlias())
		require.Equal(t, "m_src.c", value.Targets[1].GetLeaf().GetName())
		testutil.RequireEqualProto(t, &model_starlark_pb.SourceFileTarget{
			Visibility: &model_starlark_pb.PackageGroup{
				Tree: &model_starlark_pb.PackageGroup_Subpackages{},
			},
		}, value.Targets[1].GetLeaf().GetDefinition().GetSourceFileTarget())
	})

	t.Run("InvalidReferencedTargetName", func(t *testing.T) {
		e := NewMockPackageEnvironmentForTesting(ctrl)
		e.EXPECT().GetFileCreationParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		definition := newMacroExpansionContextDefinition()
		definition.ReferencedTargetNameSuffixes = []string{"x"}
		expectMacroExpansionContext(e, definition)

		_, err := computeMacroInstance(e, "m")
		require.EqualError(t, err, "macro instance \"m\": invalid referenced target name suffix \"x\"")
	})

	t.Run("ForbiddenTargetName", func(t *testing.T) {
		// The caller of a macro may declare targets in the
		// namespace of the macro instance. The macro instance
		// may then not declare these targets, as lookups don't
		// expand the macro instance.
		e := NewMockPackageEnvironmentForTesting(ctrl)
		e.EXPECT().GetFileCreationParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		definition := newMacroExpansionContextDefinition()
		definition.ForbiddenTargetNameSuffixes = []string{"", "_out"}
		expectMacroExpansionContext(e, definition)
		expectMacroDefinition(e, `
def _impl(name, visibility):
    native.alias(name = name + "_other", actual = "//baz:actual")
    native.alias(name = name + "_out", actual = "//baz:actual")
`, nil)

		_, err := computeMacroInstance(e, "m")
		require.EqualError(t, err, "macro instance \"m\": Traceback (most recent call last):\n\tFile \"@@rules+//:defs.bzl\", line 4, column 17, in _impl\nError in native.alias: target \"m_out\" is already declared outside of this macro instance")
	})

	t.Run("Finalizer", func(t *testing.T) {
		// Finalizers may observe all targets in the package,
		// except for the ones declared by finalizers. Targets
		// that don't exist should yield None.
		newAlias := func(name string) *model_analysis_pb.Package_Value_Target {
			return &model_analysis_pb.Package_Value_Target{
				Level: &model_analysis_pb.Package_Value_Target_Leaf{
					Leaf: &model_starlark_pb.Target{
						Name: name,
						Definition: &model_starlark_pb.Target_Definition{
							Kind: &model_starlark_pb.Target_Definition_Alias{
								Alias: &model_starlark_pb.Alias{
									Actual: &model_starlark_pb.Select_Group{
										NoMatch: &model_starlark_pb.Select_Group_NoMatchValue{
											NoMatchValue: &model_starlark_pb.Value{
												Kind: &model_starlark_pb.Value_Label{Label: "@@main+//baz:actual"},
											},
										},
									},
								},
							},
						},
					},
				},
			}
		}

		e := NewMockPackageEnvironmentForTesting(ctrl)
		e.EXPECT().GetFileCreationParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		definition := newMacroExpansionContextDefinition()
		definition.RootMacroInstanceDefinition.Finalizer = model_starlark_pb.MacroInstance_FINALIZER
		expectMacroExpansionContext(e, definition)
		expectMacroDefinition(e, `
def _impl(name, visibility):
    for n, r in native.existing_rules().items():
        native.alias(name = name + "_" + n, actual = r["actual"])
    if native.existing_rule("m_x") == None and native.existing_rule("other_y")["kind"] == "alias":
        native.alias(name = name + "_checked", actual = "//baz:actual")
`, nil)
		e.EXPECT().GetPackageTargetsValue(
			testutil.EqProto(t, &model_analysis_pb.PackageTargets_Key{Package: "@@main+//foo", ExcludeFinalizers: true}),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](&model_analysis_pb.PackageTargets_Value{
			Targets: []*model_analysis_pb.Package_Value_Target{newAlias("top")},
		}))
		e.EXPECT().GetTargetValue(
			testutil.EqProto(t, &model_analysis_pb.Target_Key{Label: "@@main+//foo:m_x"}),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](&model_analysis_pb.Target_Value{}))
		e.EXPECT().GetTargetValue(
			testutil.EqProto(t, &model_analysis_pb.Target_Key{Label: "@@main+//foo:other_y"}),
		).Return(model_core.NewSimpleMessage[model_core.CreatedObjectTree](&model_analysis_pb.Target_Value{
			Definition: newAlias("other_y").GetLeaf().Definition,
		}))

		value, err := computeMacroInstance(e, "m")
		require.NoError(t, err)
		var targetNames []string
		for _, target := range value.Targets {
			targetNames = append(targetNames, target.GetLeaf().GetName())
		}
		require.Equal(t, []string{"m_checked", "m_top"}, targetNames)
	})

	t.Run("ExistingRulesOutsideFinalizer", func(t *testing.T) {
		// Like Bazel, non-finalizer macros may not observe
		// other targets in the package. This also applies to
		// macros called by finalizers.
		for _, finalizer := range []model_starlark_pb.MacroInstance_Finalizer{
			model_starlark_pb.MacroInstance_NOT_A_FINALIZER,
			model_starlark_pb.MacroInstance_CALLED_BY_FINALIZER,
		} {
			e := NewMockPackageEnvironmentForTesting(ctrl)
			e.EXPECT().GetFileCreationParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
			definition := newMacroExpansionContextDefinition()
			definition.RootMacroInstanceDefinition.Finalizer = finalizer
			expectMacroExpansionContext(e, definition)
			expectMacroDefinition(e, `
def _impl(name, visibility):
    native.existing_rules()
`, nil)

			_, err := computeMacroInstance(e, "m")
			require.EqualError(t, err, "macro instance \"m\": Traceback (most recent call last):\n\tFile \"@@rules+//:defs.bzl\", line 3, column 26, in _impl\nError in native.existing_rules: existing rules can only be obtained from within finalizers")
		}
	})

	t.Run("NestedMacroInstances", func(t *testing.T) {
		// Macro instances declared by the caller whose
		// namespace is contained within the namespace of the
		// macro instance should be treated as if the macro
		// instance declared them. The macro instance may
		// declare targets in their namespace, which they may
		// then not declare.
		for name, nestedMacroInstances := range map[string]func(*model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) []*model_starlark_pb.MacroInstance_Entry{
			"Inline": func(*model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) []*model_starlark_pb.MacroInstance_Entry {
				return []*model_starlark_pb.MacroInstance_Entry{
					newMacroInstanceEntry("_lib"),
					newMacroInstanceEntry("_lib_x"),
				}
			},
			"InOtherObjects": func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) []*model_starlark_pb.MacroInstance_Entry {
				return []*model_starlark_pb.MacroInstance_Entry{
					newMacroInstancesParent(patcher, "_lib"),
					newMacroInstancesParent(patcher, "_lib_x"),
				}
			},
		} {
			t.Run(name, func(t *testing.T) {
				e := NewMockPackageEnvironmentForTesting(ctrl)
				e.EXPECT().GetFileCreationParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
				e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
				e.EXPECT().CaptureExistingObject(gomock.Any()).
					DoAndReturn(func(reference model_core.CreatedObjectTree) model_core.CreatedObjectTree {
						return reference
					}).
					AnyTimes()
				e.EXPECT().GetMacroExpansionContextValue(
					testutil.EqProto(t, &model_analysis_pb.MacroExpansionContext_Key{Package: "@@main+//foo", MacroInstanceName: []string{"m"}}),
				).Return(newMessage(func(patcher *model_core.ReferenceMessagePatcher[model_core.CreatedObjectTree]) *model_analysis_pb.MacroExpansionContext_Value {
					definition := newMacroExpansionContextDefinition()
					definition.NestedMacroInstances = nestedMacroInstances(patcher)
					return &model_analysis_pb.MacroExpansionContext_Value{Definition: definition}
				}))
				expectMacroDefinition(e, `
def _impl(name, visibility):
    native.alias(name = name + "_lib_y", actual = "//baz:actual")
`, nil)

				value, err := computeMacroInstance(e, "m")
				require.NoError(t, err)
				require.Len(t, value.Targets, 1)
				require.Equal(t, "m_lib_y", value.Targets[0].GetLeaf().GetName())
				require.Len(t, value.MacroExpansionContexts, 1)
				testutil.RequireEqualProto(t, &model_starlark_pb.MacroExpansionContext{
					NameSuffix: "_lib",
					Definition: &model_starlark_pb.MacroExpansionContext_Definition{
						RootMacroInstanceDefinition: &model_starlark_pb.MacroInstance_Definition{
							MacroIdentifier: "@@rules+//:defs.bzl%my_macro",
						},
						ForbiddenTargetNameSuffixes: []string{"_y"},
						NestedMacroInstances: []*model_starlark_pb.MacroInstance_Entry{
							newMacroInstanceEntry("_x"),
						},
					},
				}, value.MacroExpansionContexts[0].GetLeaf())
			})
		}
	})

	t.Run("ModuleAndRepoNames", func(t *testing.T) {
		// native.repo_name(), native.module_name() and
		// native.module_version() should be derived from the
		// canonical name of the repo containing the package.
		// It does not contain a version number unless multiple
		// versions of the module are permitted to coexist.
		e := NewMockPackageEnvironmentForTesting(ctrl)
		e.EXPECT().GetFileCreationParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		expectMacro(
			e,
			`
def _impl(name, visibility):
    if native.repo_name() != "main+":
        fail("unexpected repo name %r" % native.repo_name())
    if native.module_name() != "main":
        fail("unexpected module name %r" % native.module_name())
    if native.module_version() != "":
        fail("unexpected module version %r" % native.module_version())
    native.alias(name = name, actual = "//baz:actual")
`,
			nil,
		)

		value, err := computeMacroInstance(e, "m")
		require.NoError(t, err)
		require.Len(t, value.Targets, 1)
	})

	t.Run("MissingDependency", func(t *testing.T) {
		// Missing dependencies should be propagated as is, and
		// not be wrapped.
		e := NewMockPackageEnvironmentForTesting(ctrl)
		e.EXPECT().GetFileCreationParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		e.EXPECT().GetFileAccessParametersObject(gomock.Any()).Return(nil, true).AnyTimes()
		e.EXPECT().GetMacroExpansionContextValue(
			testutil.EqProto(t, &model_analysis_pb.MacroExpansionContext_Key{Package: "@@main+//foo", MacroInstanceName: []string{"m"}}),
		).Return(model_core.Message[*model_analysis_pb.MacroExpansionContext_Value, model_core.CreatedObjectTree]{})

		_, err := computeMacroInstance(e, "m")
		require.Equal(t, evaluation.ErrMissingDependency, err)
	})
}

// createdObjectTreeCapturer is an ObjectCapturer that keeps created
// objects in memory, allowing them to be referenced by messages that
// use CreatedObjectTree as their reference type.
type createdObjectTreeCapturer struct{}

func (createdObjectTreeCapturer) CaptureCreatedObject(ctx context.Context, createdObject model_core.CreatedObject[model_core.CreatedObjectTree]) (model_core.CreatedObjectTree, error) {
	return model_core.CreatedObjectTree(createdObject), nil
}

func (createdObjectTreeCapturer) CaptureExistingObject(reference model_core.CreatedObjectTree) model_core.CreatedObjectTree {
	return reference
}

func (createdObjectTreeCapturer) ReferenceObject(entry model_core.MetadataEntry[model_core.CreatedObjectTree]) model_core.CreatedObjectTree {
	return entry.Metadata
}
