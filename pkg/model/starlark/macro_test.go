package starlark_test

import (
	"fmt"
	"maps"
	"math/big"
	"slices"
	"strings"
	"testing"

	"bonanza.build/pkg/glob"
	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/inlinedtree"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"go.uber.org/mock/gomock"
)

func TestMacro(t *testing.T) {
	ctrl, ctx := gomock.WithContext(t.Context(), t)
	referenceFormat := util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1))
	encoder := model_encoding.NewChainedDeterministicBinaryEncoder(nil)
	inlinedTreeOptions := &inlinedtree.Options{
		ReferenceFormat:  referenceFormat,
		MaximumSizeBytes: 32 * 1024,
	}
	bzlFileBuiltins, _ := model_starlark.GetBuiltins[object.LocalReference, model_core.CreatedObjectTree]()
	nativeAlias := util.Must(bzlFileBuiltins["native"].(starlark.HasAttrs).Attr(&starlark.Thread{}, "alias"))
	nativeExportsFiles := util.Must(bzlFileBuiltins["native"].(starlark.HasAttrs).Attr(&starlark.Thread{}, "exports_files"))
	identifier := util.Must(label.NewCanonicalStarlarkIdentifier("@@rules+//:defs.bzl%my_macro"))
	defaultInheritableAttrs := model_core.NewSimpleMessage[object.LocalReference](&model_starlark_pb.InheritableAttrs{
		Deprecation: "Don't use this",
		Testonly:    true,
	})

	// newThread creates a Starlark thread for declaring targets in
	// package @@main+//foo.
	newThread := func(t *testing.T, targetRegistrar *model_starlark.TargetRegistrar[object.LocalReference, model_core.CreatedObjectTree], objectManager *MockObjectManagerForTesting) *starlark.Thread {
		t.Cleanup(targetRegistrar.Discard)
		thread := &starlark.Thread{}
		thread.SetLocal(model_starlark.TargetRegistrarKey, targetRegistrar)
		thread.SetLocal(model_starlark.CanonicalPackageKey, util.Must(label.NewCanonicalPackage("@@main+//foo")))
		thread.SetLocal(model_starlark.LabelResolverKey, NewMockLabelResolver(ctrl))
		thread.SetLocal(model_starlark.ValueEncodingOptionsKey, &model_starlark.ValueEncodingOptions[object.LocalReference, model_core.CreatedObjectTree]{
			Context:                ctx,
			ObjectEncoder:          encoder,
			ObjectReferenceFormat:  referenceFormat,
			ObjectCapturer:         objectManager,
			ObjectMinimumSizeBytes: 32 * 1024,
			ObjectMaximumSizeBytes: 128 * 1024,
		})
		thread.SetLocal(model_starlark.ValueDecodingOptionsKey, &model_starlark.ValueDecodingOptions[object.LocalReference]{
			Context: ctx,
			Readers: &model_starlark.ValueReaders[object.LocalReference]{},
		})
		return thread
	}
	newTopLevel := func(t *testing.T) (*starlark.Thread, *model_starlark.TargetRegistrar[object.LocalReference, model_core.CreatedObjectTree]) {
		objectManager := NewMockObjectManagerForTesting(ctrl)
		targetRegistrar := model_starlark.NewTargetRegistrar(ctx, encoder, inlinedTreeOptions, objectManager, defaultInheritableAttrs)
		return newThread(t, targetRegistrar, objectManager), targetRegistrar
	}
	// newExpansion creates a Starlark thread for expanding macro
	// instance "m", whose macro is declared in package
	// @@rules+//macros.
	newExpansion := func(t *testing.T, definition *model_starlark_pb.MacroExpansionContext_Definition) (*starlark.Thread, *model_starlark.TargetRegistrar[object.LocalReference, model_core.CreatedObjectTree]) {
		objectManager := NewMockObjectManagerForTesting(ctrl)
		targetRegistrar := model_starlark.NewMacroTargetRegistrar(ctx, encoder, inlinedTreeOptions, objectManager, "m", util.Must(label.NewCanonicalPackage("@@rules+//macros")), model_core.NewSimpleMessage[object.LocalReference](definition))
		return newThread(t, targetRegistrar, objectManager), targetRegistrar
	}
	newMacro := func(attrs map[label.StarlarkIdentifier]*model_starlark.Attr[object.LocalReference, model_core.CreatedObjectTree], finalizer bool) starlark.Value {
		return model_starlark.NewMacro(
			&identifier,
			model_starlark.NewStarlarkMacroDefinition(
				attrs,
				model_starlark.NamedFunction[object.LocalReference, model_core.CreatedObjectTree]{},
				finalizer,
			),
		)
	}
	exampleMacro := newMacro(map[label.StarlarkIdentifier]*model_starlark.Attr[object.LocalReference, model_core.CreatedObjectTree]{
		util.Must(label.NewStarlarkIdentifier("_private")): model_starlark.NewAttr[object.LocalReference, model_core.CreatedObjectTree](
			model_starlark.NewStringAttrType[object.LocalReference, model_core.CreatedObjectTree](nil),
			starlark.String("default"),
			/* configurable = */ true,
		),
		util.Must(label.NewStarlarkIdentifier("count")): model_starlark.NewAttr[object.LocalReference, model_core.CreatedObjectTree](
			model_starlark.NewIntAttrType[object.LocalReference, model_core.CreatedObjectTree](nil),
			nil,
			/* configurable = */ true,
		),
		util.Must(label.NewStarlarkIdentifier("srcs")): model_starlark.NewAttr[object.LocalReference, model_core.CreatedObjectTree](
			model_starlark.NewLabelListAttrType[object.LocalReference, model_core.CreatedObjectTree](glob.NFAMatchingEverything.Bytes(), nil),
			starlark.NewList(nil),
			/* configurable = */ true,
		),
	}, false)
	exampleFinalizer := newMacro(nil, true)
	call := func(thread *starlark.Thread, callee starlark.Value, args starlark.Tuple, kwargs ...starlark.Tuple) error {
		_, err := starlark.Call(thread, callee, args, kwargs)
		return err
	}
	kwarg := func(name string, value starlark.Value) starlark.Tuple {
		return starlark.Tuple{starlark.String(name), value}
	}
	stringList := func(values ...string) starlark.Value {
		l := make([]starlark.Value, 0, len(values))
		for _, v := range values {
			l = append(l, starlark.String(v))
		}
		return starlark.NewList(l)
	}
	noMatchGroup := func(v *model_starlark_pb.Value) *model_starlark_pb.Select_Group {
		return &model_starlark_pb.Select_Group{
			NoMatch: &model_starlark_pb.Select_Group_NoMatchValue{NoMatchValue: v},
		}
	}
	labelList := func(labels ...string) *model_starlark_pb.Value {
		elements := make([]*model_starlark_pb.List_Element, 0, len(labels))
		for _, l := range labels {
			elements = append(elements, &model_starlark_pb.List_Element{
				Level: &model_starlark_pb.List_Element_Leaf{
					Leaf: &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_Label{Label: l}},
				},
			})
		}
		return &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_List{List: &model_starlark_pb.List{Elements: elements}}}
	}

	// newPackageGroup constructs the package group that is expected
	// to be used as the visibility of a target or macro instance.
	newPackageGroup := func(labels ...string) *model_starlark_pb.PackageGroup {
		resolvedLabels := make([]label.ResolvedLabel, 0, len(labels))
		for _, l := range labels {
			resolvedLabels = append(resolvedLabels, util.Must(label.NewResolvedLabel(l)))
		}
		packageGroup, err := model_starlark.NewPackageGroupFromVisibility[model_core.CreatedObjectTree](ctx, resolvedLabels, encoder, inlinedTreeOptions, NewMockObjectManagerForTesting(ctrl))
		require.NoError(t, err)
		return packageGroup.Message
	}
	// newWithinMacro creates a Starlark thread for expanding macro
	// instance "m", which is visible to packages @@main+//bar and
	// @@main+//foo.
	newWithinMacro := func(t *testing.T) (*starlark.Thread, *model_starlark.TargetRegistrar[object.LocalReference, model_core.CreatedObjectTree]) {
		return newExpansion(t, &model_starlark_pb.MacroExpansionContext_Definition{
			RootMacroInstanceDefinition: &model_starlark_pb.MacroInstance_Definition{
				InheritableAttrs: &model_starlark_pb.InheritableAttrs{
					Deprecation: "Don't use this",
					Testonly:    true,
					Visibility:  newPackageGroup("@@main+//bar:__pkg__", "@@main+//foo:__pkg__"),
				},
			},
		})
	}
	finalize := func(thread *starlark.Thread, targetRegistrar *model_starlark.TargetRegistrar[object.LocalReference, model_core.CreatedObjectTree]) error {
		return targetRegistrar.Finalize(
			thread.Local(model_starlark.CanonicalPackageKey).(label.CanonicalPackage),
			thread.Local(model_starlark.ValueEncodingOptionsKey).(*model_starlark.ValueEncodingOptions[object.LocalReference, model_core.CreatedObjectTree]),
		)
	}
	// getMacroInstance finalizes the TargetRegistrar, and returns
	// the arguments of a macro instance from its macro expansion
	// context.
	getMacroInstance := func(t *testing.T, thread *starlark.Thread, targetRegistrar *model_starlark.TargetRegistrar[object.LocalReference, model_core.CreatedObjectTree], name string) *model_starlark_pb.MacroInstance {
		require.NoError(t, finalize(thread, targetRegistrar))
		macroExpansionContext := targetRegistrar.GetAndRemoveMacroExpansionContext(name).Message
		return &model_starlark_pb.MacroInstance{
			NameSuffix: macroExpansionContext.NameSuffix,
			Definition: macroExpansionContext.Definition.RootMacroInstanceDefinition,
		}
	}

	t.Run("TopLevel", func(t *testing.T) {
		// Calling a macro should not invoke its implementation
		// function. Instead, the arguments should be recorded.
		// The visibility should include the package from which
		// the macro was called. Labels referring to files in the
		// current package should be registered as source files.
		thread, targetRegistrar := newTopLevel(t)
		require.NoError(t, call(thread, exampleMacro, nil,
			kwarg("name", starlark.String("m")),
			kwarg("count", starlark.MakeInt(3)),
			kwarg("srcs", stringList(":a.txt", "//other:b.txt")),
			kwarg("visibility", stringList("//bar:__pkg__")),
		))
		require.Equal(t, []string{"a.txt"}, targetRegistrar.GetTargetNames())

		testutil.RequireEqualProto(t, &model_starlark_pb.MacroInstance{
			NameSuffix: "m",
			Definition: &model_starlark_pb.MacroInstance_Definition{
				MacroIdentifier: "@@rules+//:defs.bzl%my_macro",
				PublicAttrValues: []*model_starlark_pb.RuleTarget_PublicAttrValue{
					{ValueParts: []*model_starlark_pb.Select_Group{noMatchGroup(&model_starlark_pb.Value{
						Kind: &model_starlark_pb.Value_Int{Int: model_starlark.EncodeInt(big.NewInt(3))},
					})}},
					{ValueParts: []*model_starlark_pb.Select_Group{noMatchGroup(labelList("@@main+//foo:a.txt", "@@main+//other:b.txt"))}},
				},
				InheritableAttrs: &model_starlark_pb.InheritableAttrs{
					Deprecation: "Don't use this",
					Testonly:    true,
					Visibility:  newPackageGroup("@@main+//bar:__pkg__", "@@main+//foo:__pkg__"),
				},
			},
		}, getMacroInstance(t, thread, targetRegistrar, "m"))
	})

	t.Run("TopLevelFinalizer", func(t *testing.T) {
		// Finalizers may be instantiated at the top level of a
		// package. Whether a macro instance is a finalizer
		// should be recorded, so that it can be omitted from
		// the targets that finalizers observe.
		thread, targetRegistrar := newTopLevel(t)
		require.NoError(t, call(thread, exampleFinalizer, nil, kwarg("name", starlark.String("f"))))
		require.Equal(t, model_starlark_pb.MacroInstance_FINALIZER, getMacroInstance(t, thread, targetRegistrar, "f").Definition.Finalizer)
	})

	t.Run("TopLevelVisibility", func(t *testing.T) {
		// At the top level, the visibility of macro instances
		// is provided as a list of labels. Macro instances are
		// always visible to the package itself.
		for name, testCase := range map[string]struct {
			visibility         starlark.Value
			expectedVisibility *model_starlark_pb.PackageGroup
		}{
			"Default":  {starlark.None, newPackageGroup("@@main+//foo:__pkg__")},
			"Private":  {stringList("//visibility:private"), newPackageGroup("@@main+//foo:__pkg__")},
			"Public":   {stringList("//visibility:public"), newPackageGroup("@@main+//visibility:public")},
			"Explicit": {stringList("//bar:__pkg__"), newPackageGroup("@@main+//bar:__pkg__", "@@main+//foo:__pkg__")},
		} {
			t.Run(name, func(t *testing.T) {
				thread, targetRegistrar := newTopLevel(t)
				require.NoError(t, call(thread, exampleMacro, nil, kwarg("name", starlark.String("m")), kwarg("count", starlark.MakeInt(1)), kwarg("visibility", testCase.visibility)))
				testutil.RequireEqualProto(t, testCase.expectedVisibility, getMacroInstance(t, thread, targetRegistrar, "m").Definition.InheritableAttrs.GetVisibility())
			})
		}

		t.Run("DefaultVisibility", func(t *testing.T) {
			// If no visibility is provided, macro instances
			// should inherit the package's default
			// visibility, just like rule targets.
			for name, testCase := range map[string]struct {
				defaultVisibility  *model_starlark_pb.PackageGroup
				visibility         starlark.Value
				expectedVisibility *model_starlark_pb.PackageGroup
			}{
				"Explicit":         {newPackageGroup("@@main+//bar:__pkg__"), starlark.None, newPackageGroup("@@main+//bar:__pkg__", "@@main+//foo:__pkg__")},
				"Public":           {newPackageGroup("@@main+//visibility:public"), starlark.None, newPackageGroup("@@main+//visibility:public")},
				"Private":          {newPackageGroup("@@main+//visibility:private"), starlark.None, newPackageGroup("@@main+//foo:__pkg__")},
				"OverriddenByCall": {newPackageGroup("@@main+//bar:__pkg__"), stringList("//qux:__pkg__"), newPackageGroup("@@main+//foo:__pkg__", "@@main+//qux:__pkg__")},
			} {
				t.Run(name, func(t *testing.T) {
					objectManager := NewMockObjectManagerForTesting(ctrl)
					targetRegistrar := model_starlark.NewTargetRegistrar(ctx, encoder, inlinedTreeOptions, objectManager, model_core.NewSimpleMessage[object.LocalReference](&model_starlark_pb.InheritableAttrs{
						Visibility: testCase.defaultVisibility,
					}))
					thread := newThread(t, targetRegistrar, objectManager)
					require.NoError(t, call(thread, exampleMacro, nil, kwarg("name", starlark.String("m")), kwarg("count", starlark.MakeInt(1)), kwarg("visibility", testCase.visibility)))
					testutil.RequireEqualProto(t, testCase.expectedVisibility, getMacroInstance(t, thread, targetRegistrar, "m").Definition.InheritableAttrs.GetVisibility())
				})
			}
		})

		t.Run("OpaqueValue", func(t *testing.T) {
			// The opaque value is only available within
			// macros.
			thread, _ := newTopLevel(t)
			require.ErrorContains(t, call(thread, nativeAlias, nil, kwarg("name", starlark.String("a")), kwarg("actual", starlark.String("//bar:baz")), kwarg("visibility", model_starlark.MacroVisibility{})), "got visibility, want list")
		})
	})

	t.Run("OmittedOptionalAttr", func(t *testing.T) {
		// Omitted attributes should be stored as None, so that
		// the implementation function receives the default.
		thread, targetRegistrar := newTopLevel(t)
		require.NoError(t, call(thread, exampleMacro, nil,
			kwarg("name", starlark.String("m")),
			kwarg("count", starlark.MakeInt(3)),
		))
		macroInstance := getMacroInstance(t, thread, targetRegistrar, "m")
		require.Len(t, macroInstance.Definition.PublicAttrValues, 2)
		testutil.RequireEqualProto(t, &model_starlark_pb.RuleTarget_PublicAttrValue{
			ValueParts: []*model_starlark_pb.Select_Group{noMatchGroup(&model_starlark_pb.Value{
				Kind: &model_starlark_pb.Value_None{},
			})},
		}, macroInstance.Definition.PublicAttrValues[1])
	})

	t.Run("NameNotATargetName", func(t *testing.T) {
		// Names of macro instances are only used as a prefix of
		// the names of targets declared by them. They therefore
		// don't need to be valid target names.
		thread, targetRegistrar := newTopLevel(t)
		require.NoError(t, call(thread, exampleMacro, nil, kwarg("name", starlark.String("m/")), kwarg("count", starlark.MakeInt(1))))
		require.Equal(t, "m/", getMacroInstance(t, thread, targetRegistrar, "m/").NameSuffix)
	})

	t.Run("InvalidCalls", func(t *testing.T) {
		for name, testCase := range map[string]struct {
			macro         starlark.Value
			args          starlark.Tuple
			kwargs        []starlark.Tuple
			expectedError string
		}{
			"PositionalArguments": {
				macro:         exampleMacro,
				args:          starlark.Tuple{starlark.String("m")},
				expectedError: "got 1 positional arguments, want 0",
			},
			"EmptyName": {
				macro:         exampleMacro,
				kwargs:        []starlark.Tuple{kwarg("name", starlark.String("")), kwarg("count", starlark.MakeInt(1))},
				expectedError: "macro instance name cannot be empty",
			},
			"MissingMandatoryAttr": {
				macro:         exampleMacro,
				kwargs:        []starlark.Tuple{kwarg("name", starlark.String("m"))},
				expectedError: "missing argument for count",
			},
			"PrivateAttr": {
				macro:         exampleMacro,
				kwargs:        []starlark.Tuple{kwarg("name", starlark.String("m")), kwarg("count", starlark.MakeInt(1)), kwarg("_private", starlark.String("x"))},
				expectedError: "unexpected keyword argument \"_private\"",
			},
			"InvalidAttrValue": {
				macro:         exampleMacro,
				kwargs:        []starlark.Tuple{kwarg("name", starlark.String("m")), kwarg("count", starlark.String("three"))},
				expectedError: "invalid argument \"count\"",
			},
			"ReservedAttrName": {
				macro: newMacro(map[label.StarlarkIdentifier]*model_starlark.Attr[object.LocalReference, model_core.CreatedObjectTree]{
					util.Must(label.NewStarlarkIdentifier("visibility")): model_starlark.NewAttr[object.LocalReference, model_core.CreatedObjectTree](
						model_starlark.NewStringListAttrType[object.LocalReference, model_core.CreatedObjectTree](),
						starlark.NewList(nil),
						/* configurable = */ true,
					),
				}, false),
				kwargs:        []starlark.Tuple{kwarg("name", starlark.String("m"))},
				expectedError: "macro uses attribute with reserved name \"visibility\"",
			},
			"NonConfigurableAttr": {
				// Attributes that are not configurable
				// cannot be provided using select().
				macro: newMacro(map[label.StarlarkIdentifier]*model_starlark.Attr[object.LocalReference, model_core.CreatedObjectTree]{
					util.Must(label.NewStarlarkIdentifier("srcs")): model_starlark.NewAttr[object.LocalReference, model_core.CreatedObjectTree](
						model_starlark.NewStringListAttrType[object.LocalReference, model_core.CreatedObjectTree](),
						starlark.NewList(nil),
						/* configurable = */ false,
					),
				}, false),
				kwargs: []starlark.Tuple{
					kwarg("name", starlark.String("m")),
					kwarg("srcs", model_starlark.NewSelect[object.LocalReference, model_core.CreatedObjectTree](
						[]model_starlark.SelectGroup{model_starlark.NewSelectGroup(nil, starlark.NewList(nil), "")},
						0,
					)),
				},
				expectedError: "invalid argument \"srcs\": attribute is not configurable, meaning select() cannot be used",
			},
		} {
			t.Run(name, func(t *testing.T) {
				thread, _ := newTopLevel(t)
				err := call(thread, testCase.macro, testCase.args, testCase.kwargs...)
				require.ErrorContains(t, err, testCase.expectedError)
			})
		}
	})

	t.Run("ExportsFiles", func(t *testing.T) {
		// Files declared using exports_files() default to public
		// visibility at the top level, while within macros they
		// default to the macro's visibility.
		publicVisibility := &model_starlark_pb.PackageGroup{
			Tree: &model_starlark_pb.PackageGroup_Subpackages{
				IncludeSubpackages: true,
			},
		}
		macroVisibility := util.Must(model_starlark.NewPackageGroupFromVisibility[model_core.CreatedObjectTree](
			ctx,
			[]label.ResolvedLabel{util.Must(label.NewResolvedLabel("@@rules+//macros:__pkg__"))},
			encoder,
			inlinedTreeOptions,
			NewMockObjectManagerForTesting(ctrl),
		)).Message
		for name, testCase := range map[string]struct {
			newThread          func(t *testing.T) (*starlark.Thread, *model_starlark.TargetRegistrar[object.LocalReference, model_core.CreatedObjectTree])
			fileName           string
			expectedVisibility *model_starlark_pb.PackageGroup
		}{
			"TopLevel":    {newTopLevel, "a.txt", publicVisibility},
			"WithinMacro": {newWithinMacro, "m_a.txt", macroVisibility},
		} {
			t.Run(name, func(t *testing.T) {
				thread, targetRegistrar := testCase.newThread(t)
				require.NoError(t, call(thread, nativeExportsFiles, starlark.Tuple{stringList(testCase.fileName)}))
				testutil.RequireEqualProto(t, &model_starlark_pb.SourceFileTarget{
					Visibility: testCase.expectedVisibility,
				}, targetRegistrar.GetAndRemoveTarget(testCase.fileName).Message.GetSourceFileTarget())
			})
		}
	})

	t.Run("Namespace", func(t *testing.T) {
		// Callers of macros may declare targets in the
		// namespace of a macro instance. Targets in the
		// namespace that are only referenced are forwarded to
		// the macro instance, as the macro instance may declare
		// them.
		callMacro := func(thread *starlark.Thread, name string) error {
			return call(thread, exampleMacro, nil, kwarg("name", starlark.String(name)), kwarg("count", starlark.MakeInt(1)))
		}
		callAlias := func(thread *starlark.Thread, name, actual string) error {
			return call(thread, nativeAlias, nil, kwarg("name", starlark.String(name)), kwarg("actual", starlark.String(actual)))
		}
		// getNames returns the names of the targets that are
		// referenced or forbidden, and the macro instances that
		// are nested within a macro instance.
		type names struct {
			referenced, forbidden, nested []string
		}
		getNames := func(t *testing.T, targetRegistrar *model_starlark.TargetRegistrar[object.LocalReference, model_core.CreatedObjectTree], name string) names {
			definition := targetRegistrar.GetAndRemoveMacroExpansionContext(name).Message.Definition
			n := names{
				referenced: definition.ReferencedTargetNameSuffixes,
				forbidden:  definition.ForbiddenTargetNameSuffixes,
			}
			for _, entry := range definition.NestedMacroInstances {
				nestedMacroInstance := entry.GetLeaf()
				require.NotNil(t, nestedMacroInstance)
				n.nested = append(n.nested, nestedMacroInstance.NameSuffix)
			}
			return n
		}

		t.Run("ForwardedToMacroInstance", func(t *testing.T) {
			// Referenced targets should be forwarded to the
			// macro instance whose namespace contains them,
			// regardless of the order in which they are
			// declared. Macro instances whose namespace is
			// contained within the namespace of another
			// macro instance are nested inside the latter,
			// meaning targets in their namespace are
			// forwarded to the latter as well.
			for name, macroFirst := range map[string]bool{"MacroFirst": true, "MacroLast": false} {
				t.Run(name, func(t *testing.T) {
					thread, targetRegistrar := newTopLevel(t)
					if macroFirst {
						require.NoError(t, callMacro(thread, "m"))
						require.NoError(t, callMacro(thread, "m_nested"))
					}
					require.NoError(t, callAlias(thread, "a", ":m_out"))
					require.NoError(t, callAlias(thread, "b", ":m-out"))
					require.NoError(t, callAlias(thread, "c", ":m.out"))
					require.NoError(t, callAlias(thread, "d", ":m"))
					require.NoError(t, callAlias(thread, "e", ":m_nested_x"))
					require.NoError(t, callAlias(thread, "f", ":mm_out"))
					if !macroFirst {
						require.NoError(t, callMacro(thread, "m_nested"))
						require.NoError(t, callMacro(thread, "m"))
					}
					require.NoError(t, finalize(thread, targetRegistrar))
					require.Equal(t, []string{"a", "b", "c", "d", "e", "f", "mm_out"}, targetRegistrar.GetTargetNames())
					require.Equal(t, []string{"m"}, targetRegistrar.GetMacroExpansionContextNames())
					require.Equal(t, names{
						referenced: []string{"", "-out", ".out", "_nested_x", "_out"},
						nested:     []string{"_nested"},
					}, getNames(t, targetRegistrar, "m"))
				})
			}
		})

		t.Run("DeclaredByCaller", func(t *testing.T) {
			// Targets in the namespace of a macro instance
			// may be declared by the caller, even if their
			// name is equal to that of the macro instance.
			// These should not be forwarded.
			thread, targetRegistrar := newTopLevel(t)
			require.NoError(t, callAlias(thread, "m", "//bar:baz"))
			require.NoError(t, callAlias(thread, "m_out", ":m.txt"))
			require.NoError(t, call(thread, nativeExportsFiles, starlark.Tuple{stringList("m.txt")}))
			require.NoError(t, callMacro(thread, "m"))
			require.NoError(t, finalize(thread, targetRegistrar))
			require.Equal(t, []string{"m", "m.txt", "m_out"}, targetRegistrar.GetTargetNames())
			require.Empty(t, targetRegistrar.GetAndRemoveMacroExpansionContext("m").Message.Definition.ReferencedTargetNameSuffixes)
		})

		t.Run("NameClash", func(t *testing.T) {
			// Targets and macro instances have separate
			// namespaces. Names still need to be unique
			// within each of them.
			thread, _ := newTopLevel(t)
			require.NoError(t, callAlias(thread, "x", "//bar:baz"))
			require.NoError(t, callMacro(thread, "x"))
			require.EqualError(t, callAlias(thread, "x", "//bar:baz"), "package contains multiple targets with name \"x\"")
			require.EqualError(t, callMacro(thread, "x"), "package contains multiple macro instances with name \"x\"")
		})

		t.Run("WithinMacro", func(t *testing.T) {
			// Targets referenced by the caller of the macro
			// should be registered implicitly, causing them
			// to become source files if the macro doesn't
			// declare them. They should be forwarded to
			// nested macro instances as well.
			thread, targetRegistrar := newExpansion(t, &model_starlark_pb.MacroExpansionContext_Definition{
				ReferencedTargetNameSuffixes: []string{"", "_a", "_b_x", "_c"},
			})
			require.NoError(t, callAlias(thread, "m_a", "//bar:baz"))
			require.NoError(t, callMacro(thread, "m_b"))
			require.NoError(t, finalize(thread, targetRegistrar))
			require.Equal(t, []string{"m", "m_a", "m_c"}, targetRegistrar.GetTargetNames())
			require.Equal(t, []string{"_x"}, targetRegistrar.GetAndRemoveMacroExpansionContext("m_b").Message.Definition.ReferencedTargetNameSuffixes)
			require.NotNil(t, targetRegistrar.GetAndRemoveTarget("m_a").Message.GetAlias())
			require.NotNil(t, targetRegistrar.GetAndRemoveTarget("m_c").Message.GetSourceFileTarget())
		})

		t.Run("NestedTopLevel", func(t *testing.T) {
			// Macro instances may not declare targets that
			// are declared by their caller. Macro instances
			// whose namespace is contained within the
			// namespace of another macro instance are
			// nested inside the latter. Names in their
			// namespace are therefore stored in the latter.
			// Nested macro instances are sorted
			// alphabetically.
			thread, targetRegistrar := newTopLevel(t)
			for _, name := range []string{"a", "m", "m-x", "m0", "m_lib", "m_lib_y", "m_out", "mm"} {
				require.NoError(t, callAlias(thread, name, "//bar:baz"), name)
			}
			require.NoError(t, callAlias(thread, "b", ":m_lib_ref"))
			for _, name := range []string{"mm", "m_lib_x", "m_lib0", "m_lib", "m0", "m"} {
				require.NoError(t, callMacro(thread, name), name)
			}
			require.NoError(t, finalize(thread, targetRegistrar))

			require.Equal(t, []string{"m", "m0", "mm"}, targetRegistrar.GetMacroExpansionContextNames())
			require.Equal(t, names{
				referenced: []string{"_lib_ref"},
				forbidden:  []string{"", "-x", "_lib", "_lib_y", "_out"},
				nested:     []string{"_lib", "_lib0", "_lib_x"},
			}, getNames(t, targetRegistrar, "m"))
			require.Equal(t, names{
				forbidden: []string{""},
			}, getNames(t, targetRegistrar, "m0"))
			require.Equal(t, names{
				forbidden: []string{""},
			}, getNames(t, targetRegistrar, "mm"))
		})

		t.Run("NestedInOtherObjects", func(t *testing.T) {
			// Large lists of nested macro instances should
			// be stored as B-trees.
			objectManager := NewMockObjectManagerForTesting(ctrl)
			objectManager.EXPECT().CaptureCreatedObject(gomock.Any(), gomock.Any()).AnyTimes()
			targetRegistrar := model_starlark.NewTargetRegistrar(ctx, encoder, inlinedTreeOptions, objectManager, defaultInheritableAttrs)
			thread := newThread(t, targetRegistrar, objectManager)
			for _, name := range []string{"m", "m_a", "m_b", "m_c", "m_d", "m_e", "m_f", "m_g", "m_h"} {
				require.NoError(t, callMacro(thread, name), name)
			}
			require.NoError(t, targetRegistrar.Finalize(
				util.Must(label.NewCanonicalPackage("@@main+//foo")),
				&model_starlark.ValueEncodingOptions[object.LocalReference, model_core.CreatedObjectTree]{
					Context:                ctx,
					ObjectEncoder:          encoder,
					ObjectReferenceFormat:  referenceFormat,
					ObjectCapturer:         objectManager,
					ObjectMinimumSizeBytes: 100,
					ObjectMaximumSizeBytes: 200,
				},
			))

			nestedMacroInstances := targetRegistrar.GetAndRemoveMacroExpansionContext("m").Message.Definition.NestedMacroInstances
			require.Greater(t, len(nestedMacroInstances), 1)
			for _, entry := range nestedMacroInstances {
				require.NotNil(t, entry.GetParent())
			}
		})

		t.Run("Finalizers", func(t *testing.T) {
			// Finalizers are expanded after all targets
			// declared by non-finalizer macro instances.
			// Outside of finalizers, they may therefore not
			// contain non-finalizer macro instances in their
			// namespace. Other combinations are permitted.
			for name, testCase := range map[string]struct {
				finalizerNames []string
				otherNames     []string
				expectedError  string
			}{
				"FinalizerContainsFinalizer":       {[]string{"f", "f_x"}, nil, ""},
				"NonFinalizerContainsFinalizer":    {[]string{"m_f"}, []string{"m"}, ""},
				"FinalizerContainsNonFinalizer":    {[]string{"f"}, []string{"f_x"}, "non-finalizer macro instance \"f_x\" cannot be part of the namespace of finalizer \"f\""},
				"FinalizerWithSimilarNonFinalizer": {[]string{"f"}, []string{"fx"}, ""},
			} {
				t.Run(name, func(t *testing.T) {
					thread, targetRegistrar := newTopLevel(t)
					for _, name := range testCase.finalizerNames {
						require.NoError(t, call(thread, exampleFinalizer, nil, kwarg("name", starlark.String(name))))
					}
					for _, name := range testCase.otherNames {
						require.NoError(t, callMacro(thread, name))
					}
					if testCase.expectedError == "" {
						require.NoError(t, finalize(thread, targetRegistrar))
					} else {
						require.EqualError(t, finalize(thread, targetRegistrar), testCase.expectedError)
					}
				})
			}

			t.Run("WithinFinalizer", func(t *testing.T) {
				// Within finalizers and the macro
				// instances they declare, any combination
				// is permitted, as none of the targets they
				// declare are observed by finalizers.
				// Non-finalizer macro instances should be
				// marked as such.
				for _, finalizer := range []model_starlark_pb.MacroInstance_Finalizer{
					model_starlark_pb.MacroInstance_FINALIZER,
					model_starlark_pb.MacroInstance_CALLED_BY_FINALIZER,
				} {
					thread, targetRegistrar := newExpansion(t, &model_starlark_pb.MacroExpansionContext_Definition{
						RootMacroInstanceDefinition: &model_starlark_pb.MacroInstance_Definition{
							Finalizer: finalizer,
						},
					})
					require.NoError(t, call(thread, exampleFinalizer, nil, kwarg("name", starlark.String("m_f"))))
					require.NoError(t, callMacro(thread, "m_f_x"))
					require.NoError(t, finalize(thread, targetRegistrar))
					require.Equal(t, []string{"m_f"}, targetRegistrar.GetMacroExpansionContextNames())
					macroExpansionContext := targetRegistrar.GetAndRemoveMacroExpansionContext("m_f").Message
					require.Equal(t, model_starlark_pb.MacroInstance_FINALIZER, macroExpansionContext.Definition.RootMacroInstanceDefinition.Finalizer)
					require.Len(t, macroExpansionContext.Definition.NestedMacroInstances, 1)
					require.Equal(t, model_starlark_pb.MacroInstance_CALLED_BY_FINALIZER, macroExpansionContext.Definition.NestedMacroInstances[0].GetLeaf().Definition.Finalizer)
				}
			})
		})

		t.Run("NestedWithinMacro", func(t *testing.T) {
			// Macro instances that are nested inside the
			// current macro instance should be treated as
			// if they were declared by it. Names that are
			// referenced or forbidden are forwarded to
			// them, as long as they are part of their
			// namespace.
			thread, targetRegistrar := newExpansion(t, &model_starlark_pb.MacroExpansionContext_Definition{
				ReferencedTargetNameSuffixes: []string{"_lib_r", "_r"},
				ForbiddenTargetNameSuffixes:  []string{"_lib_a", "_other"},
			})
			for _, nameSuffix := range []string{"_lib", "_lib_x"} {
				require.NoError(t, targetRegistrar.RegisterNestedMacroInstance(model_core.NewSimplePatchedMessage[model_core.CreatedObjectTree](&model_starlark_pb.MacroInstance{
					NameSuffix: nameSuffix,
					Definition: &model_starlark_pb.MacroInstance_Definition{
						MacroIdentifier: "@@rules+//:defs.bzl%my_macro",
					},
				})))
			}
			require.NoError(t, callAlias(thread, "m_lib_b", "//bar:baz"))
			require.NoError(t, callMacro(thread, "m_lib_z"))
			require.EqualError(t, callMacro(thread, "m_lib"), "package contains multiple macro instances with name \"m_lib\"")
			require.NoError(t, finalize(thread, targetRegistrar))

			require.Equal(t, []string{"m_lib_b", "m_r"}, targetRegistrar.GetTargetNames())
			require.Equal(t, []string{"m_lib"}, targetRegistrar.GetMacroExpansionContextNames())
			require.Equal(t, names{
				referenced: []string{"_r"},
				forbidden:  []string{"_a", "_b"},
				nested:     []string{"_x", "_z"},
			}, getNames(t, targetRegistrar, "m_lib"))
		})

		t.Run("ForbiddenNamesDeclared", func(t *testing.T) {
			// Declaring targets that are declared outside
			// of the macro instance should fail.
			thread, targetRegistrar := newExpansion(t, &model_starlark_pb.MacroExpansionContext_Definition{
				ForbiddenTargetNameSuffixes: []string{"_out"},
			})
			require.NoError(t, callAlias(thread, "m_outer", "//bar:baz"))
			require.NoError(t, callMacro(thread, "m_out"))
			require.EqualError(t, callAlias(thread, "m_out", "//bar:baz"), "target \"m_out\" is already declared outside of this macro instance")
			require.NoError(t, finalize(thread, targetRegistrar))
			require.Equal(t, []string{"m_outer"}, targetRegistrar.GetTargetNames())
		})
	})

	t.Run("WithinMacro", func(t *testing.T) {
		t.Run("DeclarationNames", func(t *testing.T) {
			// Targets and macro instances declared by a
			// macro need to be prefixed by its name.
			thread, targetRegistrar := newWithinMacro(t)
			for _, name := range []string{"m", "m_a", "m-b", "m.c"} {
				require.NoError(t, call(thread, nativeAlias, nil, kwarg("name", starlark.String(name)), kwarg("actual", starlark.String("//bar:baz"))), name)
			}
			require.NoError(t, call(thread, exampleMacro, nil, kwarg("name", starlark.String("m_nested")), kwarg("count", starlark.MakeInt(1))))
			require.Equal(t, []string{"m", "m-b", "m.c", "m_a"}, targetRegistrar.GetTargetNames())

			for _, name := range []string{"other", "mm", "m+x"} {
				require.ErrorContains(t, call(thread, nativeAlias, nil, kwarg("name", starlark.String(name)), kwarg("actual", starlark.String("//bar:baz"))), "is not equal to the name of macro instance \"m\"", name)
			}
			require.NoError(t, finalize(thread, targetRegistrar))
			require.Equal(t, []string{"m_nested"}, targetRegistrar.GetMacroExpansionContextNames())
		})

		t.Run("Finalizer", func(t *testing.T) {
			// Unlike Bazel, finalizers may also be
			// instantiated by non-finalizer macros. This is
			// equivalent to finalizers declared at the top
			// level whose namespace is contained within the
			// namespace of a non-finalizer macro instance.
			thread, targetRegistrar := newWithinMacro(t)
			require.NoError(t, call(thread, exampleFinalizer, nil, kwarg("name", starlark.String("m_f"))))
			require.NoError(t, call(thread, exampleMacro, nil, kwarg("name", starlark.String("m_x")), kwarg("count", starlark.MakeInt(1))))
			require.NoError(t, finalize(thread, targetRegistrar))
			require.Equal(t, model_starlark_pb.MacroInstance_FINALIZER, targetRegistrar.GetAndRemoveMacroExpansionContext("m_f").Message.Definition.RootMacroInstanceDefinition.Finalizer)
			require.Equal(t, model_starlark_pb.MacroInstance_NOT_A_FINALIZER, targetRegistrar.GetAndRemoveMacroExpansionContext("m_x").Message.Definition.RootMacroInstanceDefinition.Finalizer)
		})

		t.Run("SameNameAsMacro", func(t *testing.T) {
			// A macro instance cannot declare a macro
			// instance having the same name.
			thread, _ := newWithinMacro(t)
			require.EqualError(t, call(thread, exampleMacro, nil, kwarg("name", starlark.String("m")), kwarg("count", starlark.MakeInt(1))), "macro instance \"m\" cannot declare a macro instance with the same name")
		})

		t.Run("NestedMacroVisibility", func(t *testing.T) {
			// Macro instances declared by macros are called
			// from the package containing the macro's
			// definition. The macro may forward its own
			// visibility.
			for name, testCase := range map[string]struct {
				visibility         starlark.Value
				expectedVisibility *model_starlark_pb.PackageGroup
			}{
				"Default":   {starlark.None, newPackageGroup("@@rules+//macros:__pkg__")},
				"Forwarded": {model_starlark.MacroVisibility{}, newPackageGroup("@@main+//bar:__pkg__", "@@main+//foo:__pkg__", "@@rules+//macros:__pkg__")},
			} {
				t.Run(name, func(t *testing.T) {
					thread, targetRegistrar := newWithinMacro(t)
					require.NoError(t, call(thread, exampleMacro, nil, kwarg("name", starlark.String("m_nested")), kwarg("count", starlark.MakeInt(1)), kwarg("visibility", testCase.visibility)))
					macroInstance := getMacroInstance(t, thread, targetRegistrar, "m_nested")
					require.Equal(t, "_nested", macroInstance.NameSuffix)
					testutil.RequireEqualProto(t, testCase.expectedVisibility, macroInstance.Definition.InheritableAttrs.GetVisibility())
				})
			}
		})

		t.Run("NoImplicitTargets", func(t *testing.T) {
			// Macros cannot declare source file targets.
			thread, targetRegistrar := newWithinMacro(t)
			require.NoError(t, call(thread, nativeAlias, nil, kwarg("name", starlark.String("m")), kwarg("actual", starlark.String(":src.txt"))))
			require.Equal(t, []string{"m"}, targetRegistrar.GetTargetNames())
		})

		t.Run("Visibility", func(t *testing.T) {
			// Targets declared by macros are always visible
			// to the package containing the macro's
			// definition, regardless of the package's default
			// visibility. The macro may forward its own
			// visibility.
			for name, testCase := range map[string]struct {
				macroVisibility    *model_starlark_pb.PackageGroup
				visibility         starlark.Value
				expectedVisibility *model_starlark_pb.PackageGroup
			}{
				"Default":            {newPackageGroup("@@main+//bar:__pkg__"), starlark.None, newPackageGroup("@@rules+//macros:__pkg__")},
				"Forwarded":          {newPackageGroup("@@main+//bar:__pkg__", "@@main+//foo:__pkg__"), model_starlark.MacroVisibility{}, newPackageGroup("@@main+//bar:__pkg__", "@@main+//foo:__pkg__", "@@rules+//macros:__pkg__")},
				"ForwardedPublic":    {newPackageGroup("@@main+//visibility:public"), model_starlark.MacroVisibility{}, newPackageGroup("@@main+//visibility:public")},
				"ForwardedContained": {newPackageGroup("@@rules+//:__subpackages__"), model_starlark.MacroVisibility{}, newPackageGroup("@@rules+//:__subpackages__")},
			} {
				t.Run(name, func(t *testing.T) {
					thread, targetRegistrar := newExpansion(t, &model_starlark_pb.MacroExpansionContext_Definition{
						RootMacroInstanceDefinition: &model_starlark_pb.MacroInstance_Definition{
							InheritableAttrs: &model_starlark_pb.InheritableAttrs{
								Visibility: testCase.macroVisibility,
							},
						},
					})
					require.NoError(t, call(thread, nativeAlias, nil, kwarg("name", starlark.String("m")), kwarg("actual", starlark.String("//bar:baz")), kwarg("visibility", testCase.visibility)))
					testutil.RequireEqualProto(t, testCase.expectedVisibility, targetRegistrar.GetAndRemoveTarget("m").Message.GetAlias().GetVisibility())
				})
			}

			t.Run("LabelList", func(t *testing.T) {
				// Lists of labels can only be provided at
				// the top level.
				thread, _ := newWithinMacro(t)
				require.EqualError(t, call(thread, nativeAlias, nil, kwarg("name", starlark.String("m")), kwarg("actual", starlark.String("//bar:baz")), kwarg("visibility", stringList("//visibility:public"))), "native.alias: for parameter \"visibility\": got list, while within symbolic macros visibility can only be None or the visibility provided to the macro's implementation function")
			})
		})
	})

	t.Run("DecodedFromStorage", func(t *testing.T) {
		// Macros that are loaded from storage should be
		// callable as well, both if they contain their own
		// definition and if they refer to another macro.
		definition := &model_starlark_pb.Macro_Definition{
			Finalizer: true,
			Attrs: []*model_starlark_pb.NamedAttr{{
				Name: "value",
				Attr: &model_starlark_pb.Attr{
					Type: &model_starlark_pb.Attr_String_{String_: &model_starlark_pb.Attr_StringType{}},
				},
			}},
		}
		decodingOptions := &model_starlark.ValueDecodingOptions[object.LocalReference]{Context: ctx}
		decodedDefinition := util.Must(model_starlark.DecodeValue[object.LocalReference, model_core.CreatedObjectTree](
			model_core.NewSimpleMessage[object.LocalReference](&model_starlark_pb.Value{
				Kind: &model_starlark_pb.Value_Macro{
					Macro: &model_starlark_pb.Macro{
						Kind: &model_starlark_pb.Macro_Definition_{Definition: definition},
					},
				},
			}),
			&identifier,
			decodingOptions,
		))
		decodedReference := util.Must(model_starlark.DecodeValue[object.LocalReference, model_core.CreatedObjectTree](
			model_core.NewSimpleMessage[object.LocalReference](&model_starlark_pb.Value{
				Kind: &model_starlark_pb.Value_Macro{
					Macro: &model_starlark_pb.Macro{
						Kind: &model_starlark_pb.Macro_Reference{Reference: "@@rules+//:defs.bzl%my_macro"},
					},
				},
			}),
			nil,
			decodingOptions,
		))

		for name, decodedMacro := range map[string]starlark.Value{
			"Definition": decodedDefinition,
			"Reference":  decodedReference,
		} {
			t.Run(name, func(t *testing.T) {
				thread, targetRegistrar := newTopLevel(t)
				thread.SetLocal(model_starlark.GlobalResolverKey, model_starlark.GlobalResolver[object.LocalReference](func(id label.CanonicalStarlarkIdentifier) (model_core.Message[*model_starlark_pb.Value, object.LocalReference], error) {
					require.Equal(t, identifier, id)
					return model_core.NewSimpleMessage[object.LocalReference](&model_starlark_pb.Value{
						Kind: &model_starlark_pb.Value_Macro{
							Macro: &model_starlark_pb.Macro{
								Kind: &model_starlark_pb.Macro_Definition_{Definition: definition},
							},
						},
					}), nil
				}))
				require.ErrorContains(t, call(thread, decodedMacro, nil, kwarg("name", starlark.String("m")), kwarg("value", model_starlark.NewSelect[object.LocalReference, model_core.CreatedObjectTree](
					[]model_starlark.SelectGroup{model_starlark.NewSelectGroup(nil, starlark.String("hello"), "")},
					0,
				))), "attribute is not configurable")
				require.NoError(t, call(thread, decodedMacro, nil, kwarg("name", starlark.String("m")), kwarg("value", starlark.String("hello"))))
				macroInstance := getMacroInstance(t, thread, targetRegistrar, "m")
				require.Equal(t, "@@rules+//:defs.bzl%my_macro", macroInstance.Definition.MacroIdentifier)
				require.Equal(t, model_starlark_pb.MacroInstance_FINALIZER, macroInstance.Definition.Finalizer)
				require.Len(t, macroInstance.Definition.PublicAttrValues, 1)
			})
		}
	})

	t.Run("EncodeReference", func(t *testing.T) {
		// Macros that are not encoded under their canonical
		// identifier should be encoded as references.
		otherIdentifier := util.Must(label.NewCanonicalStarlarkIdentifier("@@main+//:defs.bzl%alias"))
		encoded, _, err := exampleMacro.(model_starlark.EncodableValue[object.LocalReference, model_core.CreatedObjectTree]).EncodeValue(
			map[starlark.Value]struct{}{},
			&otherIdentifier,
			nil,
		)
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_Macro{
				Macro: &model_starlark_pb.Macro{
					Kind: &model_starlark_pb.Macro_Reference{Reference: "@@rules+//:defs.bzl%my_macro"},
				},
			},
		}, encoded.Message)
	})
}

