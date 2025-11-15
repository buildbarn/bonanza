package starlark

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"iter"
	"maps"
	"math"
	"math/big"
	"math/bits"
	"slices"
	"strings"

	pg_label "bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_parser "bonanza.build/pkg/model/parser"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/storage/object"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

const ValueEncodingOptionsKey = "value_encoding_options"

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

func NewListBuilder[TReference any, TMetadata model_core.ReferenceMetadata](options *ValueEncodingOptions[TReference, TMetadata]) btree.Builder[*model_starlark_pb.List_Element, TMetadata] {
	return newBTreeBuilder(
		options,
		func(element *model_starlark_pb.List_Element) bool {
			return element.GetParent() != nil
		},
		btree.Capturing(options.Context, options.ObjectCapturer, ComputeListParentNode),
	)
}

type EncodableValue[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	EncodeValue(path map[starlark.Value]struct{}, currentIdentifier *pg_label.CanonicalStarlarkIdentifier, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], bool, error)
}

func EncodeCompiledProgram[TReference any, TMetadata model_core.ReferenceMetadata](program *starlark.Program, globals starlark.StringDict, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.CompiledProgram, TMetadata], error) {
	needsCode := false
	var globalsKeys []string
	globalsValuesBuilder := NewListBuilder[TReference, TMetadata](options)
	for _, name := range slices.Sorted(maps.Keys(globals)) {
		identifier, err := pg_label.NewStarlarkIdentifier(name)
		if err != nil {
			return model_core.PatchedMessage[*model_starlark_pb.CompiledProgram, TMetadata]{}, err
		}
		var currentIdentifier *pg_label.CanonicalStarlarkIdentifier
		if options.CurrentFilename != nil {
			i := options.CurrentFilename.AppendStarlarkIdentifier(identifier)
			currentIdentifier = &i
		}
		value := globals[name]
		if _, ok := value.(NamedGlobal); ok || identifier.IsPublic() {
			encodedValue, valueNeedsCode, err := EncodeValue[TReference, TMetadata](value, map[starlark.Value]struct{}{}, currentIdentifier, options)
			if err != nil {
				return model_core.PatchedMessage[*model_starlark_pb.CompiledProgram, TMetadata]{}, fmt.Errorf("global %#v: %w", name, err)
			}
			needsCode = needsCode || valueNeedsCode
			globalsKeys = append(globalsKeys, name)
			if err := globalsValuesBuilder.PushChild(model_core.NewPatchedMessage(
				&model_starlark_pb.List_Element{
					Level: &model_starlark_pb.List_Element_Leaf{
						Leaf: encodedValue.Message,
					},
				},
				encodedValue.Patcher,
			)); err != nil {
				return model_core.PatchedMessage[*model_starlark_pb.CompiledProgram, TMetadata]{}, err
			}
		}
	}

	globalsValues, err := globalsValuesBuilder.FinalizeList()
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.CompiledProgram, TMetadata]{}, err
	}

	var code bytes.Buffer
	if needsCode {
		if err := program.Write(&code); err != nil {
			return model_core.PatchedMessage[*model_starlark_pb.CompiledProgram, TMetadata]{}, err
		}
	}

	return model_core.NewPatchedMessage(
		&model_starlark_pb.CompiledProgram{
			Globals: &model_starlark_pb.Struct_Fields{
				Keys:   globalsKeys,
				Values: globalsValues.Message,
			},
			Code: code.Bytes(),
		},
		globalsValues.Patcher,
	), nil
}

