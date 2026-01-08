package tag

import (
	"context"
	"crypto/sha256"
)

// BoundUpdater can be used to update references that are associated
// with tags. It differs from Updater in pkg/storage/tag as follows:
//
//   - It is bound to a specific storage namespace.
//   - It is bound to a given private key. The caller only needs to
//     provide the key's hash.
//   - The timestamp embedded in the tag's value will also be set to the
//     current time of day.
//   - References can be of an arbitrary type, instead of just
//     object.LocalReference as contained in tag.SignedValue.
type BoundUpdater[TReference any] interface {
	UpdateTag(ctx context.Context, keyHash [sha256.Size]byte, reference TReference) error
}
