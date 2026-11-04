package starlark

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"maps"
	"slices"
	"sort"
	"strings"

	pg_label "bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	model_parser "bonanza.build/pkg/model/parser"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/syntax"
	"bonanza.build/pkg/storage/object"
)

// Struct value that is either created using struct() or by calling into
// a provider.
//
// We assume that the number of fields in a struct is small enough, that
// the keys of a struct don't exceed the maximum size of an object in
// storage. This allows us to store all keys in a sorted list. The
// values of the fields may then be stored in a separate B-tree backed
// list. This allows functions like dir() and hasattr() to perform well
// and not read an excessive amount of data from storage.
//
// Values of fields may either be decoded or backed by storage. Fields
// that are backed by storage are decoded when they are first accessed.
// As Starlark values are never shared between threads, there is no
// need to synchronize access to decoded values.
type Struct[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	providerInstanceProperties *ProviderInstanceProperties[TReference, TMetadata]
	keys                       []string
	values                     []any

	decodedValues []eval.Value
	hash          uint32
}

var (
	_ EncodableValue[object.LocalReference, model_core.ReferenceMetadata] = (*Struct[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ eval.Comparable                                                     = (*Struct[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ eval.HasAttrs                                                       = (*Struct[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ eval.Hashable                                                       = (*Struct[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ eval.Mapping                                                        = (*Struct[object.LocalReference, model_core.ReferenceMetadata])(nil)
)

// NewStructFromDict creates a Starlark struct value that has the fields
// that are specified in a map. Values may either be decoded or encoded,
// having types eval.Value and
// model_core.Message[*model_starlark_pb.Value, TReference],
// respectively.
func NewStructFromDict[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](providerInstanceProperties *ProviderInstanceProperties[TReference, TMetadata], entries map[string]any) *Struct[TReference, TMetadata] {
	keys := slices.Sorted(maps.Keys(entries))
	values := make([]any, 0, len(entries))
	for _, k := range keys {
		values = append(values, entries[k])
	}
	return newStructFromLists[TReference, TMetadata](providerInstanceProperties, keys, values)
}

func newStructFromLists[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](providerInstanceProperties *ProviderInstanceProperties[TReference, TMetadata], keys []string, values []any) *Struct[TReference, TMetadata] {
	return &Struct[TReference, TMetadata]{
		providerInstanceProperties: providerInstanceProperties,
		keys:                       keys,
		values:                     values,
		decodedValues:              make([]eval.Value, len(values)),
	}
}

// Type returns the name of the type of a Starlark struct value.
func (s *Struct[TReference, TMetadata]) Type() string {
	if s.providerInstanceProperties != nil && s.providerInstanceProperties.typeName != "" {
		// Type that in this implementation is written in pure
		// Starlark, but in Bazel is implemented in Java as a
		// custom type.
		return s.providerInstanceProperties.typeName
	}
	return "struct"
}

// Truth returns whethe a Starlark struct value evaluates to true or
// false when implicitly converted to a Boolean value. Structs always
// evaluate to true, even if they are empty.
func (Struct[TReference, TMetadata]) Truth(*eval.Thread) (bool, error) {
	return true, nil
}

// Repr writes the representation of a Starlark struct value. Instances
// of providers are prefixed with the name of the provider, as is done
// by Bazel.
func (s *Struct[TReference, TMetadata]) Repr(thread *eval.Thread, p *eval.Printer) error {
	if pip := s.providerInstanceProperties; pip != nil && pip.Identifier != nil {
		p.WriteString(pip.Identifier.GetStarlarkIdentifier().String())
	} else {
		p.WriteString("struct")
	}
	p.WriteString("(")
	for i, key := range s.keys {
		if i > 0 {
			p.WriteString(", ")
		}
		p.WriteString(key)
		p.WriteString(" = ")
		value, err := s.fieldAtIndex(thread, i)
		if err != nil {
			return fmt.Errorf("field %#v: %w", key, err)
		}
		if err := value.Repr(thread, p); err != nil {
			return err
		}
	}
	p.WriteString(")")
	return nil
}

// Hash a Starlark struct value, so that it can be used as a map key or
// added to a set.
func (s *Struct[TReference, TMetadata]) Hash(thread *eval.Thread) (uint32, error) {
	if s.hash == 0 {
		// The same math as performed by starlarkstruct.
		var h, m uint32 = 8731, 9839
		for i, key := range s.keys {
			keyHash, err := eval.Hash(thread, eval.String(key))
			if err != nil {
				return 0, fmt.Errorf("key of field %#v: %w", key, err)
			}

			value, err := s.fieldAtIndex(thread, i)
			if err != nil {
				return 0, fmt.Errorf("value of field %#v: %w", key, err)
			}
			valueHash, err := eval.Hash(thread, value)
			if err != nil {
				return 0, fmt.Errorf("value of field %#v: %w", key, err)
			}

			h ^= 3 * keyHash
			h ^= m * valueHash
			m += 7349
		}
		if h == 0 {
			h = 1
		}

		// Values that are hashable are immutable, meaning it is
		// safe to cache the hash for later use.
		s.hash = h
	}
	return s.hash, nil
}

func (s *Struct[TReference, TMetadata]) fieldAtIndex(thread *eval.Thread, index int) (eval.Value, error) {
	switch typedValue := s.values[index].(type) {
	case eval.Value:
		return typedValue, nil
	case model_core.Message[*model_starlark_pb.Value, TReference]:
		if decodedValue := s.decodedValues[index]; decodedValue != nil {
			return decodedValue, nil
		}
		decodedValue, err := DecodeValue[TReference, TMetadata](thread, typedValue, nil)
		if err != nil {
			return nil, err
		}
		s.decodedValues[index] = decodedValue
		return decodedValue, nil
	default:
		panic("unknown value type")
	}
}

// Attr returns the value of a field contained in a Starlark struct
// value.
func (s *Struct[TReference, TMetadata]) Attr(thread *eval.Thread, name string) (eval.Value, error) {
	if index, ok := sort.Find(
		len(s.keys),
		func(i int) int { return strings.Compare(name, s.keys[i]) },
	); ok {
		return s.fieldAtIndex(thread, index)
	}

	if pip := s.providerInstanceProperties; pip != nil {
		if function, ok := pip.computedFields[name]; ok {
			return eval.Call(thread, function, eval.Args{Positional: []eval.Value{s}})
		}
	}

	return nil, eval.NoSuchAttrError{Value: s, Name: name}
}

// AttrNames returns the names of the fields contained in a Starlark
// struct value.
func (s *Struct[TReference, TMetadata]) AttrNames(*eval.Thread) ([]string, error) {
	if pip := s.providerInstanceProperties; pip != nil && len(pip.computedFields) > 0 {
		allKeys := append(slices.Collect(maps.Keys(pip.computedFields)), s.keys...)
		sort.Strings(allKeys)
		return slices.Compact(allKeys), nil
	}
	return s.keys, nil
}

// Get a field contained in a Starlark struct value.
func (s *Struct[TReference, TMetadata]) Get(thread *eval.Thread, key eval.Value) (eval.Value, bool, error) {
	if s.providerInstanceProperties == nil || !s.providerInstanceProperties.dictLike {
		return nil, true, errors.New("only structs that were instantiated through a provider that was declared with dict_like=True may be accessed like a dict")
	}

	keyStr, ok, err := eval.AsString(thread, key)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, errors.New("keys have to be of type string")
	}
	index, ok := sort.Find(
		len(s.keys),
		func(i int) int { return strings.Compare(string(keyStr), s.keys[i]) },
	)
	if !ok {
		return nil, false, nil
	}

	value, err := s.fieldAtIndex(thread, index)
	return value, true, err
}

func (s *Struct[TReference, TMetadata]) equals(thread *eval.Thread, other *Struct[TReference, TMetadata], depth int) (bool, error) {
	if s != other {
		// Compare providers.
		if (s.providerInstanceProperties == nil) != (other.providerInstanceProperties == nil) || (s.providerInstanceProperties != nil &&
			!s.providerInstanceProperties.LateNamedValue.equals(&other.providerInstanceProperties.LateNamedValue)) {
			return false, nil
		}

		// Compare keys.
		if !slices.Equal(s.keys, other.keys) {
			return false, nil
		}

		// Compare values.
		//
		// TODO: Do we want to optimize this to prevent unnecessary
		// decoding of values, or do we only perform struct comparisons
		// sparingly?
		for i, key := range s.keys {
			va, err := s.fieldAtIndex(thread, i)
			if err != nil {
				return false, fmt.Errorf("field %#v: %w", key, err)
			}
			vb, err := other.fieldAtIndex(thread, i)
			if err != nil {
				return false, fmt.Errorf("field %#v: %w", key, err)
			}
			if equal, err := eval.EqualDepth(thread, va, vb, depth-1); err != nil {
				return false, fmt.Errorf("field %#v: %w", key, err)
			} else if !equal {
				return false, nil
			}
		}
	}
	return true, nil
}

// CompareSameType compares two Starlark struct values for equality.
// Structs are considered equal if the names and values of each field
// are equal.
func (s *Struct[TReference, TMetadata]) CompareSameType(thread *eval.Thread, op syntax.Token, other eval.Value, depth int) (bool, error) {
	otherStruct, ok := other.(*Struct[TReference, TMetadata])
	if !ok {
		// Provider instances may have custom type names, which
		// could coincide with the names of other types.
		return op == syntax.NEQ, nil
	}
	switch op {
	case syntax.EQL:
		return s.equals(thread, otherStruct, depth)
	case syntax.NEQ:
		equal, err := s.equals(thread, otherStruct, depth)
		return !equal, err
	default:
		return false, errors.New("structs cannot be compared for inequality")
	}
}

// ToDict returns the fields contained in a Starlark struct value in the
// form of a dictionary. The resulting values may either be decoded or
// encoded, having types eval.Value and
// model_core.Message[*model_starlark_pb.Value, TReference],
// respectively.
func (s *Struct[TReference, TMetadata]) ToDict() map[string]any {
	dict := make(map[string]any, len(s.keys))
	for i, k := range s.keys {
		dict[k] = s.values[i]
	}
	return dict
}

// EncodeStructFields encodes the fields of a Starlark struct value to a
// list of a list of Protobuf messages. This allows the fields to be
// written to storage and reloaded later.
//
// This method differs from Encode() and EncodeValue() in that it
// returns a bare list of fields. This should be used in cases where a
// value is expected to only be a struct, and any provider identifier
// may be discarded.
func (s *Struct[TReference, TMetadata]) EncodeStructFields(thread *eval.Thread, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Struct_Fields, TMetadata], error) {
	listBuilder := NewListBuilder[TReference, TMetadata](options)
	defer listBuilder.Discard()

	for i, value := range s.values {
		var encodedValue model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]
		switch typedValue := value.(type) {
		case eval.Value:
			var err error
			encodedValue, err = EncodeValue[TReference, TMetadata](thread, typedValue, path, nil, options)
			if err != nil {
				return model_core.PatchedMessage[*model_starlark_pb.Struct_Fields, TMetadata]{}, fmt.Errorf("field %#v: %w", s.keys[i], err)
			}
		case model_core.Message[*model_starlark_pb.Value, TReference]:
			encodedValue = model_core.Patch(options.ObjectCapturer, typedValue)
		default:
			panic("unknown value type")
		}
		if err := listBuilder.PushChild(model_core.NewPatchedMessage(
			&model_starlark_pb.List_Element{
				Level: &model_starlark_pb.List_Element_Leaf{
					Leaf: encodedValue.Message,
				},
			},
			encodedValue.Patcher,
		)); err != nil {
			return model_core.PatchedMessage[*model_starlark_pb.Struct_Fields, TMetadata]{}, err
		}
	}

	values, err := listBuilder.FinalizeList()
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Struct_Fields, TMetadata]{}, err
	}

	// TODO: Should we use inlinedtree here to values in a separate
	// object if it turns out the keys push us over the limit?
	return model_core.NewPatchedMessage(
		&model_starlark_pb.Struct_Fields{
			Keys:   s.keys,
			Values: values.Message,
		},
		values.Patcher,
	), nil
}

// Encode a Starlark struct value and all of its fields to a Struct
// Protobuf message. This allows it to be written to storage and
// reloaded later.
//
// This method differs from EncodeValue() in that it returns a bare
// Struct message. This should be used in cases where a value is
// expected to only be a struct or provider instance, such as the return
// value of a rule implementation function.
func (s *Struct[TReference, TMetadata]) Encode(thread *eval.Thread, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Struct, TMetadata], error) {
	return model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (*model_starlark_pb.Struct, error) {
		fields, err := s.EncodeStructFields(thread, path, options)
		if err != nil {
			return nil, err
		}
		patcher.Merge(fields.Patcher)

		var providerInstanceProperties *model_starlark_pb.Provider_InstanceProperties
		if pip := s.providerInstanceProperties; pip != nil {
			m, err := pip.Encode(thread, path, options)
			if err != nil {
				return nil, err
			}
			providerInstanceProperties = m.Merge(patcher)
		}

		return &model_starlark_pb.Struct{
			Fields:                     fields.Message,
			ProviderInstanceProperties: providerInstanceProperties,
		}, nil
	})
}

// EncodeValue encodes a Starlark struct value and all of its fields to
// a Starlark value Protobuf message. This allows it to be written to
// storage and reloaded later.
func (s *Struct[TReference, TMetadata]) EncodeValue(thread *eval.Thread, path []eval.Value, currentIdentifier *pg_label.CanonicalStarlarkIdentifier, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], error) {
	encodedStruct, err := s.Encode(thread, path, options)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, err
	}
	return model_core.NewPatchedMessage(
		&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_Struct{
				Struct: encodedStruct.Message,
			},
		},
		encodedStruct.Patcher,
	), nil
}

