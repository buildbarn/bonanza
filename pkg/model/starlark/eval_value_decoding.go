package starlark

import (
	"errors"
	"fmt"
	"iter"
	"math"

	model_core "bonanza.build/pkg/model/core"
	model_parser "bonanza.build/pkg/model/parser"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/syntax"
	"bonanza.build/pkg/storage/object"
)

// The functions and types in this file decode Protobuf messages of
// Starlark values to values of pkg/starlark/eval. As opposed to
// DecodeValue(), decoding is lazy: lists, dicts and sets are returned
// as implementations of eval.List, eval.Dict and eval.Set that read
// their elements from storage when they are accessed. Values that are
// decoded this way are immutable, and are encoded by returning the
// original message.

// EvalValueDecodingOptions contains parameters that need to be passed
// along when decoding Starlark values to values of pkg/starlark/eval.
type EvalValueDecodingOptions[TReference any] struct {
	Readers *ValueReaders[TReference]
}

// EncodableEvalValue is implemented by values of pkg/starlark/eval
// that provide their own encoding. Values that are backed by storage
// implement this to return their original message, so that they can be
// encoded without reading any of their contents.
type EncodableEvalValue[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	eval.Value
	EncodeEvalValue(thread *eval.Thread, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], error)
}

// DecodeEvalValue decodes a Starlark value Protobuf message to a value
// of pkg/starlark/eval. Lists, dicts and sets are not read from storage
// until their contents are accessed.
func DecodeEvalValue[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](thread *eval.Thread, value model_core.Message[*model_starlark_pb.Value, TReference], options *EvalValueDecodingOptions[TReference]) (eval.Value, error) {
	switch typedValue := value.Message.GetKind().(type) {
	case *model_starlark_pb.Value_None:
		return eval.None, nil
	case *model_starlark_pb.Value_Bool:
		return eval.Bool(typedValue.Bool), nil
	case *model_starlark_pb.Value_Int:
		return eval.MakeBigInt(DecodeInt(typedValue.Int)), nil
	case *model_starlark_pb.Value_Str:
		return eval.String(typedValue.Str), nil
	case *model_starlark_pb.Value_List:
		return &storedList[TReference, TMetadata]{
			storedSequence: storedSequence[TReference, TMetadata]{
				value:    value,
				elements: model_core.Nested(value, typedValue.List.Elements),
				options:  options,
				length:   -1,
			},
		}, nil
	case *model_starlark_pb.Value_Set:
		return &storedSet[TReference, TMetadata]{
			storedSequence: storedSequence[TReference, TMetadata]{
				value:    value,
				elements: model_core.Nested(value, typedValue.Set.Elements),
				options:  options,
				length:   -1,
			},
		}, nil
	case *model_starlark_pb.Value_Dict:
		return &storedDict[TReference, TMetadata]{
			value:   value,
			dict:    model_core.Nested(value, typedValue.Dict),
			options: options,
		}, nil
	case *model_starlark_pb.Value_Tuple:
		// Tuples are stored inline, so there is no need to defer
		// decoding of their elements.
		elements := make([]eval.Value, 0, len(typedValue.Tuple.Elements))
		for i, element := range typedValue.Tuple.Elements {
			decodedElement, err := DecodeEvalValue[TReference, TMetadata](thread, model_core.Nested(value, element), options)
			if err != nil {
				return nil, fmt.Errorf("at index %d: %w", i, err)
			}
			elements = append(elements, decodedElement)
		}
		return eval.NewTuple(elements), nil
	case nil:
		return nil, errors.New("value has no kind")
	default:
		return nil, fmt.Errorf("values of kind %T cannot be decoded yet", typedValue)
	}
}

// storedSequence contains the state shared by lists and sets that are
// backed by storage. Both are stored as a B-tree of list elements whose
// parents contain the number of elements contained within, meaning
// that the length can be computed by only looking at the root.
type storedSequence[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	value    model_core.Message[*model_starlark_pb.Value, TReference]
	elements model_core.Message[[]*model_starlark_pb.List_Element, TReference]
	options  *EvalValueDecodingOptions[TReference]

	// Length of the sequence, or -1 if not computed yet.
	length int
}

