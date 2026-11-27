package render

import (
	"slices"
	"strings"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/storage/object"
)

// starlarkTargetArgument is a keyword argument of a call to a rule or
// macro in a BUILD file.
type starlarkTargetArgument struct {
	name  string
	value Doc
}

// starlarkTargetCall renders a call to a rule or macro in a BUILD file.
// As opposed to other function calls, each argument is always placed on
// a separate line, followed by a comma. The name of the target is
// placed first, followed by all other arguments in alphabetical order.
func starlarkTargetCall(callee Doc, name string, arguments []starlarkTargetArgument) Doc {
	slices.SortFunc(arguments, func(a, b starlarkTargetArgument) int {
		return strings.Compare(a.name, b.name)
	})
	parts := make([]Doc, 0, 3*len(arguments)+3)
	parts = append(parts, Line(), starlarkKeywordArgument("name", starlarkString(name)), Text(","))
	for _, argument := range arguments {
		parts = append(parts, Line(), starlarkKeywordArgument(argument.name, argument.value), Text(","))
	}
	return Concat(
		callee,
		Text("("),
		Nest(Concat(parts...)),
		Line(),
		Text(")"),
	)
}

// renderSelectGroups renders the value of an attribute of a target,
// consisting of a sequence of select() expressions whose values are
// concatenated.
func (sr *StarlarkRenderer) renderSelectGroups(groups model_core.Message[[]*model_starlark_pb.Select_Group, object.LocalReference]) Doc {
	return sr.renderSelect(model_core.Nested(groups, model_starlark_pb.Select_builder{Groups: groups.Message}.Build()))
}

// isEmptyListAttrValue returns true if the value of an attribute
// consists exclusively of empty lists that are not wrapped in a
// select() expression. This is used to omit attributes like
// target_compatible_with, which are always set, but are empty in the
// common case.
func isEmptyListAttrValue(groups []*model_starlark_pb.Select_Group) bool {
	for _, group := range groups {
		if len(group.GetConditions()) > 0 {
			return false
		}
		noMatch := group.GetNoMatchValue()
		if noMatch == nil {
			return false
		}
		list := noMatch.GetList()
		if list == nil || len(list.GetElements()) > 0 {
			return false
		}
	}
	return true
}

// isIntAttrValue returns true if the value of an attribute consists of
// a single int that is not wrapped in a select() expression, having a
// given value. This is used to omit attributes like shard_count, which
// are always set, but have a default value in the common case.
func isIntAttrValue(groups []*model_starlark_pb.Select_Group, expected int64) bool {
	if len(groups) != 1 || len(groups[0].GetConditions()) > 0 {
		return false
	}
	noMatch := groups[0].GetNoMatchValue()
	if noMatch == nil {
		return false
	}
	if !noMatch.HasInt() {
		return false
	}
	value := model_starlark.DecodeInt(noMatch.GetInt())
	return value.IsInt64() && value.Int64() == expected
}

