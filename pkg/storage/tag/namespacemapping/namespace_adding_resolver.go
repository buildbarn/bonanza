package namespacemapping

import (
	"context"
	"time"

	"bonanza.build/pkg/storage/object"
	"bonanza.build/pkg/storage/tag"
)

// NamespaceAddingNamespace is a constraint for tag store namespaces
// accepted by NewNamespaceAddingResolver().
type NamespaceAddingNamespace[T any] interface {
	WithReferenceFormat(referenceFormat object.ReferenceFormat) T
}

type namespaceAddingResolver[TNamespace NamespaceAddingNamespace[TReference], TReference any] struct {
	base      tag.Resolver[TReference]
	namespace TNamespace
}

// NewNamespaceAddingResolver creates a decorator for Resolver that
// converts the ReferenceFormat provided to ResolveTag() to full object
// namespaces. This is useful if the client is oblivious of namespaces,
// but the storage backend requires them (e.g., a networked multi-tenant
// storage server).
func NewNamespaceAddingResolver[TNamespace NamespaceAddingNamespace[TReference], TReference any](base tag.Resolver[TReference], namespace TNamespace) tag.Resolver[object.ReferenceFormat] {
	return &namespaceAddingResolver[TNamespace, TReference]{
		base:      base,
		namespace: namespace,
	}
}

func (d *namespaceAddingResolver[TNamespace, TReference]) ResolveTag(ctx context.Context, referenceFormat object.ReferenceFormat, key tag.Key, minimumTimestamp *time.Time) (tag.SignedValue, bool, error) {
	return d.base.ResolveTag(ctx, d.namespace.WithReferenceFormat(referenceFormat), key, minimumTimestamp)
}