func EncodeValue[TReference any, TMetadata model_core.ReferenceMetadata](value starlark.Value, path map[starlark.Value]struct{}, currentIdentifier *pg_label.CanonicalStarlarkIdentifier, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], bool, error) {
	if value == starlark.None {
		return model_core.NewSimplePatchedMessage[TMetadata](&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_None{
				None: &emptypb.Empty{},
			},
		}), false, nil
	}
	switch typedValue := value.(type) {
	case starlark.Bool:
		return model_core.NewSimplePatchedMessage[TMetadata](&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_Bool{
				Bool: bool(typedValue),
			},
		}), false, nil
	case *starlark.Builtin:
		return model_core.NewSimplePatchedMessage[TMetadata](&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_Builtin{
				Builtin: typedValue.Name(),
			},
		}), false, nil
	case starlark.Bytes:
		return model_core.NewSimplePatchedMessage[TMetadata](&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_Bytes{
				Bytes: []byte(typedValue),
			},
		}), false, nil
	case *starlark.Dict:
		if _, ok := path[value]; ok {
			return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, false, errors.New("value is defined recursively")
		}
		path[value] = struct{}{}
		defer delete(path, value)

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

		needsCode := false
		for key, value := range starlark.Entries(nil, typedValue) {
			encodedKey, keyNeedsCode, err := EncodeValue[TReference, TMetadata](key, path, nil, options)
			if err != nil {
				return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, false, fmt.Errorf("in key: %w", err)
			}
			encodedValue, valueNeedsCode, err := EncodeValue[TReference, TMetadata](value, path, nil, options)
			if err != nil {
				return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, false, fmt.Errorf("in value: %w", err)
			}
			encodedKey.Patcher.Merge(encodedValue.Patcher)
			needsCode = needsCode || keyNeedsCode || valueNeedsCode
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
				return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, false, err
			}
		}

		entries, err := treeBuilder.FinalizeList()
		if err != nil {
			return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, false, err
		}

		// TODO: This should use inlinedtree to ensure the
		// resulting Value object is not too large.
		return model_core.NewPatchedMessage(
			&model_starlark_pb.Value{
				Kind: &model_starlark_pb.Value_Dict{
					Dict: &model_starlark_pb.Dict{
						Entries: entries.Message,
					},
				},
			},
			entries.Patcher,
		), needsCode, nil
	case *starlark.Function:
		return NewNamedFunction(NewStarlarkNamedFunctionDefinition[TReference, TMetadata](typedValue)).
			EncodeValue(path, currentIdentifier, options)
	case starlark.Int:
		bigInt := typedValue.BigInt()
		return model_core.NewSimplePatchedMessage[TMetadata](&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_Int{
				Int: &model_starlark_pb.Int{
					AbsoluteValue: bigInt.Bytes(),
					Negative:      bigInt.Sign() < 0,
				},
			},
		}), false, nil
	case *starlark.List:
		if _, ok := path[value]; ok {
			return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, false, errors.New("value is defined recursively")
		}
		path[value] = struct{}{}
		defer delete(path, value)

		elements, needsCode, err := encodeListElements(typedValue.Elements(), path, options)
		if err != nil {
			return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, false, err
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
		), needsCode, nil
	case *starlark.Set:
		elements, needsCode, err := encodeListElements(typedValue.Elements(), path, options)
		if err != nil {
			return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, false, err
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
		), needsCode, nil
	case starlark.String:
		return model_core.NewSimplePatchedMessage[TMetadata](&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_Str{
				Str: string(typedValue),
			},
		}), false, nil
	case starlark.Tuple:
		encodedValues := make([]*model_starlark_pb.Value, 0, len(typedValue))
		patcher := model_core.NewReferenceMessagePatcher[TMetadata]()
		needsCode := false
		for _, value := range typedValue {
			encodedValue, valueNeedsCode, err := EncodeValue[TReference, TMetadata](value, path, nil, options)
			if err != nil {
				return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, false, err
			}
			encodedValues = append(encodedValues, encodedValue.Message)
			patcher.Merge(encodedValue.Patcher)
			needsCode = needsCode || valueNeedsCode
		}
		return model_core.NewPatchedMessage(
			&model_starlark_pb.Value{
				Kind: &model_starlark_pb.Value_Tuple{
					Tuple: &model_starlark_pb.Tuple{
						Elements: encodedValues,
					},
				},
			},
			patcher,
		), false, nil
	case EncodableValue[TReference, TMetadata]:
		return typedValue.EncodeValue(path, currentIdentifier, options)
	case nil:
		return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, false, errors.New("no value provided")
	default:
		return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, false, fmt.Errorf("value of type %s cannot be encoded", value.Type())
	}
}

// encodeListElements encodes a sequence of Starlark values to a B-tree
// of Protobuf messages.
func encodeListElements[TReference any, TMetadata model_core.ReferenceMetadata](values iter.Seq[starlark.Value], path map[starlark.Value]struct{}, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[[]*model_starlark_pb.List_Element, TMetadata], bool, error) {
	listBuilder := NewListBuilder[TReference, TMetadata](options)
	defer listBuilder.Discard()

	needsCode := false
	for value := range values {
		encodedValue, valueNeedsCode, err := EncodeValue[TReference, TMetadata](value, path, nil, options)
		if err != nil {
			return model_core.PatchedMessage[[]*model_starlark_pb.List_Element, TMetadata]{}, false, err
		}
		needsCode = needsCode || valueNeedsCode
		if err := listBuilder.PushChild(model_core.NewPatchedMessage(
			&model_starlark_pb.List_Element{
				Level: &model_starlark_pb.List_Element_Leaf{
					Leaf: encodedValue.Message,
				},
			},
			encodedValue.Patcher,
		)); err != nil {
			return model_core.PatchedMessage[[]*model_starlark_pb.List_Element, TMetadata]{}, false, err
		}
	}
	elements, err := listBuilder.FinalizeList()
	return elements, needsCode, err
}

