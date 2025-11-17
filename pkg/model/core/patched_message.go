package core

import (
	"encoding"
	"math"

	model_encoding "bonanza.build/pkg/model/encoding"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	"bonanza.build/pkg/storage/object"

	"google.golang.org/protobuf/proto"
)

// PatchedMessage is a tuple for storing a Protobuf message that
// contains model_core_pb.Reference messages, and the associated
// ReferenceMessagePatcher that can be used to assign indices to these
// references.
type PatchedMessage[TMessage any, TMetadata ReferenceMetadata] struct {
	Message TMessage
	Patcher *ReferenceMessagePatcher[TMetadata]
}

// NewPatchedMessage creates a PatchedMessage, given an existing Protobuf
// message and reference message patcher.
func NewPatchedMessage[TMessage any, TMetadata ReferenceMetadata](
	message TMessage,
	patcher *ReferenceMessagePatcher[TMetadata],
) PatchedMessage[TMessage, TMetadata] {
	return PatchedMessage[TMessage, TMetadata]{
		Message: message,
		Patcher: patcher,
	}
}

// NewPatchedMessageFromExisting creates a PatchedMessage, given an
// existing Protobuf message that may contain one or more references to
// other objects. For each reference that is found, a callback is
// invoked to create metadata to associate with the reference.
func NewPatchedMessageFromExisting[
	TMessage proto.Message,
	TMetadata ReferenceMetadata,
	TReference object.BasicReference,
](
	existing Message[TMessage, TReference],
	createMetadata ReferenceMetadataCreator[TMetadata],
) PatchedMessage[TMessage, TMetadata] {
	return MustBuildPatchedMessage(func(patcher *ReferenceMessagePatcher[TMetadata]) TMessage {
		if existing.OutgoingReferences.GetDegree() == 0 {
			return existing.Message
		}

		clonedMessage := proto.Clone(existing.Message)
		a := referenceMessageAdder[TMetadata, TReference]{
			patcher:            patcher,
			outgoingReferences: existing.OutgoingReferences,
			createMetadata:     createMetadata,
		}
		a.addReferenceMessagesRecursively(clonedMessage.ProtoReflect())
		return clonedMessage.(TMessage)
	})
}

// NewSimplePatchedMessage is a helper function for creating instances
// of PatchedMessage for messages that don't contain any references.
func NewSimplePatchedMessage[TMetadata ReferenceMetadata, TMessage any](v TMessage) PatchedMessage[TMessage, TMetadata] {
	return NewPatchedMessage(v, NewReferenceMessagePatcher[TMetadata]())
}

// BuildPatchedMessage is a convenience function for constructing
// PatchedMessage that has a newly created ReferenceMessagePatcher
// attached to it. A callback is invoked for creating a message that is
// managed by the ReferenceMessagePatcher. If the callback returns an
// error, any intermediate metadata is discarded.
func BuildPatchedMessage[TMessage any, TMetadata ReferenceMetadata](
	builder func(*ReferenceMessagePatcher[TMetadata]) (TMessage, error),
) (PatchedMessage[TMessage, TMetadata], error) {
	patcher := NewReferenceMessagePatcher[TMetadata]()
	m, err := builder(patcher)
	if err != nil {
		patcher.Discard()
		return PatchedMessage[TMessage, TMetadata]{}, err
	}
	return NewPatchedMessage(m, patcher), nil
}

// MustBuildPatchedMessage is equivalent to BuildPatchedMessage, except
// that the callback that is invoked may not fail.
func MustBuildPatchedMessage[TMessage any, TMetadata ReferenceMetadata](
	builder func(*ReferenceMessagePatcher[TMetadata]) TMessage,
) PatchedMessage[TMessage, TMetadata] {
	patcher := NewReferenceMessagePatcher[TMetadata]()
	return NewPatchedMessage(builder(patcher), patcher)
}

// IsSet returns true if the PatchedMessage is assigned to a message
// and its associated reference message patcher.
func (m PatchedMessage[T, TMetadata]) IsSet() bool {
	return m.Patcher != nil
}

// Clear the instance of PatchedMessage, disassociating it from its
// message and reference message patcher. The reference message patcher
// is not discarded, meaning that any resources owned by reference
// metadata is not released.
func (m *PatchedMessage[T, TMetadata]) Clear() {
	*m = PatchedMessage[T, TMetadata]{}
}

// Discard the reference message patcher, releasing any resources owned
// by reference metadata.
func (m *PatchedMessage[T, TMetadata]) Discard() {
	if m.Patcher != nil {
		m.Patcher.Discard()
	}
	m.Clear()
}

// Move the message out of the PatchedMessage by copying it and clearing
// the original instance.
func (m *PatchedMessage[T, TMetadata]) Move() PatchedMessage[T, TMetadata] {
	defer m.Clear()
	return *m
}

// Merge the references contained in the patched message into a provided
// patcher, and return the message that is now owned by the provided
// patcher.
func (m PatchedMessage[T, TMetadata]) Merge(patcher *ReferenceMessagePatcher[TMetadata]) T {
	patcher.Merge(m.Patcher)
	return m.Message
}

// SortAndSetReferences assigns indices to outgoing references.
func (m PatchedMessage[T, TMetadata]) SortAndSetReferences() (TopLevelMessage[T, object.LocalReference], []TMetadata) {
	references, metadata := m.Patcher.SortAndSetReferences()
	return NewTopLevelMessage(m.Message, references), metadata
}