func (s *storedSequence[TReference, TMetadata]) Truth(t *eval.Thread) (bool, error) {
	n, err := s.Len(t)
	return n > 0, err
}

func (s *storedSequence[TReference, TMetadata]) Len(*eval.Thread) (int, error) {
	if s.length < 0 {
		count := uint64(0)
		for _, element := range s.elements.Message {
			switch level := element.Level.(type) {
			case *model_starlark_pb.List_Element_Leaf:
				count++
			case *model_starlark_pb.List_Element_Parent_:
				count += level.Parent.Count
				if count < level.Parent.Count {
					count = math.MaxUint64
				}
			default:
				return 0, errors.New("invalid list element level")
			}
		}
		if count > math.MaxInt32 {
			return 0, errors.New("sequence contains too many elements")
		}
		s.length = int(count)
	}
	return s.length, nil
}

func (s *storedSequence[TReference, TMetadata]) Iterate(t *eval.Thread, errOut *error) iter.Seq[eval.Value] {
	return func(yield func(eval.Value) bool) {
		var errIter error
		for element := range AllListLeafElements(t.Context(), s.options.Readers.List, s.elements, &errIter) {
			decodedElement, err := DecodeEvalValue[TReference, TMetadata](t, element, s.options)
			if err != nil {
				*errOut = err
				return
			}
			if !yield(decodedElement) {
				return
			}
		}
		if errIter != nil {
			*errOut = errIter
		}
	}
}

func (s *storedSequence[TReference, TMetadata]) EncodeEvalValue(thread *eval.Thread, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], error) {
	return model_core.Patch(options.ObjectCapturer, s.value), nil
}

// storedList is an immutable list that is backed by storage.
type storedList[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	eval.ListMarker
	storedSequence[TReference, TMetadata]
}

var _ eval.List = (*storedList[object.LocalReference, model_core.NoopReferenceMetadata])(nil)

func (*storedList[TReference, TMetadata]) Type() string { return "list" }

func (l *storedList[TReference, TMetadata]) Repr(t *eval.Thread, p *eval.Printer) error {
	p.WriteString("[")
	first := true
	var errIter error
	for element := range l.Iterate(t, &errIter) {
		if !first {
			p.WriteString(", ")
		}
		first = false
		if err := element.Repr(t, p); err != nil {
			return err
		}
	}
	if errIter != nil {
		return errIter
	}
	p.WriteString("]")
	return nil
}

// Index returns the element at a given index. It descends into the
// B-tree using the element counts stored in parents, meaning that only
// the objects on the path to the element are read.
func (l *storedList[TReference, TMetadata]) Index(t *eval.Thread, i int) (eval.Value, error) {
	remaining := uint64(i)
	elements := l.elements
descend:
	for {
		for _, element := range elements.Message {
			switch level := element.Level.(type) {
			case *model_starlark_pb.List_Element_Leaf:
				if remaining == 0 {
					return DecodeEvalValue[TReference, TMetadata](t, model_core.Nested(elements, level.Leaf), l.options)
				}
				remaining--
			case *model_starlark_pb.List_Element_Parent_:
				if remaining < level.Parent.Count {
					var err error
					elements, err = model_parser.Dereference(t.Context(), l.options.Readers.List, model_core.Nested(elements, level.Parent.Reference))
					if err != nil {
						return nil, err
					}
					continue descend
				}
				remaining -= level.Parent.Count
			default:
				return nil, errors.New("invalid list element level")
			}
		}
		return nil, errors.New("list element counts stored in parents are inconsistent with the actual number of elements")
	}
}

func (l *storedList[TReference, TMetadata]) Slice(t *eval.Thread, start, end, step int) (eval.Value, error) {
	elements, err := eval.Elements(t, l)
	if err != nil {
		return nil, err
	}
	return eval.NewImmutableList(elements).Slice(t, start, end, step)
}

