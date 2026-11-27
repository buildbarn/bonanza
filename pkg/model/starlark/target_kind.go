package starlark

import (
	"errors"
	"fmt"

	"bonanza.build/pkg/label"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
)

// GetTargetKind returns the kind of a target, as displayed by "bazel
// query --output=label_kind" and matched by the kind() query function.
// Examples include "cc_library rule", "source file" and "generated
// file".
func GetTargetKind(definition *model_starlark_pb.Target_Definition) (string, error) {
	switch definition.WhichKind() {
	case model_starlark_pb.Target_Definition_Alias_case:
		return "alias rule", nil
	case model_starlark_pb.Target_Definition_LabelSetting_case:
		if definition.GetLabelSetting().GetFlag() {
			return "label_flag rule", nil
		}
		return "label_setting rule", nil
	case model_starlark_pb.Target_Definition_PackageGroup_case:
		return "package group", nil
	case model_starlark_pb.Target_Definition_PredeclaredOutputFileTarget_case:
		return "generated file", nil
	case model_starlark_pb.Target_Definition_RuleTarget_case:
		ruleIdentifier, err := label.NewCanonicalStarlarkIdentifier(definition.GetRuleTarget().GetRuleIdentifier())
		if err != nil {
			return "", fmt.Errorf("invalid rule identifier %#v: %w", definition.GetRuleTarget().GetRuleIdentifier(), err)
		}
		return ruleIdentifier.GetStarlarkIdentifier().String() + " rule", nil
	case model_starlark_pb.Target_Definition_SourceFileTarget_case:
		return "source file", nil
	case model_starlark_pb.Target_Definition_TestSuite_case:
		return "test_suite rule", nil
	default:
		return "", errors.New("unknown target kind")
	}
}
