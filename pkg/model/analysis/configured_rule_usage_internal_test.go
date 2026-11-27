package analysis

import (
	"testing"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"

	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func newStringValue(s string) *model_starlark_pb.Value {
	contents := &model_filesystem_pb.MaybeInlineFileContents{}
	if s != "" {
		contents.SetContentsInline([]byte(s))
	}
	return model_starlark_pb.Value_builder{
		Str: proto.ValueOrDefault(contents),
	}.Build()
}

var noneValue = model_starlark_pb.Value_builder{None: &emptypb.Empty{}}.Build()

func newValueSelectGroup(v *model_starlark_pb.Value) *model_starlark_pb.Select_Group {
	return model_starlark_pb.Select_Group_builder{
		NoMatchValue: proto.ValueOrDefault(v),
	}.Build()
}

func TestIsPublicAttrExplicitlySet(t *testing.T) {
	t.Run("None", func(t *testing.T) {
		// Attrs whose value is None are left at their default
		// value.
		require.False(t, isPublicAttrExplicitlySet(model_starlark_pb.RuleTarget_PublicAttrValue_builder{
			ValueParts: []*model_starlark_pb.Select_Group{newValueSelectGroup(noneValue)},
		}.Build()))
	})

	t.Run("Value", func(t *testing.T) {
		require.True(t, isPublicAttrExplicitlySet(model_starlark_pb.RuleTarget_PublicAttrValue_builder{
			ValueParts: []*model_starlark_pb.Select_Group{newValueSelectGroup(newStringValue("hello"))},
		}.Build()))
	})

	t.Run("SelectWithConditions", func(t *testing.T) {
		// Even if the default branch of the select() is None,
		// another branch may yield an explicit value.
		require.True(t, isPublicAttrExplicitlySet(model_starlark_pb.RuleTarget_PublicAttrValue_builder{
			ValueParts: []*model_starlark_pb.Select_Group{model_starlark_pb.Select_Group_builder{
				Conditions: []*model_starlark_pb.Select_Condition{model_starlark_pb.Select_Condition_builder{
					ConditionIdentifier: "@@main+//:linux",
					Value:               newStringValue("linux"),
				}.Build()},
				NoMatchValue: proto.ValueOrDefault(noneValue),
			}.Build()},
		}.Build()))
	})

	t.Run("SelectWithoutDefault", func(t *testing.T) {
		require.True(t, isPublicAttrExplicitlySet(model_starlark_pb.RuleTarget_PublicAttrValue_builder{
			ValueParts: []*model_starlark_pb.Select_Group{model_starlark_pb.Select_Group_builder{
				NoMatchError: proto.String("no match"),
			}.Build()},
		}.Build()))
	})

	t.Run("Concatenation", func(t *testing.T) {
		// Values consisting of multiple parts are concatenated,
		// meaning they are never None.
		require.True(t, isPublicAttrExplicitlySet(model_starlark_pb.RuleTarget_PublicAttrValue_builder{
			ValueParts: []*model_starlark_pb.Select_Group{
				newValueSelectGroup(noneValue),
				newValueSelectGroup(noneValue),
			},
		}.Build()))
	})
}

func TestGetExplicitPublicAttrIndices(t *testing.T) {
	require.Equal(t, []uint32{1, 3}, getExplicitPublicAttrIndices([]*model_starlark_pb.RuleTarget_PublicAttrValue{
		model_starlark_pb.RuleTarget_PublicAttrValue_builder{ValueParts: []*model_starlark_pb.Select_Group{newValueSelectGroup(noneValue)}}.Build(),
		model_starlark_pb.RuleTarget_PublicAttrValue_builder{ValueParts: []*model_starlark_pb.Select_Group{newValueSelectGroup(newStringValue("b"))}}.Build(),
		model_starlark_pb.RuleTarget_PublicAttrValue_builder{ValueParts: []*model_starlark_pb.Select_Group{newValueSelectGroup(noneValue)}}.Build(),
		model_starlark_pb.RuleTarget_PublicAttrValue_builder{ValueParts: []*model_starlark_pb.Select_Group{newValueSelectGroup(newStringValue("d"))}}.Build(),
	}))
	require.Empty(t, getExplicitPublicAttrIndices(nil))
}

// newTestRuleDefinition returns a rule definition that has a mixture of
// public and private attrs, some of which are mandatory.
func newTestRuleDefinition() model_core.Message[*model_starlark_pb.Rule_Definition, model_core.CreatedObjectTree] {
	stringAttr := func(defaultValue *model_starlark_pb.Value) *model_starlark_pb.Attr {
		return model_starlark_pb.Attr_builder{
			String:  &model_starlark_pb.Attr_StringType{},
			Default: defaultValue,
		}.Build()
	}
	return model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_starlark_pb.Rule_Definition_builder{
		Attrs: []*model_starlark_pb.NamedAttr{
			model_starlark_pb.NamedAttr_builder{
				// Public attr 0.
				Name: "a_default",
				Attr: stringAttr(newStringValue("default of a")),
			}.Build(),
			model_starlark_pb.NamedAttr_builder{
				Name: "_private",
				Attr: model_starlark_pb.Attr_builder{
					Label: &model_starlark_pb.Attr_LabelType{},
					Default: model_starlark_pb.Value_builder{
						Label: proto.String("@@rules+//:tool"),
					}.Build(),
				}.Build(),
			}.Build(),
			model_starlark_pb.NamedAttr_builder{
				// Public attr 1.
				Name: "b_explicit",
				Attr: stringAttr(newStringValue("default of b")),
			}.Build(),
			model_starlark_pb.NamedAttr_builder{
				// Public attr 2.
				Name: "c_mandatory",
				Attr: stringAttr(nil),
			}.Build(),
		},
	}.Build())
}

