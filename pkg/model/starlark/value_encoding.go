package starlark

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"math/bits"
	"slices"

	pg_label "bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	"bonanza.build/pkg/model/core/inlinedtree"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/storage/object"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

// ValueEncodingOptionsKey is the key under which ValueEncodingOptions
// can be stored in the thread local variables of a Starlark thread.
//
// Calling into rules or repository rules causes new targets and
// repositories to be registered. These are immediately converted to
// Protobuf messages, which is why in those contexts the
// ValueEncodingOptions must be available.
const ValueEncodingOptionsKey = "value_encoding_options"

// ValueEncodingOptions contains parameters that always need to be
// passed along when recursively encoding Starlark value objects.
type ValueEncodingOptions[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	CurrentFilename *pg_label.CanonicalLabel

	// Options to use when storing Starlark values in separate objects.
	Context                context.Context
	ObjectEncoder          model_encoding.DeterministicBinaryEncoder
	ObjectReferenceFormat  object.ReferenceFormat
	ObjectCapturer         model_core.ObjectCapturer[TReference, TMetadata]
	ObjectMinimumSizeBytes int
	ObjectMaximumSizeBytes int
}

// getInlinedTreeOptions returns the options to use when deciding which
// fields of a message need to be stored in separate objects, so that
// the message does not become too large.
//
// Any message that is at least twice ObjectMinimumSizeBytes in size has
// fields moved out of it.
func (o *ValueEncodingOptions[TReference, TMetadata]) getInlinedTreeOptions() *inlinedtree.Options {
	return &inlinedtree.Options{
		ReferenceFormat:  o.ObjectReferenceFormat,
		MaximumSizeBytes: o.ObjectMinimumSizeBytes*2 - 1,
	}
}

// ComputeListParentNode generates a parent node of a Starlark list that
// is stored in a B-tree. Parent nodes are created when a Starlark list
// is too large to store in a single object.
func ComputeListParentNode[TMetadata model_core.ReferenceMetadata](createdObject model_core.Decodable[model_core.MetadataEntry[TMetadata]], childNodes model_core.Message[[]*model_starlark_pb.List_Element, object.LocalReference]) model_core.PatchedMessage[*model_starlark_pb.List_Element, TMetadata] {
	// Compute the total number of elements
	// contained in the new list.
	//
	// For depsets it is easy to craft instances
	// that have more than 2^64-1 elements due to
	// excessive repetition. Make sure to clamp the
	// value in that case, so that consumers know
	// they can't use this field to jump to
	// arbitrary elements.
	count := uint64(0)
	for _, childNode := range childNodes.Message {
		childCount := uint64(1)
		if level, ok := childNode.Level.(*model_starlark_pb.List_Element_Parent_); ok {
			childCount = level.Parent.Count
		}
		var carryOut uint64
		count, carryOut = bits.Add64(count, childCount, 0)
		if carryOut > 0 {
			count = math.MaxUint64
			break
		}
	}

	return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_starlark_pb.List_Element {
		return &model_starlark_pb.List_Element{
			Level: &model_starlark_pb.List_Element_Parent_{
				Parent: &model_starlark_pb.List_Element_Parent{
					Reference: patcher.AddDecodableReference(createdObject),
					Count:     count,
				},
			},
		}
	})
}

// newBTreeBuilder creates a B-tree builder suitable for encoding lists
// of Starlark values.
func newBTreeBuilder[TReference any, TMessage proto.Message, TMetadata model_core.ReferenceMetadata](
	options *ValueEncodingOptions[TReference, TMetadata],
	isParent func(TMessage) bool,
	parentNodeComputer btree.ParentNodeComputer[TMessage, TMetadata],
) btree.Builder[TMessage, TMetadata] {
	return btree.NewHeightAwareBuilder(
		btree.NewProllyChunkerFactory[TMetadata](
			options.ObjectMinimumSizeBytes,
			options.ObjectMaximumSizeBytes,
			isParent,
		),
		btree.NewObjectCreatingNodeMerger(
			options.ObjectEncoder,
			options.ObjectReferenceFormat,
			parentNodeComputer,
		),
	)
}

// NewListBuilder creates a B-tree builder for writing Starlark lists to
// storage.
func NewListBuilder[TReference any, TMetadata model_core.ReferenceMetadata](options *ValueEncodingOptions[TReference, TMetadata]) btree.Builder[*model_starlark_pb.List_Element, TMetadata] {
	return newBTreeBuilder(
		options,
		func(element *model_starlark_pb.List_Element) bool {
			return element.GetParent() != nil
		},
		btree.Capturing(options.Context, options.ObjectCapturer, ComputeListParentNode),
	)
}

