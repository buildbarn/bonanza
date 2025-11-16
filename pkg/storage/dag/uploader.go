package dag

import (
	"context"

	"bonanza.build/pkg/storage/tag"
)

// Uploader of DAGs of objects.
//
// The object.Uploader and tag.Updater can be used to upload and create
// individual objects and tags, however they don't guarantee that graphs
// of objects are complete. This is why the storage frontends require
// the use of a higher level API that enforces this.
type Uploader[TNamespace, TReference any] interface {
	// Upload a DAG of objects. After uploading completes, the DAG
	// and any of the objects within can be accessed by their
	// reference.
	UploadDAG(
		ctx context.Context,
		rootReference TReference,
		rootObjectContentsWalker ObjectContentsWalker,
	) error

	// Upload a DAG of objects, and create a tag that references the
	// root of the DAG.
	UploadTaggedDAG(
		ctx context.Context,
		namespace TNamespace,
		rootTagKey tag.Key,
		rootTagSignedValue tag.SignedValue,
		rootObjectContentsWalker ObjectContentsWalker,
	) error
}