// GetProviderIdentifier returns the identifier of the provider that was
// used to construct this Starlark struct value. This method fails if
// this Starlark struct value was created by calling struct(), as
// opposed to using a provider.
func (s *Struct[TReference, TMetadata]) GetProviderIdentifier() (pg_label.CanonicalStarlarkIdentifier, error) {
	var bad pg_label.CanonicalStarlarkIdentifier
	pip := s.providerInstanceProperties
	if pip == nil {
		return bad, errors.New("struct was not created using a provider")
	}
	if pip.Identifier == nil {
		return bad, errors.New("provider that was used to create the struct does not have a name")
	}
	return *pip.Identifier, nil
}

// DecodeStruct converts a Protobuf message of a struct to a Starlark
// struct value. The values of fields are only decoded when accessed.
func DecodeStruct[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](thread *eval.Thread, m model_core.Message[*model_starlark_pb.Struct, TReference]) (*Struct[TReference, TMetadata], error) {
	options, err := getValueDecodingOptions[TReference](thread)
	if err != nil {
		return nil, err
	}

	var providerInstanceProperties *ProviderInstanceProperties[TReference, TMetadata]
	if pip := m.Message.ProviderInstanceProperties; pip != nil {
		providerInstanceProperties, err = decodeProviderInstanceProperties[TReference, TMetadata](model_core.Nested(m, pip))
		if err != nil {
			return nil, err
		}
	}

	var keys []string
	var values []any
	var errIter error
	for key, value := range AllStructFields(
		thread.Context(),
		options.Readers.List,
		model_core.Nested(m, m.Message.Fields),
		&errIter,
	) {
		keys = append(keys, key)
		values = append(values, value)
	}
	if errIter != nil {
		return nil, errIter
	}

	return newStructFromLists[TReference, TMetadata](providerInstanceProperties, keys, values), nil
}

