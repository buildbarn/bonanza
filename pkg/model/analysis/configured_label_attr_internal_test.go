package analysis

import (
	"testing"

	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"

	"github.com/stretchr/testify/require"
)

func TestCheckRequiredProviders(t *testing.T) {
	const (
		ccInfo     = "@@rules_cc+//cc:defs.bzl%CcInfo"
		javaInfo   = "@@rules_java+//java:defs.bzl%JavaInfo"
		pyInfo     = "@@rules_python+//python:defs.bzl%PyInfo"
		ruleTarget = "@@main+//:rule"
	)
	newProviderInstances := func(providerIdentifiers ...string) []*model_starlark_pb.Struct {
		providerInstances := make([]*model_starlark_pb.Struct, 0, len(providerIdentifiers))
		for _, providerIdentifier := range providerIdentifiers {
			providerInstances = append(providerInstances, model_starlark_pb.Struct_builder{
				ProviderInstanceProperties: model_starlark_pb.Provider_InstanceProperties_builder{
					ProviderIdentifier: providerIdentifier,
				}.Build(),
			}.Build())
		}
		return providerInstances
	}
	newProviderSet := func(providerIdentifiers ...string) *model_starlark_pb.ProviderSet {
		return model_starlark_pb.ProviderSet_builder{ProviderIdentifiers: providerIdentifiers}.Build()
	}

	t.Run("EmptySet", func(t *testing.T) {
		// An empty set is satisfied by any target.
		require.NoError(t, checkRequiredProviders(ruleTarget, nil, []*model_starlark_pb.ProviderSet{newProviderSet()}))
	})

	t.Run("NoSets", func(t *testing.T) {
		// If no sets are provided, no rule targets are
		// accepted.
		require.EqualError(
			t,
			checkRequiredProviders(ruleTarget, newProviderInstances(ccInfo), nil),
			"target \"@@main+//:rule\" does not have all mandatory providers of any of the sets ",
		)
	})

	t.Run("SingleSetSatisfied", func(t *testing.T) {
		require.NoError(t, checkRequiredProviders(
			ruleTarget,
			newProviderInstances(ccInfo, javaInfo, pyInfo),
			[]*model_starlark_pb.ProviderSet{newProviderSet(ccInfo, pyInfo)},
		))
	})

	t.Run("SingleSetUnsatisfied", func(t *testing.T) {
		require.EqualError(
			t,
			checkRequiredProviders(
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
			ruleTarget,
			newProviderInstances(javaInfo),
			[]*model_starlark_pb.ProviderSet{newProviderSet(ccInfo), newProviderSet(javaInfo)},
		))
		require.NoError(t, checkRequiredProviders(
			ruleTarget,
			newProviderInstances(ccInfo, javaInfo, pyInfo),
			[]*model_starlark_pb.ProviderSet{newProviderSet(ccInfo, javaInfo), newProviderSet(ccInfo, pyInfo)},
		))
	})

	t.Run("MultipleSetsInterleaved", func(t *testing.T) {
		// The first set fails on a provider that sorts after a
		// provider of the second set, which is still matched.
		require.NoError(t, checkRequiredProviders(
			ruleTarget,
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
				ruleTarget,
				newProviderInstances(pyInfo),
				[]*model_starlark_pb.ProviderSet{newProviderSet(ccInfo), newProviderSet(javaInfo)},
			),
			"target \"@@main+//:rule\" does not have all mandatory providers of any of the sets [@@rules_cc+//cc:defs.bzl%CcInfo], [@@rules_java+//java:defs.bzl%JavaInfo]",
		)
	})
}
