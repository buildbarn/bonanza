package starlark

import (
	"slices"

	model_core "bonanza.build/pkg/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/syntax"
	"bonanza.build/pkg/storage/object"
)

type attrValuePartKind int

const (
	attrValuePartKindScalar attrValuePartKind = iota
	attrValuePartKindList
	attrValuePartKindDict
)

// AttrValuePart is one of the parts of the value of a rule attribute.
// An attribute value consists of multiple parts if it is constructed
// by concatenating select() expressions.
type AttrValuePart struct {
	kind     attrValuePartKind
	scalar   eval.Value
	elements []eval.Value
	entries  [][2]eval.Value
}

// DecodeAttrValuePart decodes a part of the value of a rule attribute,
// using options that may differ from the ones of the thread, such as a
// LabelCreator that converts labels to Target objects belonging to a
// specific configuration.
//
// Storage-backed values are decoded lazily using the options of the
// thread, meaning that all labels need to be decoded before this
// function returns. This function only decodes the outermost list or
// dict, as attribute values do not contain labels at a greater depth.
// Support for this needs to be added if attribute types are introduced
// for which this is no longer the case.
func DecodeAttrValuePart[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](thread *eval.Thread, value model_core.Message[*model_starlark_pb.Value, TReference], options *ValueDecodingOptions[TReference]) (AttrValuePart, error) {
	previousOptions := thread.Local(ValueDecodingOptionsKey)
	thread.SetLocal(ValueDecodingOptionsKey, options)
	defer thread.SetLocal(ValueDecodingOptionsKey, previousOptions)

	decodedValue, err := DecodeValue[TReference, TMetadata](thread, value, nil)
	if err != nil {
		return AttrValuePart{}, err
	}
	var errIter error
	switch typedValue := decodedValue.(type) {
	case eval.List:
		part := AttrValuePart{kind: attrValuePartKindList}
		for element := range typedValue.Iterate(thread, &errIter) {
			part.elements = append(part.elements, element)
		}
		return part, errIter
	case eval.Dict:
		part := AttrValuePart{kind: attrValuePartKindDict}
		for key, dictValue := range typedValue.Items(thread, &errIter) {
			part.entries = append(part.entries, [2]eval.Value{key, dictValue})
		}
		return part, errIter
	default:
		return AttrValuePart{scalar: decodedValue}, nil
	}
}

// Value returns the part in the form of an immutable Starlark value.
func (p AttrValuePart) Value(thread *eval.Thread) (eval.Value, error) {
	switch p.kind {
	case attrValuePartKindList:
		return eval.NewImmutableList(slices.Clone(p.elements)), nil
	case attrValuePartKindDict:
		return eval.NewImmutableDict(thread, p.entries)
	default:
		return p.scalar, nil
	}
}

// AsList returns a part corresponding to a list that contains the
// value of this part as its only element. If omitNone is set and the
// value of this part is None, the list is empty.
func (p AttrValuePart) AsList(thread *eval.Thread, omitNone bool) (AttrValuePart, error) {
	if omitNone && p.kind == attrValuePartKindScalar && p.scalar == eval.None {
		return AttrValuePart{kind: attrValuePartKindList}, nil
	}
	value, err := p.Value(thread)
	if err != nil {
		return AttrValuePart{}, err
	}
	return AttrValuePart{kind: attrValuePartKindList, elements: []eval.Value{value}}, nil
}

// AttrValueBuilder concatenates the parts of the value of a rule
// attribute, yielding an immutable value. As lists and dicts are
// concatenated by collecting their elements and entries, the value is
// only constructed once.
type AttrValueBuilder struct {
	part    AttrValuePart
	hasPart bool
}

// Add a part to the attribute value. The same part may be added to
// multiple builders.
func (b *AttrValueBuilder) Add(thread *eval.Thread, part AttrValuePart) error {
	if !b.hasPart {
		b.part = part
		b.part.elements = slices.Clone(part.elements)
		b.part.entries = slices.Clone(part.entries)
		b.hasPart = true
		return nil
	}

	if b.part.kind == part.kind {
		switch part.kind {
		case attrValuePartKindList:
			b.part.elements = append(b.part.elements, part.elements...)
			return nil
		case attrValuePartKindDict:
			b.part.entries = append(b.part.entries, part.entries...)
			return nil
		}
	}

	// Values that are not lists and dicts, such as strings, or
	// parts of different types.
	left, err := b.part.Value(thread)
	if err != nil {
		return err
	}
	right, err := part.Value(thread)
	if err != nil {
		return err
	}
	operator := syntax.PLUS
	if b.part.kind == attrValuePartKindDict {
		operator = syntax.PIPE
	}
	result, err := eval.Binary(thread, operator, left, right)
	if err != nil {
		return err
	}
	b.part = AttrValuePart{scalar: result}
	return nil
}

// Build returns the attribute value, or nil if no parts have been
// added.
func (b *AttrValueBuilder) Build(thread *eval.Thread) (eval.Value, error) {
	if !b.hasPart {
		return nil, nil
	}
	return b.part.Value(thread)
}
