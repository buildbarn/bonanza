package evaluation

import (
	"context"
	"fmt"

	model_core "bonanza.build/pkg/model/core"

	"google.golang.org/protobuf/proto"
)

type leakCheckingComputer[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	base Computer[TReference, *model_core.LeakCheckingReferenceMetadata[TMetadata]]
}

// NewLeakCheckingComputer creates a decorator for Computer that keeps
// track of all ReferenceMetadata objects that a call to Compute*Value()
// creates. When any computation causes ReferenceMetadata objects to
// leak, an error is reported.
func NewLeakCheckingComputer[TReference any, TMetadata model_core.ReferenceMetadata](base Computer[TReference, *model_core.LeakCheckingReferenceMetadata[TMetadata]]) Computer[TReference, TMetadata] {
	return &leakCheckingComputer[TReference, TMetadata]{
		base: base,
	}
}

func (c *leakCheckingComputer[TReference, TMetadata]) ComputeMessageValue(ctx context.Context, key model_core.Message[proto.Message, TReference], e Environment[TReference, TMetadata]) (model_core.PatchedMessage[proto.Message, TMetadata], error) {
	objectManager := model_core.NewLeakCheckingObjectManager(e)
	value, err := c.base.ComputeMessageValue(
		ctx,
		key,
		&leakCheckingEnvironment[TReference, TMetadata]{
			ObjectManager: objectManager,
			environment:   e,
		},
	)
	if err != nil {
		if c := objectManager.GetCurrentValidMetadataCount(); c != 0 {
			return model_core.PatchedMessage[proto.Message, TMetadata]{}, fmt.Errorf("leaked %d metadata values", c)
		}
		return model_core.PatchedMessage[proto.Message, TMetadata]{}, err
	}

	// Unwrap ReferenceMetadata objects created by the base implementation.
	mappedValue := model_core.NewPatchedMessage(
		value.Message,
		model_core.MapReferenceMessagePatcherMetadata(
			value.Patcher,
			func(entry model_core.MetadataEntry[*model_core.LeakCheckingReferenceMetadata[TMetadata]]) TMetadata {
				return entry.Metadata.Unwrap()
			},
		),
	)
	if c := objectManager.GetCurrentValidMetadataCount(); c != 0 {
		return model_core.PatchedMessage[proto.Message, TMetadata]{}, fmt.Errorf("leaked %d metadata values", c)
	}
	return mappedValue, nil
}

func (c *leakCheckingComputer[TReference, TMetadata]) ComputeNativeValue(ctx context.Context, key model_core.Message[proto.Message, TReference], e Environment[TReference, TMetadata]) (any, error) {
	objectManager := model_core.NewLeakCheckingObjectManager(e)
	value, err := c.base.ComputeNativeValue(
		ctx,
		key,
		&leakCheckingEnvironment[TReference, TMetadata]{
			ObjectManager: objectManager,
			environment:   e,
		},
	)
	if c := objectManager.GetCurrentValidMetadataCount(); c != 0 {
		return nil, fmt.Errorf("leaked %d metadata values", c)
	}
	return value, err
}

// leakCheckingEnvironment is a decorator for Environment that unwraps
// ReferenceMetadata objects provided to Get*Value(). This is needed,
// because the base implementation of Environment takes ownership of any
// ReferenceMetadata objects provided to these methods.
type leakCheckingEnvironment[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	model_core.ObjectManager[TReference, *model_core.LeakCheckingReferenceMetadata[TMetadata]]
	environment Environment[TReference, TMetadata]
}

func (e *leakCheckingEnvironment[TReference, TMetadata]) GetMessageValue(key model_core.PatchedMessage[proto.Message, *model_core.LeakCheckingReferenceMetadata[TMetadata]]) model_core.Message[proto.Message, TReference] {
	return e.environment.GetMessageValue(
		model_core.NewPatchedMessage(
			key.Message,
			model_core.MapReferenceMessagePatcherMetadata(
				key.Patcher,
				func(entry model_core.MetadataEntry[*model_core.LeakCheckingReferenceMetadata[TMetadata]]) TMetadata {
					return entry.Metadata.Unwrap()
				},
			),
		),
	)
}

func (e *leakCheckingEnvironment[TReference, TMetadata]) GetNativeValue(key model_core.PatchedMessage[proto.Message, *model_core.LeakCheckingReferenceMetadata[TMetadata]]) (any, bool) {
	return e.environment.GetNativeValue(
		model_core.NewPatchedMessage(
			key.Message,
			model_core.MapReferenceMessagePatcherMetadata(
				key.Patcher,
				func(entry model_core.MetadataEntry[*model_core.LeakCheckingReferenceMetadata[TMetadata]]) TMetadata {
					return entry.Metadata.Unwrap()
				},
			),
		),
	)
}
