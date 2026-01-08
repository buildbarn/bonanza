package tag

// BoundStore is a store for tags, which is both accessible for reading
// (resolving) and writing (updating). Differences from Store in
// pkg/storage tag include:
//
//   - It is bound to a specific storage namespace.
//   - It is bound to a given key pair. The caller only needs to
//     provide the key's hash.
//   - References can be of an arbitrary type, instead of just
//     object.LocalReference as contained in tag.SignedValue.
type BoundStore[TReference any] interface {
	BoundResolver[TReference]
	BoundUpdater[TReference]
}

// NewBoundStore is a helper function for creating a BoundStore that is
// backed by separate instances of BoundResolver and BoundUpdater.
func NewBoundStore[TReference any](resolver BoundResolver[TReference], updater BoundUpdater[TReference]) BoundStore[TReference] {
	return struct {
		BoundResolver[TReference]
		BoundUpdater[TReference]
	}{
		BoundResolver: resolver,
		BoundUpdater:  updater,
	}
}
