package core

import (
	"bytes"
	"math"
	"sort"

	"bonanza.build/pkg/ds"
	"bonanza.build/pkg/proto/model/core"
	"bonanza.build/pkg/storage/object"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// ReferenceMetadata is freeform metadata that can be associated with
// any reference managed by a ReferenceMessagePatcher.
//
// Simple implementations of ReferenceMessagePatcher may capture the
// contents of the object referenced, causing the full Merkle tree to
// reside in memory. Alternatively, implementations may also store
// information on how the data that is referenced was obtained, so that
// it may be recomputed if needed.
//
// As ReferenceMatadata may contain things like file descriptors, a
// Discard() method is provided to release such resources in case
// ReferenceMessagePatcher encounters duplicate references.
type ReferenceMetadata interface {
	Discard()
}

// ReferenceMetadataCreator is invoked by NewPatchedMessageFromExisting
// for every reference that is encountered, allowing metadata for the
// outgoing reference to be created.
type ReferenceMetadataCreator[T any] func(index int) T

type referenceMessages[TMetadata ReferenceMetadata] struct {
	metadata TMetadata
	indices  []*uint32
}

// ReferenceMessagePatcher keeps track of all Reference messages that
// are contained within a given Protobuf message. For each of these
// Reference messages, it stores the actual object.LocalReference that
// is associated with them.
//
// Once the Protobuf message has been fully constructed,
// SortAndSetReferences() can be used to return a sorted, deduplicated
// list of all outgoing references of the message, and to update the
// indices in the Reference messages to refer to the correct outgoing
// reference.
type ReferenceMessagePatcher[TMetadata ReferenceMetadata] struct {
	messagesByReference map[object.LocalReference]referenceMessages[TMetadata]
	height              int
}

// NewReferenceMessagePatcher creates a new ReferenceMessagePatcher that
// does not contain any Reference messages.
func NewReferenceMessagePatcher[TMetadata ReferenceMetadata]() *ReferenceMessagePatcher[TMetadata] {
	return &ReferenceMessagePatcher[TMetadata]{}
}

func (p *ReferenceMessagePatcher[TMetadata]) maybeIncreaseHeight(height int) {
	if p.height < height {
		p.height = height
	}
}

// AddReference allocates a new Reference message that is associated
// with a given object.LocalReference and caller provided metadata.
func (p *ReferenceMessagePatcher[TMetadata]) AddReference(capturedObject CapturedObject[TMetadata]) *core.Reference {
	message := &core.Reference{
		Index: math.MaxUint32,
	}
	p.addIndex(&message.Index, capturedObject)
	return message
}

// CaptureAndAddDecodableReference is a helper function for AddReference
// that can be called in the common case where a DecodableReference
// needs to be emitted, referring to a newly created object.
func (p *ReferenceMessagePatcher[TMetadata]) CaptureAndAddDecodableReference(
	createdObject Decodable[CreatedObject[TMetadata]],
	capturer CreatedObjectCapturer[TMetadata],
) *core.DecodableReference {
	return &core.DecodableReference{
		Reference:          p.AddReference(createdObject.Value.Capture(capturer)),
		DecodingParameters: createdObject.GetDecodingParameters(),
	}
}

func (p *ReferenceMessagePatcher[TMetadata]) addIndex(index *uint32, capturedObject CapturedObject[TMetadata]) {
	if p.messagesByReference == nil {
		p.messagesByReference = map[object.LocalReference]referenceMessages[TMetadata]{}
	}
	reference := capturedObject.LocalReference
	if existingMessages, ok := p.messagesByReference[reference]; ok {
		capturedObject.Metadata.Discard()
		p.messagesByReference[reference] = referenceMessages[TMetadata]{
			metadata: existingMessages.metadata,
			indices:  append(p.messagesByReference[reference].indices, index),
		}
	} else {
		p.messagesByReference[reference] = referenceMessages[TMetadata]{
			metadata: capturedObject.Metadata,
			indices:  []*uint32{index},
		}
		p.maybeIncreaseHeight(reference.GetHeight() + 1)
	}
}

type referenceMessageAdder[TMetadata ReferenceMetadata, TReference object.BasicReference] struct {
	patcher            *ReferenceMessagePatcher[TMetadata]
	outgoingReferences object.OutgoingReferences[TReference]
	createMetadata     ReferenceMetadataCreator[TMetadata]
}

func (a *referenceMessageAdder[TMetadata, TReference]) addReferenceMessagesRecursively(message protoreflect.Message) {
	switch m := message.Interface().(type) {
	case *core.Reference:
		// If the reference message refers to a valid object,
		// let it be managed by the patcher. If it is invalid,
		// we at least change the index to MaxUint32, so that
		// any future attempts to resolve it will fail.
		if index, err := GetIndexFromReferenceMessage(m, a.outgoingReferences.GetDegree()); err == nil {
			reference := a.outgoingReferences.GetOutgoingReference(index).GetLocalReference()
			a.patcher.addIndex(&m.Index, CapturedObject[TMetadata]{
				LocalReference: reference,
				Metadata:       a.createMetadata(index),
			})
		}
		m.Index = math.MaxUint32
	case *core.ReferenceSet:
		// Similarly, let all entries in a reference set message
		// be managed by the patcher.
		previousRawIndex := uint32(0)
		degree := a.outgoingReferences.GetDegree()
		for i, rawIndex := range m.Indices {
			if rawIndex <= previousRawIndex || int64(rawIndex) > int64(degree) {
				// Set is not sorted or contains indices
				// that are out of bounds. Truncate the
				// set, so that at least the leading
				// references remain functional.
				m.Indices = m.Indices[:i]
				break
			}
			index := int(rawIndex - 1)
			reference := a.outgoingReferences.GetOutgoingReference(index).GetLocalReference()
			a.patcher.addIndex(&m.Indices[i], CapturedObject[TMetadata]{
				LocalReference: reference,
				Metadata:       a.createMetadata(index),
			})
			m.Indices[i] = math.MaxUint32
			previousRawIndex = rawIndex
		}
	default:
		message.Range(func(fieldDescriptor protoreflect.FieldDescriptor, value protoreflect.Value) bool {
			if k := fieldDescriptor.Kind(); k == protoreflect.MessageKind || k == protoreflect.GroupKind {
				if fieldDescriptor.IsList() {
					l := value.List()
					n := l.Len()
					for i := 0; i < n; i++ {
						a.addReferenceMessagesRecursively(l.Get(i).Message())
					}
				} else {
					a.addReferenceMessagesRecursively(value.Message())
				}
			}
			return true
		})
	}
}

// Merge multiple instances of ReferenceMessagePatcher together. This
// method can be used when multiple Protobuf messages are combined into
// a larger message, and are eventually stored as a single object.
func (p *ReferenceMessagePatcher[TMetadata]) Merge(other *ReferenceMessagePatcher[TMetadata]) {
	// Reduce the worst-case time complexity by always merging the
	// small map into the larger one.
	if len(p.messagesByReference) < len(other.messagesByReference) {
		p.messagesByReference, other.messagesByReference = other.messagesByReference, p.messagesByReference
	}
	for reference, newMessages := range other.messagesByReference {
		if existingMessages, ok := p.messagesByReference[reference]; ok {
			newMessages.metadata.Discard()
			p.messagesByReference[reference] = referenceMessages[TMetadata]{
				metadata: existingMessages.metadata,
				indices:  append(existingMessages.indices, newMessages.indices...),
			}
		} else {
			p.messagesByReference[reference] = newMessages
		}
	}
	p.maybeIncreaseHeight(other.height)
	other.empty()
}

func (p *ReferenceMessagePatcher[TMetadata]) empty() {
	clear(p.messagesByReference)
	p.height = 0
}

// Discard all resources owned by all metadata managed by this reference
// message patcher.
func (p *ReferenceMessagePatcher[TMetadata]) Discard() {
	for _, messages := range p.messagesByReference {
		messages.metadata.Discard()
	}
	p.empty()
}

// GetHeight returns the height that the object of the Protobuf message
// would have if it were created with the current set of outgoing
// references.
func (p *ReferenceMessagePatcher[TMetadata]) GetHeight() int {
	return p.height
}

// GetReferencesSizeBytes returns the size that all of the outgoing
// references would have if an object were created with the current set
// of outgoing references.
func (p *ReferenceMessagePatcher[TMetadata]) GetReferencesSizeBytes() int {
	for reference := range p.messagesByReference {
		return len(reference.GetRawReference()) * len(p.messagesByReference)
	}
	return 0
}

// SortAndSetReferences returns a sorted list of all outgoing references
// of the Protobuf message. This list can be provided to
// object.NewContents() to construct an actual object for storage. In
// addition to that, a list of user provided metadata is returned that
// sorted along the same order.
func (p *ReferenceMessagePatcher[TMetadata]) SortAndSetReferences() (object.OutgoingReferencesList[object.LocalReference], []TMetadata) {
	// Created a sorted list of outgoing references.
	sortedReferences := referencesList{
		Slice: make(ds.Slice[object.LocalReference], 0, len(p.messagesByReference)),
	}
	for reference := range p.messagesByReference {
		sortedReferences.Slice = append(sortedReferences.Slice, reference)
	}
	sort.Sort(sortedReferences)

	// Extract metadata associated with the references. Also assign
	// indices to the Reference messages. These should both respect
	// the same order as the outgoing references.
	sortedMetadata := make([]TMetadata, 0, len(p.messagesByReference))
	for i, reference := range sortedReferences.Slice {
		referenceMessages := p.messagesByReference[reference]
		for _, index := range referenceMessages.indices {
			*index = uint32(i) + 1
		}
		sortedMetadata = append(sortedMetadata, referenceMessages.metadata)
	}
	return object.OutgoingReferencesList[object.LocalReference](sortedReferences.Slice), sortedMetadata
}

type referencesList struct {
	ds.Slice[object.LocalReference]
}

func (l referencesList) Less(i, j int) bool {
	return bytes.Compare(
		l.Slice[i].GetRawReference(),
		l.Slice[j].GetRawReference(),
	) < 0
}

// MapReferenceMessagePatcherMetadata replaces a ReferenceMessagePatcher
// with a new instance that contains the same references, but has
// metadata mapped to other values, potentially of another type.
func MapReferenceMessagePatcherMetadata[TOld, TNew ReferenceMetadata](pOld *ReferenceMessagePatcher[TOld], mapMetadata func(CapturedObject[TOld]) TNew) *ReferenceMessagePatcher[TNew] {
	pNew := &ReferenceMessagePatcher[TNew]{
		messagesByReference: make(map[object.LocalReference]referenceMessages[TNew], len(pOld.messagesByReference)),
		height:              pOld.height,
	}
	for reference, oldMessages := range pOld.messagesByReference {
		pNew.messagesByReference[reference] = referenceMessages[TNew]{
			metadata: mapMetadata(CapturedObject[TOld]{
				LocalReference: reference,
				Metadata:       oldMessages.metadata,
			}),
			indices: oldMessages.indices,
		}
	}
	pOld.empty()
	return pNew
}

// ReferenceMetadataCreatorForTesting is an instantiation of
// ReferenceMetadataCreator for generating mocks to be used by tests.
type ReferenceMetadataCreatorForTesting ReferenceMetadataCreator[ReferenceMetadata]