func DecodeGlobals[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](encodedGlobals model_core.Message[*model_starlark_pb.Struct_Fields, TReference], currentFilename pg_label.CanonicalLabel, options *ValueDecodingOptions[TReference]) (starlark.StringDict, error) {
	globals := map[string]starlark.Value{}
	var errIter error
	for key, encodedValue := range AllStructFields(
		options.Context,
		options.Readers.List,
		encodedGlobals,
		&errIter,
	) {
		identifier, err := pg_label.NewStarlarkIdentifier(key)
		if err != nil {
			return nil, err
		}
		currentIdentifier := currentFilename.AppendStarlarkIdentifier(identifier)
		value, err := DecodeValue[TReference, TMetadata](encodedValue, &currentIdentifier, options)
		if err != nil {
			return nil, err
		}
		value.Freeze()
		globals[key] = value
	}
	if errIter != nil {
		return nil, errIter
	}
	return globals, nil
}

const ValueDecodingOptionsKey = "value_decoding_options"

type ValueReaders[TReference any] struct {
	Dict model_parser.MessageObjectReader[TReference, []*model_starlark_pb.Dict_Entry]
	List model_parser.MessageObjectReader[TReference, []*model_starlark_pb.List_Element]
}

type ValueDecodingOptions[TReference any] struct {
	Context         context.Context
	Readers         *ValueReaders[TReference]
	LabelCreator    func(pg_label.ResolvedLabel) (starlark.Value, error)
	BzlFileBuiltins starlark.StringDict
}

// getThread creates a Starlark thread that can be used whenever the
// decoding process depends on having a valid Starlark thread. This is,
// for example, needed when inserting elements into dicts and sets.
func (o *ValueDecodingOptions[TReference]) getThread() *starlark.Thread {
	thread := &starlark.Thread{}
	thread.SetLocal(ValueDecodingOptionsKey, o)
	return thread
}

