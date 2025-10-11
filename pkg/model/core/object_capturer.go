package core

import (
	"context"

	"bonanza.build/pkg/storage/dag"
	"bonanza.build/pkg/storage/object"

	"google.golang.org/protobuf/proto"
)

// CreatedObjectCapturer can be used as a factory type for reference
// metadata. Given the contents of an object and the metadata of all of
// its children, it may yield new metadata.
type CreatedObjectCapturer[TMetadata any] interface {
	CaptureCreatedObject(ctx context.Context, createdObject CreatedObject[TMetadata]) (TMetadata, error)
}

// CreatedObjectCapturerFunc has the same signature as
// CreatedObjectCapturer.CaptureCreatedObject(). It is provided to allow
// a single type to provide multiple capturing functions, and to pass
// each of these methods to a function taking a CreatedObjectCapturer.
type CreatedObjectCapturerFunc[TMetadata any] func(ctx context.Context, createdObject CreatedObject[TMetadata]) (TMetadata, error)

// CaptureCreatedObject forwards the call to create reference metadata
// to the underlying CreatedObjectCapturerFunc.
func (f CreatedObjectCapturerFunc[TMetadata]) CaptureCreatedObject(ctx context.Context, createdObject CreatedObject[TMetadata]) (TMetadata, error) {
	return f(ctx, createdObject)
}

type walkableCreatedObjectCapturer struct{}

// WalkableCreatedObjectCapturer is an implementation of ObjectCapturer
// that creates a dag.ObjectContentsWalker for each created object. This
// ends up keeping objects in memory and only allows them to be
// traversed as part of the upload process.
//
// This implementation is sufficient when given existing Merkle trees in
// the form of a dag.ObjectContentsWalkers that need to be combined into
// single Merkle tree.
var WalkableCreatedObjectCapturer CreatedObjectCapturer[dag.ObjectContentsWalker] = walkableCreatedObjectCapturer{}

func (walkableCreatedObjectCapturer) CaptureCreatedObject(ctx context.Context, createdObject CreatedObject[dag.ObjectContentsWalker]) (dag.ObjectContentsWalker, error) {
	return dag.NewSimpleObjectContentsWalker(createdObject.Contents, createdObject.Metadata), nil
}

// ExistingObjectCapturer can be used as a factory type for reference
// metadata. Given a reference of an object that already exists in
// storage, it may yield metadata.
type ExistingObjectCapturer[TReference, TMetadata any] interface {
	CaptureExistingObject(TReference) TMetadata
}

// CaptureExistingObject creates reference metadata for an object that
// already exists, and returns it in the form of a MetadataEntry that
// can be passed to ReferenceMessagePatcher.AddReference().
func CaptureExistingObject[TReference object.BasicReference, TMetadata any](
	capturer ExistingObjectCapturer[TReference, TMetadata],
	reference TReference,
) MetadataEntry[TMetadata] {
	return MetadataEntry[TMetadata]{
		LocalReference: reference.GetLocalReference(),
		Metadata:       capturer.CaptureExistingObject(reference),
	}
}

// ObjectCapturer is a combination of CreatedObjectCapturer and
// ExistingObjectCapturer, allowing the construction of metadata both
// for newly created objects and ones that exist in storage.
type ObjectCapturer[TReference, TMetadata any] interface {
	CreatedObjectCapturer[TMetadata]
	ExistingObjectCapturer[TReference, TMetadata]
}

// Patch an existing message, copying the message and causing all
// containing references to be managed by a ReferenceMessagePatcher. For
// each references contained within, metadata is created by calling into
// an ExistingObjectCapturer.
func Patch[
	TMessage proto.Message,
	TMetadata ReferenceMetadata,
	TReference object.BasicReference,
](
	capturer ExistingObjectCapturer[TReference, TMetadata],
	m Message[TMessage, TReference],
) PatchedMessage[TMessage, TMetadata] {
	return NewPatchedMessageFromExisting(
		m,
		func(index int) TMetadata {
			return capturer.CaptureExistingObject(
				m.OutgoingReferences.GetOutgoingReference(index),
			)
		},
	)
}

// PatchList is identical to Patch(), except that it patches a list of
// messages.
func PatchList[
	TMessage any,
	TMessagePtr interface {
		*TMessage
		proto.Message
	},
	TMetadata ReferenceMetadata,
	TReference object.BasicReference,
](
	capturer ExistingObjectCapturer[TReference, TMetadata],
	existingList Message[[]*TMessage, TReference],
) PatchedMessage[[]*TMessage, TMetadata] {
	return MustBuildPatchedMessage(func(patcher *ReferenceMessagePatcher[TMetadata]) []*TMessage {
		if existingList.OutgoingReferences.GetDegree() == 0 {
			return existingList.Message
		}

		a := referenceMessageAdder[TMetadata, TReference]{
			patcher:            patcher,
			outgoingReferences: existingList.OutgoingReferences,
			createMetadata: func(index int) TMetadata {
				return capturer.CaptureExistingObject(
					existingList.OutgoingReferences.GetOutgoingReference(index),
				)
			},
		}

		newList := make([]*TMessage, 0, len(existingList.Message))
		for _, element := range existingList.Message {
			clonedElement := proto.Clone(TMessagePtr(element))
			a.addReferenceMessagesRecursively(clonedElement.ProtoReflect())
			newList = append(newList, clonedElement.(TMessagePtr))
		}
		return newList
	})
}

type ObjectReferencer[TReference, TMetadata any] interface {
	ReferenceObject(MetadataEntry[TMetadata]) TReference
}

// Unpatch performs the opposite of Patch(). Namely, it assigns indices
// to all references contained in the current message. Metadata that is
// tracked by the ReferenceMessagePatcher is converted to references by
// calling into an ObjectReferencer.
func Unpatch[TMessage, TReference any, TMetadata ReferenceMetadata](
	referencer ObjectReferencer[TReference, TMetadata],
	m PatchedMessage[TMessage, TMetadata],
) TopLevelMessage[TMessage, TReference] {
	references, metadata := m.Patcher.SortAndSetReferences()
	outgoingReferences := make(object.OutgoingReferencesList[TReference], 0, len(metadata))
	for i, m := range metadata {
		outgoingReferences = append(
			outgoingReferences,
			referencer.ReferenceObject(MetadataEntry[TMetadata]{
				LocalReference: references.GetOutgoingReference(i),
				Metadata:       m,
			}),
		)
	}
	return NewTopLevelMessage(m.Message, outgoingReferences)
}

// ObjectManager is an extension to ObjectCapturer, allowing metadata to
// be converted back to references. This can be of use in environments
// where objects also need to be accessible for reading right after they
// have been constructed, without explicitly waiting for them to be
// written to storage.
type ObjectManager[TReference, TMetadata any] interface {
	ObjectCapturer[TReference, TMetadata]
	ObjectReferencer[TReference, TMetadata]
}

// ObjectExporter can be used to exported references obtained from
// ObjectManager to those of another format, and vice versa.
//
// Calls to ExportReference() also need to ensure that any objects
// referenced by the internal reference format are flushed to storage,
// so that the external reference becomes valid. bonanza_builder may use
// this to flush actions to storage, so that any request to execute an
// action on another worker doesn't fail due to missing objects.
type ObjectExporter[TInternal, TExternal any] interface {
	ExportReference(ctx context.Context, internalReference TInternal) (TExternal, error)
	ImportReference(externalReference TExternal) TInternal
}
