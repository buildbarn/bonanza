package evaluation

import (
	model_core "bonanza.build/pkg/model/core"

	"google.golang.org/protobuf/proto"
)

// Environment that is provided to Computer.Compute*Value() to obtain
// access to values of other keys, and to attach Merkle tree nodes to
// computed keys and values.
type Environment[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	model_core.ObjectManager[TReference, TMetadata]

	// Methods that implementations of Computer can invoke to get
	// access to the value of another key.
	GetMessageValue(key model_core.PatchedMessage[proto.Message, TMetadata]) model_core.Message[proto.Message, TReference]
	GetNativeValue(key model_core.PatchedMessage[proto.Message, TMetadata]) (any, bool)
}