func TestCompareMacroInstanceNames(t *testing.T) {
	// "-", "." and "_" should sort before all other bytes, so that
	// the namespace of a macro instance is a contiguous range of
	// names starting at the name of the macro instance.
	require.Equal(
		t,
		[]string{"l", "m", "m-y", "m.z", "m_", "m_x", "m+", "m0", "mm", "m~"},
		slices.SortedFunc(slices.Values([]string{"m~", "mm", "m0", "m+", "m_x", "m_", "m.z", "m-y", "m", "l"}), model_starlark.CompareMacroInstanceNames),
	)
}

func TestMacroExpansionContextListBuilder(t *testing.T) {
	// Large lists of macro expansion contexts should be stored as
	// B-trees, whose parent nodes contain the name suffix of their
	// first macro expansion context.
	ctrl, ctx := gomock.WithContext(t.Context(), t)
	objectManager := NewMockObjectManagerForTesting(ctrl)
	objectManager.EXPECT().CaptureCreatedObject(gomock.Any(), gomock.Any()).AnyTimes()
	treeBuilder := model_starlark.NewMacroExpansionContextListBuilder(&model_starlark.ValueEncodingOptions[object.LocalReference, model_core.CreatedObjectTree]{
		Context:                ctx,
		ObjectEncoder:          model_encoding.NewChainedDeterministicBinaryEncoder(nil),
		ObjectReferenceFormat:  util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1)),
		ObjectCapturer:         objectManager,
		ObjectMinimumSizeBytes: 100,
		ObjectMaximumSizeBytes: 200,
	})
	defer treeBuilder.Discard()
	var nameSuffixes []string
	for i := range 50 {
		nameSuffix := fmt.Sprintf("m%02d", i)
		nameSuffixes = append(nameSuffixes, nameSuffix)
		require.NoError(t, treeBuilder.PushChild(model_core.NewSimplePatchedMessage[model_core.CreatedObjectTree](&model_starlark_pb.MacroExpansionContext_Entry{
			Level: &model_starlark_pb.MacroExpansionContext_Entry_Leaf{
				Leaf: &model_starlark_pb.MacroExpansionContext{NameSuffix: nameSuffix},
			},
		})))
	}
	entries, err := treeBuilder.FinalizeList()
	require.NoError(t, err)
	defer entries.Discard()

	require.Greater(t, len(entries.Message), 1)
	var firstNameSuffixes []string
	for _, entry := range entries.Message {
		parent := entry.GetParent()
		require.NotNil(t, parent)
		require.Contains(t, nameSuffixes, parent.FirstNameSuffix)
		firstNameSuffixes = append(firstNameSuffixes, parent.FirstNameSuffix)
	}
	require.Equal(t, "m00", firstNameSuffixes[0])
	require.True(t, slices.IsSorted(firstNameSuffixes))
}

