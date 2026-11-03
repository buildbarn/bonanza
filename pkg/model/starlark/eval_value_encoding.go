package starlark

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/storage/object"

	"google.golang.org/protobuf/types/known/emptypb"
)

// The functions in this file encode values of the core types of
// pkg/starlark/eval (None, bool, int, string, list, tuple, dict and
// set) to Protobuf messages, using the same representation as
// EncodeValue() does for values of go.starlark.net. They are used by
// code that has already been migrated to pkg/starlark/eval.

// EncodeEvalValue encodes a value of one of the core types of
// pkg/starlark/eval to a Starlark value Protobuf message, so that it
// can be written to storage.
func EncodeEvalValue[TReference any, TMetadata model_core.ReferenceMetadata](thread *eval.Thread, value eval.Value, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], error) {
	return encodeEvalValue(thread, value, nil, options)
}

// EncodeEvalStructFields encodes a set of named values of the core
// types of pkg/starlark/eval to a Struct.Fields message, such as the
// attributes of a module extension tag or repo.
func EncodeEvalStructFields[TReference any, TMetadata model_core.ReferenceMetadata](thread *eval.Thread, fields map[string]eval.Value, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Struct_Fields, TMetadata], error) {
	keys := slices.Sorted(maps.Keys(fields))
	values, err := encodeEvalListElements(thread, func(yield func(eval.Value) bool) {
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

// encodeEvalValue encodes a value. path contains the mutable composite
// values that are being encoded, which is used to detect cycles.
func encodeEvalValue[TReference any, TMetadata model_core.ReferenceMetadata](thread *eval.Thread, value eval.Value, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], error) {
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
	case eval.List:
		elements, err := encodeEvalListElements(thread, typedValue.Iterate(thread, nil), path, options)
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
		elements, err := encodeEvalListElements(thread, typedValue.Iterate(thread, nil), path, options)
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
			encodedElement, err := encodeEvalValue(thread, element, path, options)
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
		return encodeEvalDict(thread, typedValue, path, options)
	default:
		return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, fmt.Errorf("value of type %s cannot be encoded", value.Type())
	}
}

// encodeEvalListElements encodes a sequence of values to a B-tree of
// Protobuf messages.
func encodeEvalListElements[TReference any, TMetadata model_core.ReferenceMetadata](thread *eval.Thread, values func(func(eval.Value) bool), path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[[]*model_starlark_pb.List_Element, TMetadata], error) {
	listBuilder := NewListBuilder[TReference, TMetadata](options)
	defer listBuilder.Discard()

	i := 0
	var errEncode error
	for value := range values {
		encodedValue, err := encodeEvalValue(thread, value, path, options)
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

// encodeEvalDict encodes a dict to a B-tree of Protobuf messages,
// preserving insertion order.
func encodeEvalDict[TReference any, TMetadata model_core.ReferenceMetadata](thread *eval.Thread, d eval.Dict, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], error) {
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
		encodedKey, err := encodeEvalValue(thread, key, path, options)
		if err != nil {
			return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, fmt.Errorf("in key: %w", err)
		}
		encodedValue, err := encodeEvalValue(thread, value, path, options)
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
