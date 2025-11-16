package dag

import (
	"context"

	"bonanza.build/pkg/storage/object"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ObjectContentsWalker is called into by Uploader.UploadDAG() to
// request the contents of an object. Uploader.UploadDAG() may also
// discard them in case a server responds that the object already
// exists, or if multiple walkers for the same object exist.
type ObjectContentsWalker interface {
	GetContents(ctx context.Context) (*object.Contents, []ObjectContentsWalker, error)
	Discard()
}

type simpleObjectContentsWalker struct {
	contents *object.Contents
	walkers  []ObjectContentsWalker
}

// NewSimpleObjectContentsWalker creates an ObjectContentsWalker that is
// backed by a literal message and a list of ObjectContentsWalkers for
// its children. This implementation is sufficient when uploading DAGs
// that reside fully in memory.
func NewSimpleObjectContentsWalker(contents *object.Contents, walkers []ObjectContentsWalker) ObjectContentsWalker {
	return &simpleObjectContentsWalker{
		contents: contents,
		walkers:  walkers,
	}
}

func (w *simpleObjectContentsWalker) GetContents(ctx context.Context) (contents *object.Contents, walkers []ObjectContentsWalker, err error) {
	if w.contents == nil {
		panic("attempted to get contents of an object that was already discarded")
	}
	contents = w.contents
	walkers = w.walkers
	*w = simpleObjectContentsWalker{}
	return contents, walkers, err
}

func (w *simpleObjectContentsWalker) Discard() {
	if w.contents == nil {
		panic("attempted to discard contents of an object that was already discarded")
	}
	for _, wChild := range w.walkers {
		wChild.Discard()
	}
	*w = simpleObjectContentsWalker{}
}

type existingObjectContentsWalker struct{}

func (existingObjectContentsWalker) GetContents(ctx context.Context) (*object.Contents, []ObjectContentsWalker, error) {
	return nil, nil, status.Error(codes.Internal, "Contents for this object are not available for upload, as this object was expected to already exist")
}

func (existingObjectContentsWalker) Discard() {}

// ExistingObjectContentsWalker is an implementation of
// ObjectContentsWalker that always returns an error when an attempt is
// made to upload. It can be used in places where we expect the
// underlying object to already be present in storage. The storage
// server should therefore never attempt to request the object's
// contents from the client.
var ExistingObjectContentsWalker ObjectContentsWalker = existingObjectContentsWalker{}
