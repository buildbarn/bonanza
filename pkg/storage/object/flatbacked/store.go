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
	base                 object.Store[object.FlatReference, time.Time]
	clock                clock.Clock
	getWatermark         func() time.Time
	leaseRenewalFraction float64

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
// compensates for it by handing out timestamp based leases. The base
// storage backend returns a "safe since" timestamp for each object,
// meaning that the object is guaranteed to be present as long as the
// watermark of the cluster does not exceed it. The lease of a parent
// object is the minimum of its own timestamp and that of its children.
//
// The watermark is provided by getWatermark. Leases that are less than
// leaseRenewalFraction of the cluster's retention (i.e., the time
// between the watermark and the current time) above the watermark are
// renewed proactively.
func NewStore(
	base object.Store[object.FlatReference, time.Time],
	clock clock.Clock,
	getWatermark func() time.Time,
	leaseRenewalFraction float64,
	leasesMap LeasesMap,
) object.Store[object.LocalReference, Lease] {
	return &store{
		base:                 base,
		clock:                clock,
		getWatermark:         getWatermark,
		leaseRenewalFraction: leaseRenewalFraction,
		leasesMap:            leasesMap,
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

	switch resultType := result.(type) {
	case object.UploadObjectComplete[time.Time]:
		lease := NewLeaseFromTime(resultType.Lease)
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

			// Leases below the watermark are expired.
			// Leases close to the watermark are reported
			// as being incomplete, so that they get renewed
			// before they expire.
			//
			// Lease renewing involves calling UploadObject()
			// without children leases first, followed by
			// another one where any expired leases are updated.
			//
			// Because there is some time between these calls
			// and we don't want to tell the caller that one or
			// more children leases have expired, be less strict
			// during the second call.
			watermark := s.getWatermark()
			leaseExpirationCutoff := NewLeaseFromTime(watermark)
			leaseIncompleteCutoff := NewLeaseFromTime(GetLeaseRenewalCutoff(watermark, s.clock.Now(), s.leaseRenewalFraction))
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
				} else if lease > childLease {
					// The parent is only present as
					// long as all of its children are.
					lease = childLease
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
			Lease: lease,
		}, nil
	case object.UploadObjectMissing[time.Time]:
		return object.UploadObjectMissing[Lease]{}, nil
	default:
		panic("unknown upload object result type")
	}
}