// renderRuleTargetArguments renders the arguments of a call to a rule
// whose values have been provided explicitly. As rule targets only
// store the values of public attributes, their names need to be
// provided separately, in the order in which they are declared by the
// rule.
func (sr *StarlarkRenderer) renderRuleTargetArguments(ruleTarget model_core.Message[*model_starlark_pb.RuleTarget, object.LocalReference], publicAttrNames []string) []starlarkTargetArgument {
	publicAttrValues := ruleTarget.Message.GetPublicAttrValues()
	if len(publicAttrValues) != len(publicAttrNames) {
		return []starlarkTargetArgument{{
			name:  "public_attrs",
			value: starlarkErrorf("Rule target has %d public attr values, while %d public attr names were provided", len(publicAttrValues), len(publicAttrNames)),
		}}
	}

	var arguments []starlarkTargetArgument
	for i, publicAttrValue := range publicAttrValues {
		valueParts := publicAttrValue.GetValueParts()
		if len(valueParts) == 1 && len(valueParts[0].GetConditions()) == 0 {
			if noMatch := valueParts[0].GetNoMatchValue(); noMatch != nil {
				if noMatch.GetNone() != nil {
					// No value provided, meaning the
					// default value is used.
					continue
				}
			}
		}
		arguments = append(arguments, starlarkTargetArgument{
			name:  publicAttrNames[i],
			value: sr.renderSelectGroups(model_core.Nested(ruleTarget, valueParts)),
		})
	}

	// Attributes that are common to all rules.
	if l := ruleTarget.Message.GetExecCompatibleWith(); len(l) > 0 {
		arguments = append(arguments, starlarkTargetArgument{
			name:  "exec_compatible_with",
			value: starlarkStringList(l),
		})
	}
	if l := ruleTarget.Message.GetTags(); len(l) > 0 {
		arguments = append(arguments, starlarkTargetArgument{
			name:  "tags",
			value: starlarkStringList(l),
		})
	}
	if l := ruleTarget.Message.GetTargetCompatibleWith(); !isEmptyListAttrValue(l) {
		arguments = append(arguments, starlarkTargetArgument{
			name:  "target_compatible_with",
			value: sr.renderSelectGroups(model_core.Nested(ruleTarget, l)),
		})
	}
	if v := ruleTarget.Message.GetBuildSetting().GetDefault(); v != nil {
		arguments = append(arguments, starlarkTargetArgument{
			name:  "build_setting_default",
			value: sr.renderValue(model_core.Nested(ruleTarget, v)),
		})
	}
	if v := ruleTarget.Message.GetBuildSetting().GetOnLeaveScope(); v != nil {
		arguments = append(arguments, starlarkTargetArgument{
			name:  "on_leave_scope",
			value: sr.renderValue(model_core.Nested(ruleTarget, v)),
		})
	}
	if v := ruleTarget.Message.GetBuildSetting(); v != nil && !isDefaultBuildSettingScope(v.GetScope()) {
		arguments = append(arguments, starlarkTargetArgument{
			name:  "scope",
			value: renderBuildSettingScope(v.GetScope()),
		})
	}

	// Attributes that are common to all tests. Only render the ones
	// whose values differ from the defaults.
	if test := ruleTarget.Message.GetTest(); test != nil {
		if l := test.GetArgs(); !isEmptyListAttrValue(l) {
			arguments = append(arguments, starlarkTargetArgument{
				name:  "args",
				value: sr.renderSelectGroups(model_core.Nested(ruleTarget, l)),
			})
		}
		if test.GetFlaky() {
			arguments = append(arguments, starlarkTargetArgument{
				name:  "flaky",
				value: starlarkBool(true),
			})
		}
		if test.GetLocal() {
			arguments = append(arguments, starlarkTargetArgument{
				name:  "local",
				value: starlarkBool(true),
			})
		}
		if l := test.GetShardCount(); !isIntAttrValue(l, 1) {
			arguments = append(arguments, starlarkTargetArgument{
				name:  "shard_count",
				value: sr.renderSelectGroups(model_core.Nested(ruleTarget, l)),
			})
		}
		if test.GetSize() != model_starlark_pb.TestSize_MEDIUM {
			arguments = append(arguments, starlarkTargetArgument{
				name:  "size",
				value: starlarkString(model_starlark.FormatTestSize(test.GetSize())),
			})
		}
		if test.GetTimeout() != model_starlark.GetDefaultTestTimeout(test.GetSize()) {
			arguments = append(arguments, starlarkTargetArgument{
				name:  "timeout",
				value: starlarkString(model_starlark.FormatTestTimeout(test.GetTimeout())),
			})
		}
	}
	return arguments
}

