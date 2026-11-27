package starlark_test

import (
	"testing"

	model_starlark "bonanza.build/pkg/model/starlark"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"

	"github.com/stretchr/testify/require"
)

func TestProviderSetsSatisfiedBy(t *testing.T) {
	newProviderSet := func(providerIdentifiers ...string) *model_starlark_pb.ProviderSet {
		return model_starlark_pb.ProviderSet_builder{ProviderIdentifiers: providerIdentifiers}.Build()
	}
	providerIdentifiers := []string{"A", "C", "D"}

	t.Run("NoSets", func(t *testing.T) {
		require.False(t, model_starlark.ProviderSetsSatisfiedBy(nil, providerIdentifiers))
	})

	t.Run("EmptySet", func(t *testing.T) {
		// An empty set is satisfied by any list of providers.
		require.True(t, model_starlark.ProviderSetsSatisfiedBy([]*model_starlark_pb.ProviderSet{newProviderSet()}, nil))
	})

	t.Run("Satisfied", func(t *testing.T) {
		require.True(t, model_starlark.ProviderSetsSatisfiedBy([]*model_starlark_pb.ProviderSet{newProviderSet("A", "D")}, providerIdentifiers))
		require.True(t, model_starlark.ProviderSetsSatisfiedBy([]*model_starlark_pb.ProviderSet{newProviderSet("B"), newProviderSet("C")}, providerIdentifiers))
	})

	t.Run("Unsatisfied", func(t *testing.T) {
		require.False(t, model_starlark.ProviderSetsSatisfiedBy([]*model_starlark_pb.ProviderSet{newProviderSet("A", "B")}, providerIdentifiers))
		require.False(t, model_starlark.ProviderSetsSatisfiedBy([]*model_starlark_pb.ProviderSet{newProviderSet("E")}, providerIdentifiers))
	})
}