func TestNewRuleUsageTransitionAttr(t *testing.T) {
	ruleDefinition := newTestRuleDefinition()
	starlarkValueEncoders := &StarlarkValueEncoders[model_core.CreatedObjectTree]{}

	t.Run("Valid", func(t *testing.T) {
		// Indices only count public attrs, so index 1 refers to
		// "b_explicit", not "_private".
		a, err := newRuleUsageTransitionAttr[model_core.CreatedObjectTree, model_core.CreatedObjectTree](ruleDefinition, []uint32{1, 2}, starlarkValueEncoders)
		require.NoError(t, err)
		require.Equal(t, map[string]struct{}{
			"b_explicit":  {},
			"c_mandatory": {},
		}, a.explicitAttrs)
	})

	t.Run("NotSorted", func(t *testing.T) {
		_, err := newRuleUsageTransitionAttr[model_core.CreatedObjectTree, model_core.CreatedObjectTree](ruleDefinition, []uint32{2, 1}, starlarkValueEncoders)
		require.EqualError(t, err, "explicit public attr indices are not sorted in strictly increasing order")
	})

	t.Run("Duplicate", func(t *testing.T) {
		_, err := newRuleUsageTransitionAttr[model_core.CreatedObjectTree, model_core.CreatedObjectTree](ruleDefinition, []uint32{1, 1}, starlarkValueEncoders)
		require.EqualError(t, err, "explicit public attr indices are not sorted in strictly increasing order")
	})

	t.Run("OutOfRange", func(t *testing.T) {
		_, err := newRuleUsageTransitionAttr[model_core.CreatedObjectTree, model_core.CreatedObjectTree](ruleDefinition, []uint32{3}, starlarkValueEncoders)
		require.EqualError(t, err, "explicit public attr index 3 exceeds the number of public attrs of the rule, which is 3")
	})
}