func TestMacroInheritAttrs(t *testing.T) {
	ctrl, ctx := gomock.WithContext(t.Context(), t)
	referenceFormat := util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1))
	encoder := model_encoding.NewChainedDeterministicBinaryEncoder(nil)
	valueEncodingOptions := &model_starlark.ValueEncodingOptions[object.LocalReference, model_core.CreatedObjectTree]{
		Context:                ctx,
		ObjectEncoder:          encoder,
		ObjectReferenceFormat:  referenceFormat,
		ObjectMinimumSizeBytes: 32 * 1024,
		ObjectMaximumSizeBytes: 128 * 1024,
	}
	bzlFileBuiltins, _ := model_starlark.GetBuiltins[object.LocalReference, model_core.CreatedObjectTree]()
	canonicalLabel := util.Must(label.NewCanonicalLabel("@@rules+//:defs.bzl"))

	// Rules that are declared in Go, so that they can be called
	// without needing to compile a .bzl file.
	newRule := func(name string, attrs map[string]*model_starlark.Attr[object.LocalReference, model_core.CreatedObjectTree], buildSetting *model_starlark.BuildSetting[object.LocalReference, model_core.CreatedObjectTree], ruleType model_starlark_pb.Rule_Definition_Type) starlark.Value {
		identifier := canonicalLabel.AppendStarlarkIdentifier(util.Must(label.NewStarlarkIdentifier(name)))
		namedAttrs := map[label.StarlarkIdentifier]*model_starlark.Attr[object.LocalReference, model_core.CreatedObjectTree]{}
		for attrName, attr := range attrs {
			namedAttrs[util.Must(label.NewStarlarkIdentifier(attrName))] = attr
		}
		return model_starlark.NewRule(
			&identifier,
			model_starlark.NewStarlarkRuleDefinition(
				namedAttrs,
				buildSetting,
				/* cfg = */ nil,
				/* execGroups = */ nil,
				/* implementation = */ model_starlark.NamedFunction[object.LocalReference, model_core.CreatedObjectTree]{},
				/* initializer = */ nil,
				/* provides = */ nil,
				ruleType,
				/* subrules = */ nil,
			),
		)
	}
	stringAttr := func(defaultValue starlark.Value) *model_starlark.Attr[object.LocalReference, model_core.CreatedObjectTree] {
		return model_starlark.NewAttr(model_starlark.NewStringAttrType[object.LocalReference, model_core.CreatedObjectTree](nil), defaultValue, true)
	}
	myTest := newRule(
		"my_test",
		map[string]*model_starlark.Attr[object.LocalReference, model_core.CreatedObjectTree]{
			"mandatory": stringAttr(nil),
			"optional":  stringAttr(starlark.String("default")),
			"_private":  stringAttr(starlark.String("default")),
			"out": model_starlark.NewAttr(
				model_starlark.NewOutputAttrType[object.LocalReference, model_core.CreatedObjectTree]("%{name}.out"),
				starlark.String("%{name}.out"),
				false,
			),
		},
		/* buildSetting = */ nil,
		model_starlark_pb.Rule_Definition_TEST,
	)

	// exec evaluates a .bzl file declaring macros, returning the
	// attributes of each macro in a compact form.
	exec := func(t *testing.T, thread *starlark.Thread, code string, predeclared starlark.StringDict) (map[string]map[string]string, error) {
		builtins := maps.Clone(bzlFileBuiltins)
		maps.Copy(builtins, predeclared)
		globals, err := starlark.ExecFileOptions(
			&syntax.FileOptions{},
			thread,
			canonicalLabel.String(),
			"def _impl(name, visibility, **kwargs):\n    pass\n"+code,
			builtins,
		)
		if err != nil {
			return nil, err
		}
		model_starlark.NameAndExtractGlobals(globals, canonicalLabel)

		macros := map[string]map[string]string{}
		for name, value := range globals {
			if value.Type() != "macro" {
				continue
			}
			identifier := canonicalLabel.AppendStarlarkIdentifier(util.Must(label.NewStarlarkIdentifier(name)))
			encodedMacro, _, err := value.(model_starlark.EncodableValue[object.LocalReference, model_core.CreatedObjectTree]).EncodeValue(map[starlark.Value]struct{}{}, &identifier, valueEncodingOptions)
			require.NoError(t, err)
			attrs := map[string]string{}
			for _, namedAttr := range encodedMacro.Message.GetMacro().GetDefinition().GetAttrs() {
				defaultValue := "mandatory"
				if d := namedAttr.Attr.Default; d != nil {
					defaultValue = "default=" + strings.TrimPrefix(fmt.Sprintf("%T", d.Kind), "*starlark.Value_")
				}
				attrs[namedAttr.Name] = fmt.Sprintf(
					"%s %s configurable=%t",
					strings.TrimPrefix(fmt.Sprintf("%T", namedAttr.Attr.Type), "*starlark.Attr_"),
					defaultValue,
					namedAttr.Attr.Configurable,
				)
			}
			macros[name] = attrs
		}
		return macros, nil
	}

	commonAttrs := map[string]string{
		"applicable_licenses":    "LabelList default=None configurable=false",
		"compatible_with":        "LabelList default=None configurable=false",
		"deprecation":            "String_ default=None configurable=false",
		"exec_compatible_with":   "LabelList default=None configurable=false",
		"features":               "StringList default=None configurable=true",
		"package_metadata":       "LabelList default=None configurable=false",
		"tags":                   "StringList default=None configurable=false",
		"target_compatible_with": "LabelList default=None configurable=true",
		"testonly":               "Bool default=None configurable=false",
	}

	t.Run("None", func(t *testing.T) {
		macros, err := exec(t, &starlark.Thread{}, `m = macro(implementation = _impl, inherit_attrs = None, attrs = {"x": attr.string()})`, nil)
		require.NoError(t, err)
		require.Equal(t, map[string]string{
			"x": "String_ default=Str configurable=true",
		}, macros["m"])
	})

	t.Run("Common", func(t *testing.T) {
		macros, err := exec(t, &starlark.Thread{}, `m = macro(implementation = _impl, inherit_attrs = "common")`, nil)
		require.NoError(t, err)
		require.Equal(t, commonAttrs, macros["m"])
	})

	t.Run("Rule", func(t *testing.T) {
		// All public attributes of the rule should be inherited,
		// including the ones that are implicitly added to all
		// rules and tests. Attributes that are declared by the
		// macro take precedence, and setting them to None
		// prevents them from being inherited. Non-mandatory
		// attributes should have their default set to None.
		macros, err := exec(t, &starlark.Thread{}, `
m = macro(
    implementation = _impl,
    inherit_attrs = my_test,
    attrs = {
        "extra": attr.string(configurable = False),
        "optional": attr.int(),
        "size": None,
    },
)
`, starlark.StringDict{"my_test": myTest})
		require.NoError(t, err)
		expectedAttrs := maps.Clone(commonAttrs)
		maps.Copy(expectedAttrs, map[string]string{
			"args":        "StringList default=None configurable=true",
			"extra":       "String_ default=Str configurable=false",
			"flaky":       "Bool default=None configurable=false",
			"local":       "Bool default=None configurable=false",
			"mandatory":   "String_ mandatory configurable=true",
			"optional":    "Int default=Int configurable=true",
			"shard_count": "Int default=None configurable=true",
			"timeout":     "String_ default=None configurable=false",
		})
		require.Equal(t, expectedAttrs, macros["m"])
	})

	t.Run("RuleCallWithNone", func(t *testing.T) {
		// Macros forward inherited attributes to the rule by
		// calling it with **kwargs. Rules should therefore treat
		// all of these attributes being None as if they were
		// omitted.
		macros, err := exec(t, &starlark.Thread{}, `m = macro(implementation = _impl, inherit_attrs = my_test)`, starlark.StringDict{"my_test": myTest})
		require.NoError(t, err)

		callRule := func(kwargs []starlark.Tuple) *model_starlark_pb.RuleTarget {
			objectManager := NewMockObjectManagerForTesting(ctrl)
			targetRegistrar := model_starlark.NewTargetRegistrar[object.LocalReference, model_core.CreatedObjectTree](
				ctx,
				encoder,
				&inlinedtree.Options{
					ReferenceFormat:  referenceFormat,
					MaximumSizeBytes: 32 * 1024,
				},
				objectManager,
				model_core.NewSimpleMessage[object.LocalReference](&model_starlark_pb.InheritableAttrs{}),
			)
			defer targetRegistrar.Discard()

			thread := &starlark.Thread{}
			thread.SetLocal(model_starlark.TargetRegistrarKey, targetRegistrar)
			thread.SetLocal(model_starlark.CanonicalPackageKey, util.Must(label.NewCanonicalPackage("@@main+//foo")))
			thread.SetLocal(model_starlark.ValueEncodingOptionsKey, valueEncodingOptions)
			_, err := starlark.Call(thread, myTest, nil, append(
				[]starlark.Tuple{
					{starlark.String("name"), starlark.String("t")},
					{starlark.String("mandatory"), starlark.String("value")},
				},
				kwargs...,
			))
			require.NoError(t, err)
			return targetRegistrar.GetAndRemoveTarget("t").Message.GetRuleTarget()
		}

		var noneKwargs []starlark.Tuple
		for name, attr := range macros["m"] {
			if name != "mandatory" {
				require.Contains(t, attr, "default=None")
				noneKwargs = append(noneKwargs, starlark.Tuple{starlark.String(name), starlark.None})
			}
		}
		require.Len(t, noneKwargs, 16)
		testutil.RequireEqualProto(t, callRule(nil), callRule(noneKwargs))
	})

	t.Run("Macro", func(t *testing.T) {
		// Public attributes of macros can be inherited as well.
		macros, err := exec(t, &starlark.Thread{}, `
inner = macro(
    implementation = _impl,
    attrs = {
        "_private": attr.string(),
        "mandatory": attr.string(mandatory = True),
        "optional": attr.string(configurable = False),
    },
)
outer = macro(implementation = _impl, inherit_attrs = inner)
`, nil)
		require.NoError(t, err)
		require.Equal(t, map[string]string{
			"mandatory": "String_ mandatory configurable=true",
			"optional":  "String_ default=None configurable=false",
		}, macros["outer"])
	})

	t.Run("RuleFromOtherFile", func(t *testing.T) {
		// Rules that are declared in other .bzl files need to
		// be resolved.
		identifier := util.Must(label.NewCanonicalStarlarkIdentifier("@@other+//:defs.bzl%other_rule"))
		thread := &starlark.Thread{}
		thread.SetLocal(model_starlark.GlobalResolverKey, model_starlark.GlobalResolver[object.LocalReference](func(id label.CanonicalStarlarkIdentifier) (model_core.Message[*model_starlark_pb.Value, object.LocalReference], error) {
			require.Equal(t, identifier, id)
			return model_core.NewSimpleMessage[object.LocalReference](&model_starlark_pb.Value{
				Kind: &model_starlark_pb.Value_Rule{
					Rule: &model_starlark_pb.Rule{
						Kind: &model_starlark_pb.Rule_Definition_{
							Definition: &model_starlark_pb.Rule_Definition{
								Attrs: []*model_starlark_pb.NamedAttr{{
									Name: "srcs",
									Attr: &model_starlark_pb.Attr{
										Default:      &model_starlark_pb.Value{Kind: &model_starlark_pb.Value_List{List: &model_starlark_pb.List{}}},
										Type:         &model_starlark_pb.Attr_StringList{StringList: &model_starlark_pb.Attr_StringListType{}},
										Configurable: true,
									},
								}},
							},
						},
					},
				},
			}), nil
		}))
		macros, err := exec(t, thread, `m = macro(implementation = _impl, inherit_attrs = other_rule)`, starlark.StringDict{
			"other_rule": model_starlark.NewRule(&identifier, model_starlark.NewReloadingRuleDefinition[object.LocalReference, model_core.CreatedObjectTree](identifier)),
		})
		require.NoError(t, err)
		expectedAttrs := maps.Clone(commonAttrs)
		expectedAttrs["srcs"] = "StringList default=None configurable=true"
		require.Equal(t, expectedAttrs, macros["m"])
	})

	t.Run("BuildSetting", func(t *testing.T) {
		// Build settings additionally accept a mandatory
		// "build_setting_default" attribute whose type matches
		// that of the build setting, and "help".
		for buildSettingType, expectedType := range map[model_starlark.BuildSettingType[object.LocalReference, model_core.CreatedObjectTree]]string{
			model_starlark.NewBoolBuildSettingType[object.LocalReference, model_core.CreatedObjectTree]():           "Bool",
			model_starlark.NewIntBuildSettingType[object.LocalReference, model_core.CreatedObjectTree]():            "Int",
			model_starlark.NewLabelListBuildSettingType[object.LocalReference, model_core.CreatedObjectTree](false): "LabelList",
			model_starlark.NewStringBuildSettingType[object.LocalReference, model_core.CreatedObjectTree]():         "String_",
			model_starlark.NewStringListBuildSettingType[object.LocalReference, model_core.CreatedObjectTree](true): "StringList",
		} {
			myFlag := newRule(
				"my_flag",
				map[string]*model_starlark.Attr[object.LocalReference, model_core.CreatedObjectTree]{
					"extra": stringAttr(starlark.String("default")),
				},
				model_starlark.NewBuildSetting(buildSettingType, true),
				model_starlark_pb.Rule_Definition_NON_EXECUTABLE,
			)
			macros, err := exec(t, &starlark.Thread{}, `m = macro(implementation = _impl, inherit_attrs = my_flag)`, starlark.StringDict{"my_flag": myFlag})
			require.NoError(t, err)
			expectedAttrs := maps.Clone(commonAttrs)
			maps.Copy(expectedAttrs, map[string]string{
				"build_setting_default": expectedType + " mandatory configurable=false",
				"extra":                 "String_ default=None configurable=true",
				"help":                  "String_ default=None configurable=false",
			})
			require.Equal(t, expectedAttrs, macros["m"], buildSettingType.Type())
		}
	})

	t.Run("Invalid", func(t *testing.T) {
		for value, expectedError := range map[string]string{
			`"uncommon"`: `macro: for parameter "inherit_attrs": unknown set of attributes "uncommon"`,
			`42`:         `macro: for parameter "inherit_attrs": got int, want rule, macro or "common"`,
			`len`:        `macro: for parameter "inherit_attrs": got builtin_function_or_method, want rule, macro or "common"`,
		} {
			_, err := exec(t, &starlark.Thread{}, `m = macro(implementation = _impl, inherit_attrs = `+value+`)`, nil)
			require.ErrorContains(t, err, expectedError, value)
		}
	})
}
