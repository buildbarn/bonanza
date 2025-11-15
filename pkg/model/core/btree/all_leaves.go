package btree

import (
	"context"
	"iter"

	model_core "bonanza.build/pkg/model/core"
	model_parser "bonanza.build/pkg/model/parser"
	model_core_pb "bonanza.build/pkg/proto/model/core"

	"google.golang.org/protobuf/proto"
)

// AllLeaves can be used to iterate all leaf entries contained in a B-tree.
func AllLeaves[
	TMessage any,
	TMessagePtr interface {
		*TMessage
		proto.Message
	},
	TReference any,
](
	ctx context.Context,
	reader model_parser.MessageObjectReader[TReference, []TMessagePtr],
	root model_core.Message[[]TMessagePtr, TReference],
	traverser func(model_core.Message[TMessagePtr, TReference]) (*model_core_pb.DecodableReference, error),
	errOut *error,
) iter.Seq[model_core.Message[TMessagePtr, TReference]] {
	lists := []model_core.Message[[]TMessagePtr, TReference]{root}
	return func(yield func(model_core.Message[TMessagePtr, TReference]) bool) {
		for len(lists) > 0 {
			lastList := &lists[len(lists)-1]
			if len(lastList.Message) == 0 {
				lists = lists[:len(lists)-1]
			} else {
				entry := lastList.Message[0]
				lastList.Message = lastList.Message[1:]
				if childReference, err := traverser(model_core.Nested(*lastList, entry)); err != nil {
					*errOut = err
					return
				} else if childReference == nil {
					// Traverser wants us to yield a leaf.
					if !yield(model_core.Nested(*lastList, entry)) {
						*errOut = nil
						return
					}
				} else {
					// Traverser wants us to enter a child.
					child, err := model_parser.Dereference[model_core.Message[[]TMessagePtr, TReference]](
						ctx,
						reader,
						model_core.Nested(*lastList, childReference),
					)
					if err != nil {
						*errOut = err
						return
					}
					lists = append(lists, child)
				}
			}
		}
	}
}
