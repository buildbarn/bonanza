package btree

import (
	"context"

	model_core "bonanza.build/pkg/model/core"
	model_parser "bonanza.build/pkg/model/parser"
	model_core_pb "bonanza.build/pkg/proto/model/core"

	"google.golang.org/protobuf/proto"
)

// Find an entry contained in a B-tree.
func Find[
	TMessage any,
	TMessagePtr interface {
		*TMessage
		proto.Message
	},
	TReference any,
](
	ctx context.Context,
	reader model_parser.MessageObjectReader[TReference, []TMessagePtr],
	list model_core.Message[[]TMessagePtr, TReference],
	cmp func(model_core.Message[TMessagePtr, TReference]) (int, *model_core_pb.DecodableReference),
) (model_core.Message[TMessagePtr, TReference], error) {
	for {
		low, high := 0, len(list.Message)
		var childReference *model_core_pb.DecodableReference
		for low < high {
			index := int(uint(low+high) >> 1)
			r, reference := cmp(model_core.Nested(list, list.Message[index]))
			if r < 0 {
				// Entry should be closer to the start
				// of the list.
				high = index
			} else if r > 0 {
				// Entry should be closer to the end of
				// the list. Do preserve the reference
				// to the child in case we don't find
				// any better matches.
				low = index + 1
				childReference = reference
			} else {
				// Exact match. We may stop searching.
				if reference == nil {
					return model_core.Nested(list, list.Message[index]), nil
				}
				childReference = reference
				break
			}
		}
		if childReference == nil {
			// Found no exact match, and also did not find a
			// child that may include a matching entry.
			return model_core.Message[TMessagePtr, TReference]{}, nil
		}

		// Load the child from storage and continue searching.
		var err error
		list, err = model_parser.Dereference(ctx, reader, model_core.Nested(list, childReference))
		if err != nil {
			return model_core.Message[TMessagePtr, TReference]{}, err
		}
	}
}
