package evaluation

import (
	"context"
	"errors"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/storage/object"

	"google.golang.org/protobuf/proto"
)

// ErrMissingDependency is an error that can be returned by
// Computer.Compute*Value() to indicate that a value could not be
// computed due to a dependent value being missing. Retrying the
// computation after the values of dependencies will allow further
// progress.
var ErrMissingDependency = errors.New("missing dependency")

// Computer of values belonging to keys. Both keys and values are always
// provided in the form of a Protobuf message.
//
// Values may have an object associated with them, consisting of an
// arbitrary Go value that is derived from the message. These are useful
// for values that are expensive to convert to the form in which they
// are used, or for which it is desirable that only a single instance
// exists. Objects are not written to the cache, and can't depend on
// anything other than the value.
type Computer[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	ComputeValue(ctx context.Context, key model_core.Message[proto.Message, TReference], e Environment[TReference, TMetadata]) (model_core.PatchedMessage[proto.Message, TMetadata], error)
	ComputeObject(value model_core.Message[proto.Message, TReference]) (any, error)
	IsLookup(typeURL string) bool
}

// ComputerForTesting is used to generate mocks that are used by
// RecursiveComputer's unit tests.
type ComputerForTesting Computer[object.LocalReference, model_core.ReferenceMetadata]
