package starlark_test

import (
	"testing"

	"bonanza.build/pkg/label"
	"bonanza.build/pkg/model/chunking"
	model_core "bonanza.build/pkg/model/core"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_chunking_pb "bonanza.build/pkg/proto/model/chunking"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	object_pb "bonanza.build/pkg/proto/storage/object"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestAspect(t *testing.T) {
	referenceFormat := util.Must(object.NewReferenceFormat(object_pb.ReferenceFormat_SHA256_V1))
	valueEncodingOptions := &model_starlark.ValueEncodingOptions[object.LocalReference, model_core.CreatedObjectTree]{
		Context:               t.Context(),
		ObjectEncoder:         model_encoding.NewChainedDeterministicBinaryEncoder(nil),
		ObjectReferenceFormat: referenceFormat,
		ObjectChunkingParameters: util.Must(chunking.NewParametersFromProto(&model_chunking_pb.Parameters{
			MinimumSizeBytes: 32 * 1024,
			HorizonSizeBytes: 8 * 32 * 1024,
		}, referenceFormat)),
	}
	const preamble = "A = provider()\nB = provider()\nC = provider()\ndef _impl(target, ctx):\n    pass\n"
	exec := func(t *testing.T, code string) (map[string]eval.Value, error) {
		thread := newTestThread(t)
		thread.SetLocal(model_starlark.LabelResolverKey, NewMockLabelResolver(gomock.NewController(t)))
		return execBzlFile(
			thread,
			util.Must(label.NewCanonicalLabel("@@rules+//:defs.bzl")),
			preamble+code,
			nil,
		)
	}
	encodeAspect := func(t *testing.T, code string) *model_starlark_pb.Aspect_Definition {
		globals, err := exec(t, code+"\nASPECT = aspect(implementation = _impl, **ARGS)\n")
		require.NoError(t, err)
		identifier := util.Must(label.NewCanonicalStarlarkIdentifier("@@rules+//:defs.bzl%ASPECT"))
		encodedAspect, err := globals["ASPECT"].(model_starlark.EncodableValue[object.LocalReference, model_core.CreatedObjectTree]).EncodeValue(newTestThread(t), nil, &identifier, valueEncodingOptions)
		require.NoError(t, err)
		return encodedAspect.Message.GetAspect().GetDefinition()
	}
	attrNames := func(definition *model_starlark_pb.Aspect_Definition) []string {
		names := make([]string, 0, len(definition.Attrs))
		for _, namedAttr := range definition.Attrs {
			names = append(names, namedAttr.Name)
		}
		return names
	}

	t.Run("Defaults", func(t *testing.T) {
		definition := encodeAspect(t, "ARGS = {}")
		require.NotNil(t, definition.Implementation)
		require.Empty(t, definition.AttrAspects)

		// Aspects get the same private attrs as rules, except
		// for the public "toolchains" attr.
		require.Equal(t, []string{"__configuration", "__default_toolchains", "__target_platform"}, attrNames(definition))

		// Toolchains are placed in the default exec group,
		// which is always present.
		require.Len(t, definition.ExecGroups, 1)
		require.Equal(t, "", definition.ExecGroups[0].Name)

		// By default, aspects apply to all targets, but don't
		// see the providers of other aspects.
		require.Len(t, definition.RequiredProviders, 1)
		testutil.RequireEqualProto(t, &model_starlark_pb.ProviderSet{}, definition.RequiredProviders[0])
		require.Empty(t, definition.RequiredAspectProviders)
		require.Nil(t, definition.PropagationPredicate)
	})

	t.Run("AllFields", func(t *testing.T) {
		definition := encodeAspect(t, `
_dep = aspect(implementation = _impl)
def _predicate(ctx):
    return True
ARGS = {
    "apply_to_generating_rules": True,
    "attr_aspects": ["srcs", "deps", "srcs"],
    "attrs": {
        "flag": attr.bool(),
        "count": attr.int(),
        "mode": attr.string(values = ["a", "b"]),
        "_tool": attr.label(cfg = "exec"),
    },
    "fragments": ["cpp"],
    "propagation_predicate": _predicate,
    "provides": [B, A],
    "required_aspect_providers": [[C], [A, B]],
    "required_providers": [A],
    "requires": [_dep, _dep],
    "toolchains": ["//:toolchain_type"],
    "toolchains_aspects": ["//:z_type", "//:toolchain_type"],
}`)
		require.True(t, definition.ApplyToGeneratingRules)
		require.Equal(t, []string{"deps", "srcs"}, definition.AttrAspects)
		require.Equal(t, []string{"__configuration", "__default_toolchains", "__fragments", "__target_platform", "_tool", "count", "flag", "mode"}, attrNames(definition))
		require.NotNil(t, definition.PropagationPredicate)
		require.Equal(t, []string{"@@rules+//:defs.bzl%A", "@@rules+//:defs.bzl%B"}, definition.Provides)
		require.Len(t, definition.RequiredAspectProviders, 2)
		testutil.RequireEqualProto(t, &model_starlark_pb.ProviderSet{ProviderIdentifiers: []string{"@@rules+//:defs.bzl%A", "@@rules+//:defs.bzl%B"}}, definition.RequiredAspectProviders[0])
		testutil.RequireEqualProto(t, &model_starlark_pb.ProviderSet{ProviderIdentifiers: []string{"@@rules+//:defs.bzl%C"}}, definition.RequiredAspectProviders[1])
		require.Len(t, definition.RequiredProviders, 1)
		testutil.RequireEqualProto(t, &model_starlark_pb.ProviderSet{ProviderIdentifiers: []string{"@@rules+//:defs.bzl%A"}}, definition.RequiredProviders[0])
		require.Equal(t, []string{"@@rules+//:defs.bzl%_dep"}, definition.Requires)
		require.Equal(t, []string{"@@rules+//:toolchain_type", "@@rules+//:z_type"}, definition.ToolchainsAspects)
		require.Len(t, definition.ExecGroups, 1)
		require.Len(t, definition.ExecGroups[0].ExecGroup.Toolchains, 1)
	})

	t.Run("AttrAspectsWildcard", func(t *testing.T) {
		definition := encodeAspect(t, `ARGS = {"attr_aspects": ["*"]}`)
		require.Equal(t, []string{"*"}, definition.AttrAspects)

		_, err := exec(t, `ASPECT = aspect(implementation = _impl, attr_aspects = ["*", "deps"])`)
		require.ErrorContains(t, err, "attr_aspects: \"*\" cannot be combined with attr names")
	})

	t.Run("InvalidAttrs", func(t *testing.T) {
		_, err := exec(t, `ASPECT = aspect(implementation = _impl, attrs = {"deps": attr.label_list()})`)
		require.ErrorContains(t, err, "attr \"deps\": public aspect attrs must be of type bool, int or string, not label_list")

		_, err = exec(t, `ASPECT = aspect(implementation = _impl, attrs = {"_tool": attr.label(mandatory = True)})`)
		require.ErrorContains(t, err, "attr \"_tool\": private aspect attrs must have a default value")

		_, err = exec(t, `
def _transition_impl(settings, attr):
    return {}
_transition = transition(implementation = _transition_impl, inputs = [], outputs = [])
ASPECT = aspect(implementation = _impl, attrs = {"_tool": attr.label(cfg = _transition)})`)
		require.ErrorContains(t, err, "attr \"_tool\": aspect attrs cannot use user-defined transitions")

		_, err = exec(t, `ASPECT = aspect(implementation = _impl, attrs = {"__configuration": attr.label()})`)
		require.ErrorContains(t, err, "attr \"__configuration\" cannot be declared explicitly")
	})

	t.Run("LabelAttrAspects", func(t *testing.T) {
		// Aspects provided to attr.label() are stored by
		// identifier, preserving their order.
		globals, err := exec(t, `
_a = aspect(implementation = _impl)
_b = aspect(implementation = _impl)
ATTR = attr.label_list(aspects = [_b, _a, _b])`)
		require.NoError(t, err)
		encodedAttr, err := globals["ATTR"].(model_starlark.EncodableValue[object.LocalReference, model_core.CreatedObjectTree]).EncodeValue(newTestThread(t), nil, nil, valueEncodingOptions)
		require.NoError(t, err)
		require.Equal(
			t,
			[]string{"@@rules+//:defs.bzl%_b", "@@rules+//:defs.bzl%_a"},
			encodedAttr.Message.GetAttr().GetLabelList().GetListValueOptions().GetAspects(),
		)
	})
}