func DecodeValue[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](encodedValue model_core.Message[*model_starlark_pb.Value, TReference], currentIdentifier *pg_label.CanonicalStarlarkIdentifier, options *ValueDecodingOptions[TReference]) (starlark.Value, error) {
	switch typedValue := encodedValue.Message.GetKind().(type) {
	case *model_starlark_pb.Value_Aspect:
		switch aspectKind := typedValue.Aspect.Kind.(type) {
		case *model_starlark_pb.Aspect_Reference:
			identifier, err := pg_label.NewCanonicalStarlarkIdentifier(aspectKind.Reference)
			if err != nil {
				return nil, err
			}
			return NewAspect[TReference, TMetadata](&identifier, nil), nil
		case *model_starlark_pb.Aspect_Definition_:
			if currentIdentifier == nil {
				return nil, errors.New("encoded aspect does not have a name")
			}
			return NewAspect[TReference, TMetadata](currentIdentifier, aspectKind.Definition), nil
		default:
			return nil, errors.New("encoded aspect does not have a reference or definition")
		}
	case *model_starlark_pb.Value_Attr:
		attrType, err := DecodeAttrType[TReference, TMetadata](model_core.Nested(encodedValue, typedValue.Attr))
		if err != nil {
			return nil, err
		}

		var defaultValue starlark.Value
		if d := typedValue.Attr.Default; d != nil {
			// TODO: Should we also canonicalize?
			var err error
			defaultValue, err = DecodeValue[TReference, TMetadata](
				model_core.Nested(encodedValue, d),
				nil,
				options,
			)
			if err != nil {
				return nil, err
			}
		}
		return NewAttr[TReference, TMetadata](attrType, defaultValue), nil
	case *model_starlark_pb.Value_Bool:
		return starlark.Bool(typedValue.Bool), nil
	case *model_starlark_pb.Value_Builtin:
		parts := strings.Split(typedValue.Builtin, ".")
		value, ok := options.BzlFileBuiltins[parts[0]]
		if !ok {
			return nil, fmt.Errorf("builtin %#v does not exist", parts[0])
		}
		for i := 1; i < len(parts); i++ {
			hasAttrs, ok := value.(starlark.HasAttrs)
			if !ok {
				return nil, fmt.Errorf("builtin %#v does have attributes", strings.Join(parts[:i], "."))
			}
			var err error
			value, err = hasAttrs.Attr(nil, parts[i])
			if err != nil {
				return nil, fmt.Errorf("builtin %#v does not exist", strings.Join(parts[:i+1], "."))
			}
		}
		return value, nil
	case *model_starlark_pb.Value_Bytes:
		return starlark.Bytes(typedValue.Bytes), nil
	case *model_starlark_pb.Value_Depset:
		return decodeDepset[TReference, TMetadata](model_core.Nested(encodedValue, typedValue.Depset)), nil
	case *model_starlark_pb.Value_Dict:
		dict := starlark.NewDict(len(typedValue.Dict.Entries))
		if err := decodeDictEntries[TReference, TMetadata](
			model_core.Nested(encodedValue, typedValue.Dict),
			&dictEntriesDecodingOptions[TReference]{
				valueDecodingOptions: options,
				out:                  dict,
			},
		); err != nil {
			return nil, err
		}
		return dict, nil
	case *model_starlark_pb.Value_ExecGroup:
		execCompatibleWith := make([]pg_label.ResolvedLabel, 0, len(typedValue.ExecGroup.ExecCompatibleWith))
		for _, labelStr := range typedValue.ExecGroup.ExecCompatibleWith {
			label, err := pg_label.NewResolvedLabel(labelStr)
			if err != nil {
				return nil, fmt.Errorf("invalid label %#v: %w", labelStr, err)
			}
			execCompatibleWith = append(execCompatibleWith, label)
		}

		toolchains := make([]*ToolchainType[TReference, TMetadata], 0, len(typedValue.ExecGroup.Toolchains))
		for i, toolchain := range typedValue.ExecGroup.Toolchains {
			toolchainType, err := decodeToolchainType[TReference, TMetadata](toolchain)
			if err != nil {
				return nil, fmt.Errorf("toolchain %d: %w", i, err)
			}
			toolchains = append(toolchains, toolchainType)
		}

		return NewExecGroup(execCompatibleWith, toolchains), nil
	case *model_starlark_pb.Value_File:
		return NewFile[TReference, TMetadata](model_core.Nested(encodedValue, typedValue.File)), nil
	case *model_starlark_pb.Value_Function:
		return NewNamedFunction(NewProtoNamedFunctionDefinition[TReference, TMetadata](
			model_core.Nested(encodedValue, typedValue.Function),
		)), nil
	case *model_starlark_pb.Value_Int:
		var i big.Int
		i.SetBytes(typedValue.Int.AbsoluteValue)
		if typedValue.Int.Negative {
			i.Neg(&i)
		}
		return starlark.MakeBigInt(&i), nil
	case *model_starlark_pb.Value_Label:
		resolvedLabel, err := pg_label.NewResolvedLabel(typedValue.Label)
		if err != nil {
			return nil, fmt.Errorf("invalid label %#v: %w", typedValue.Label, err)
		}
		return options.LabelCreator(resolvedLabel)
	case *model_starlark_pb.Value_Provider:
		return DecodeProvider[TReference, TMetadata](model_core.Nested(encodedValue, typedValue.Provider))
	case *model_starlark_pb.Value_List:
		list := starlark.NewList(nil)
		if err := decodeList_Elements[TReference, TMetadata](
			model_core.Nested(encodedValue, typedValue.List),
			&listElementsDecodingOptions[TReference]{
				valueDecodingOptions: options,
				out:                  list,
			}); err != nil {
			return nil, err
		}
		return list, nil
	case *model_starlark_pb.Value_ModuleExtension:
		return NewModuleExtension(NewProtoModuleExtensionDefinition[TReference, TMetadata](
			model_core.Nested(encodedValue, typedValue.ModuleExtension),
		)), nil
	case *model_starlark_pb.Value_None:
		return starlark.None, nil
	case *model_starlark_pb.Value_RepositoryRule:
		switch repositoryRuleKind := typedValue.RepositoryRule.Kind.(type) {
		case *model_starlark_pb.RepositoryRule_Reference:
			identifier, err := pg_label.NewCanonicalStarlarkIdentifier(repositoryRuleKind.Reference)
			if err != nil {
				return nil, err
			}
			return NewRepositoryRule[TReference, TMetadata](&identifier, nil), nil
		case *model_starlark_pb.RepositoryRule_Definition_:
			if currentIdentifier == nil {
				return nil, errors.New("encoded repository_rule does not have a name")
			}
			return NewRepositoryRule(currentIdentifier, NewProtoRepositoryRuleDefinition[TReference, TMetadata](
				model_core.Nested(encodedValue, repositoryRuleKind.Definition),
			)), nil
		default:
			return nil, errors.New("encoded repository_rule does not have a reference or definition")
		}
	case *model_starlark_pb.Value_Rule:
		switch ruleKind := typedValue.Rule.Kind.(type) {
		case *model_starlark_pb.Rule_Reference:
			identifier, err := pg_label.NewCanonicalStarlarkIdentifier(ruleKind.Reference)
			if err != nil {
				return nil, err
			}
			return NewRule(&identifier, NewReloadingRuleDefinition[TReference, TMetadata](identifier)), nil
		case *model_starlark_pb.Rule_Definition_:
			if currentIdentifier == nil {
				return nil, errors.New("encoded rule does not have a name")
			}
			return NewRule(currentIdentifier, NewProtoRuleDefinition[TReference, TMetadata](
				model_core.Nested(encodedValue, ruleKind.Definition),
			)), nil
		default:
			return nil, errors.New("encoded rule does not have a reference or definition")
		}
	case *model_starlark_pb.Value_Select:
		if len(typedValue.Select.Groups) < 1 {
			return nil, errors.New("select does not contain any groups")
		}
		groups := make([]SelectGroup, 0, len(typedValue.Select.Groups))
		for groupIndex, group := range typedValue.Select.Groups {
			conditions := make(map[pg_label.ResolvedLabel]starlark.Value, len(group.Conditions))
			for _, condition := range group.Conditions {
				conditionIdentifier, err := pg_label.NewResolvedLabel(condition.ConditionIdentifier)
				if err != nil {
					return nil, fmt.Errorf("invalid condition identifier %#v in group %d: %w", condition.ConditionIdentifier, groupIndex, err)
				}
				conditionValue, err := DecodeValue[TReference, TMetadata](model_core.Nested(encodedValue, condition.Value), nil, options)
				if err != nil {
					return nil, fmt.Errorf("condition with identifier %#v in group %d: %w", condition.ConditionIdentifier, groupIndex, err)
				}
				conditions[conditionIdentifier] = conditionValue
			}
			var defaultValue starlark.Value
			noMatchError := ""
			switch noMatch := group.NoMatch.(type) {
			case *model_starlark_pb.Select_Group_NoMatchValue:
				var err error
				defaultValue, err = DecodeValue[TReference, TMetadata](model_core.Nested(encodedValue, noMatch.NoMatchValue), nil, options)
				if err != nil {
					return nil, fmt.Errorf("no match value of group %d: %w", groupIndex, err)
				}
			case *model_starlark_pb.Select_Group_NoMatchError:
				noMatchError = noMatch.NoMatchError
			case nil:
			default:
				return nil, fmt.Errorf("invalid no match value for group %d", groupIndex)
			}
			groups = append(groups, NewSelectGroup(conditions, defaultValue, noMatchError))
		}
		var concatenationOperator syntax.Token
		if len(typedValue.Select.Groups) > 1 {
			switch typedValue.Select.ConcatenationOperator {
			case model_starlark_pb.Select_PIPE:
				concatenationOperator = syntax.PIPE
			case model_starlark_pb.Select_PLUS:
				concatenationOperator = syntax.PLUS
			default:
				return nil, errors.New("invalid concatenation operator")
			}
		}
		return NewSelect[TReference, TMetadata](groups, concatenationOperator), nil
	case *model_starlark_pb.Value_Set:
		thread := options.getThread()
		set := starlark.NewSet(0)
		var errIter error
		for element := range AllListLeafElementsSkippingDuplicateParents(
			options.Context,
			options.Readers.List,
			model_core.Nested(encodedValue, typedValue.Set.Elements),
			map[model_core.Decodable[object.LocalReference]]struct{}{},
			&errIter,
		) {
			element, err := DecodeValue[TReference, TMetadata](element, nil, options)
			if err != nil {
				return nil, err
			}
			if err := set.Insert(thread, element); err != nil {
				return nil, err
			}
		}
		return set, errIter
	case *model_starlark_pb.Value_Str:
		return starlark.String(typedValue.Str), nil
	case *model_starlark_pb.Value_Struct:
		strukt, err := DecodeStruct[TReference, TMetadata](model_core.Nested(encodedValue, typedValue.Struct), options)
		if err != nil {
			return nil, err
		}
		return strukt, nil
	case *model_starlark_pb.Value_Subrule:
		switch subruleKind := typedValue.Subrule.Kind.(type) {
		case *model_starlark_pb.Subrule_Reference:
			identifier, err := pg_label.NewCanonicalStarlarkIdentifier(subruleKind.Reference)
			if err != nil {
				return nil, err
			}
			return NewSubrule[TReference, TMetadata](&identifier, nil), nil
		case *model_starlark_pb.Subrule_Definition_:
			if currentIdentifier == nil {
				return nil, errors.New("encoded subrule does not have a name")
			}
			return NewSubrule(currentIdentifier, NewProtoSubruleDefinition[TReference, TMetadata]()), nil
		default:
			return nil, errors.New("encoded subrule does not have a reference or definition")
		}
	case *model_starlark_pb.Value_TargetReference:
		originalLabel, err := pg_label.NewResolvedLabel(typedValue.TargetReference.OriginalLabel)
		if err != nil {
			return nil, fmt.Errorf("invalid original label %#v: %w", typedValue.TargetReference.OriginalLabel, err)
		}
		var configuredTargetReference *ConfiguredTargetReference[TReference, TMetadata]
		if configured := typedValue.TargetReference.Configured; configured != nil {
			label, err := pg_label.NewCanonicalLabel(configured.Label)
			if err != nil {
				return nil, fmt.Errorf("invalid label %#v: %w", configured.Label, err)
			}
			configuredTargetReference = NewConfiguredTargetReference[TReference, TMetadata](
				label,
				model_core.Nested(encodedValue, configured.Providers),
			)
		}
		return NewTargetReference[TReference, TMetadata](
			originalLabel,
			configuredTargetReference,
		), nil
	case *model_starlark_pb.Value_TagClass:
		return NewTagClass(NewProtoTagClassDefinition[TReference, TMetadata](
			model_core.Nested(encodedValue, typedValue.TagClass),
		)), nil
	case *model_starlark_pb.Value_ToolchainType:
		return decodeToolchainType[TReference, TMetadata](typedValue.ToolchainType)
	case *model_starlark_pb.Value_Transition:
		t := NewTransition(
			NewProtoTransitionDefinition[TReference, TMetadata](
				model_core.Nested(encodedValue, typedValue.Transition),
			),
		)
		if currentIdentifier != nil {
			t.AssignIdentifier(*currentIdentifier)
		}
		return t, nil
	case *model_starlark_pb.Value_Tuple:
		encodedElements := typedValue.Tuple.Elements
		tuple := make(starlark.Tuple, 0, len(encodedElements))
		for _, encodedElement := range encodedElements {
			element, err := DecodeValue[TReference, TMetadata](model_core.Nested(encodedValue, encodedElement), nil, options)
			if err != nil {
				return nil, err
			}
			tuple = append(tuple, element)
		}
		return tuple, nil
	default:
		return nil, errors.New("unknown value kind")
	}
}