// EncodableValue is implemented by Starlark value types in this package
// that can be converted to a Protobuf message. This includes values
// that are backed by storage, which encode by returning their original
// message.
type EncodableValue[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	// EncodeValue encodes the value. path contains the mutable
	// composite values that are being encoded, which is used to
	// detect cycles. currentIdentifier is set if the value is being
	// assigned to a global, which is needed by values that track
	// their own name, such as rules and providers.
	EncodeValue(thread *eval.Thread, path []eval.Value, currentIdentifier *pg_label.CanonicalStarlarkIdentifier, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], error)
}

// EncodeValue converts a Starlark value to a Protobuf message, so that
// it can be written to storage. path contains the mutable composite
// values that are currently being encoded, and should be nil when
// called on a value that is not contained in another value.
func EncodeValue[TReference any, TMetadata model_core.ReferenceMetadata](thread *eval.Thread, value eval.Value, path []eval.Value, currentIdentifier *pg_label.CanonicalStarlarkIdentifier, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], error) {
	if encodableValue, ok := value.(EncodableValue[TReference, TMetadata]); ok {
		return encodableValue.EncodeValue(thread, path, currentIdentifier, options)
	}

	switch value.(type) {
	case eval.List, eval.Dict, eval.Set:
		for _, v := range path {
			if v == value {
				return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, errors.New("value is defined recursively")
			}
		}
		path = append(path, value)
	}

	switch typedValue := value.(type) {
	case eval.NoneType:
		return model_core.NewSimplePatchedMessage[TMetadata](&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_None{
				None: &emptypb.Empty{},
			},
		}), nil
	case eval.Bool:
		return model_core.NewSimplePatchedMessage[TMetadata](&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_Bool{
				Bool: bool(typedValue),
			},
		}), nil
	case eval.Int:
		return model_core.NewSimplePatchedMessage[TMetadata](&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_Int{
				Int: EncodeInt(typedValue.BigInt()),
			},
		}), nil
	case eval.String:
		return model_core.NewSimplePatchedMessage[TMetadata](&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_Str{
				Str: string(typedValue),
			},
		}), nil
	case *eval.Builtin:
		if typedValue.Receiver() != nil {
			// Builtins are encoded by name, meaning the
			// receiver of methods would get lost.
			return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, fmt.Errorf("method %s of %s value cannot be encoded", typedValue.Name(), typedValue.Receiver().Type())
		}
		return model_core.NewSimplePatchedMessage[TMetadata](&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_Builtin{
				Builtin: typedValue.Name(),
			},
		}), nil
	case eval.Function:
		return encodeFunctionValue(thread, typedValue, path, options)
	case eval.List:
		elements, err := encodeIterable(thread, typedValue, path, options)
		if err != nil {
			return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, err
		}
		return model_core.NewPatchedMessage(
			&model_starlark_pb.Value{
				Kind: &model_starlark_pb.Value_List{
					List: &model_starlark_pb.List{
						Elements: elements.Message,
					},
				},
			},
			elements.Patcher,
		), nil
	case eval.Set:
		elements, err := encodeIterable(thread, typedValue, path, options)
		if err != nil {
			return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, err
		}
		return model_core.NewPatchedMessage(
			&model_starlark_pb.Value{
				Kind: &model_starlark_pb.Value_Set{
					Set: &model_starlark_pb.Set{
						Elements: elements.Message,
					},
				},
			},
			elements.Patcher,
		), nil
	case eval.Tuple:
		elements, err := eval.Elements(thread, typedValue)
		if err != nil {
			return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, err
		}
		encodedElements := make([]*model_starlark_pb.Value, 0, len(elements))
		patcher := model_core.NewReferenceMessagePatcher[TMetadata]()
		for i, element := range elements {
			encodedElement, err := EncodeValue(thread, element, path, nil, options)
			if err != nil {
				return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, fmt.Errorf("at index %d: %w", i, err)
			}
			encodedElements = append(encodedElements, encodedElement.Message)
			patcher.Merge(encodedElement.Patcher)
		}
		return model_core.NewPatchedMessage(
			&model_starlark_pb.Value{
				Kind: &model_starlark_pb.Value_Tuple{
					Tuple: &model_starlark_pb.Tuple{
						Elements: encodedElements,
					},
				},
			},
			patcher,
		), nil
	case eval.Dict:
		return encodeDict(thread, typedValue, path, options)
	case nil:
		return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, errors.New("no value provided")
	default:
		return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, fmt.Errorf("value of type %s cannot be encoded", value.Type())
	}
}

