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
)

func AllDictLeafEntries[TReference any](
	ctx context.Context,
	reader model_parser.MessageObjectReader[TReference, []*model_starlark_pb.Dict_Entry],
	rootDict model_core.Message[*model_starlark_pb.Dict, TReference],
	errOut *error,
) iter.Seq2[model_core.Message[*model_starlark_pb.Value, TReference], model_core.Message[*model_starlark_pb.Value, TReference]] {
	allLeaves := btree.AllLeaves(
		ctx,
		reader,
		model_core.Nested(rootDict, rootDict.Message.Entries),
		func(entry model_core.Message[*model_starlark_pb.Dict_Entry, TReference]) (*model_core_pb.DecodableReference, error) {
			return entry.Message.GetParent().GetReference(), nil
		},
		errOut,
	)
	return func(yield func(model_core.Message[*model_starlark_pb.Value, TReference], model_core.Message[*model_starlark_pb.Value, TReference]) bool) {
		allLeaves(func(entry model_core.Message[*model_starlark_pb.Dict_Entry, TReference]) bool {
			leafEntry, ok := entry.Message.Level.(*model_starlark_pb.Dict_Entry_Leaf_)
			if !ok {
				*errOut = errors.New("not a valid leaf entry")
				return false
			}
			return yield(
				model_core.Nested(entry, leafEntry.Leaf.Key),
				model_core.Nested(entry, leafEntry.Leaf.Value),
			)
		})
	}
}
