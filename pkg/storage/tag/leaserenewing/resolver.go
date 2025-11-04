package leaserenewing

import (
	"context"
	"time"

	"bonanza.build/pkg/storage/object"
	"bonanza.build/pkg/storage/tag"

	"github.com/buildbarn/bb-storage/pkg/util"
)

// Namespace in which references reside. This is used by the resolver to
// create references to objects to which the tag points.
type Namespace[TReference any] interface {
	WithLocalReference(localReference object.LocalReference) TReference
}

type resolver[TNamespace Namespace[TReference], TReference any, TLease any] struct {
	tagStore       tag.Store[TNamespace, TLease]
	objectUploader object.Uploader[TReference, TLease]
}

// NewResolver creates a decorator for tag.Resolver that attempts to
// reobtain leases for tags that reference objects for which the lease
// is expired or missing. Upon success, the tag is updated to use the
// new lease.
func NewResolver[TNamespace Namespace[TReference], TReference, TLease any](tagStore tag.Store[TNamespace, TLease], objectUploader object.Uploader[TReference, TLease]) tag.Resolver[TNamespace] {
	return &resolver[TNamespace, TReference, TLease]{
		tagStore:       tagStore,
		objectUploader: objectUploader,
	}
}

func (r *resolver[TNamespace, TReference, TLease]) ResolveTag(ctx context.Context, namespace TNamespace, key tag.Key, minimumTimestamp *time.Time) (tag.SignedValue, bool, error) {
	// No need to renew leases if the current lease is already
	// valid, or if the caller indicated that it doesn't care about
	// tags above a certain age.
	signedValue, complete, err := r.tagStore.ResolveTag(ctx, namespace, key, minimumTimestamp)
	if err != nil || complete || (minimumTimestamp != nil && signedValue.Value.Timestamp.Before(*minimumTimestamp)) {
		return signedValue, complete, err
	}

	// Tag has an expired lease. Attempt to obtain a new lease on
	// the root object referenced by the tag.
	localReference := signedValue.Value.Reference
	globalReference := namespace.WithLocalReference(localReference)
	result, err := r.objectUploader.UploadObject(
		ctx,
		globalReference,
		/* contents = */ nil,
		/* childrenLeases = */ nil,
		/* wantContentsIfIncomplete = */ false,
	)
	if err != nil {
		var badSignedValue tag.SignedValue
		return badSignedValue, false, util.StatusWrapf(err, "Failed to obtain lease for object with reference %s", localReference)
	}

	switch resultType := result.(type) {
	case object.UploadObjectComplete[TLease]:
		// Update the tag to use the new lease. This permits
		// lookups for the same tag in the nearby future to go
		// faster.
		if err := r.tagStore.UpdateTag(
			ctx,
			namespace,
			key,
			signedValue,
			resultType.Lease,
		); err != nil {
			var badSignedValue tag.SignedValue
			return badSignedValue, false, util.StatusWrapf(err, "Failed to update tag with lease for object with reference %s", localReference)
		}
		return signedValue, true, nil
	case object.UploadObjectIncomplete[TLease], object.UploadObjectMissing[TLease]:
		return signedValue, false, nil
	default:
		panic("unknown upload object result type")
	}
}
