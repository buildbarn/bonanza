package evaluation

import (
	model_core "bonanza.build/pkg/model/core"
	model_format "bonanza.build/pkg/model/format"

	"google.golang.org/protobuf/proto"
)

// Environment that is provided to Computer.Compute*Value() to obtain
// access to values of other keys, and to attach Merkle tree nodes to
// computed keys and values.
type Environment[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	model_core.ObjectManager[TReference, TMetadata]

	// Methods that implementations of Computer can invoke to get
	// access to the value of another key, or the object derived from it.
	//
	// As values are stored without type information, callers of
	// GetValue() need to provide an unmarshaler for the value
	// belonging to the key. Objects are computed by
	// Computer.ComputeObject(), which is responsible for
	// unmarshaling the value itself.
	GetValue(key model_core.PatchedMessage[proto.Message, TMetadata], valueUnmarshaler model_format.Unmarshaler[any]) model_core.TopLevelMessage[any, TReference]
	GetObject(key model_core.PatchedMessage[proto.Message, TMetadata]) (any, bool)
}
