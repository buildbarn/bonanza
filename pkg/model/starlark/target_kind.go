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
	switch kind := definition.GetKind().(type) {
	case *model_starlark_pb.Target_Definition_Alias:
		return "alias rule", nil
	case *model_starlark_pb.Target_Definition_LabelSetting:
		if kind.LabelSetting.Flag {
			return "label_flag rule", nil
		}
		return "label_setting rule", nil
	case *model_starlark_pb.Target_Definition_PackageGroup:
		return "package group", nil
	case *model_starlark_pb.Target_Definition_PredeclaredOutputFileTarget:
		return "generated file", nil
	case *model_starlark_pb.Target_Definition_RuleTarget:
		ruleIdentifier, err := label.NewCanonicalStarlarkIdentifier(kind.RuleTarget.RuleIdentifier)
		if err != nil {
			return "", fmt.Errorf("invalid rule identifier %#v: %w", kind.RuleTarget.RuleIdentifier, err)
		}
		return ruleIdentifier.GetStarlarkIdentifier().String() + " rule", nil
	case *model_starlark_pb.Target_Definition_SourceFileTarget:
		return "source file", nil
	default:
		return "", errors.New("unknown target kind")
	}
}