func DecodeAttrType[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](attr model_core.Message[*model_starlark_pb.Attr, TReference]) (AttrType[TReference, TMetadata], error) {
	switch attrTypeInfo := attr.Message.Type.(type) {
	case *model_starlark_pb.Attr_Bool:
		return NewBoolAttrType[TReference, TMetadata](), nil
	case *model_starlark_pb.Attr_Int:
		return NewIntAttrType[TReference, TMetadata](attrTypeInfo.Int.Values), nil
	case *model_starlark_pb.Attr_IntList:
		return NewIntListAttrType[TReference, TMetadata](), nil
	case *model_starlark_pb.Attr_Label:
		if attrTypeInfo.Label.ValueOptions == nil || attrTypeInfo.Label.ValueOptions.Cfg == nil {
			return nil, errors.New("missing value options")
		}
		return NewLabelAttrType[TReference, TMetadata](
			attrTypeInfo.Label.AllowNone,
			attrTypeInfo.Label.AllowSingleFile,
			attrTypeInfo.Label.Executable,
			attrTypeInfo.Label.ValueOptions.AllowFiles,
			NewProtoTransitionDefinition[TReference, TMetadata](
				model_core.Nested(attr, attrTypeInfo.Label.ValueOptions.Cfg),
			),
		), nil
	case *model_starlark_pb.Attr_LabelKeyedStringDict:
		if attrTypeInfo.LabelKeyedStringDict.DictKeyOptions == nil || attrTypeInfo.LabelKeyedStringDict.DictKeyOptions.Cfg == nil {
			return nil, errors.New("missing dict key options")
		}
		return NewLabelKeyedStringDictAttrType[TReference, TMetadata](
			attrTypeInfo.LabelKeyedStringDict.DictKeyOptions.AllowFiles,
			NewProtoTransitionDefinition[TReference, TMetadata](
				model_core.Nested(attr, attrTypeInfo.LabelKeyedStringDict.DictKeyOptions.Cfg),
			),
		), nil
	case *model_starlark_pb.Attr_LabelList:
		if attrTypeInfo.LabelList.ListValueOptions == nil || attrTypeInfo.LabelList.ListValueOptions.Cfg == nil {
			return nil, errors.New("missing list value options")
		}
		return NewLabelListAttrType[TReference, TMetadata](
			attrTypeInfo.LabelList.ListValueOptions.AllowFiles,
			NewProtoTransitionDefinition[TReference, TMetadata](
				model_core.Nested(attr, attrTypeInfo.LabelList.ListValueOptions.Cfg),
			),
		), nil
	case *model_starlark_pb.Attr_Output:
		return NewOutputAttrType[TReference, TMetadata](attrTypeInfo.Output.FilenameTemplate), nil
	case *model_starlark_pb.Attr_OutputList:
		return NewOutputListAttrType[TReference, TMetadata](), nil
	case *model_starlark_pb.Attr_String_:
		return NewStringAttrType[TReference, TMetadata](attrTypeInfo.String_.Values), nil
	case *model_starlark_pb.Attr_StringDict:
		return NewStringDictAttrType[TReference, TMetadata](), nil
	case *model_starlark_pb.Attr_StringList:
		return NewStringListAttrType[TReference, TMetadata](), nil
	case *model_starlark_pb.Attr_StringListDict:
		return NewStringListDictAttrType[TReference, TMetadata](), nil
	default:
		return nil, errors.New("unknown attribute type")
	}
}