// AllStructFields iterates over all fields contained in a Starlark
// struct that has been encoded and is backed by storage.
func AllStructFields[TReference any](
	ctx context.Context,
	reader model_parser.MessageObjectReader[TReference, []*model_starlark_pb.List_Element],
	structFields model_core.Message[*model_starlark_pb.Struct_Fields, TReference],
	errOut *error,
) iter.Seq2[string, model_core.Message[*model_starlark_pb.Value, TReference]] {
	if structFields.Message == nil {
		*errOut = errors.New("no struct fields provided")
		return func(yield func(string, model_core.Message[*model_starlark_pb.Value, TReference]) bool) {
		}
	}

	allLeaves := btree.AllLeaves(
		ctx,
		reader,
		model_core.Nested(structFields, structFields.Message.Values),
		func(element model_core.Message[*model_starlark_pb.List_Element, TReference]) (*model_core_pb.DecodableReference, error) {
			return element.Message.GetParent().GetReference(), nil
		},
		errOut,
	)

	keys := structFields.Message.Keys
	return func(yield func(string, model_core.Message[*model_starlark_pb.Value, TReference]) bool) {
		allLeaves(func(entry model_core.Message[*model_starlark_pb.List_Element, TReference]) bool {
			leaf, ok := entry.Message.Level.(*model_starlark_pb.List_Element_Leaf)
			if !ok {
				*errOut = errors.New("not a valid leaf entry")
				return false
			}

			if len(keys) == 0 {
				*errOut = errors.New("struct has fewer keys than values")
				return false
			}
			key := keys[0]
			keys = keys[1:]

			return yield(key, model_core.Nested(entry, leaf.Leaf))
		})
	}
}

