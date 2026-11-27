package starlark_test

import (
	"testing"

	model_starlark "bonanza.build/pkg/model/starlark"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"

	"github.com/stretchr/testify/require"
)

func TestGetTargetKind(t *testing.T) {
	for kind, definition := range map[string]*model_starlark_pb.Target_Definition{
		"alias rule": model_starlark_pb.Target_Definition_builder{
			Alias: &model_starlark_pb.Alias{},
		}.Build(),
		"label_flag rule": model_starlark_pb.Target_Definition_builder{
			LabelSetting: model_starlark_pb.LabelSetting_builder{Flag: true}.Build(),
		}.Build(),
		"label_setting rule": model_starlark_pb.Target_Definition_builder{
			LabelSetting: &model_starlark_pb.LabelSetting{},
		}.Build(),
		"package group": model_starlark_pb.Target_Definition_builder{
			PackageGroup: &model_starlark_pb.PackageGroup{},
		}.Build(),
		"generated file": model_starlark_pb.Target_Definition_builder{
			PredeclaredOutputFileTarget: &model_starlark_pb.PredeclaredOutputFileTarget{},
		}.Build(),
		"cc_library rule": model_starlark_pb.Target_Definition_builder{
			RuleTarget: model_starlark_pb.RuleTarget_builder{
				RuleIdentifier: "@@rules_cc+//cc:defs.bzl%cc_library",
			}.Build(),
		}.Build(),
		"source file": model_starlark_pb.Target_Definition_builder{
			SourceFileTarget: &model_starlark_pb.SourceFileTarget{},
		}.Build(),
		"test_suite rule": model_starlark_pb.Target_Definition_builder{
			TestSuite: &model_starlark_pb.TestSuite{},
		}.Build(),
	} {
		t.Run(kind, func(t *testing.T) {
			got, err := model_starlark.GetTargetKind(definition)
			require.NoError(t, err)
			require.Equal(t, kind, got)
		})
	}

	t.Run("InvalidRuleIdentifier", func(t *testing.T) {
		_, err := model_starlark.GetTargetKind(model_starlark_pb.Target_Definition_builder{
			RuleTarget: model_starlark_pb.RuleTarget_builder{
				RuleIdentifier: "cc_library",
			}.Build(),
		}.Build())
		require.ErrorContains(t, err, "invalid rule identifier \"cc_library\": ")
	})
}
