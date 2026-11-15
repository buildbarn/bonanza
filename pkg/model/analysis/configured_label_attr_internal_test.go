package analysis

import (
	"testing"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"

	"github.com/stretchr/testify/require"
)

// fakeTargetValueGetter returns the definitions of targets from a map.
// Targets that are not present in the map are reported as missing.
type fakeTargetValueGetter map[string]*model_starlark_pb.Target_Definition

func (g fakeTargetValueGetter) GetTargetValue(key *model_analysis_pb.Target_Key) model_core.Message[*model_analysis_pb.Target_Value, struct{}] {
	definition, ok := g[key.Label]
	if !ok {
		return model_core.Message[*model_analysis_pb.Target_Value, struct{}]{}
	}
	return model_core.NewSimpleMessage[struct{}](&model_analysis_pb.Target_Value{
		Definition: definition,
	})
}

func TestCheckRequiredProviders(t *testing.T) {
	const (
		ccInfo     = "@@rules_cc+//cc:defs.bzl%CcInfo"
		javaInfo   = "@@rules_java+//java:defs.bzl%JavaInfo"
		pyInfo     = "@@rules_python+//python:defs.bzl%PyInfo"
		ruleTarget = "@@main+//:rule"
		sourceFile = "@@main+//:file.txt"
	)
	e := fakeTargetValueGetter{
		ruleTarget: {
			Kind: &model_starlark_pb.Target_Definition_RuleTarget{
				RuleTarget: &model_starlark_pb.RuleTarget{},
			},
		},
		sourceFile: {
			Kind: &model_starlark_pb.Target_Definition_SourceFileTarget{
				SourceFileTarget: &model_starlark_pb.SourceFileTarget{},
			},
		},
	}
	newProviderInstances := func(providerIdentifiers ...string) []*model_starlark_pb.Struct {
		providerInstances := make([]*model_starlark_pb.Struct, 0, len(providerIdentifiers))
		for _, providerIdentifier := range providerIdentifiers {
			providerInstances = append(providerInstances, &model_starlark_pb.Struct{
				ProviderInstanceProperties: &model_starlark_pb.Provider_InstanceProperties{
					ProviderIdentifier: providerIdentifier,
				},
			})
		}
		return providerInstances
	}
	newProviderSet := func(providerIdentifiers ...string) *model_starlark_pb.ProviderSet {
		return &model_starlark_pb.ProviderSet{ProviderIdentifiers: providerIdentifiers}
	}

	t.Run("EmptySet", func(t *testing.T) {
		// An empty set is satisfied by any target. The target
		// does not need to be loaded.
		require.NoError(t, checkRequiredProviders(e, "@@main+//:missing", nil, []*model_starlark_pb.ProviderSet{newProviderSet()}))
	})

	t.Run("NoSets", func(t *testing.T) {
		// If no sets are provided, no rule targets are
		// accepted. File targets are still accepted.
		require.EqualError(
			t,
			checkRequiredProviders(e, ruleTarget, newProviderInstances(ccInfo), nil),
			"target \"@@main+//:rule\" does not have all mandatory providers of any of the sets ",
		)
		require.NoError(t, checkRequiredProviders(e, sourceFile, nil, nil))
	})

	t.Run("MissingTarget", func(t *testing.T) {
		require.Equal(
			t,
			evaluation.ErrMissingDependency,
			checkRequiredProviders(e, "@@main+//:missing", nil, []*model_starlark_pb.ProviderSet{newProviderSet(ccInfo)}),
		)
	})

	t.Run("FileTarget", func(t *testing.T) {
		// File targets are accepted, regardless of the
		// providers they yield.
		require.NoError(t, checkRequiredProviders(e, sourceFile, nil, []*model_starlark_pb.ProviderSet{newProviderSet(ccInfo)}))
	})

	t.Run("SingleSetSatisfied", func(t *testing.T) {
		// If the providers match, the target does not need to
		// be loaded.
		require.NoError(t, checkRequiredProviders(
			e,
			"@@main+//:missing",
			newProviderInstances(ccInfo, javaInfo, pyInfo),
			[]*model_starlark_pb.ProviderSet{newProviderSet(ccInfo, pyInfo)},
		))
	})

	t.Run("SingleSetUnsatisfied", func(t *testing.T) {
		require.EqualError(
			t,
			checkRequiredProviders(
				e,
				ruleTarget,
				newProviderInstances(ccInfo),
				[]*model_starlark_pb.ProviderSet{newProviderSet(ccInfo, javaInfo, pyInfo)},
			),
			"target \"@@main+//:rule\" does not have all mandatory providers of any of the sets [@@rules_cc+//cc:defs.bzl%CcInfo, @@rules_java+//java:defs.bzl%JavaInfo, @@rules_python+//python:defs.bzl%PyInfo]",
		)
	})

	t.Run("MultipleSetsSatisfied", func(t *testing.T) {
		// It is sufficient if one of the sets is satisfied.
		require.NoError(t, checkRequiredProviders(
			e,
			"@@main+//:missing",
			newProviderInstances(javaInfo),
			[]*model_starlark_pb.ProviderSet{newProviderSet(ccInfo), newProviderSet(javaInfo)},
		))
		require.NoError(t, checkRequiredProviders(
			e,
			"@@main+//:missing",
			newProviderInstances(ccInfo, javaInfo, pyInfo),
			[]*model_starlark_pb.ProviderSet{newProviderSet(ccInfo, javaInfo), newProviderSet(ccInfo, pyInfo)},
		))
	})

	t.Run("MultipleSetsInterleaved", func(t *testing.T) {
		// The first set fails on a provider that sorts after a
		// provider of the second set, which is still matched.
		require.NoError(t, checkRequiredProviders(
			e,
			"@@main+//:missing",
			newProviderInstances(ccInfo, pyInfo),
			[]*model_starlark_pb.ProviderSet{newProviderSet(ccInfo, javaInfo), newProviderSet(pyInfo)},
		))
	})

	t.Run("RequiredProviderAfterAllInstances", func(t *testing.T) {
		// A required provider that sorts after all provider
		// instances is missing.
		require.EqualError(
			t,
			checkRequiredProviders(
				e,
				ruleTarget,
				newProviderInstances(ccInfo),
				[]*model_starlark_pb.ProviderSet{newProviderSet(ccInfo, pyInfo)},
			),
			"target \"@@main+//:rule\" does not have all mandatory providers of any of the sets [@@rules_cc+//cc:defs.bzl%CcInfo, @@rules_python+//python:defs.bzl%PyInfo]",
		)
	})

	t.Run("MultipleSetsUnsatisfied", func(t *testing.T) {
		require.EqualError(
			t,
			checkRequiredProviders(
				e,
				ruleTarget,
				newProviderInstances(pyInfo),
				[]*model_starlark_pb.ProviderSet{newProviderSet(ccInfo), newProviderSet(javaInfo)},
			),
			"target \"@@main+//:rule\" does not have all mandatory providers of any of the sets [@@rules_cc+//cc:defs.bzl%CcInfo], [@@rules_java+//java:defs.bzl%JavaInfo]",
		)
	})
}
