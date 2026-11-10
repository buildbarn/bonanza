package starlark_test

import (
	"testing"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"
	"github.com/stretchr/testify/require"
)

func TestRepositoryRuleReference(t *testing.T) {
	// Repository rules that are reloaded from storage by
	// reference (e.g., because they are assigned to multiple
	// globals) should obtain their definition through the global
	// resolver when called.
	thread := newTestThread(t)
	thread.SetLocal(model_starlark.ValueDecodingOptionsKey, &model_starlark.ValueDecodingOptions[object.LocalReference]{
		Readers: &model_starlark.ValueReaders[object.LocalReference]{},
	})
	repositoryRule, err := model_starlark.DecodeValue[object.LocalReference, model_core.NoopReferenceMetadata](
		thread,
		model_core.NewSimpleMessage[object.LocalReference](&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_RepositoryRule{
				RepositoryRule: &model_starlark_pb.RepositoryRule{
					Kind: &model_starlark_pb.RepositoryRule_Reference{
						Reference: "@@rules+//:defs.bzl%my_repository_rule",
					},
				},
			},
		}),
		/* currentIdentifier = */ nil,
	)
	require.NoError(t, err)

	var resolvedIdentifiers []string
	thread.SetLocal(model_starlark.GlobalResolverKey, model_starlark.GlobalResolver[object.LocalReference](func(identifier label.CanonicalStarlarkIdentifier) (model_core.Message[*model_starlark_pb.Value, object.LocalReference], error) {
		resolvedIdentifiers = append(resolvedIdentifiers, identifier.String())
		return model_core.NewSimpleMessage[object.LocalReference](&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_RepositoryRule{
				RepositoryRule: &model_starlark_pb.RepositoryRule{
					Kind: &model_starlark_pb.RepositoryRule_Definition_{
						Definition: &model_starlark_pb.RepositoryRule_Definition{
							Attrs: []*model_starlark_pb.NamedAttr{{
								Name: "value",
								Attr: &model_starlark_pb.Attr{
									Type: &model_starlark_pb.Attr_String_{String_: &model_starlark_pb.Attr_StringType{}},
								},
							}},
						},
					},
				},
			},
		}), nil
	}))
	repoRegistrar := model_starlark.NewRepoRegistrar[model_core.NoopReferenceMetadata]()
	thread.SetLocal(model_starlark.RepoRegistrarKey, repoRegistrar)
	thread.SetLocal(model_starlark.CanonicalPackageKey, util.Must(label.NewCanonicalPackage("@@rules+")))
	thread.SetLocal(model_starlark.ValueEncodingOptionsKey, &model_starlark.ValueEncodingOptions[object.LocalReference, model_core.NoopReferenceMetadata]{
		Context:                t.Context(),
		ObjectEncoder:          model_encoding.NewChainedDeterministicBinaryEncoder(nil),
		ObjectReferenceFormat:  object.SHA256V1ReferenceFormat,
		ObjectCapturer:         model_core.NewDiscardingObjectCapturer[object.LocalReference](),
		ObjectMinimumSizeBytes: 1024,
		ObjectHorizonSizeBytes: 8 * 1024,
	})

	_, err = eval.Call(thread, repositoryRule, eval.Args{
		Named: namedArgs("name", eval.String("repo"), "value", eval.String("hello")),
	})
	require.NoError(t, err)
	require.Equal(t, []string{"@@rules+//:defs.bzl%my_repository_rule"}, resolvedIdentifiers)
	repos := repoRegistrar.GetRepos()
	require.Contains(t, repos, "repo")
	require.Equal(t, "@@rules+//:defs.bzl%my_repository_rule", repos["repo"].Message.Definition.RepositoryRuleIdentifier)
}
