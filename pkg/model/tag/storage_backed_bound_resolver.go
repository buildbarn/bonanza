package tag

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"

	"bonanza.build/pkg/storage/object"
	"bonanza.build/pkg/storage/tag"
)

type storageBackedBoundResolver struct {
	resolver           tag.Resolver[struct{}]
	signaturePublicKey [ed25519.PublicKeySize]byte
}

// NewStorageBackedBoundResolver creates a basic implementation of
// BoundResolver that calls into a storage backend.
func NewStorageBackedBoundResolver(resolver tag.Resolver[struct{}], signaturePublicKey [ed25519.PublicKeySize]byte) BoundResolver[object.LocalReference] {
	return &storageBackedBoundResolver{
		resolver:           resolver,
		signaturePublicKey: signaturePublicKey,
	}
}

func (r *storageBackedBoundResolver) ResolveTag(ctx context.Context, keyHash [sha256.Size]byte) (object.LocalReference, error) {
	signedValue, err := tag.ResolveCompleteTag(
		ctx,
		r.resolver,
		struct{}{},
		tag.Key{
			SignaturePublicKey: r.signaturePublicKey,
			Hash:               keyHash,
		},
		/* minimumTimestamp = */ nil,
	)
	if err != nil {
		var badReference object.LocalReference
		return badReference, err
	}
	return signedValue.Value.Reference, nil
}
