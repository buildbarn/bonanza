package parser

import (
	"context"

	model_core "bonanza.build/pkg/model/core"
	model_core_pb "bonanza.build/pkg/proto/model/core"
)

// Dereference a reference message, returning the value that's
// associated with it.
func Dereference[
	TValue any,
	TReference any,
](
	ctx context.Context,
	reader DecodingObjectReader[TReference, TValue],
	m model_core.Message[*model_core_pb.DecodableReference, TReference],
) (TValue, error) {
	reference, err := model_core.FlattenDecodableReference(m)
	if err != nil {
		var bad TValue
		return bad, err
	}
	value, err := reader.ReadObject(ctx, reference)
	return value, err
}

// MaybeDereference is identical to Dereference, except that it returns
// a default instance in case the reference message is not set.
func MaybeDereference[
	TValue any,
	TReference any,
](
	ctx context.Context,
	reader DecodingObjectReader[TReference, TValue],
	m model_core.Message[*model_core_pb.DecodableReference, TReference],
) (TValue, error) {
	if m.Message == nil {
		var zero TValue
		return zero, nil
	}
	return Dereference(ctx, reader, m)
}
