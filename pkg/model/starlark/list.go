package starlark

import (
	"context"
	"errors"
	"iter"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	model_parser "bonanza.build/pkg/model/parser"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/storage/object"
)

func mapListElementsToValues[TReference any](
	base iter.Seq[model_core.Message[*model_starlark_pb.List_Element, TReference]],
	errOut *error,
) iter.Seq[model_core.Message[*model_starlark_pb.Value, TReference]] {
	return func(yield func(model_core.Message[*model_starlark_pb.Value, TReference]) bool) {
		base(func(entry model_core.Message[*model_starlark_pb.List_Element, TReference]) bool {
			switch level := entry.Message.Level.(type) {
			case *model_starlark_pb.List_Element_Leaf:
				return yield(model_core.Nested(entry, level.Leaf))
			default:
				*errOut = errors.New("not a valid leaf entry")
				return false
			}
		})
	}
}

func listElementGetParentReference[TReference any](element model_core.Message[*model_starlark_pb.List_Element, TReference]) (*model_core_pb.DecodableReference, error) {
	return element.Message.GetParent().GetReference(), nil
}

// AllListLeafElements walks over a list and returns all leaf elements
// contained within.
func AllListLeafElements[TReference object.BasicReference](
	ctx context.Context,
	reader model_parser.MessageObjectReader[TReference, []*model_starlark_pb.List_Element],
	rootList model_core.Message[[]*model_starlark_pb.List_Element, TReference],
	errOut *error,
) iter.Seq[model_core.Message[*model_starlark_pb.Value, TReference]] {
	return mapListElementsToValues(
		btree.AllLeaves(
			ctx,
			reader,
			rootList,
			listElementGetParentReference,
			errOut,
		),
		errOut,
	)
}

// AllListLeafElementsSkippingDuplicateParents walks over a list and
// returns all leaf elements contained within. In the process, it
// records which parent elements are encountered and skips duplicates.
//
// This function can be used to efficiently iterate lists that should be
// interpreted as sets. As depsets are backed by lists internally, this
// function can be used in part to implement depset.to_list().
//
// Note that this function does not perform deduplication of leaf
// elements. Only parents are deduplicated.
func AllListLeafElementsSkippingDuplicateParents[TReference object.BasicReference](
	ctx context.Context,
	reader model_parser.MessageObjectReader[TReference, []*model_starlark_pb.List_Element],
	rootList model_core.Message[[]*model_starlark_pb.List_Element, TReference],
	listsSeen map[model_core.Decodable[object.LocalReference]]struct{},
	errOut *error,
) iter.Seq[model_core.Message[*model_starlark_pb.Value, TReference]] {
	return mapListElementsToValues(
		btree.AllLeavesSkippingDuplicateParents(
			ctx,
			reader,
			rootList,
			listElementGetParentReference,
			listsSeen,
			errOut,
		),
		errOut,
	)
}
