package tag

// Store for tags, which is both accessible for reading (resolving) and
// writing (updating).
type Store[TNamespace any, TLease any] interface {
	Resolver[TNamespace]
	Updater[TNamespace, TLease]
}

// NewStore is a helper function for creating a Store that is backed by
// separate instances of Resolver and Updater.
func NewStore[TNamespace, TLease any](resolver Resolver[TNamespace], updater Updater[TNamespace, TLease]) Store[TNamespace, TLease] {
	return struct {
		Resolver[TNamespace]
		Updater[TNamespace, TLease]
	}{
		Resolver: resolver,
		Updater:  updater,
	}
}
