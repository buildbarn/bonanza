package btree

import (
	model_core "bonanza.build/pkg/model/core"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_filesystem_pb "bonanza.build/pkg/proto/model/filesystem"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/protobuf/proto"
)

// ParentNodeComputer can be used by ObjectCreatingNodeMerger to combine
// the values of nodes stored in an object into a single node that can
// be stored in its parent.
type ParentNodeComputer[TNode any, TMetadata model_core.ReferenceMetadata] func(
	createdObject model_core.Decodable[model_core.CreatedObject[TMetadata]],
	childNodes []TNode,
) (model_core.PatchedMessage[TNode, TMetadata], error)

// NewObjectCreatingNodeMerger creates a NodeMerger that can be used in
// combination with Builder to construct B-trees that are backed by
// storage objects that reference each other.
func NewObjectCreatingNodeMerger[TNode proto.Message, TMetadata model_core.ReferenceMetadata](encoder model_encoding.DeterministicBinaryEncoder, referenceFormat object.ReferenceFormat, parentNodeComputer ParentNodeComputer[TNode, TMetadata]) NodeMerger[TNode, TMetadata] {
	return func(list model_core.PatchedMessage[[]TNode, TMetadata]) (model_core.PatchedMessage[TNode, TMetadata], error) {
		// Marshal each of the messages, prepending its size.
		createdObject, err := model_core.MarshalAndEncodeDeterministic(model_core.ProtoListToBinaryMarshaler(list), referenceFormat, encoder)
		if err != nil {
			return model_core.PatchedMessage[TNode, TMetadata]{}, util.StatusWrap(err, "Failed to marshal list")
		}

		// Construct a parent node that references the object
		// containing the children.
		return parentNodeComputer(createdObject, list.Message)
	}
}

// MaybeMergeNodes either returns the top-level elements of a B-tree in
// literal form, or emits a single parent node referring to the elements
// stored in a separate object.
//
// This function is typically used in combination with
// inlinedtree.Build() to spill the top-level elements of a B-tree into
// a separate object, if storing them inline would cause the parent
// object to become too large.
func MaybeMergeNodes[TNode proto.Message, TMetadata model_core.ReferenceMetadata](
	nodes []TNode,
	externalObject *model_core.Decodable[model_core.CreatedObject[TMetadata]],
	patcher *model_core.ReferenceMessagePatcher[TMetadata],
	parentNodeComputer ParentNodeComputer[TNode, TMetadata],
) ([]TNode, error) {
	if externalObject == nil || len(nodes) < 1 {
		return nodes, nil
	}

	merged, err := parentNodeComputer(*externalObject, nodes)
	if err != nil {
		return nil, err
	}
	patcher.Merge(merged.Patcher)
	return []TNode{merged.Message}, nil
}

// ParentNodeComputerForTesting is an instantiation of
// ParentNodeComputer for generating mocks to be used by tests.
type ParentNodeComputerForTesting ParentNodeComputer[*model_filesystem_pb.FileContents, model_core.ReferenceMetadata]
