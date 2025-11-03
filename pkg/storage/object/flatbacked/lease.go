package flatbacked

import (
	"bonanza.build/pkg/encoding/varint"
	"bonanza.build/pkg/storage/object/leasemarshaling"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Lease against objects that are stored in a flat storage backend. In
// the case of this implementation, it is a 64-bit UNIX timestamp at
// which the existence of the object (and all of its children) was last
// checked.
//
// A UNIX timestamp is chosen, because it allows leases to be shared
// between shards.
type Lease uint64

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
