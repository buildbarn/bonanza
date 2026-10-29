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
	return sr.renderSelect(model_core.Nested(groups, &model_starlark_pb.Select{Groups: groups.Message}))
}

// isEmptyListAttrValue returns true if the value of an attribute
// consists exclusively of empty lists that are not wrapped in a
// select() expression. This is used to omit attributes like
// target_compatible_with, which are always set, but are empty in the
// common case.
func isEmptyListAttrValue(groups []*model_starlark_pb.Select_Group) bool {
	for _, group := range groups {
		if len(group.Conditions) > 0 {
			return false
		}
		noMatch, ok := group.NoMatch.(*model_starlark_pb.Select_Group_NoMatchValue)
		if !ok {
			return false
		}
		list, ok := noMatch.NoMatchValue.GetKind().(*model_starlark_pb.Value_List)
		if !ok || len(list.List.Elements) > 0 {
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
	if len(groups) != 1 || len(groups[0].Conditions) > 0 {
		return false
	}
	noMatch, ok := groups[0].NoMatch.(*model_starlark_pb.Select_Group_NoMatchValue)
	if !ok {
		return false
	}
	i, ok := noMatch.NoMatchValue.GetKind().(*model_starlark_pb.Value_Int)
	if !ok {
		return false
	}
	value := model_starlark.DecodeInt(i.Int)
	return value.IsInt64() && value.Int64() == expected
}

// renderRuleTargetArguments renders the arguments of a call to a rule
// whose values have been provided explicitly. As rule targets only
// store the values of public attributes, their names need to be
// provided separately, in the order in which they are declared by the
// rule.
func (sr *StarlarkRenderer) renderRuleTargetArguments(ruleTarget model_core.Message[*model_starlark_pb.RuleTarget, object.LocalReference], publicAttrNames []string) []starlarkTargetArgument {
	publicAttrValues := ruleTarget.Message.PublicAttrValues
	if len(publicAttrValues) != len(publicAttrNames) {
		return []starlarkTargetArgument{{
			name:  "public_attrs",
			value: starlarkErrorf("Rule target has %d public attr values, while %d public attr names were provided", len(publicAttrValues), len(publicAttrNames)),
		}}
	}

	var arguments []starlarkTargetArgument
	for i, publicAttrValue := range publicAttrValues {
		valueParts := publicAttrValue.ValueParts
		if len(valueParts) == 1 && len(valueParts[0].Conditions) == 0 {
			if noMatch, ok := valueParts[0].NoMatch.(*model_starlark_pb.Select_Group_NoMatchValue); ok {
				if _, ok := noMatch.NoMatchValue.GetKind().(*model_starlark_pb.Value_None); ok {
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
	if l := ruleTarget.Message.ExecCompatibleWith; len(l) > 0 {
		arguments = append(arguments, starlarkTargetArgument{
			name:  "exec_compatible_with",
			value: starlarkStringList(l),
		})
	}
	if l := ruleTarget.Message.Tags; len(l) > 0 {
		arguments = append(arguments, starlarkTargetArgument{
			name:  "tags",
			value: starlarkStringList(l),
		})
	}
	if l := ruleTarget.Message.TargetCompatibleWith; !isEmptyListAttrValue(l) {
		arguments = append(arguments, starlarkTargetArgument{
			name:  "target_compatible_with",
			value: sr.renderSelectGroups(model_core.Nested(ruleTarget, l)),
		})
	}
	if v := ruleTarget.Message.BuildSettingDefault; v != nil {
		arguments = append(arguments, starlarkTargetArgument{
			name:  "build_setting_default",
			value: sr.renderValue(model_core.Nested(ruleTarget, v)),
		})
	}

	// Attributes that are common to all tests. Only render the ones
	// whose values differ from the defaults.
	if test := ruleTarget.Message.Test; test != nil {
		if l := test.Args; !isEmptyListAttrValue(l) {
			arguments = append(arguments, starlarkTargetArgument{
				name:  "args",
				value: sr.renderSelectGroups(model_core.Nested(ruleTarget, l)),
			})
		}
		if test.Flaky {
			arguments = append(arguments, starlarkTargetArgument{
				name:  "flaky",
				value: starlarkBool(true),
			})
		}
		if test.Local {
			arguments = append(arguments, starlarkTargetArgument{
				name:  "local",
				value: starlarkBool(true),
			})
		}
		if l := test.ShardCount; !isIntAttrValue(l, 1) {
			arguments = append(arguments, starlarkTargetArgument{
				name:  "shard_count",
				value: sr.renderSelectGroups(model_core.Nested(ruleTarget, l)),
			})
		}
		if test.Size != model_starlark_pb.TestSize_MEDIUM {
			arguments = append(arguments, starlarkTargetArgument{
				name:  "size",
				value: starlarkString(model_starlark.FormatTestSize(test.Size)),
			})
		}
		if test.Timeout != model_starlark.GetDefaultTestTimeout(test.Size) {
			arguments = append(arguments, starlarkTargetArgument{
				name:  "timeout",
				value: starlarkString(model_starlark.FormatTestTimeout(test.Timeout)),
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
	switch kind := definition.Message.GetKind().(type) {
	case *model_starlark_pb.Target_Definition_Alias:
		return starlarkTargetCall(
			starlarkCallee("alias", ""),
			name,
			[]starlarkTargetArgument{{
				name:  "actual",
				value: sr.renderSelectGroup(model_core.Nested(definition, kind.Alias.Actual)),
			}},
		)
	case *model_starlark_pb.Target_Definition_LabelSetting:
		callee := "label_setting"
		if kind.LabelSetting.Flag {
			callee = "label_flag"
		}
		var arguments []starlarkTargetArgument
		if l := kind.LabelSetting.BuildSettingDefault; l != "" {
			arguments = append(arguments, starlarkTargetArgument{
				name:  "build_setting_default",
				value: starlarkString(l),
			})
		}
		return starlarkTargetCall(starlarkCallee(callee, ""), name, arguments)
	case *model_starlark_pb.Target_Definition_PackageGroup:
		var arguments []starlarkTargetArgument
		if l := kind.PackageGroup.IncludePackageGroups; len(l) > 0 {
			arguments = append(arguments, starlarkTargetArgument{
				name:  "includes",
				value: starlarkStringList(l),
			})
		}
		return starlarkTargetCall(starlarkCallee("package_group", ""), name, arguments)
	case *model_starlark_pb.Target_Definition_PredeclaredOutputFileTarget:
		return Styled(RoleDim, "# "+targetLabel.String()+" (generated file)")
	case *model_starlark_pb.Target_Definition_RuleTarget:
		ruleIdentifier := kind.RuleTarget.RuleIdentifier
		return starlarkTargetCall(
			starlarkCallee(starlarkIdentifierName(ruleIdentifier), ruleIdentifier),
			name,
			sr.renderRuleTargetArguments(model_core.Nested(definition, kind.RuleTarget), publicAttrNames),
		)
	case *model_starlark_pb.Target_Definition_SourceFileTarget:
		return Styled(RoleDim, "# "+targetLabel.String()+" (source file)")
	default:
		return starlarkErrorf("Unknown target kind")
	}
}
