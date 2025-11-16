package dag

import (
	"context"
)

// Uploader of DAGs of objects.
//
// The object.Uploader and tag.Updater can be used to upload and create
// individual objects and tags, however they don't guarantee that graphs
// of objects are complete. This is why the storage frontends require
// the use of a higher level API that enforces this.
type Uploader[TReference any] interface {
	UploadDAG(ctx context.Context, rootReference TReference, rootObjectContentsWalker ObjectContentsWalker) error
}