func (l *storedList[TReference, TMetadata]) CompareSameType(t *eval.Thread, op syntax.Token, y eval.Value, depth int) (bool, error) {
	elements, err := eval.Elements(t, l)
	if err != nil {
		return false, err
	}
	return eval.NewImmutableList(elements).(eval.Comparable).CompareSameType(t, op, y, depth)
}

func (*storedList[TReference, TMetadata]) Append(*eval.Thread, eval.Value) error {
	return eval.NewFrozenError("list")
}

func (*storedList[TReference, TMetadata]) SetIndex(*eval.Thread, int, eval.Value) error {
	return eval.NewFrozenError("list")
}

func (*storedList[TReference, TMetadata]) Insert(*eval.Thread, int, eval.Value) error {
	return eval.NewFrozenError("list")
}

func (*storedList[TReference, TMetadata]) RemoveIndex(*eval.Thread, int) error {
	return eval.NewFrozenError("list")
}

func (*storedList[TReference, TMetadata]) Clear(*eval.Thread) error {
	return eval.NewFrozenError("list")
}

// storedSet is an immutable set that is backed by storage. Its
// elements are stored in insertion order, meaning that membership
// tests require all elements to be loaded. This is done the first time
// it is needed.
type storedSet[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	eval.SetMarker
	storedSequence[TReference, TMetadata]

	// In-memory copy of the set, constructed on first use. It is
	// never exposed, so it is not mutated.
	index eval.Set
}

var _ eval.Set = (*storedSet[object.LocalReference, model_core.NoopReferenceMetadata])(nil)

func (*storedSet[TReference, TMetadata]) Type() string { return "set" }

func (s *storedSet[TReference, TMetadata]) getIndex(t *eval.Thread) (eval.Set, error) {
	if s.index == nil {
		index := eval.NewSet()
		var errIter error
		for element := range s.Iterate(t, &errIter) {
			if err := index.Add(t, element); err != nil {
				return nil, err
			}
		}
		if errIter != nil {
			return nil, errIter
		}
		s.index = index
	}
	return s.index, nil
}

func (s *storedSet[TReference, TMetadata]) Repr(t *eval.Thread, p *eval.Printer) error {
	if len(s.elements.Message) == 0 {
		p.WriteString("set()")
		return nil
	}
	p.WriteString("set([")
	first := true
	var errIter error
	for element := range s.Iterate(t, &errIter) {
		if !first {
			p.WriteString(", ")
		}
		first = false
		if err := element.Repr(t, p); err != nil {
			return err
		}
	}
	if errIter != nil {
		return errIter
	}
	p.WriteString("])")
	return nil
}

func (s *storedSet[TReference, TMetadata]) Has(t *eval.Thread, v eval.Value) (bool, error) {
	index, err := s.getIndex(t)
	if err != nil {
		return false, err
	}
	return index.Has(t, v)
}

func (s *storedSet[TReference, TMetadata]) CompareSameType(t *eval.Thread, op syntax.Token, y eval.Value, depth int) (bool, error) {
	index, err := s.getIndex(t)
	if err != nil {
		return false, err
	}
	return index.(eval.Comparable).CompareSameType(t, op, y, depth)
}

func (*storedSet[TReference, TMetadata]) Add(*eval.Thread, eval.Value) error {
	return eval.NewFrozenError("set")
}

func (*storedSet[TReference, TMetadata]) Discard(*eval.Thread, eval.Value) (bool, error) {
	return false, eval.NewFrozenError("set")
}

func (*storedSet[TReference, TMetadata]) Clear(*eval.Thread) error {
	return eval.NewFrozenError("set")
}

// storedDict is an immutable dict that is backed by storage. Its
// entries are stored in insertion order, and parents don't contain
// the number of entries contained within. This means that computing
// the length and lookups require all entries to be loaded. This is
// done the first time it is needed.
type storedDict[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	eval.DictMarker

	value   model_core.Message[*model_starlark_pb.Value, TReference]
	dict    model_core.Message[*model_starlark_pb.Dict, TReference]
	options *EvalValueDecodingOptions[TReference]

	// In-memory copy of the dict, constructed on first use. It is
	// never exposed, so it is not mutated.
	index eval.Dict
}

