package flatbacked

import (
	"time"

	"bonanza.build/pkg/encoding/varint"
	"bonanza.build/pkg/storage/object/leasemarshaling"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Lease against objects that are stored in a flat storage backend. In
// the case of this implementation, it is a 64-bit UNIX timestamp in
// nanoseconds. The object (and all of its children) is guaranteed to be
// present as long as the watermark of the cluster does not exceed this
// timestamp. The zero value indicates that no lease is held.
//
// A UNIX timestamp is chosen, because it allows leases to be shared
// between shards. Taking the minimum of the leases of an object's
// children yields a lease that covers the whole DAG, regardless of the
// shards on which its objects are stored.
type Lease uint64

// NewLeaseFromTime converts a point in time to a lease. The zero time
// is converted to the zero lease.
func NewLeaseFromTime(t time.Time) Lease {
	if t.IsZero() || t.Before(time.Unix(0, 0)) {
		return 0
	}
	return Lease(t.UnixNano())
}

// GetLeaseRenewalCutoff computes the lease below which leases should be
// renewed proactively. It lies leaseRenewalFraction of the cluster's
// retention (i.e., the time between the watermark and the current
// time) above the watermark.
func GetLeaseRenewalCutoff(watermark, now time.Time, leaseRenewalFraction float64) time.Time {
	if !now.After(watermark) {
		return watermark
	}
	return watermark.Add(time.Duration(float64(now.Sub(watermark)) * leaseRenewalFraction))
}

type leaseMarshaler struct{}

func (leaseMarshaler) MarshalLease(lease Lease, dst []byte) []byte {
	return varint.AppendForward(dst, lease)
}

func (leaseMarshaler) UnmarshalLease(src []byte) (Lease, error) {
	v, n := varint.ConsumeForward[Lease](src)
	if n != len(src) {
		return 0, status.Error(codes.InvalidArgument, "Malformed lease")
	}
	return v, nil
}

// LeaseMarshaler can marshal and unmarshal leases used by the local
// object store, so that they can be transmitted as part of gRPC
// requests and responses. This necessary to make sharding work.
var LeaseMarshaler leasemarshaling.LeaseMarshaler[Lease] = leaseMarshaler{}
