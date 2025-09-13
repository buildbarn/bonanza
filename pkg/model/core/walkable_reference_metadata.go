package core

import (
	"bonanza.build/pkg/storage/dag"
)

// WalkableReferenceMetadata can be implemented by reference metadata to
// indicate that they can be converted to a dag.ObjectContentsWalker,
// allowing the contents of any captured objects to be obtained up to
// once. This allows them to be written to a storage server.
type WalkableReferenceMetadata interface {
	ReferenceMetadata

	ToObjectContentsWalker() dag.ObjectContentsWalker
}

// MapReferenceMetadataToWalkers replaces all the reference metadata in
// a patched message with instances of dag.ObjectContentsWalker.
//
// This function can be used in contexts where we only care about
// transmitting the contents of a DAG to a server, and no longer need to
// access any of the additional metadata that was gathered.
func MapReferenceMetadataToWalkers[TMetadata WalkableReferenceMetadata](p *ReferenceMessagePatcher[TMetadata]) *ReferenceMessagePatcher[dag.ObjectContentsWalker] {
	return MapReferenceMessagePatcherMetadata(
		p,
		func(capturedObject MetadataEntry[TMetadata]) dag.ObjectContentsWalker {
			return capturedObject.Metadata.ToObjectContentsWalker()
		},
	)
}
