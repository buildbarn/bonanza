package starlark_test

import (
	"testing"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"
)

func TestTargetReferenceProviders(t *testing.T) {
	newProvider := func(identifier string) eval.Value {
		providerIdentifier := util.Must(label.NewCanonicalStarlarkIdentifier(identifier))
		return model_starlark.NewProvider[object.LocalReference, model_core.NoopReferenceMetadata](
			model_starlark.NewProviderInstanceProperties[object.LocalReference, model_core.NoopReferenceMetadata](&providerIdentifier, false, nil, ""),
			/* fields = */ nil,
			/* initFunction = */ nil,
		)
	}

	// Target that only yields an instance of FooInfo.
	target := model_starlark.NewTargetReference(
		util.Must(label.NewResolvedLabel("@@main+//:target")),
		model_starlark.NewConfiguredTargetReference[object.LocalReference, model_core.NoopReferenceMetadata](
			util.Must(label.NewCanonicalLabel("@@main+//:target")),
			model_core.NewSimpleMessage[object.LocalReference]([]*model_starlark_pb.Struct{{
				Fields: &model_starlark_pb.Struct_Fields{},
				ProviderInstanceProperties: &model_starlark_pb.Provider_InstanceProperties{
					ProviderIdentifier: "@@rules+//:defs.bzl%FooInfo",
				},
			}}),
		),
	)

	thread := newTestThread(t)
	thread.SetLocal(model_starlark.ValueDecodingOptionsKey, &model_starlark.ValueDecodingOptions[object.LocalReference]{
		Readers: &model_starlark.ValueReaders[object.LocalReference]{},
	})
	_, err := execBzlFile(
		thread,
		util.Must(label.NewCanonicalLabel("@@main+//:test.bzl")),
		`
def check(condition, message):
    if not condition:
        fail(message)

def test():
    # Testing for the presence of a provider must not fail if the
    # target did not yield it.
    check(FooInfo in target, "FooInfo in target")
    check(BarInfo not in target, "BarInfo not in target")
    check(type(target[FooInfo]) == "struct", "target[FooInfo]")

test()
`,
		map[string]eval.Value{
			"BarInfo": newProvider("@@rules+//:defs.bzl%BarInfo"),
			"FooInfo": newProvider("@@rules+//:defs.bzl%FooInfo"),
			"target":  target,
		},
	)
	require.NoError(t, err)

	// Accessing a provider that the target did not yield should
	// fail with a descriptive error.
	_, err = execBzlFile(
		newTestThread(t),
		util.Must(label.NewCanonicalLabel("@@main+//:test.bzl")),
		"target[BarInfo]\n",
		map[string]eval.Value{
			"BarInfo": newProvider("@@rules+//:defs.bzl%BarInfo"),
			"target":  target,
		},
	)
	require.ErrorContains(t, err, "key <provider @@rules+//:defs.bzl%BarInfo> not found in Target")
}
