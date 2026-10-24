package starlark_test

import (
	"testing"

	model_starlark "bonanza.build/pkg/model/starlark"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"

	"github.com/stretchr/testify/require"
)

func TestGetTargetKind(t *testing.T) {
	for kind, definition := range map[string]*model_starlark_pb.Target_Definition{
		"alias rule": {
			Kind: &model_starlark_pb.Target_Definition_Alias{
				Alias: &model_starlark_pb.Alias{},
			},
		},
		"label_flag rule": {
			Kind: &model_starlark_pb.Target_Definition_LabelSetting{
				LabelSetting: &model_starlark_pb.LabelSetting{Flag: true},
			},
		},
		"label_setting rule": {
			Kind: &model_starlark_pb.Target_Definition_LabelSetting{
				LabelSetting: &model_starlark_pb.LabelSetting{},
			},
		},
		"package group": {
			Kind: &model_starlark_pb.Target_Definition_PackageGroup{
				PackageGroup: &model_starlark_pb.PackageGroup{},
			},
		},
		"generated file": {
			Kind: &model_starlark_pb.Target_Definition_PredeclaredOutputFileTarget{
				PredeclaredOutputFileTarget: &model_starlark_pb.PredeclaredOutputFileTarget{},
			},
		},
		"cc_library rule": {
			Kind: &model_starlark_pb.Target_Definition_RuleTarget{
				RuleTarget: &model_starlark_pb.RuleTarget{
					RuleIdentifier: "@@rules_cc+//cc:defs.bzl%cc_library",
				},
			},
		},
		"source file": {
			Kind: &model_starlark_pb.Target_Definition_SourceFileTarget{
				SourceFileTarget: &model_starlark_pb.SourceFileTarget{},
			},
		},
	} {
		t.Run(kind, func(t *testing.T) {
			got, err := model_starlark.GetTargetKind(definition)
			require.NoError(t, err)
			require.Equal(t, kind, got)
		})
	}

	t.Run("InvalidRuleIdentifier", func(t *testing.T) {
		_, err := model_starlark.GetTargetKind(&model_starlark_pb.Target_Definition{
			Kind: &model_starlark_pb.Target_Definition_RuleTarget{
				RuleTarget: &model_starlark_pb.RuleTarget{
					RuleIdentifier: "cc_library",
				},
			},
		})
		require.ErrorContains(t, err, "invalid rule identifier \"cc_library\": ")
	})
}