func TestRuleUsageTransitionAttr(t *testing.T) {
	thread := eval.NewThread(t.Context(), eval.Universe())
	universe := eval.Universe()
	call := func(t *testing.T, builtin string, args ...eval.Value) (eval.Value, error) {
		return eval.Call(thread, universe[builtin], eval.Args{Positional: args})
	}
	newAttr := func(t *testing.T) *ruleUsageTransitionAttr[model_core.CreatedObjectTree, model_core.CreatedObjectTree] {
		a, err := newRuleUsageTransitionAttr[model_core.CreatedObjectTree, model_core.CreatedObjectTree](
			newTestRuleDefinition(),
			/* explicitPublicAttrIndices = */ []uint32{1},
			&StarlarkValueEncoders[model_core.CreatedObjectTree]{},
		)
		require.NoError(t, err)
		return a
	}

	t.Run("DefaultValue", func(t *testing.T) {
		// Attrs that are not explicitly set should yield their
		// default value, both for public and private attrs.
		a := newAttr(t)
		v, err := a.Attr(thread, "a_default")
		require.NoError(t, err)
		require.Equal(t, eval.String("default of a"), v)

		v, err = call(t, "getattr", a, eval.String("_private"))
		require.NoError(t, err)
		require.Equal(
			t,
			model_starlark.NewLabel[model_core.CreatedObjectTree, model_core.CreatedObjectTree](util.Must(label.NewResolvedLabel("@@rules+//:tool"))),
			v,
		)
	})

	t.Run("ExplicitlySet", func(t *testing.T) {
		// Accessing attrs that are explicitly set should cause
		// the transition to be computed for each target
		// individually. getattr() and hasattr() should
		// propagate this error, as opposed to returning the
		// provided default value or False.
		a := newAttr(t)
		_, err := a.Attr(thread, "b_explicit")
		require.ErrorIs(t, err, errTransitionDependsOnAttrs)

		_, err = call(t, "getattr", a, eval.String("b_explicit"), eval.None)
		require.ErrorIs(t, err, errTransitionDependsOnAttrs)

		_, err = call(t, "hasattr", a, eval.String("b_explicit"))
		require.ErrorIs(t, err, errTransitionDependsOnAttrs)
	})

	t.Run("CommonAttrs", func(t *testing.T) {
		// Common attrs like "name" are always specific to the
		// target.
		a := newAttr(t)
		for _, name := range []string{"name", "tags", "testonly"} {
			_, err := a.Attr(thread, name)
			require.ErrorIs(t, err, errTransitionDependsOnAttrs, name)
		}
	})

	t.Run("MandatoryNotSet", func(t *testing.T) {
		// Mandatory attrs that are not explicitly set don't
		// have a value. Let the target report the error.
		a, err := newRuleUsageTransitionAttr[model_core.CreatedObjectTree, model_core.CreatedObjectTree](
			newTestRuleDefinition(),
			/* explicitPublicAttrIndices = */ nil,
			&StarlarkValueEncoders[model_core.CreatedObjectTree]{},
		)
		require.NoError(t, err)
		_, err = a.Attr(thread, "c_mandatory")
		require.ErrorIs(t, err, errTransitionDependsOnAttrs)
	})

	t.Run("NonexistentAttr", func(t *testing.T) {
		// Attrs that don't exist should behave like they do for
		// regular structs, so that getattr() and hasattr() can
		// be used to probe for them.
		a := newAttr(t)
		v, err := call(t, "getattr", a, eval.String("nonexistent"), eval.String("fallback"))
		require.NoError(t, err)
		require.Equal(t, eval.String("fallback"), v)

		v, err = call(t, "hasattr", a, eval.String("nonexistent"))
		require.NoError(t, err)
		require.Equal(t, eval.False, v)
	})

	t.Run("Dir", func(t *testing.T) {
		// The set of attrs visible to transitions depends on
		// whether attrs use select(), so dir() can't be
		// computed for all targets of a rule at once.
		a := newAttr(t)
		_, err := call(t, "dir", a)
		require.ErrorIs(t, err, errTransitionDependsOnAttrs)
	})

	t.Run("Caching", func(t *testing.T) {
		// Default values should only be decoded once, even if
		// they are accessed repeatedly.
		a := newAttr(t)
		v1, err := a.Attr(thread, "_private")
		require.NoError(t, err)
		require.Equal(t, map[string]eval.Value{"_private": v1}, a.decodedAttrs)
		v2, err := a.Attr(thread, "_private")
		require.NoError(t, err)
		require.Equal(t, v1, v2)
		require.Len(t, a.decodedAttrs, 1)
	})
}