var _ eval.Dict = (*storedDict[object.LocalReference, model_core.NoopReferenceMetadata])(nil)

func (*storedDict[TReference, TMetadata]) Type() string { return "dict" }

func (d *storedDict[TReference, TMetadata]) Truth(*eval.Thread) (bool, error) {
	return len(d.dict.Message.Entries) > 0, nil
}

func (d *storedDict[TReference, TMetadata]) getIndex(t *eval.Thread) (eval.Dict, error) {
	if d.index == nil {
		index := eval.NewDict()
		var errIter error
		for key, value := range d.Items(t, &errIter) {
			if err := index.SetKey(t, key, value); err != nil {
				return nil, err
			}
		}
		if errIter != nil {
			return nil, errIter
		}
		d.index = index
	}
	return d.index, nil
}

func (d *storedDict[TReference, TMetadata]) Len(t *eval.Thread) (int, error) {
	index, err := d.getIndex(t)
	if err != nil {
		return 0, err
	}
	return index.Len(t)
}

func (d *storedDict[TReference, TMetadata]) Get(t *eval.Thread, k eval.Value) (eval.Value, bool, error) {
	index, err := d.getIndex(t)
	if err != nil {
		return nil, false, err
	}
	return index.Get(t, k)
}

func (d *storedDict[TReference, TMetadata]) Items(t *eval.Thread, errOut *error) iter.Seq2[eval.Value, eval.Value] {
	return func(yield func(eval.Value, eval.Value) bool) {
		var errIter error
		for key, value := range AllDictLeafEntries(t.Context(), d.options.Readers.Dict, d.dict, &errIter) {
			decodedKey, err := DecodeEvalValue[TReference, TMetadata](t, key, d.options)
			if err != nil {
				*errOut = fmt.Errorf("in key: %w", err)
				return
			}
			decodedValue, err := DecodeEvalValue[TReference, TMetadata](t, value, d.options)
			if err != nil {
				*errOut = fmt.Errorf("in value: %w", err)
				return
			}
			if !yield(decodedKey, decodedValue) {
				return
			}
		}
		if errIter != nil {
			*errOut = errIter
		}
	}
}

func (d *storedDict[TReference, TMetadata]) Iterate(t *eval.Thread, errOut *error) iter.Seq[eval.Value] {
	return func(yield func(eval.Value) bool) {
		for key := range d.Items(t, errOut) {
			if !yield(key) {
				return
			}
		}
	}
}

func (d *storedDict[TReference, TMetadata]) Repr(t *eval.Thread, p *eval.Printer) error {
	p.WriteString("{")
	first := true
	var errIter error
	for key, value := range d.Items(t, &errIter) {
		if !first {
			p.WriteString(", ")
		}
		first = false
		if err := key.Repr(t, p); err != nil {
			return err
		}
		p.WriteString(": ")
		if err := value.Repr(t, p); err != nil {
			return err
		}
	}
	if errIter != nil {
		return errIter
	}
	p.WriteString("}")
	return nil
}

func (d *storedDict[TReference, TMetadata]) CompareSameType(t *eval.Thread, op syntax.Token, y eval.Value, depth int) (bool, error) {
	index, err := d.getIndex(t)
	if err != nil {
		return false, err
	}
	return index.(eval.Comparable).CompareSameType(t, op, y, depth)
}

func (d *storedDict[TReference, TMetadata]) EncodeEvalValue(thread *eval.Thread, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], error) {
	return model_core.Patch(options.ObjectCapturer, d.value), nil
}

func (*storedDict[TReference, TMetadata]) SetKey(*eval.Thread, eval.Value, eval.Value) error {
	return eval.NewFrozenError("dict")
}

func (*storedDict[TReference, TMetadata]) Delete(*eval.Thread, eval.Value) (eval.Value, bool, error) {
	return nil, false, eval.NewFrozenError("dict")
}

func (*storedDict[TReference, TMetadata]) Clear(*eval.Thread) error {
	return eval.NewFrozenError("dict")
}