// GetStructFieldValue returns the value that is associated with a field
// of a struct. This function assumes that the struct was created
// previous and is backed by storage.
func GetStructFieldValue[TReference any](
	ctx context.Context,
	reader model_parser.MessageObjectReader[TReference, []*model_starlark_pb.List_Element],
	structFields model_core.Message[*model_starlark_pb.Struct_Fields, TReference],
	key string,
) (model_core.Message[*model_starlark_pb.Value, TReference], error) {
	if structFields.Message == nil {
		return model_core.Message[*model_starlark_pb.Value, TReference]{}, errors.New("no struct fields provided")
	}

	// Look up the index of the provided key.
	keys := structFields.Message.Keys
	keyIndex, ok := sort.Find(
		len(keys),
		func(i int) int { return strings.Compare(key, keys[i]) },
	)
	if !ok {
		return model_core.Message[*model_starlark_pb.Value, TReference]{}, errors.New("struct field not found")
	}

	// Look up the value with the given index. Values are stored in
	// a list, having the same length as the list of keys.
	list := model_core.Nested(structFields, structFields.Message.Values)
	valueIndex := uint64(keyIndex)
	leafCount := uint64(len(keys))
GetValueAtIndex:
	for {
		// List elements may never refer to empty nested lists,
		// meaning that if the length of a list is equal to the
		// expected total number of elements, each list element
		// contains exactly one value. This allows us to jump
		// directly to the right spot.
		if uint64(len(list.Message)) == leafCount {
			list.Message = list.Message[valueIndex:]
			valueIndex = 0
		}

		for _, element := range list.Message {
			switch level := element.Level.(type) {
			case *model_starlark_pb.List_Element_Parent_:
				if valueIndex < level.Parent.Count {
					var err error
					list, err = model_parser.Dereference(ctx, reader, model_core.Nested(list, level.Parent.Reference))
					if err != nil {
						return model_core.Message[*model_starlark_pb.Value, TReference]{}, err
					}
					leafCount = level.Parent.Count
					continue GetValueAtIndex
				} else {
					valueIndex -= level.Parent.Count
				}
			case *model_starlark_pb.List_Element_Leaf:
				if valueIndex == 0 {
					return model_core.Nested(list, level.Leaf), nil
				}
				valueIndex--
			}
		}
		return model_core.Message[*model_starlark_pb.Value, TReference]{}, errors.New("number of keys does not match number of values")
	}
}