func DecodeBuildSettingType[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](buildSetting *model_starlark_pb.BuildSetting) (BuildSettingType, error) {
	switch buildSettingTypeInfo := buildSetting.Type.(type) {
	case *model_starlark_pb.BuildSetting_Bool:
		return BoolBuildSettingType, nil
	case *model_starlark_pb.BuildSetting_Int:
		return IntBuildSettingType, nil
	case *model_starlark_pb.BuildSetting_LabelList:
		return NewLabelListBuildSettingType[TReference, TMetadata](buildSettingTypeInfo.LabelList.Repeatable), nil
	case *model_starlark_pb.BuildSetting_String_:
		return StringBuildSettingType, nil
	case *model_starlark_pb.BuildSetting_StringList:
		return NewStringListBuildSettingType(buildSettingTypeInfo.StringList.Repeatable), nil
	default:
		return nil, errors.New("unknown build setting type")
	}
}

func decodeBuildSetting[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](buildSetting *model_starlark_pb.BuildSetting) (*BuildSetting, error) {
	buildSettingType, err := DecodeBuildSettingType[TReference, TMetadata](buildSetting)
	if err != nil {
		return nil, err
	}
	return NewBuildSetting(buildSettingType, buildSetting.Flag), nil
}

func decodeDepset[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](depset model_core.Message[*model_starlark_pb.Depset, TReference]) *Depset[TReference, TMetadata] {
	children := make([]any, 0, len(depset.Message.Elements))
	for _, element := range depset.Message.Elements {
		children = append(children, model_core.Nested(depset, element))
	}
	identifier := depset.Message.Identifier
	return NewDepset(
		NewDepsetContentsFromList[TReference, TMetadata](
			children,
			depset.Message.Order,
		),
		func() []byte { return identifier },
	)
}

