package core

import (
	"bonanza.build/pkg/encoding/varint"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// TopLevelMessage is identical to Message, except that it may only
// refer to a message for which all embedded references are
// consecutively numbered, and the outgoing references list contains no
// references in addition to the ones embedded in the message.
//
// As top-level messages are considered being canonical, this type
// provides some additional methods for generating keys and performing
// comparisons.
type TopLevelMessage[TMessage any, TReference any] struct {
	Message            TMessage
	OutgoingReferences object.OutgoingReferences[TReference]
}

// NewTopLevelMessage creates a TopLevelMessage that corresponds to an
// already existing Protobuf message.
func NewTopLevelMessage[TMessage, TReference any](
	m TMessage,
	outgoingReferences object.OutgoingReferences[TReference],
) TopLevelMessage[TMessage, TReference] {
	return TopLevelMessage[TMessage, TReference]{
		Message:            m,
		OutgoingReferences: outgoingReferences,
	}
}

// NewSimpleTopLevelMessage creates a TopLevelMessage that has no
// outgoing references.
func NewSimpleTopLevelMessage[TReference, TMessage any](m TMessage) TopLevelMessage[TMessage, TReference] {
	return TopLevelMessage[TMessage, TReference]{
		Message:            m,
		OutgoingReferences: object.OutgoingReferences[TReference](object.OutgoingReferencesList[TReference]{}),
	}
}

// Decay a top-level message to a plain message.
func (m TopLevelMessage[TMessage, TReference]) Decay() Message[TMessage, TReference] {
	return NewMessage(m.Message, m.OutgoingReferences)
}

// MarshalTopLevelMessage converts the contents of a top-level Protobuf
// message to a byte sequence. This byte sequence can be used to sort
// messages along a total order, or to act as a map key.
func MarshalTopLevelMessage[TMessage proto.Message, TReference object.BasicReference](m TopLevelMessage[TMessage, TReference]) ([]byte, error) {
	degree := m.OutgoingReferences.GetDegree()
	key := varint.AppendForward(nil, degree)
	for i := 0; i < degree; i++ {
		key = append(key, m.OutgoingReferences.GetOutgoingReference(i).GetRawReference()...)
	}
	return marshalOptions.MarshalAppend(key, m.Message)
}

// UnmarshalTopLevelMessage performs the inverse of
// MarshalTopLevelMessage. It can be used to convert a byte sequence to
// a top-level Protobuf message.
func UnmarshalTopLevelMessage[
	TMessage any,
	TMessagePtr interface {
		*TMessage
		proto.Message
	},
](referenceFormat object.ReferenceFormat, data []byte) (TopLevelMessage[TMessagePtr, object.LocalReference], error) {
	// Extract the leading number of outgoing references.
	degree, n := varint.ConsumeForward[uint](data)
	if n < 0 {
		return TopLevelMessage[TMessagePtr, object.LocalReference]{}, status.Error(codes.InvalidArgument, "Failed to extract degree")
	}
	data = data[n:]

	referenceSizeBytes := referenceFormat.GetReferenceSizeBytes()
	if maximumDegree := uint(len(data) / referenceSizeBytes); degree > maximumDegree {
		return TopLevelMessage[TMessagePtr, object.LocalReference]{}, status.Errorf(codes.InvalidArgument, "Message has degree %d, while %d bytes is sufficient to only fit %d outgoing references", degree, len(data), maximumDegree)
	}

	// Convert the outgoing references to their native representation.
	// TODO: Any way we can converge with object.Contents? Ideally
	// we'd keep these references in their byte slice.
	outgoingReferences := make(object.OutgoingReferencesList[object.LocalReference], 0, degree)
	for i := 0; i < int(degree); i++ {
		localReference, err := referenceFormat.NewLocalReference(data[i*referenceSizeBytes:][:referenceSizeBytes])
		if err != nil {
			return TopLevelMessage[TMessagePtr, object.LocalReference]{}, util.StatusWrapf(err, "Invalid outgoing reference at index %d", i)
		}
		outgoingReferences = append(outgoingReferences, localReference)
	}

	var m TMessage
	if err := proto.Unmarshal(data[int(degree)*referenceSizeBytes:], TMessagePtr(&m)); err != nil {
		return TopLevelMessage[TMessagePtr, object.LocalReference]{}, err
	}
	return NewTopLevelMessage(TMessagePtr(&m), outgoingReferences), nil
}

// TopLevelMessagesEqual returns true if two top-level messages contain
// the same data. This function can only be called against top-level
// messages, as only those consistently number any outgoing references.
func TopLevelMessagesEqual[
	TMessage proto.Message,
	TReference1, TReference2 object.BasicReference,
](m1 TopLevelMessage[TMessage, TReference1], m2 TopLevelMessage[TMessage, TReference2]) bool {
	degree1, degree2 := m1.OutgoingReferences.GetDegree(), m2.OutgoingReferences.GetDegree()
	if degree1 != degree2 {
		return false
	}
	for i := 0; i < degree1; i++ {
		if m1.OutgoingReferences.GetOutgoingReference(i).GetLocalReference() != m2.OutgoingReferences.GetOutgoingReference(i).GetLocalReference() {
			return false
		}
	}
	return proto.Equal(m1.Message, m2.Message)
}

// MarshalTopLevelAny wraps a message in an anypb.Any.
//
// This function assumes that the provided message is a top-level
// message. Only top-level messages are permitted to be wrapped in an
// anypb.Any, as it's impossible to create a model_core_pb.Any for
// messages having outgoing references with sparse indices.
func MarshalTopLevelAny[TMessage proto.Message, TReference any](m TopLevelMessage[TMessage, TReference]) (TopLevelMessage[*anypb.Any, TReference], error) {
	var value anypb.Any
	if err := anypb.MarshalFrom(&value, m.Message, marshalOptions); err != nil {
		return TopLevelMessage[*anypb.Any, TReference]{}, err
	}
	return NewTopLevelMessage(&value, m.OutgoingReferences), nil
}

// UnmarshalTopLevelAnyNew extracts the message contained in an
// anypb.Any, and ensures that any references contained within are
// accessible.
//
// This function assumes that the anypb.Any is a top-level message,
// meaning that the inner message has access to the same set of
// references as the outer message.
func UnmarshalTopLevelAnyNew[TReference any](m TopLevelMessage[*anypb.Any, TReference]) (TopLevelMessage[proto.Message, TReference], error) {
	message, err := m.Message.UnmarshalNew()
	if err != nil {
		return TopLevelMessage[proto.Message, TReference]{}, err
	}
	return NewTopLevelMessage(message, m.OutgoingReferences), nil
}

// ComputeTopLevelMessageReference computes the local reference of an
// object whose contents are equal to that of a provided top-level
// message after it has been referenced, without any encoding applied.
//
// This function should typically only be used to compute tag key
// hashes, as it only produces references of fictive objects. In
// practice you almost always want to encode objects prior to storing
// them in an object.
func ComputeTopLevelMessageReference[TMessage proto.Message, TReference object.BasicReference](
	m TopLevelMessage[TMessage, TReference],
	referenceFormat object.ReferenceFormat,
) (object.LocalReference, error) {
	data, err := NewProtoBinaryMarshaler(m.Message).MarshalBinary()
	if err != nil {
		var badReference object.LocalReference
		return badReference, err
	}
	return object.NewLocalReferenceFromData(referenceFormat, m.OutgoingReferences, data)
}
