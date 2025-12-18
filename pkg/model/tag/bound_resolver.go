package tag

import (
	"context"
	"crypto/sha256"

	model_core "bonanza.build/pkg/model/core"
)

// BoundResolver can be used to resolve references that are associated
// with tags. It differs from Resolver in pkg/storage/tag as follows:
//
//   - It is bound to a specific storage namespace.
//   - It is bound to a given public key. The caller only needs to
//     provide the key's hash.
//   - Only references to complete graphs are returned.
//   - References can be of an arbitrary type, instead of just
//     object.LocalReference as contained in tag.SignedValue.
type BoundResolver[TReference any] interface {
	ResolveTag(ctx context.Context, keyHash [sha256.Size]byte) (TReference, error)
}

// ResolveDecodableTag resolves the reference associated with a tag
// using a BoundResolver. In addition to that, it copies the decoding
// parameters that are attached to a key hash to the resulting
// reference. This allows the referenced object to be read using
// model_parser.DecodingObjectReader.
func ResolveDecodableTag[TReference any](
	ctx context.Context,
	resolver BoundResolver[TReference],
	keyHash DecodableKeyHash,
) (model_core.Decodable[TReference], error) {
	reference, err := resolver.ResolveTag(ctx, keyHash.Value)
	if err != nil {
		var badReference model_core.Decodable[TReference]
		return badReference, err
	}
	return model_core.CopyDecodable(keyHash, reference), nil
}
