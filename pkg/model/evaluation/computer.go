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

// Computer of values belonging to keys. Keys are always provided in the
// form of a Protobuf message. The resulting values can either be
// Protobuf messages, or ones belonging to native Go types.
type Computer[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	ComputeMessageValue(ctx context.Context, key model_core.Message[proto.Message, TReference], e Environment[TReference, TMetadata]) (model_core.PatchedMessage[proto.Message, TMetadata], error)
	ComputeNativeValue(ctx context.Context, key model_core.Message[proto.Message, TReference], e Environment[TReference, TMetadata]) (any, error)
}

// ComputerForTesting is used to generate mocks that are used by
// RecursiveComputer's unit tests.
type ComputerForTesting Computer[object.LocalReference, model_core.ReferenceMetadata]
