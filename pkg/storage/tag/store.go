package tag

import (
	"bonanza.build/pkg/storage/object"
)

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

// StoreForTesting is an instantiated version of Store, which may be
// used for generating mocks to be used by tests.
type StoreForTesting Store[object.Namespace, any]
