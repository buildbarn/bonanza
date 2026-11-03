package starlark

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"iter"
	"maps"
	"math"
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

// EncodeCompiledProgram converts a Starlark program to a Protobuf
// message, so that it can be written to storage.
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

// DecodeGlobals decodes all of the globals declared in a .bzl file that
// was previously compiled and written to storage.
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

// DecodeAttrType extracts the type of a rule attribute from a rule
// attribute's Protobuf message.
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
	case *model_starlark_pb.Attr_StringKeyedLabelDict:
		if attrTypeInfo.StringKeyedLabelDict.DictValueOptions == nil || attrTypeInfo.StringKeyedLabelDict.DictValueOptions.Cfg == nil {
			return nil, errors.New("missing dict value options")
		}
		return NewStringKeyedLabelDictAttrType[TReference, TMetadata](
			attrTypeInfo.StringKeyedLabelDict.DictValueOptions.AllowFiles,
			NewProtoTransitionDefinition[TReference, TMetadata](
				model_core.Nested(attr, attrTypeInfo.StringKeyedLabelDict.DictValueOptions.Cfg),
			),
		), nil
	case *model_starlark_pb.Attr_StringList:
		return NewStringListAttrType[TReference, TMetadata](), nil
	case *model_starlark_pb.Attr_StringListDict:
		return NewStringListDictAttrType[TReference, TMetadata](), nil
	default:
		return nil, errors.New("unknown attribute type")
	}
}

// DecodeBuildSettingType extracts the type of a build setting from a
// build setting's Protobuf message.
func DecodeBuildSettingType[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](buildSetting *model_starlark_pb.BuildSetting) (BuildSettingType[TReference, TMetadata], error) {
	switch buildSettingTypeInfo := buildSetting.Type.(type) {
	case *model_starlark_pb.BuildSetting_Bool:
		return NewBoolBuildSettingType[TReference, TMetadata](), nil
	case *model_starlark_pb.BuildSetting_Int:
		return NewIntBuildSettingType[TReference, TMetadata](), nil
	case *model_starlark_pb.BuildSetting_LabelList:
		return NewLabelListBuildSettingType[TReference, TMetadata](buildSettingTypeInfo.LabelList.Repeatable), nil
	case *model_starlark_pb.BuildSetting_String_:
		return NewStringBuildSettingType[TReference, TMetadata](), nil
	case *model_starlark_pb.BuildSetting_StringList:
		return NewStringListBuildSettingType[TReference, TMetadata](buildSettingTypeInfo.StringList.Repeatable), nil
	default:
		return nil, errors.New("unknown build setting type")
	}
}

func decodeBuildSetting[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](buildSetting *model_starlark_pb.BuildSetting) (*BuildSetting[TReference, TMetadata], error) {
	buildSettingType, err := DecodeBuildSettingType[TReference, TMetadata](buildSetting)
	if err != nil {
		return nil, err
	}
	return NewBuildSetting(buildSettingType, buildSetting.Flag), nil
}

// DecodeProvider converts a Protobuf message of a provider to a native
// Starlark provider object.
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
