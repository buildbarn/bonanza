package btree

import (
	"context"
	"iter"

	model_core "bonanza.build/pkg/model/core"
	model_parser "bonanza.build/pkg/model/parser"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	"bonanza.build/pkg/storage/object"

	"google.golang.org/protobuf/proto"
)

// Traverser is called by AllLeaves() to extract references of nested
// parent objects from B-tree entries.
type Traverser[TMessage, TReference any] func(model_core.Message[TMessage, TReference]) (*model_core_pb.DecodableReference, error)

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
	traverser Traverser[TMessagePtr, TReference],
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

// AllLeavesSkippingDuplicateParents is identical to AllLeaves(), except
// that it only traverses the first occurrence of a parent object. This
// can be used to traverse structures that contain a large amount of
// redundancy, such as depsets.
//
// Note that this function does not perform deduplication of leaves.
// Only parents are deduplicated.
func AllLeavesSkippingDuplicateParents[
	TMessage any,
	TMessagePtr interface {
		*TMessage
		proto.Message
	},
	TReference object.BasicReference,
](
	ctx context.Context,
	reader model_parser.MessageObjectReader[TReference, []TMessagePtr],
	root model_core.Message[[]TMessagePtr, TReference],
	traverser Traverser[TMessagePtr, TReference],
	parentsSeen map[model_core.Decodable[object.LocalReference]]struct{},
	errOut *error,
) iter.Seq[model_core.Message[TMessagePtr, TReference]] {
	allLeaves := AllLeaves(
		ctx,
		reader,
		root,
		func(m model_core.Message[TMessagePtr, TReference]) (*model_core_pb.DecodableReference, error) {
			if parentReferenceMessage, err := traverser(m); err != nil {
				return nil, err
			} else if parentReferenceMessage != nil {
				parentReference, err := model_core.FlattenDecodableReference(model_core.Nested(m, parentReferenceMessage))
				if err != nil {
					return nil, err
				}
				key := model_core.CopyDecodable(parentReference, parentReference.Value.GetLocalReference())
				if _, ok := parentsSeen[key]; ok {
					// Parent was already seen before.
					// Skip it.
					return nil, nil
				}

				// Parent was not seen before. Enter it.
				parentsSeen[key] = struct{}{}
				return parentReferenceMessage, nil
			}
			return nil, nil
		},
		errOut,
	)
	return func(yield func(model_core.Message[TMessagePtr, TReference]) bool) {
		allLeaves(func(m model_core.Message[TMessagePtr, TReference]) bool {
			parentReferenceMessage, err := traverser(m)
			if err == nil && parentReferenceMessage != nil {
				// Parent that was traversed previously,
				// which needs to be skipped.
				return true
			}
			return yield(m)
		})
	}
}
