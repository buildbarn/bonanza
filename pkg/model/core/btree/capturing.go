package btree

import (
	"context"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/storage/object"
)

// CapturedParentNodeComputer is a simplified version of
// ParentNodeAppender that receives an already captured object, as
// opposed to receiving the literal contents of the created object. This
// is sufficient for most parent node computers.
type CapturedParentNodeComputer[TNode any, TMetadata model_core.ReferenceMetadata] func(
	createdObject model_core.Decodable[model_core.MetadataEntry[TMetadata]],
	childNodes model_core.Message[[]TNode, object.LocalReference],
) model_core.PatchedMessage[TNode, TMetadata]

// Capturing converts a CapturedParentNodeComputer to a plain
// ParentNodeComputer.
func Capturing[TNode any, TMetadata model_core.ReferenceMetadata](
	ctx context.Context,
	capturer model_core.CreatedObjectCapturer[TMetadata],
	parentNodeComputer CapturedParentNodeComputer[TNode, TMetadata],
) ParentNodeComputer[TNode, TMetadata] {
	return func(
		createdObject model_core.Decodable[model_core.CreatedObject[TMetadata]],
		childNodes []TNode,
	) (model_core.PatchedMessage[TNode, TMetadata], error) {
		metadataEntry, err := createdObject.Value.Capture(ctx, capturer)
		if err != nil {
			return model_core.PatchedMessage[TNode, TMetadata]{}, err
		}
		capturedObject := model_core.CopyDecodable(createdObject, metadataEntry)
		return parentNodeComputer(
			capturedObject,
			model_core.NewMessage(childNodes, createdObject.Value.Contents),
		), nil
	}
}