// Target renders the definition of a target in the form in which it
// would be declared in a BUILD file, as done by "bazel query
// --output=build". Only attributes whose values have been provided
// explicitly are rendered. Targets that are not declared by calling a
// function, such as source files, are rendered as comments.
//
// For rule targets, the names of the public attributes of the rule
// need to be provided, in the order in which they are declared by the
// rule.
func (sr *StarlarkRenderer) Target(targetLabel label.CanonicalLabel, definition model_core.Message[*model_starlark_pb.Target_Definition, object.LocalReference], publicAttrNames []string) Doc {
	name := targetLabel.GetTargetName().String()
	switch definition.Message.WhichKind() {
	case model_starlark_pb.Target_Definition_Alias_case:
		return starlarkTargetCall(
			starlarkCallee("alias", ""),
			name,
			[]starlarkTargetArgument{{
				name:  "actual",
				value: sr.renderSelectGroup(model_core.Nested(definition, definition.Message.GetAlias().GetActual())),
			}},
		)
	case model_starlark_pb.Target_Definition_LabelSetting_case:
		callee := "label_setting"
		if definition.Message.GetLabelSetting().GetFlag() {
			callee = "label_flag"
		}
		var arguments []starlarkTargetArgument
		if l := definition.Message.GetLabelSetting().GetBuildSettingDefault(); l != "" {
			arguments = append(arguments, starlarkTargetArgument{
				name:  "build_setting_default",
				value: starlarkString(l),
			})
		}
		if l := definition.Message.GetLabelSetting().GetOnLeaveScope(); l != "" {
			arguments = append(arguments, starlarkTargetArgument{
				name:  "on_leave_scope",
				value: starlarkString(l),
			})
		}
		if v := definition.Message.GetLabelSetting().GetScope(); !isDefaultBuildSettingScope(v) {
			arguments = append(arguments, starlarkTargetArgument{
				name:  "scope",
				value: renderBuildSettingScope(v),
			})
		}
		return starlarkTargetCall(starlarkCallee(callee, ""), name, arguments)
	case model_starlark_pb.Target_Definition_PackageGroup_case:
		var arguments []starlarkTargetArgument
		if l := definition.Message.GetPackageGroup().GetIncludePackageGroups(); len(l) > 0 {
			arguments = append(arguments, starlarkTargetArgument{
				name:  "includes",
				value: starlarkStringList(l),
			})
		}
		return starlarkTargetCall(starlarkCallee("package_group", ""), name, arguments)
	case model_starlark_pb.Target_Definition_PredeclaredOutputFileTarget_case:
		return Styled(RoleDim, "# "+targetLabel.String()+" (generated file)")
	case model_starlark_pb.Target_Definition_RuleTarget_case:
		ruleIdentifier := definition.Message.GetRuleTarget().GetRuleIdentifier()
		return starlarkTargetCall(
			starlarkCallee(starlarkIdentifierName(ruleIdentifier), ruleIdentifier),
			name,
			sr.renderRuleTargetArguments(model_core.Nested(definition, definition.Message.GetRuleTarget()), publicAttrNames),
		)
	case model_starlark_pb.Target_Definition_SourceFileTarget_case:
		return Styled(RoleDim, "# "+targetLabel.String()+" (source file)")
	case model_starlark_pb.Target_Definition_TestSuite_case:
		var arguments []starlarkTargetArgument
		if l := model_starlark.GetTestSuiteTags(definition.Message.GetTestSuite()); len(l) > 0 {
			arguments = append(arguments, starlarkTargetArgument{
				name:  "tags",
				value: starlarkStringList(l),
			})
		}
		if l := definition.Message.GetTestSuite().GetTests(); len(l) > 0 {
			arguments = append(arguments, starlarkTargetArgument{
				name:  "tests",
				value: sr.renderTestSuiteTests(model_core.Nested(definition, l)),
			})
		}
		return starlarkTargetCall(starlarkCallee("test_suite", ""), name, arguments)
	default:
		return starlarkErrorf("Unknown target kind")
	}
}

// renderBuildSettingScope renders the value of the "scope" attribute
// of a build setting.
func renderBuildSettingScope(scope *model_starlark_pb.BuildSettingScope) Doc {
	scopeStr, err := model_starlark.FormatBuildSettingScope(scope)
	if err != nil {
		return starlarkErrorf("%s", err)
	}
	return starlarkString(scopeStr)
}

// isDefaultBuildSettingScope returns true if the scope of a build
// setting is equal to the default value of the "scope" attribute, and
// can therefore be omitted.
func isDefaultBuildSettingScope(scope *model_starlark_pb.BuildSettingScope) bool {
	return scope.GetTarget() != nil
}