func DecodeProvider[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](m model_core.Message[*model_starlark_pb.Provider, TReference]) (*Provider[TReference, TMetadata], error) {
	instanceProperties := m.Message.InstanceProperties
	if instanceProperties == nil {
		return nil, errors.New("provider instance properties are missing")
	}
	providerInstanceProperties, err := decodeProviderInstanceProperties[TReference, TMetadata](model_core.Nested(m, instanceProperties))
	if err != nil {
		return nil, err
	}
	var initFunction *NamedFunction[TReference, TMetadata]
	if m.Message.InitFunction != nil {
		f := NewNamedFunction(NewProtoNamedFunctionDefinition[TReference, TMetadata](
			model_core.Nested(m, m.Message.InitFunction),
		))
		initFunction = &f
	}
	return NewProvider[TReference](
		providerInstanceProperties,
		m.Message.Fields,
		initFunction,
	), nil
}

func decodeProviderInstanceProperties[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](m model_core.Message[*model_starlark_pb.Provider_InstanceProperties, TReference]) (*ProviderInstanceProperties[TReference, TMetadata], error) {
	providerIdentifier, err := pg_label.NewCanonicalStarlarkIdentifier(m.Message.ProviderIdentifier)
	if err != nil {
		return nil, err
	}

	computedFields := make(map[string]NamedFunction[TReference, TMetadata], len(m.Message.ComputedFields))
	for _, computedField := range m.Message.ComputedFields {
		computedFields[computedField.Name] = NewNamedFunction(
			NewProtoNamedFunctionDefinition[TReference, TMetadata](
				model_core.Nested(m, computedField.Function),
			),
		)
	}

	return NewProviderInstanceProperties(&providerIdentifier, m.Message.DictLike, computedFields, m.Message.TypeName), nil
}