func TestGetAllPublicAttrIndices(t *testing.T) {
	// Private attrs should not be counted.
	require.Equal(t, []uint32{0, 1, 2}, getAllPublicAttrIndices(newTestRuleDefinition().Message))
}

func TestGetConfiguredRuleUsageExplicitPublicAttrIndices(t *testing.T) {
	labelListAttr := func(cfg *model_starlark_pb.Transition, defaultValue *model_starlark_pb.Value) *model_starlark_pb.Attr {
		return model_starlark_pb.Attr_builder{
			LabelList: model_starlark_pb.Attr_LabelListType_builder{
				ListValueOptions: model_starlark_pb.Attr_LabelOptions_builder{Cfg: cfg}.Build(),
			}.Build(),
			Default: defaultValue,
		}.Build()
	}
	targetTransition := model_starlark_pb.Transition_builder{
		Target: &emptypb.Empty{},
	}.Build()
	userDefinedTransition := model_starlark_pb.Transition_builder{
		UserDefined: model_starlark_pb.Transition_UserDefined_builder{
			Identifier: proto.String("@@rules+//:defs.bzl%my_transition"),
		}.Build(),
	}.Build()
	labelDefault := model_starlark_pb.Value_builder{
		Label: proto.String("@@rules+//:default"),
	}.Build()
	explicitValue := model_starlark_pb.RuleTarget_PublicAttrValue_builder{
		ValueParts: []*model_starlark_pb.Select_Group{newValueSelectGroup(newStringValue("explicit"))},
	}.Build()
	unsetValue := model_starlark_pb.RuleTarget_PublicAttrValue_builder{
		ValueParts: []*model_starlark_pb.Select_Group{newValueSelectGroup(noneValue)},
	}.Build()
	newRuleDefinition := func(cfg *model_starlark_pb.Transition) *model_starlark_pb.Rule_Definition {
		return model_starlark_pb.Rule_Definition_builder{
			Attrs: []*model_starlark_pb.NamedAttr{
				model_starlark_pb.NamedAttr_builder{
					// Public attr 0: non-label attr.
					Name: "a_string",
					Attr: model_starlark_pb.Attr_builder{
						String:  &model_starlark_pb.Attr_StringType{},
						Default: newStringValue(""),
					}.Build(),
				}.Build(),
				model_starlark_pb.NamedAttr_builder{
					// Public attr 1: label attr with a
					// default value.
					Name: "b_with_default",
					Attr: labelListAttr(cfg, labelDefault),
				}.Build(),
				model_starlark_pb.NamedAttr_builder{
					Name: "_private",
					Attr: labelListAttr(targetTransition, labelDefault),
				}.Build(),
				model_starlark_pb.NamedAttr_builder{
					// Public attr 2: label attr defaulting
					// to None.
					Name: "c_none_default",
					Attr: labelListAttr(targetTransition, noneValue),
				}.Build(),
			},
		}.Build()
	}

	t.Run("BuiltinTransitionsAllUnset", func(t *testing.T) {
		// The label attr with a default value is left unset, so
		// its labels need to be resolved by ConfiguredRuleUsage.
		indices, needed := getConfiguredRuleUsageExplicitPublicAttrIndices(
			newRuleDefinition(targetTransition),
			[]*model_starlark_pb.RuleTarget_PublicAttrValue{unsetValue, unsetValue, unsetValue},
		)
		require.Empty(t, indices)
		require.True(t, needed)
	})

	t.Run("BuiltinTransitionsAllSet", func(t *testing.T) {
		// All label attrs with default values are explicitly
		// set, so ConfiguredRuleUsage is not needed. Only the
		// explicitness of label attrs with default values is
		// part of the key.
		indices, needed := getConfiguredRuleUsageExplicitPublicAttrIndices(
			newRuleDefinition(targetTransition),
			[]*model_starlark_pb.RuleTarget_PublicAttrValue{explicitValue, explicitValue, explicitValue},
		)
		require.Equal(t, []uint32{1}, indices)
		require.False(t, needed)
	})

	t.Run("BuiltinTransitionsOnlyIrrelevantAttrsUnset", func(t *testing.T) {
		// Leaving attrs unset whose default value doesn't
		// contain any labels should not require
		// ConfiguredRuleUsage.
		indices, needed := getConfiguredRuleUsageExplicitPublicAttrIndices(
			newRuleDefinition(targetTransition),
			[]*model_starlark_pb.RuleTarget_PublicAttrValue{unsetValue, explicitValue, unsetValue},
		)
		require.Equal(t, []uint32{1}, indices)
		require.False(t, needed)
	})

	t.Run("UserDefinedTransition", func(t *testing.T) {
		// User-defined transitions may read any public attr, so
		// ConfiguredRuleUsage is always needed, and all
		// explicitly set public attrs are part of the key.
		indices, needed := getConfiguredRuleUsageExplicitPublicAttrIndices(
			newRuleDefinition(userDefinedTransition),
			[]*model_starlark_pb.RuleTarget_PublicAttrValue{explicitValue, explicitValue, unsetValue},
		)
		require.Equal(t, []uint32{0, 1}, indices)
		require.True(t, needed)
	})
}

