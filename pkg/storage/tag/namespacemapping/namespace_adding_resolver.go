package namespacemapping

import (
	"context"
	"time"

	"bonanza.build/pkg/storage/tag"
)

type namespaceAddingResolver[TNamespace any] struct {
	base      tag.Resolver[TNamespace]
	namespace TNamespace
}

// NewNamespaceAddingResolver creates a decorator for Resolver that
// converts the ReferenceFormat provided to ResolveTag() to full object
// namespaces. This is useful if the client is oblivious of namespaces,
// but the storage backend requires them (e.g., a networked multi-tenant
// storage server).
func NewNamespaceAddingResolver[TNamespace any](base tag.Resolver[TNamespace], namespace TNamespace) tag.Resolver[struct{}] {
	return &namespaceAddingResolver[TNamespace]{
		base:      base,
		namespace: namespace,
	}
}

func (d *namespaceAddingResolver[TNamespace]) ResolveTag(ctx context.Context, namespace struct{}, key tag.Key, minimumTimestamp *time.Time) (tag.SignedValue, bool, error) {
	return d.base.ResolveTag(ctx, d.namespace, key, minimumTimestamp)
}
