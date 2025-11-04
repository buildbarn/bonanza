package tag

import (
	"context"

	"bonanza.build/pkg/storage/object"
)

// Updater of tags in storage. Tags can be used to assign a stable
// identifier to an object.
type Updater[TNamespace any, TLease any] interface {
	UpdateTag(ctx context.Context, namespace TNamespace, key Key, value SignedValue, lease TLease) error
}

// UpdaterForTesting is an instantiated version of Updater, which may be
// used for generating mocks to be used by tests.
type UpdaterForTesting Updater[object.Namespace, any]