func TestIndexedConfiguredLabelAttrCursor(t *testing.T) {
	newCursor := func(attrIndices ...uint32) indexedConfiguredLabelAttrCursor[model_core.CreatedObjectTree] {
		labelAttrs := make([]*model_analysis_pb.IndexedConfiguredLabelAttr, 0, len(attrIndices))
		for _, attrIndex := range attrIndices {
			labelAttrs = append(labelAttrs, model_analysis_pb.IndexedConfiguredLabelAttr_builder{
				AttrIndex:  attrIndex,
				Configured: &model_analysis_pb.ConfiguredLabelAttr{},
			}.Build())
		}
		return newIndexedConfiguredLabelAttrCursor(model_core.NewSimpleMessage[model_core.CreatedObjectTree](labelAttrs))
	}

	t.Run("Empty", func(t *testing.T) {
		c := newCursor()
		_, ok, err := c.get(3)
		require.NoError(t, err)
		require.False(t, ok)
		require.NoError(t, c.finish())
	})

	t.Run("Sparse", func(t *testing.T) {
		// Only attrs 1 and 4 have entries. Attrs 0, 2 and 3
		// should be reported as absent.
		c := newCursor(1, 4)
		for _, attrIndex := range []int{0, 1, 2, 3, 4} {
			_, ok, err := c.get(attrIndex)
			require.NoError(t, err)
			require.Equal(t, attrIndex == 1 || attrIndex == 4, ok, attrIndex)
		}
		require.NoError(t, c.finish())
	})

	t.Run("NotLabelAttr", func(t *testing.T) {
		// An entry for attr 1 is skipped over if attr 1 is not
		// a label attr, meaning get() is never called for it.
		c := newCursor(1)
		_, _, err := c.get(2)
		require.EqualError(t, err, "label attr entry with attr index 1 is not sorted, or does not refer to a label attr")
	})

	t.Run("Duplicate", func(t *testing.T) {
		c := newCursor(1, 1)
		_, ok, err := c.get(1)
		require.NoError(t, err)
		require.True(t, ok)
		_, _, err = c.get(2)
		require.EqualError(t, err, "label attr entry with attr index 1 is not sorted, or does not refer to a label attr")
	})

	t.Run("TrailingEntry", func(t *testing.T) {
		// Entries past the last label attr refer to attrs that
		// don't exist, or are not label attrs.
		c := newCursor(5)
		_, ok, err := c.get(2)
		require.NoError(t, err)
		require.False(t, ok)
		require.EqualError(t, c.finish(), "label attr entry with attr index 5 does not refer to a label attr")
	})
}

