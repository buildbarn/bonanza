package tag

import (
	"context"
	"time"

	"bonanza.build/pkg/storage/object"
)

// Resolver of tags.
type Resolver[TNamespace any] interface {
	ResolveTag(ctx context.Context, namespace TNamespace, key Key, minimumTimestamp *time.Time) (value SignedValue, complete bool, err error)
}

// ResolverForTesting is an instantiated version of Resolver, which may
// be used to generate mocks for tests.
type ResolverForTesting Resolver[object.Namespace]
