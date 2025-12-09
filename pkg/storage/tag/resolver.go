package tag

import (
	"context"
	"time"

	"bonanza.build/pkg/storage/object"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Resolver of tags.
type Resolver[TNamespace any] interface {
	ResolveTag(ctx context.Context, namespace TNamespace, key Key, minimumTimestamp *time.Time) (value SignedValue, complete bool, err error)
}

// ResolveCompleteTag resolves a tag, and only returns the tag's value
// if the graph it references is complete.
func ResolveCompleteTag[TNamespace any](
	ctx context.Context,
	resolver Resolver[TNamespace],
	namespace TNamespace,
	key Key,
	minimumTimestamp *time.Time,
) (SignedValue, error) {
	signedValue, complete, err := resolver.ResolveTag(ctx, namespace, key, minimumTimestamp)
	if err != nil {
		var badSignedValue SignedValue
		return badSignedValue, err
	}
	if !complete {
		var badSignedValue SignedValue
		return badSignedValue, status.Error(codes.NotFound, "Tag refers to incomplete graph")
	}
	return signedValue, nil
}

// ResolverForTesting is an instantiated version of Resolver, which may
// be used to generate mocks for tests.
type ResolverForTesting Resolver[object.Namespace]