func TestConfiguredRuleUsageResolvesDefaultValue(t *testing.T) {
	// Regression test: a rule without user-defined transitions has
	// a label attr whose default value is None. Targets setting it
	// explicitly should not have it overridden by
	// ConfiguredRuleUsage, even though the attr is omitted from the
	// key. This happened for go_config's "export_stdlib" attr.
	labelAttr := func(defaultValue *model_starlark_pb.Value) *model_starlark_pb.Attr {
		return model_starlark_pb.Attr_builder{
			Label: model_starlark_pb.Attr_LabelType_builder{
				ValueOptions: model_starlark_pb.Attr_LabelOptions_builder{
					Cfg: model_starlark_pb.Transition_builder{
						Target: &emptypb.Empty{},
					}.Build(),
				}.Build(),
			}.Build(),
			Default: defaultValue,
		}.Build()
	}
	ruleDefinition := model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_starlark_pb.Rule_Definition_builder{
		Attrs: []*model_starlark_pb.NamedAttr{
			model_starlark_pb.NamedAttr_builder{
				Name: "a_none_default",
				Attr: labelAttr(noneValue),
			}.Build(),
			model_starlark_pb.NamedAttr_builder{
				Name: "b_label_default",
				Attr: labelAttr(model_starlark_pb.Value_builder{
					Label: proto.String("@@rules+//:default"),
				}.Build()),
			}.Build(),
			model_starlark_pb.NamedAttr_builder{
				Name: "c_mandatory",
				Attr: labelAttr(nil),
			}.Build(),
		},
	}.Build())
	explicitValue := model_starlark_pb.RuleTarget_PublicAttrValue_builder{
		ValueParts: []*model_starlark_pb.Select_Group{newValueSelectGroup(model_starlark_pb.Value_builder{
			Label: proto.String("@@main+//:explicit"),
		}.Build())},
	}.Build()
	unsetValue := model_starlark_pb.RuleTarget_PublicAttrValue_builder{
		ValueParts: []*model_starlark_pb.Select_Group{newValueSelectGroup(noneValue)},
	}.Build()

	for _, publicAttrValues := range [][]*model_starlark_pb.RuleTarget_PublicAttrValue{
		{explicitValue, explicitValue, explicitValue},
		{explicitValue, unsetValue, explicitValue},
		{unsetValue, explicitValue, unsetValue},
		{unsetValue, unsetValue, unsetValue},
	} {
		indices, needed := getConfiguredRuleUsageExplicitPublicAttrIndices(ruleDefinition.Message, publicAttrValues)
		a, err := newRuleUsageTransitionAttr[model_core.CreatedObjectTree, model_core.CreatedObjectTree](
			ruleDefinition,
			indices,
			&StarlarkValueEncoders[model_core.CreatedObjectTree]{},
		)
		require.NoError(t, err)

		anyResolved := false
		for i, namedAttr := range ruleDefinition.Message.GetAttrs() {
			resolves := configuredRuleUsageResolvesDefaultValue(namedAttr, a.explicitAttrs)
			anyResolved = anyResolved || resolves
			if isPublicAttrExplicitlySet(publicAttrValues[i]) {
				// Attrs set explicitly by the target must
				// never be overridden.
				require.False(t, resolves, namedAttr.GetName())
			} else {
				// Only the attr with a label default
				// needs its default resolved.
				require.Equal(t, namedAttr.GetName() == "b_label_default", resolves, namedAttr.GetName())
			}
		}
		// ConfiguredRuleUsage should be requested if and only
		// if it resolves anything.
		require.Equal(t, anyResolved, needed)
	}
}

