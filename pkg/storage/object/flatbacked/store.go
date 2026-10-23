package flatbacked

import (
	"context"
	"sync"
	"time"

	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/clock"
	"github.com/buildbarn/bb-storage/pkg/lossymap"
	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type store struct {
	base                      object.Store[object.FlatReference, struct{}]
	clock                     clock.Clock
	leaseCompletenessDuration Lease

	lock      sync.Mutex
	leasesMap lossymap.Map[object.LocalReference, Lease, Lease]
}

// LeasesMap is used by this storage backend to keep track of leases of
// children that were confirmed to exist.
type LeasesMap = lossymap.Map[object.LocalReference, Lease, Lease]

// NewStore creates an object store that is implemented on top of a
// storage backend that is only capable of storing objects in a flat
// namespace (e.g., a key-value store).
//
// As flat storage backends do not provide facilities for checking that
// all transitive children of a node are present, this backend
// compensates for it by handing out timestamp based leases. This causes
// objects in the flat storage backend to be checked for existence
// periodically.
func NewStore(
	base object.Store[object.FlatReference, struct{}],
	clock clock.Clock,
	leaseCompletenessDuration time.Duration,
	leasesMap LeasesMap,
) object.Store[object.LocalReference, Lease] {
	return &store{
		base:                      base,
		clock:                     clock,
		leaseCompletenessDuration: Lease(leaseCompletenessDuration.Nanoseconds()),
		leasesMap:                 leasesMap,
	}
}

func (s *store) DownloadObject(ctx context.Context, reference object.LocalReference) (*object.Contents, error) {
	flatContents, err := s.base.DownloadObject(ctx, reference.Flatten())
	if err != nil {
		return nil, err
	}
	contents, err := flatContents.Unflatten(reference.GetLocalReference())
	if err != nil {
		return nil, util.StatusWrap(err, "Unflatten object contents")
	}
	return contents, nil
}

func (s *store) UploadObject(ctx context.Context, reference object.LocalReference, contents *object.Contents, childrenLeases []Lease, wantContentsIfIncomplete bool) (object.UploadObjectResult[Lease], error) {
	leaseNow := Lease(s.clock.Now().UnixNano())

	// Upload a flattened version of the object to the backing store.
	var flatContents *object.Contents
	if contents != nil {
		flatContents = contents.FlattenContents()
	}
	result, err := s.base.UploadObject(ctx, reference.Flatten(), flatContents, nil, false)
	if err != nil {
		return nil, err
	}

	switch result.(type) {
	case object.UploadObjectComplete[struct{}]:
		if degree := reference.GetDegree(); degree > 0 {
			// The object has outgoing references. Make sure
			// we have access to them if the caller did not
			// provide the object contents explicitly.
			if contents == nil {
				contents, err = s.DownloadObject(ctx, reference)
				if err != nil {
					if status.Code(err) == codes.NotFound {
						return nil, status.Error(codes.Internal, "Backend reported that the object does not exist, even though uploading it reported it as being complete")
					}
					return nil, err
				}
			}

			// Lease renewing involves calling UploadObject()
			// without children leases first, followed by
			// another one where any expired leases are updated.
			//
			// Because there is some time between these calls
			// and we don't want to tell the caller that one or
			// more children leases have expired, be less strict
			// during the second call.
			leaseIncompleteCutoff := leaseNow - s.leaseCompletenessDuration
			leaseExpirationCutoff := leaseIncompleteCutoff - s.leaseCompletenessDuration
			if len(childrenLeases) > 0 {
				leaseIncompleteCutoff = leaseExpirationCutoff
			}

			wantOutgoingReferencesLeases := make([]int, 0, degree)
			s.lock.Lock()
			for i := 0; i < degree; i++ {
				// Insert the leases the caller provided
				// into the cache if they are valid.
				childReference := contents.GetOutgoingReference(i)
				var childLease Lease
				if len(childrenLeases) > 0 {
					childLease = childrenLeases[i]
					if childLease > leaseNow {
						childLease = leaseNow
					}
					if childLease >= leaseExpirationCutoff {
						if err := s.leasesMap.Put(childReference, childLease, leaseExpirationCutoff); err != nil {
							s.lock.Unlock()
							return nil, util.StatusWrapf(err, "Failed to update lease for reference %#v", childReference.String())
						}
					}
				}

				// Look up lease in the cache if the
				// client provided none that is valid.
				if childLease < leaseIncompleteCutoff {
					if storedChildLease, err := s.leasesMap.Get(childReference, leaseExpirationCutoff); err == nil {
						childLease = storedChildLease
					} else if status.Code(err) != codes.NotFound {
						s.lock.Unlock()
						return nil, util.StatusWrapf(err, "Failed to obtain lease for reference %#v", childReference.String())
					}
				}
				if childLease < leaseIncompleteCutoff {
					wantOutgoingReferencesLeases = append(wantOutgoingReferencesLeases, i)
				}
			}
			s.lock.Unlock()

			if len(wantOutgoingReferencesLeases) > 0 {
				result := object.UploadObjectIncomplete[Lease]{
					WantOutgoingReferencesLeases: wantOutgoingReferencesLeases,
				}
				if wantContentsIfIncomplete {
					result.Contents = contents
				}
				return result, nil
			}
		}

		return object.UploadObjectComplete[Lease]{
			Lease: leaseNow,
		}, nil
	case object.UploadObjectMissing[struct{}]:
		return object.UploadObjectMissing[Lease]{}, nil
	default:
		panic("unknown upload object result type")
	}
}
