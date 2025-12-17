package evaluation

import (
	"context"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_evaluation_pb "bonanza.build/pkg/proto/model/evaluation"
	"bonanza.build/pkg/storage/object"
)

// newKeysBTreeBuilder creates a B-tree builder for lists of evaluation
// keys. These are used as part of the evaluation results, but also used
// by some of the evaluation cache messages.
func newKeysBTreeBuilder[TMetadata model_core.ReferenceMetadata](
	ctx context.Context,
	objectCapturer model_core.CreatedObjectCapturer[TMetadata],
	referenceFormat object.ReferenceFormat,
	objectEncoder model_encoding.DeterministicBinaryEncoder,
) btree.Builder[*model_evaluation_pb.Keys, TMetadata] {
	return btree.NewHeightAwareBuilder(
		btree.NewProllyChunkerFactory[TMetadata](
			/* minimumSizeBytes = */ 1<<16,
			/* minimumSizeBytes = */ 1<<18,
			/* isParent = */ func(keys *model_evaluation_pb.Keys) bool {
				return keys.GetParent() != nil
			},
		),
		btree.NewObjectCreatingNodeMerger(
			objectEncoder,
			referenceFormat,
			/* parentNodeComputer = */ btree.Capturing(ctx, objectCapturer, func(createdObject model_core.Decodable[model_core.MetadataEntry[TMetadata]], childNodes model_core.Message[[]*model_evaluation_pb.Keys, object.LocalReference]) model_core.PatchedMessage[*model_evaluation_pb.Keys, TMetadata] {
				return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_evaluation_pb.Keys {
					return &model_evaluation_pb.Keys{
						Level: &model_evaluation_pb.Keys_Parent_{
							Parent: &model_evaluation_pb.Keys_Parent{
								Reference: patcher.AddDecodableReference(createdObject),
							},
						},
					}
				})
			}),
		),
	)
}