func TestGetDefaultValueLabels(t *testing.T) {
	labelElement := func(l string) *model_starlark_pb.List_Element {
		return model_starlark_pb.List_Element_builder{
			Leaf: model_starlark_pb.Value_builder{
				Label: proto.String(l),
			}.Build(),
		}.Build()
	}

	// Labels should be deduplicated and sorted, so that resolved
	// labels can be stored without the original labels.
	originalLabels, err := getDefaultValueLabels(
		t.Context(),
		&model_starlark.ValueReaders[model_core.CreatedObjectTree]{},
		model_core.NewSimpleMessage[model_core.CreatedObjectTree](model_starlark_pb.Value_builder{
			List: model_starlark_pb.List_builder{
				Elements: []*model_starlark_pb.List_Element{
					labelElement("@@rules+//:c"),
					labelElement("@@rules+//:a"),
					labelElement("@@rules+//:c"),
					labelElement("@@rules+//:b"),
				},
			}.Build(),
		}.Build()),
	)
	require.NoError(t, err)
	require.Equal(t, []string{"@@rules+//:a", "@@rules+//:b", "@@rules+//:c"}, originalLabels)

	resolvedLabels := []string{"@@rules+//:a_actual", "", "@@rules+//:c_actual"}
	resolvedLabel, err := lookupResolvedLabel(originalLabels, resolvedLabels, "@@rules+//:c")
	require.NoError(t, err)
	require.Equal(t, "@@rules+//:c_actual", resolvedLabel)

	resolvedLabel, err = lookupResolvedLabel(originalLabels, resolvedLabels, "@@rules+//:b")
	require.NoError(t, err)
	require.Equal(t, "", resolvedLabel)

	_, err = lookupResolvedLabel(originalLabels, resolvedLabels, "@@rules+//:d")
	require.EqualError(t, err, "label \"@@rules+//:d\" is not contained in the default value")
}

func TestGetLabelAttrOptions(t *testing.T) {
	labelOptions := &model_starlark_pb.Attr_LabelOptions{}
	for _, tc := range []struct {
		name             string
		attr             *model_starlark_pb.Attr
		expectedScalar   bool
		expectedIsLabels bool
	}{
		{"Label", model_starlark_pb.Attr_builder{Label: model_starlark_pb.Attr_LabelType_builder{ValueOptions: labelOptions}.Build()}.Build(), true, true},
		{"LabelKeyedStringDict", model_starlark_pb.Attr_builder{LabelKeyedStringDict: model_starlark_pb.Attr_LabelKeyedStringDictType_builder{DictKeyOptions: labelOptions}.Build()}.Build(), false, true},
		{"LabelList", model_starlark_pb.Attr_builder{LabelList: model_starlark_pb.Attr_LabelListType_builder{ListValueOptions: labelOptions}.Build()}.Build(), false, true},
		{"StringKeyedLabelDict", model_starlark_pb.Attr_builder{StringKeyedLabelDict: model_starlark_pb.Attr_StringKeyedLabelDictType_builder{DictValueOptions: labelOptions}.Build()}.Build(), false, true},
		{"String", model_starlark_pb.Attr_builder{String: &model_starlark_pb.Attr_StringType{}}.Build(), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actualOptions, isScalar, isLabelAttr := getLabelAttrOptions(tc.attr)
			require.Equal(t, tc.expectedScalar, isScalar)
			require.Equal(t, tc.expectedIsLabels, isLabelAttr)
			if tc.expectedIsLabels {
				require.Same(t, labelOptions, actualOptions)
			} else {
				require.Nil(t, actualOptions)
			}
		})
	}
}
