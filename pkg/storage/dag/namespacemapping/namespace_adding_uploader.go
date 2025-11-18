package namespacemapping

import (
	"context"

	"bonanza.build/pkg/storage/dag"
	"bonanza.build/pkg/storage/object"
	"bonanza.build/pkg/storage/tag"
)

// NamespaceAddingNamespace is a constraint for tag store namespaces
// accepted by NewNamespaceAddingUploader().
type NamespaceAddingNamespace[TNamespace, TReference any] interface {
	WithLocalReference(localReference object.LocalReference) TReference
}

type namespaceAddingUploader[TNamespace NamespaceAddingNamespace[TNamespace, TReference], TReference any] struct {
	base      dag.Uploader[TNamespace, TReference]
	namespace TNamespace
}

// NewNamespaceAddingUploader creates a decorator for Uploader that
// converts the LocalReference and ReferenceFormat provided to its
// methods to full object namespaces. This is useful if the client is
// oblivious of namespaces, but the storage backend requires them (e.g.,
// a networked multi-tenant storage server).
func NewNamespaceAddingUploader[TNamespace NamespaceAddingNamespace[TNamespace, TReference], TReference any](base dag.Uploader[TNamespace, TReference], namespace TNamespace) dag.Uploader[struct{}, object.LocalReference] {
	return &namespaceAddingUploader[TNamespace, TReference]{
		base:      base,
		namespace: namespace,
	}
}

func (d *namespaceAddingUploader[TNamespace, TReference]) UploadDAG(ctx context.Context, rootReference object.LocalReference, rootObjectContentsWalker dag.ObjectContentsWalker) error {
	return d.base.UploadDAG(ctx, d.namespace.WithLocalReference(rootReference), rootObjectContentsWalker)
}

func (d *namespaceAddingUploader[TNamespace, TReference]) UploadTaggedDAG(ctx context.Context, unused struct{}, rootTagKey tag.Key, rootTagSignedValue tag.SignedValue, rootObjectContentsWalker dag.ObjectContentsWalker) error {
	return d.base.UploadTaggedDAG(ctx, d.namespace, rootTagKey, rootTagSignedValue, rootObjectContentsWalker)
}
