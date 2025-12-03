package core

import (
	model_core_pb "bonanza.build/pkg/proto/model/core"
	"bonanza.build/pkg/storage/object"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// Message is a piece of data, typically a Protobuf message, that has
// zero or more outgoing references associated with it.
type Message[TMessage any, TReference any] struct {
	Message            TMessage
	OutgoingReferences object.OutgoingReferences[TReference]
}

// NewMessage is a helper function for creating instances of Message.
func NewMessage[TMessage, TReference any](
	m TMessage,
	outgoingReferences object.OutgoingReferences[TReference],
) Message[TMessage, TReference] {
	return Message[TMessage, TReference]{
		Message:            m,
		OutgoingReferences: outgoingReferences,
	}
}

// NewRawMessage creates a Message whose contents correspond to the
// payload of an object in storage. Typically additional processing
// needs to be performed to consume this data, such as decoding and
// parsing.
func NewRawMessage(contents *object.Contents) Message[[]byte, object.LocalReference] {
	return Message[[]byte, object.LocalReference]{
		Message:            contents.GetPayload(),
		OutgoingReferences: contents,
	}
}

// NewSimpleMessage is a helper function for creating instances of
// Message for messages that don't contain any references.
func NewSimpleMessage[TReference, TMessage any](m TMessage) Message[TMessage, TReference] {
	return Message[TMessage, TReference]{
		Message:            m,
		OutgoingReferences: object.OutgoingReferences[TReference](object.OutgoingReferencesList[TReference]{}),
	}
}

// Nested is a helper function for creating instances of Message that
// refer to a message that was mebedded into another one.
func Nested[TMessage1, TMessage2, TReference any](parent Message[TMessage1, TReference], child TMessage2) Message[TMessage2, TReference] {
	return Message[TMessage2, TReference]{
		Message:            child,
		OutgoingReferences: parent.OutgoingReferences,
	}
}

// IsSet returns true if the instance of Message is initialized.
func (m Message[TMessage, TReference]) IsSet() bool {
	return m.OutgoingReferences != nil
}

// Clear the Message, releasing the data and outgoing references that
// are associated with it.
func (m *Message[TMessage, TReference]) Clear() {
	*m = Message[TMessage, TReference]{}
}

// FlattenReference returns the actual reference that is associated with
// a given Reference Protobuf message.
func FlattenReference[TReference any](m Message[*model_core_pb.Reference, TReference]) (TReference, error) {
	index, err := GetIndexFromReferenceMessage(m.Message, m.OutgoingReferences.GetDegree())
	if err != nil {
		var badReference TReference
		return badReference, err
	}
	return m.OutgoingReferences.GetOutgoingReference(index), nil
}

// FlattenDecodableReference returns the actual reference that is
// associated with a given DecodableReference Protobuf message, and
// attaches decoding parameters to it.
func FlattenDecodableReference[TReference any](m Message[*model_core_pb.DecodableReference, TReference]) (Decodable[TReference], error) {
	var badReference Decodable[TReference]
	if m.Message == nil {
		return badReference, status.Error(codes.InvalidArgument, "No decodable reference message provided")
	}
	reference, err := FlattenReference(Nested(m, m.Message.Reference))
	if err != nil {
		return badReference, err
	}
	return NewDecodable[TReference](reference, m.Message.DecodingParameters)
}

// FlattenReferenceSet returns the actual references that are contained
// in a given ReferenceSet Protobuf message.
func FlattenReferenceSet[TReference any](m Message[*model_core_pb.ReferenceSet, TReference]) (object.OutgoingReferences[TReference], error) {
	if m.Message == nil {
		return nil, status.Error(codes.InvalidArgument, "No reference set message provided")
	}

	// Messages without outgoing references can be fully detached
	// from their parent.
	indices := m.Message.Indices
	if len(indices) == 0 {
		return object.OutgoingReferencesList[TReference]{}, nil
	}

	// Check that all indices are in bounds. Instead of validating
	// each index separately, require that they are provided in
	// sorted order and that the first and last index are in range.
	// This is more strict and equally expensive to check.
	for i := 1; i < len(indices); i++ {
		if indices[i-1] >= indices[i] {
			return object.OutgoingReferencesList[TReference]{}, status.Errorf(codes.InvalidArgument, "References at indices %d and %d are not properly sorted", i-1, i)
		}
	}
	degree := m.OutgoingReferences.GetDegree()
	if firstIndex, lastIndex := indices[0], indices[len(indices)-1]; firstIndex <= 0 || int64(lastIndex) > int64(degree) {
		return object.OutgoingReferencesList[TReference]{}, status.Errorf(codes.InvalidArgument, "Reference message set contains indices in range [%d, %d], which is outside expected range [1, %d]", firstIndex, lastIndex, degree)
	}

	// Instead of eagerly remapping all references, return a
	// decorator that only remaps references when requested.
	return &remappingOutgoingReferences[TReference]{
		indices: indices,
		outer:   m.OutgoingReferences,
	}, nil
}

// remappingOutgoingReferences is a wrapper for OutgoingReferences that
// limits access to references contained in a ReferenceSet.
type remappingOutgoingReferences[TReference any] struct {
	indices []uint32
	outer   object.OutgoingReferences[TReference]
}

func (or *remappingOutgoingReferences[TReference]) GetDegree() int {
	return len(or.indices)
}

func (or *remappingOutgoingReferences[TReference]) GetOutgoingReference(index int) TReference {
	return or.outer.GetOutgoingReference(int(or.indices[index] - 1))
}

func (or *remappingOutgoingReferences[TReference]) DetachOutgoingReferences() object.OutgoingReferences[TReference] {
	list := make(object.OutgoingReferencesList[TReference], len(or.indices))
	for _, index := range or.indices {
		list = append(list, or.outer.GetOutgoingReference(int(or.indices[index]-1)))
	}
	return list
}

// FlattenAny extracts the anypb.Any message that's embedded in a
// model_core_pb.Any message. The resulting message only has access to
// the outgoing references of the inner message.
func FlattenAny[TReference any](m Message[*model_core_pb.Any, TReference]) (TopLevelMessage[*anypb.Any, TReference], error) {
	outgoingReferences, err := FlattenReferenceSet(Nested(m, m.Message.GetReferences()))
	if err != nil {
		return TopLevelMessage[*anypb.Any, TReference]{}, err
	}
	return NewTopLevelMessage(m.Message.Value, outgoingReferences), nil
}

// UnmarshalAnyNew extracts the message contained in a model_core.Any,
// and ensures that any references contained within are accessible.
func UnmarshalAnyNew[TReference any](m Message[*model_core_pb.Any, TReference]) (TopLevelMessage[proto.Message, TReference], error) {
	flattened, err := FlattenAny(m)
	if err != nil {
		return TopLevelMessage[proto.Message, TReference]{}, err
	}
	return UnmarshalTopLevelAnyNew(flattened)
}

// MessagesEqual returns true if two messages contain the same data.
//
// Nested messages belonging to different objects may contain the same
// data, but use different reference numbers. This function therefore
// needs to convert the provided messages to top-level messages, which
// is expensive. We therefore compare the message size first.
func MessagesEqual[
	TMessage proto.Message,
	TReference1, TReference2 object.BasicReference,
](m1 Message[TMessage, TReference1], m2 Message[TMessage, TReference2]) bool {
	if marshalOptions.Size(m1.Message) != marshalOptions.Size(m2.Message) {
		return false
	}

	tlm1, _ := Patch(NewDiscardingObjectCapturer[TReference1](), m1).SortAndSetReferences()
	tlm2, _ := Patch(NewDiscardingObjectCapturer[TReference2](), m2).SortAndSetReferences()
	return TopLevelMessagesEqual(tlm1, tlm2)
}

// MessagesEqualList returns true if two messages contain the same list
// of Protobuf message.
//
// Nested messages belonging to different objects may contain the same
// data, but use different reference numbers. This function therefore
// needs to convert each of the provided pairs of elements to top-level
// messages, which is expensive. We therefore compare the message size
// first.
func MessagesEqualList[
	TMessage proto.Message,
	TReference1, TReference2 object.BasicReference,
](m1 Message[[]TMessage, TReference1], m2 Message[[]TMessage, TReference2]) bool {
	if len(m1.Message) != len(m2.Message) {
		return false
	}
	for i := 0; i < len(m1.Message); i++ {
		if marshalOptions.Size(m1.Message[i]) != marshalOptions.Size(m2.Message[i]) {
			return false
		}
	}
	for i := 0; i < len(m1.Message); i++ {
		tlm1, _ := Patch(
			NewDiscardingObjectCapturer[TReference1](),
			Nested(m1, m1.Message[i]),
		).SortAndSetReferences()
		tlm2, _ := Patch(
			NewDiscardingObjectCapturer[TReference2](),
			Nested(m2, m2.Message[i]),
		).SortAndSetReferences()
		if !TopLevelMessagesEqual(tlm1, tlm2) {
			return false
		}
	}
	return true
}
