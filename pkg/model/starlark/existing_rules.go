package starlark

import (
	"errors"
	"fmt"
	"strings"

	pg_label "bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/unpack"
	"bonanza.build/pkg/storage/object"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// ExistingRulesResolverKey is the key under which an instance of
// ExistingRulesResolver is stored in the thread local variables of a
// Starlark thread that is expanding a finalizer. It is used by
// native.existing_rule() and native.existing_rules().
const ExistingRulesResolverKey = "existing_rules_resolver"

// ExistingRulesResolver is used by native.existing_rule() and
// native.existing_rules() to obtain the targets in the current package
// that finalizers may observe.
type ExistingRulesResolver[TReference any] interface {
	// LookupExistingRule returns the definition of a target in the
	// current package. If no such target exists, an unset message
	// is returned.
	//
	// Unlike Bazel, targets declared by other finalizers may be
	// returned. Looking up names in the namespace of the finalizer
	// that is being expanded fails due to cyclic dependencies.
	LookupExistingRule(targetName pg_label.TargetName) (model_core.Message[*model_starlark_pb.Target_Definition, TReference], error)

	// ForEachExistingRule calls a function for each target that
	// finalizers may observe, in alphabetical order.
	ForEachExistingRule(yield func(targetName pg_label.TargetName, definition model_core.Message[*model_starlark_pb.Target_Definition, TReference]) error) error
}

func getExistingRulesResolver[TReference any](thread *starlark.Thread) (ExistingRulesResolver[TReference], error) {
	existingRulesResolver := thread.Local(ExistingRulesResolverKey)
	if existingRulesResolver == nil {
		return nil, errors.New("existing rules can only be obtained from within finalizers")
	}
	return existingRulesResolver.(ExistingRulesResolver[TReference]), nil
}

func nativeExistingRule[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	existingRulesResolver, err := getExistingRulesResolver[TReference](thread)
	if err != nil {
		return nil, err
	}
	var name pg_label.TargetName
	if err := starlark.UnpackArgs(
		b.Name(), args, kwargs,
		"name", unpack.Bind(thread, &name, unpack.TargetName),
	); err != nil {
		return nil, err
	}

	definition, err := existingRulesResolver.LookupExistingRule(name)
	if err != nil {
		return nil, err
	}
	if !definition.IsSet() {
		return starlark.None, nil
	}
	existingRule, err := newExistingRule[TReference, TMetadata](thread, name, definition)
	if err != nil {
		return nil, fmt.Errorf("target %#v: %w", name.String(), err)
	}
	if existingRule == nil {
		return starlark.None, nil
	}
	return existingRule, nil
}

func nativeExistingRules[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	existingRulesResolver, err := getExistingRulesResolver[TReference](thread)
	if err != nil {
		return nil, err
	}
	omitMacroInternals := false
	if err := starlark.UnpackArgs(
		b.Name(), args, kwargs,
		"omit_macro_internals?", unpack.Bind(thread, &omitMacroInternals, unpack.Bool),
	); err != nil {
		return nil, err
	}
	if omitMacroInternals {
		return nil, errors.New("omitting targets declared by symbolic macros is not supported")
	}

	existingRules := starlark.NewDict(0)
	if err := existingRulesResolver.ForEachExistingRule(func(targetName pg_label.TargetName, definition model_core.Message[*model_starlark_pb.Target_Definition, TReference]) error {
		existingRule, err := newExistingRule[TReference, TMetadata](thread, targetName, definition)
		if err != nil {
			return fmt.Errorf("target %#v: %w", targetName.String(), err)
		}
		if existingRule != nil {
			return existingRules.SetKey(thread, starlark.String(targetName.String()), existingRule)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	existingRules.Freeze()
	return existingRules, nil
}

// existingRuleBuilder is used to construct the dict that is returned
// by native.existing_rule() and native.existing_rules().
type existingRuleBuilder[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	thread         *starlark.Thread
	currentPackage pg_label.CanonicalPackage
	options        *ValueDecodingOptions[TReference]
	attrs          *starlark.Dict
}

func (b *existingRuleBuilder[TReference, TMetadata]) set(name string, value starlark.Value) error {
	return b.attrs.SetKey(b.thread, starlark.String(name), value)
}

// setValue sets an attribute to a value that is decoded from storage.
// Like Bazel, labels are converted to strings, and lists are converted
// to tuples.
func (b *existingRuleBuilder[TReference, TMetadata]) setValue(name string, value starlark.Value) error {
	convertedValue, err := b.convertValue(value)
	if err != nil {
		return fmt.Errorf("attr %#v: %w", name, err)
	}
	return b.set(name, convertedValue)
}

func (b *existingRuleBuilder[TReference, TMetadata]) setValueParts(name string, valueParts model_core.Message[[]*model_starlark_pb.Select_Group, TReference], concatenationOperator syntax.Token) error {
	value, err := b.decodeValueParts(valueParts, concatenationOperator)
	if err != nil {
		return fmt.Errorf("attr %#v: %w", name, err)
	}
	return b.setValue(name, value)
}

// decodeValueParts decodes the value of an attribute that may be
// configurable. Values that don't depend on any conditions are
// returned as is. All other values are returned as select() objects.
func (b *existingRuleBuilder[TReference, TMetadata]) decodeValueParts(valueParts model_core.Message[[]*model_starlark_pb.Select_Group, TReference], concatenationOperator syntax.Token) (starlark.Value, error) {
	if groups := valueParts.Message; len(groups) == 1 && len(groups[0].Conditions) == 0 {
		if noMatchValue, ok := groups[0].NoMatch.(*model_starlark_pb.Select_Group_NoMatchValue); ok {
			return DecodeValue[TReference, TMetadata](model_core.Nested(valueParts, noMatchValue.NoMatchValue), nil, b.options)
		}
	}
	return DecodeSelectGroups[TReference, TMetadata](valueParts, concatenationOperator, b.options)
}

func (b *existingRuleBuilder[TReference, TMetadata]) labelToString(l pg_label.ResolvedLabel) string {
	canonicalLabel, err := l.AsCanonical()
	if err != nil {
		return l.String()
	}
	canonicalPackage := canonicalLabel.GetCanonicalPackage()
	if canonicalPackage == b.currentPackage {
		return ":" + canonicalLabel.GetTargetName().String()
	}
	if canonicalPackage.GetCanonicalRepo() == b.currentPackage.GetCanonicalRepo() {
		return "//" + canonicalPackage.GetPackagePath() + ":" + canonicalLabel.GetTargetName().String()
	}
	return canonicalLabel.String()
}

func (b *existingRuleBuilder[TReference, TMetadata]) labelStringsToTuple(labels []string) (starlark.Tuple, error) {
	tuple := make(starlark.Tuple, 0, len(labels))
	for _, l := range labels {
		resolvedLabel, err := pg_label.NewResolvedLabel(l)
		if err != nil {
			return nil, fmt.Errorf("invalid label %#v: %w", l, err)
		}
		tuple = append(tuple, starlark.String(b.labelToString(resolvedLabel)))
	}
	return tuple, nil
}

// convertValue converts a value that is decoded from storage to the
// form in which native.existing_rule() returns it.
func (b *existingRuleBuilder[TReference, TMetadata]) convertValue(value starlark.Value) (starlark.Value, error) {
	switch v := value.(type) {
	case Label[TReference, TMetadata]:
		return starlark.String(b.labelToString(v.value)), nil
	case *starlark.List:
		tuple := make(starlark.Tuple, 0, v.Len())
		for element := range starlark.Elements(v) {
			convertedElement, err := b.convertValue(element)
			if err != nil {
				return nil, err
			}
			tuple = append(tuple, convertedElement)
		}
		return tuple, nil
	case starlark.Tuple:
		tuple := make(starlark.Tuple, 0, len(v))
		for _, element := range v {
			convertedElement, err := b.convertValue(element)
			if err != nil {
				return nil, err
			}
			tuple = append(tuple, convertedElement)
		}
		return tuple, nil
	case *starlark.Dict:
		dict := starlark.NewDict(v.Len())
		for key, value := range starlark.Entries(b.thread, v) {
			convertedKey, err := b.convertValue(key)
			if err != nil {
				return nil, err
			}
			convertedValue, err := b.convertValue(value)
			if err != nil {
				return nil, err
			}
			if err := dict.SetKey(b.thread, convertedKey, convertedValue); err != nil {
				return nil, err
			}
		}
		return dict, nil
	case *Select[TReference, TMetadata]:
		groups := make([]SelectGroup, 0, len(v.groups))
		for _, group := range v.groups {
			conditions := make(map[pg_label.ResolvedLabel]starlark.Value, len(group.conditions))
			for condition, conditionValue := range group.conditions {
				convertedConditionValue, err := b.convertValue(conditionValue)
				if err != nil {
					return nil, err
				}
				conditions[condition] = convertedConditionValue
			}
			var defaultValue starlark.Value
			if group.defaultValue != nil {
				var err error
				defaultValue, err = b.convertValue(group.defaultValue)
				if err != nil {
					return nil, err
				}
			}
			groups = append(groups, NewSelectGroup(conditions, defaultValue, group.noMatchError))
		}
		return NewSelect[TReference, TMetadata](groups, v.concatenationOperator), nil
	default:
		return value, nil
	}
}

var (
	testSizeNames = map[model_starlark_pb.TestSize]string{
		model_starlark_pb.TestSize_SMALL:    "small",
		model_starlark_pb.TestSize_MEDIUM:   "medium",
		model_starlark_pb.TestSize_LARGE:    "large",
		model_starlark_pb.TestSize_ENORMOUS: "enormous",
	}
	testTimeoutNames = map[model_starlark_pb.TestTimeout]string{
		model_starlark_pb.TestTimeout_SHORT:    "short",
		model_starlark_pb.TestTimeout_MODERATE: "moderate",
		model_starlark_pb.TestTimeout_LONG:     "long",
		model_starlark_pb.TestTimeout_ETERNAL:  "eternal",
	}
)

// newExistingRule creates the dict that is returned by
// native.existing_rule() for a given target. Like Bazel, the dict
// contains the name and the kind of the target, and the values of its
// public attributes. Unset attributes having computed defaults are
// omitted. If the target is not a rule target, nil is returned.
func newExistingRule[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](thread *starlark.Thread, targetName pg_label.TargetName, definition model_core.Message[*model_starlark_pb.Target_Definition, TReference]) (*starlark.Dict, error) {
	b := existingRuleBuilder[TReference, TMetadata]{
		thread:         thread,
		currentPackage: thread.Local(CanonicalPackageKey).(pg_label.CanonicalPackage),
		options:        thread.Local(ValueDecodingOptionsKey).(*ValueDecodingOptions[TReference]),
		attrs:          starlark.NewDict(0),
	}
	if err := b.set("name", starlark.String(targetName.String())); err != nil {
		return nil, err
	}

	switch kind := definition.Message.Kind.(type) {
	case *model_starlark_pb.Target_Definition_Alias:
		if err := b.set("kind", starlark.String("alias")); err != nil {
			return nil, err
		}
		if err := b.setValueParts(
			"actual",
			model_core.Nested(definition, []*model_starlark_pb.Select_Group{kind.Alias.Actual}),
			0,
		); err != nil {
			return nil, err
		}
	case *model_starlark_pb.Target_Definition_LabelSetting:
		labelSetting := kind.LabelSetting
		ruleKind := "label_setting"
		if labelSetting.Flag {
			ruleKind = "label_flag"
		}
		if err := b.set("kind", starlark.String(ruleKind)); err != nil {
			return nil, err
		}
		var buildSettingDefault starlark.Value = starlark.None
		if labelSetting.BuildSettingDefault != "" {
			buildSettingDefaults, err := b.labelStringsToTuple([]string{labelSetting.BuildSettingDefault})
			if err != nil {
				return nil, err
			}
			buildSettingDefault = buildSettingDefaults[0]
		}
		if err := b.set("build_setting_default", buildSettingDefault); err != nil {
			return nil, err
		}
	case *model_starlark_pb.Target_Definition_RuleTarget:
		if err := b.addRuleTargetAttrs(model_core.Nested(definition, kind.RuleTarget)); err != nil {
			return nil, err
		}
	case *model_starlark_pb.Target_Definition_TestSuite:
		if err := b.addTestSuiteAttrs(model_core.Nested(definition, kind.TestSuite)); err != nil {
			return nil, err
		}
	default:
		// Not a rule target.
		return nil, nil
	}

	b.attrs.Freeze()
	return b.attrs, nil
}

func (b *existingRuleBuilder[TReference, TMetadata]) addRuleTargetAttrs(ruleTarget model_core.Message[*model_starlark_pb.RuleTarget, TReference]) error {
	ruleIdentifier, err := pg_label.NewCanonicalStarlarkIdentifier(ruleTarget.Message.RuleIdentifier)
	if err != nil {
		return fmt.Errorf("invalid rule identifier %#v: %w", ruleTarget.Message.RuleIdentifier, err)
	}
	if err := b.set("kind", starlark.String(ruleIdentifier.GetStarlarkIdentifier().String())); err != nil {
		return err
	}

	// Obtain the definition of the rule, so that the values of
	// public attributes can be matched with their names and
	// default values.
	ruleValue, err := b.thread.Local(GlobalResolverKey).(GlobalResolver[TReference])(ruleIdentifier)
	if err != nil {
		return err
	}
	ruleValueKind, ok := ruleValue.Message.Kind.(*model_starlark_pb.Value_Rule)
	if !ok {
		return fmt.Errorf("identifier %#v is not a rule", ruleIdentifier.String())
	}
	ruleDefinitionKind, ok := ruleValueKind.Rule.Kind.(*model_starlark_pb.Rule_Definition_)
	if !ok {
		return fmt.Errorf("rule %#v does not have a definition", ruleIdentifier.String())
	}
	ruleDefinition := model_core.Nested(ruleValue, ruleDefinitionKind.Definition)

	publicAttrValues := ruleTarget.Message.PublicAttrValues
	for _, namedAttr := range ruleDefinition.Message.Attrs {
		if strings.HasPrefix(namedAttr.Name, "_") {
			continue
		}
		if len(publicAttrValues) == 0 {
			return errors.New("rule target has fewer public attr values than the rule definition has public attrs")
		}
		valueParts := model_core.Nested(ruleTarget, publicAttrValues[0].ValueParts)
		publicAttrValues = publicAttrValues[1:]

		if groups := valueParts.Message; len(groups) == 1 && len(groups[0].Conditions) == 0 {
			if noMatchValue, ok := groups[0].NoMatch.(*model_starlark_pb.Select_Group_NoMatchValue); ok {
				if _, ok := noMatchValue.NoMatchValue.Kind.(*model_starlark_pb.Value_None); ok {
					// No value provided. Use the
					// default value of the attribute,
					// unless it is computed.
					defaultValue := namedAttr.Attr.GetDefault()
					if _, ok := defaultValue.GetKind().(*model_starlark_pb.Value_Function); ok {
						continue
					}
					var value starlark.Value = starlark.None
					if defaultValue != nil {
						value, err = DecodeValue[TReference, TMetadata](model_core.Nested(ruleDefinition, defaultValue), nil, b.options)
						if err != nil {
							return fmt.Errorf("attr %#v: %w", namedAttr.Name, err)
						}
					}
					if err := b.setValue(namedAttr.Name, value); err != nil {
						return err
					}
					continue
				}
			}
		}

		attrType, err := DecodeAttrType[TReference, TMetadata](model_core.Nested(ruleDefinition, namedAttr.Attr))
		if err != nil {
			return fmt.Errorf("attr %#v: %w", namedAttr.Name, err)
		}
		if err := b.setValueParts(namedAttr.Name, valueParts, attrType.GetCanonicalizer(b.currentPackage).GetConcatenationOperator()); err != nil {
			return err
		}
	}
	if len(publicAttrValues) != 0 {
		return errors.New("rule target has more public attr values than the rule definition has public attrs")
	}

	// Attributes that are common to all rule targets.
	inheritableAttrs := ruleTarget.Message.InheritableAttrs
	var deprecation starlark.Value = starlark.None
	if d := inheritableAttrs.GetDeprecation(); d != "" {
		deprecation = starlark.String(d)
	}
	if err := b.set("deprecation", deprecation); err != nil {
		return err
	}
	execCompatibleWith, err := b.labelStringsToTuple(ruleTarget.Message.ExecCompatibleWith)
	if err != nil {
		return fmt.Errorf("attr \"exec_compatible_with\": %w", err)
	}
	if err := b.set("exec_compatible_with", execCompatibleWith); err != nil {
		return err
	}
	tags := make(starlark.Tuple, 0, len(ruleTarget.Message.Tags))
	for _, tag := range ruleTarget.Message.Tags {
		tags = append(tags, starlark.String(tag))
	}
	if err := b.set("tags", tags); err != nil {
		return err
	}
	if targetCompatibleWith := ruleTarget.Message.TargetCompatibleWith; len(targetCompatibleWith) > 0 {
		if err := b.setValueParts("target_compatible_with", model_core.Nested(ruleTarget, targetCompatibleWith), syntax.PLUS); err != nil {
			return err
		}
	} else if err := b.set("target_compatible_with", starlark.Tuple{}); err != nil {
		return err
	}
	if err := b.set("testonly", starlark.Bool(inheritableAttrs.GetTestonly())); err != nil {
		return err
	}

	if buildSettingDefault := ruleTarget.Message.BuildSettingDefault; buildSettingDefault != nil {
		value, err := DecodeValue[TReference, TMetadata](model_core.Nested(ruleTarget, buildSettingDefault), nil, b.options)
		if err != nil {
			return fmt.Errorf("attr \"build_setting_default\": %w", err)
		}
		if err := b.setValue("build_setting_default", value); err != nil {
			return err
		}
	}

	if test := ruleTarget.Message.Test; test != nil {
		if len(test.Args) > 0 {
			if err := b.setValueParts("args", model_core.Nested(ruleTarget, test.Args), syntax.PLUS); err != nil {
				return err
			}
		} else if err := b.set("args", starlark.Tuple{}); err != nil {
			return err
		}
		if err := b.set("flaky", starlark.Bool(test.Flaky)); err != nil {
			return err
		}
		if err := b.set("local", starlark.Bool(test.Local)); err != nil {
			return err
		}
		if len(test.ShardCount) > 0 {
			if err := b.setValueParts("shard_count", model_core.Nested(ruleTarget, test.ShardCount), 0); err != nil {
				return err
			}
		} else if err := b.set("shard_count", starlark.None); err != nil {
			return err
		}
		if err := b.set("size", starlark.String(testSizeNames[test.Size])); err != nil {
			return err
		}
		if err := b.set("timeout", starlark.String(testTimeoutNames[test.Timeout])); err != nil {
			return err
		}
	}
	return nil
}

func (b *existingRuleBuilder[TReference, TMetadata]) addTestSuiteAttrs(testSuite model_core.Message[*model_starlark_pb.TestSuite, TReference]) error {
	if err := b.set("kind", starlark.String("test_suite")); err != nil {
		return err
	}

	// Test suites store their tags in a decomposed form.
	// Reconstruct them.
	var tags starlark.Tuple
	for _, tag := range testSuite.Message.PositiveTags {
		tags = append(tags, starlark.String(tag))
	}
	for _, tag := range testSuite.Message.NegativeTags {
		tags = append(tags, starlark.String("-"+tag))
	}
	if len(testSuite.Message.Sizes) < len(testSizeNames) {
		for _, size := range testSuite.Message.Sizes {
			tags = append(tags, starlark.String(testSizeNames[size]))
		}
	}
	if testSuite.Message.Manual {
		tags = append(tags, starlark.String("manual"))
	}
	if err := b.set("tags", tags); err != nil {
		return err
	}

	var tests []string
	var errIter error
	for entry := range btree.AllLeaves(
		b.options.Context,
		b.options.Readers.TestSuiteTest,
		model_core.Nested(testSuite, testSuite.Message.Tests),
		func(entry model_core.Message[*model_starlark_pb.TestSuite_Test, TReference]) (*model_core_pb.DecodableReference, error) {
			return entry.Message.GetParent().GetReference(), nil
		},
		&errIter,
	) {
		test, ok := entry.Message.Level.(*model_starlark_pb.TestSuite_Test_Leaf)
		if !ok {
			return errors.New("not a valid leaf entry")
		}
		tests = append(tests, test.Leaf)
	}
	if errIter != nil {
		return errIter
	}
	testsTuple, err := b.labelStringsToTuple(tests)
	if err != nil {
		return fmt.Errorf("attr \"tests\": %w", err)
	}
	return b.set("tests", testsTuple)
}
