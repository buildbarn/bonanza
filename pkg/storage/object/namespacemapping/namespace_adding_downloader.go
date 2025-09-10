package namespacemapping

import (
	"context"

	"bonanza.build/pkg/storage/object"
)

// NamespaceAddingNamespace is a constraint for object store namespaces
// accepted by NewNamespaceAddingDownloader().
type NamespaceAddingNamespace[T any] interface {
	WithLocalReference(reference object.LocalReference) T
}

type namespaceAddingDownloader[TNamespace NamespaceAddingNamespace[TReference], TReference any] struct {
	base      object.Downloader[TReference]
	namespace TNamespace
}

// NewNamespaceAddingDownloader creates a decorator for Downloader that
// converts LocalReferences provided to DownloadObject() to references
// that have a namespace associated with them. This is useful if the
// client is oblivious of namespaces, but the storage backend requires
// them (e.g., a networked multi-tenant storage server).
func NewNamespaceAddingDownloader[TNamespace NamespaceAddingNamespace[TReference], TReference any](base object.Downloader[TReference], namespace TNamespace) object.Downloader[object.LocalReference] {
	return &namespaceAddingDownloader[TNamespace, TReference]{
		base:      base,
		namespace: namespace,
	}
}

func (d *namespaceAddingDownloader[TNamespace, TReference]) DownloadObject(ctx context.Context, reference object.LocalReference) (*object.Contents, error) {
	return d.base.DownloadObject(ctx, d.namespace.WithLocalReference(reference))
}