// EncodeStructFields encodes a set of named values to a Struct.Fields
// message, such as the attributes of a module extension tag or repo.
func EncodeStructFields[TReference any, TMetadata model_core.ReferenceMetadata](thread *eval.Thread, fields map[string]eval.Value, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Struct_Fields, TMetadata], error) {
	keys := slices.Sorted(maps.Keys(fields))
	values, err := EncodeListElements(thread, func(yield func(eval.Value) bool) {
		for _, key := range keys {
			if !yield(fields[key]) {
				return
			}
		}
	}, nil, options)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Struct_Fields, TMetadata]{}, err
	}
	return model_core.NewPatchedMessage(
		&model_starlark_pb.Struct_Fields{
			Keys:   keys,
			Values: values.Message,
		},
		values.Patcher,
	), nil
}

// encodeIterable encodes the elements of an iterable value, such as a
// list or set, to a B-tree of Protobuf messages.
func encodeIterable[TReference any, TMetadata model_core.ReferenceMetadata](thread *eval.Thread, iterable eval.Iterable, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[[]*model_starlark_pb.List_Element, TMetadata], error) {
	var errIter error
	elements, err := EncodeListElements(thread, iterable.Iterate(thread, &errIter), path, options)
	if err != nil {
		return model_core.PatchedMessage[[]*model_starlark_pb.List_Element, TMetadata]{}, err
	}
	if errIter != nil {
		elements.Discard()
		return model_core.PatchedMessage[[]*model_starlark_pb.List_Element, TMetadata]{}, errIter
	}
	return elements, nil
}

// EncodeListElements encodes a sequence of values to a B-tree of
// Protobuf messages.
func EncodeListElements[TReference any, TMetadata model_core.ReferenceMetadata](thread *eval.Thread, values func(func(eval.Value) bool), path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[[]*model_starlark_pb.List_Element, TMetadata], error) {
	listBuilder := NewListBuilder[TReference, TMetadata](options)
	defer listBuilder.Discard()

	i := 0
	var errEncode error
	for value := range values {
		encodedValue, err := EncodeValue(thread, value, path, nil, options)
		if err != nil {
			errEncode = fmt.Errorf("at index %d: %w", i, err)
			break
		}
		if err := listBuilder.PushChild(model_core.NewPatchedMessage(
			&model_starlark_pb.List_Element{
				Level: &model_starlark_pb.List_Element_Leaf{
					Leaf: encodedValue.Message,
				},
			},
			encodedValue.Patcher,
		)); err != nil {
			errEncode = err
			break
		}
		i++
	}
	if errEncode != nil {
		return model_core.PatchedMessage[[]*model_starlark_pb.List_Element, TMetadata]{}, errEncode
	}
	return listBuilder.FinalizeList()
}

// encodeDict encodes a dict to a B-tree of Protobuf messages,
// preserving insertion order.
func encodeDict[TReference any, TMetadata model_core.ReferenceMetadata](thread *eval.Thread, d eval.Dict, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], error) {
	treeBuilder := newBTreeBuilder(
		options,
		/* isParent = */ func(entry *model_starlark_pb.Dict_Entry) bool {
			return entry.GetParent() != nil
		},
		/* parentNodeComputer = */ btree.Capturing(options.Context, options.ObjectCapturer, func(
			createdObject model_core.Decodable[model_core.MetadataEntry[TMetadata]],
			childNodes model_core.Message[[]*model_starlark_pb.Dict_Entry, object.LocalReference],
		) model_core.PatchedMessage[*model_starlark_pb.Dict_Entry, TMetadata] {
			return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_starlark_pb.Dict_Entry {
				return &model_starlark_pb.Dict_Entry{
					Level: &model_starlark_pb.Dict_Entry_Parent_{
						Parent: &model_starlark_pb.Dict_Entry_Parent{
							Reference: patcher.AddDecodableReference(createdObject),
						},
					},
				}
			})
		}),
	)
	defer treeBuilder.Discard()

	var errIter error
	for key, value := range d.Items(thread, &errIter) {
		encodedKey, err := EncodeValue(thread, key, path, nil, options)
		if err != nil {
			return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, fmt.Errorf("in key: %w", err)
		}
		encodedValue, err := EncodeValue(thread, value, path, nil, options)
		if err != nil {
			return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, fmt.Errorf("in value: %w", err)
		}
		encodedKey.Patcher.Merge(encodedValue.Patcher)
		if err := treeBuilder.PushChild(model_core.NewPatchedMessage(
			&model_starlark_pb.Dict_Entry{
				Level: &model_starlark_pb.Dict_Entry_Leaf_{
					Leaf: &model_starlark_pb.Dict_Entry_Leaf{
						Key:   encodedKey.Message,
						Value: encodedValue.Message,
					},
				},
			},
			encodedKey.Patcher,
		)); err != nil {
			return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, err
		}
	}
	if errIter != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, errIter
	}
	entries, err := treeBuilder.FinalizeList()
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, err
	}
	return model_core.NewPatchedMessage(
		&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_Dict{
				Dict: &model_starlark_pb.Dict{
					Entries: entries.Message,
				},
			},
		},
		entries.Patcher,
	), nil
}