func decodeToolchainType[TReference any, TMetadata model_core.ReferenceMetadata](toolchainType *model_starlark_pb.ToolchainType) (*ToolchainType[TReference, TMetadata], error) {
	toolchainTypeLabel, err := pg_label.NewResolvedLabel(toolchainType.ToolchainType)
	if err != nil {
		return nil, err
	}
	return NewToolchainType[TReference, TMetadata](toolchainTypeLabel, toolchainType.Mandatory), nil
}

func DecodeStruct[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](m model_core.Message[*model_starlark_pb.Struct, TReference], options *ValueDecodingOptions[TReference]) (*Struct[TReference, TMetadata], error) {
	var providerInstanceProperties *ProviderInstanceProperties[TReference, TMetadata]
	if pip := m.Message.ProviderInstanceProperties; pip != nil {
		var err error
		providerInstanceProperties, err = decodeProviderInstanceProperties[TReference, TMetadata](model_core.Nested(m, pip))
		if err != nil {
			return nil, err
		}
	}

	var keys []string
	var values []any
	var errIter error
	for key, value := range AllStructFields(
		options.Context,
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

type dictEntriesDecodingOptions[TReference any] struct {
	valueDecodingOptions *ValueDecodingOptions[TReference]
	out                  *starlark.Dict
}

func decodeDictEntries[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](in model_core.Message[*model_starlark_pb.Dict, TReference], options *dictEntriesDecodingOptions[TReference]) error {
	thread := options.valueDecodingOptions.getThread()
	var errIter error
	for key, value := range AllDictLeafEntries(
		options.valueDecodingOptions.Context,
		options.valueDecodingOptions.Readers.Dict,
		in,
		&errIter,
	) {
		decodedKey, err := DecodeValue[TReference, TMetadata](
			key,
			nil,
			options.valueDecodingOptions,
		)
		if err != nil {
			return err
		}
		decodedValue, err := DecodeValue[TReference, TMetadata](
			value,
			nil,
			options.valueDecodingOptions,
		)
		if err != nil {
			return err
		}
		if err := options.out.SetKey(thread, decodedKey, decodedValue); err != nil {
			return err
		}
	}
	return errIter
}

type listElementsDecodingOptions[TReference any] struct {
	valueDecodingOptions *ValueDecodingOptions[TReference]
	out                  *starlark.List
}

func decodeList_Elements[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](in model_core.Message[*model_starlark_pb.List, TReference], options *listElementsDecodingOptions[TReference]) error {
	var errIter error
	for element := range AllListLeafElements(
		options.valueDecodingOptions.Context,
		options.valueDecodingOptions.Readers.List,
		model_core.Nested(in, in.Message.Elements),
		&errIter,
	) {
		value, err := DecodeValue[TReference, TMetadata](
			element,
			nil,
			options.valueDecodingOptions,
		)
		if err != nil {
			return fmt.Errorf("index %d: %w", options.out.Len(), err)
		}
		if err := options.out.Append(value); err != nil {
			return err
		}
	}
	if errIter != nil {
		return fmt.Errorf("failed to iterate list: %w", errIter)
	}
	return nil
}