// PatchedMessagesEqual returns true if two patched messages contain the
// same data.
//
// Nested messages belonging to different objects may contain the same
// data, but use different reference numbers. This function therefore
// needs to convert the provided messages to top-level messages, which
// is expensive. We therefore compare the message size first.
func PatchedMessagesEqual[
	TMessage proto.Message,
	TMetadata1, TMetadata2 ReferenceMetadata,
](m1 PatchedMessage[TMessage, TMetadata1], m2 PatchedMessage[TMessage, TMetadata2]) bool {
	if marshalOptions.Size(m1.Message) != marshalOptions.Size(m2.Message) {
		return false
	}

	tlm1, _ := m1.SortAndSetReferences()
	tlm2, _ := m2.SortAndSetReferences()
	return TopLevelMessagesEqual(tlm1, tlm2)
}

// MarshalAndEncodeDeterministic marshals a patched message, encodes it
// deterministically, and converts it to an object that can be written
// to storage.
func MarshalAndEncodeDeterministic[TMetadata ReferenceMetadata](
	m PatchedMessage[encoding.BinaryMarshaler, TMetadata],
	referenceFormat object.ReferenceFormat,
	encoder model_encoding.DeterministicBinaryEncoder,
) (Decodable[CreatedObject[TMetadata]], error) {
	references, metadata := m.Patcher.SortAndSetReferences()
	data, err := m.Message.MarshalBinary()
	if err != nil {
		m.Discard()
		return Decodable[CreatedObject[TMetadata]]{}, err
	}
	encodedData, decodingParameters, err := encoder.EncodeBinary(data)
	if err != nil {
		m.Discard()
		return Decodable[CreatedObject[TMetadata]]{}, err
	}
	contents, err := referenceFormat.NewContents(references, encodedData)
	if err != nil {
		m.Discard()
		return Decodable[CreatedObject[TMetadata]]{}, err
	}
	return NewDecodable(
		CreatedObject[TMetadata]{
			Contents: contents,
			Metadata: metadata,
		},
		decodingParameters,
	)
}

// MarshalAny wraps a patched message into a model_core_pb.Any message.
// References stored in the original message are kept intact.
func MarshalAny[TMessage proto.Message, TMetadata ReferenceMetadata](m PatchedMessage[TMessage, TMetadata]) (PatchedMessage[*model_core_pb.Any, TMetadata], error) {
	topLevelMessage, metadata := m.SortAndSetReferences()
	anyMessage, err := MarshalTopLevelAny(topLevelMessage)
	if err != nil {
		m.Discard()
		return PatchedMessage[*model_core_pb.Any, TMetadata]{}, err
	}

	// Construct a table for remapping references contained in the
	// message to indices on the other message.
	degree := anyMessage.OutgoingReferences.GetDegree()
	indices := make([]uint32, 0, degree)
	for i := 0; i < degree; i++ {
		indices = append(indices, math.MaxUint32)
	}

	// Create a new reference message patcher that contains the same
	// references and metadata as the original message, but instead
	// managing the table used for remapping.
	newPatcher := &ReferenceMessagePatcher[TMetadata]{
		messagesByReference: make(map[object.LocalReference]referenceMessages[TMetadata], degree),
		height:              m.Patcher.height,
	}
	for i := 0; i < degree; i++ {
		newPatcher.messagesByReference[anyMessage.OutgoingReferences.GetOutgoingReference(i)] = referenceMessages[TMetadata]{
			metadata: metadata[i],
			indices:  []*uint32{&indices[i]},
		}
	}

	return NewPatchedMessage(
		&model_core_pb.Any{
			Value: anyMessage.Message,
			References: &model_core_pb.ReferenceSet{
				Indices: indices,
			},
		},
		newPatcher,
	), nil
}

func ProtoToBinaryMarshaler[TMessage proto.Message, TMetadata ReferenceMetadata](m PatchedMessage[TMessage, TMetadata]) PatchedMessage[encoding.BinaryMarshaler, TMetadata] {
	return NewPatchedMessage(
		NewProtoBinaryMarshaler(m.Message),
		m.Patcher,
	)
}

func ProtoListToBinaryMarshaler[TMessage proto.Message, TMetadata ReferenceMetadata](m PatchedMessage[[]TMessage, TMetadata]) PatchedMessage[encoding.BinaryMarshaler, TMetadata] {
	return NewPatchedMessage(
		NewProtoListBinaryMarshaler(m.Message),
		m.Patcher,
	)
}

// FlattenPatchedSingletonList converts a PatchedMessage containing a
// single element list to a PatchedMessage containing just that single
// element.
//
// A dedicated function for this is provided, as this is one of the few
// cases in which obtaining a PatchedMessage for a nested message value
// is safe. In the general case it is not, as it would cause the
// resulting PatchedMessage to contain unused reference metadata.
func FlattenPatchedSingletonList[TMessage any, TMetadata ReferenceMetadata](m PatchedMessage[[]TMessage, TMetadata]) PatchedMessage[TMessage, TMetadata] {
	if len(m.Message) != 1 {
		panic("list does not consist of a single element")
	}
	return NewPatchedMessage(m.Message[0], m.Patcher)
}
