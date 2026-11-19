package core

import (
	"google.golang.org/protobuf/proto"
)

// marshalOptions that are used to marshal Protobuf messages. These
// need to be kept in sync with the ones in pkg/model/format, as
// messages need to be encoded identically, regardless of how they are
// marshaled.
var marshalOptions = proto.MarshalOptions{
	Deterministic: true,
	UseCachedSize: true,
}
