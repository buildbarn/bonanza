package analysis

import (
	"fmt"
	"slices"
	"strconv"

	model_core "bonanza.build/pkg/model/core"
	model_encoding "bonanza.build/pkg/model/encoding"
	model_format "bonanza.build/pkg/model/format"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/unpack"
	"bonanza.build/pkg/storage/object"
)

// encodeAspectParameters computes the parameters of an aspect, and
// stores them in a newly created object that can be referenced by
// AppliedAspect.parameters. getValue is called for each public attr of
// the aspect to obtain its value. If it returns nil, the attr's default
// value is used. Values that are equal to the default values of the
// aspect's attrs are omitted. If no values remain, nil is returned.
func encodeAspectParameters[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	thread *eval.Thread,
	referenceFormat object.ReferenceFormat,
	analysisEncoder model_encoding.DeterministicBinaryEncoder,
	valueEncodingOptions *model_starlark.ValueEncodingOptions[TReference, TMetadata],
	aspect model_core.Message[*model_analysis_pb.LabelAttrAspects_Value_Aspect, TReference],
	getValue func(publicAttr *model_starlark_pb.NamedAttr) (eval.Value, error),
) (*model_core.Decodable[model_core.CreatedObject[TMetadata]], error) {
	parameters := map[string]any{}
	for _, publicAttr := range aspect.Message.PublicAttrs {
		value, err := getValue(publicAttr)
		if err != nil {
			return nil, err
		}
		if value == nil {
			continue
		}

		// Validate that the value is permitted by the aspect's
		// attr.
		switch attrType := publicAttr.Attr.GetType().(type) {
		case *model_starlark_pb.Attr_Bool:
			var v bool
			if err := unpack.Bool.UnpackInto(thread, value, &v); err != nil {
				return nil, fmt.Errorf("attr %#v of aspect %#v: %w", publicAttr.Name, aspect.Message.Identifier, err)
			}
		case *model_starlark_pb.Attr_Int:
			var v int32
			if err := unpack.Int[int32]().UnpackInto(thread, value, &v); err != nil {
				return nil, fmt.Errorf("attr %#v of aspect %#v: %w", publicAttr.Name, aspect.Message.Identifier, err)
			}
			if values := attrType.Int.Values; len(values) > 0 && !slices.Contains(values, v) {
				return nil, fmt.Errorf("attr %#v of aspect %#v does not permit value %d", publicAttr.Name, aspect.Message.Identifier, v)
			}
		case *model_starlark_pb.Attr_String_:
			var v string
			if err := unpack.String.UnpackInto(thread, value, &v); err != nil {
				return nil, fmt.Errorf("attr %#v of aspect %#v: %w", publicAttr.Name, aspect.Message.Identifier, err)
			}
			if values := attrType.String_.Values; len(values) > 0 && !slices.Contains(values, v) {
				return nil, fmt.Errorf("attr %#v of aspect %#v does not permit value %#v", publicAttr.Name, aspect.Message.Identifier, v)
			}
		default:
			return nil, fmt.Errorf("attr %#v of aspect %#v is not of type bool, int or string", publicAttr.Name, aspect.Message.Identifier)
		}

		// Omit values that are equal to the default value.
		if defaultValue := publicAttr.Attr.GetDefault(); defaultValue != nil {
			decodedDefaultValue, err := model_starlark.DecodeValue[TReference, TMetadata](thread, model_core.Nested(aspect, defaultValue), nil)
			if err != nil {
				return nil, fmt.Errorf("default value of attr %#v of aspect %#v: %w", publicAttr.Name, aspect.Message.Identifier, err)
			}
			equal, err := eval.Equal(thread, value, decodedDefaultValue)
			if err != nil {
				return nil, err
			}
			if equal {
				continue
			}
		}
		parameters[publicAttr.Name] = value
	}
	if len(parameters) == 0 {
		return nil, nil
	}

	// The fields are encoded like any other Starlark value, while
	// the object containing them is created by analysis.
	fields, err := model_starlark.NewStructFromDict[TReference, TMetadata](nil, parameters).
		EncodeStructFields(thread, nil, valueEncodingOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to encode parameters of aspect %#v: %w", aspect.Message.Identifier, err)
	}
	createdObject, err := model_core.MarshalAndEncodeDeterministic(
		fields.ToMarshaler(model_format.NewProto[model_starlark_pb.Struct_Fields]()),
		referenceFormat,
		analysisEncoder,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create parameters of aspect %#v: %w", aspect.Message.Identifier, err)
	}
	return &createdObject, nil
}

// parseAspectParameterValue converts the value of an aspect parameter
// that is provided on the command line through --aspects_parameters to
// a Starlark value, based on the type of the aspect's public attr.
func parseAspectParameterValue(publicAttr *model_starlark_pb.NamedAttr, value string) (eval.Value, error) {
	switch publicAttr.Attr.GetType().(type) {
	case *model_starlark_pb.Attr_Bool:
		switch value {
		case "true", "True", "1":
			return eval.True, nil
		case "false", "False", "0":
			return eval.False, nil
		default:
			return nil, fmt.Errorf("value %#v of parameter %#v is not a valid boolean value", value, publicAttr.Name)
		}
	case *model_starlark_pb.Attr_Int:
		v, err := strconv.ParseInt(value, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("value %#v of parameter %#v is not a valid integer value", value, publicAttr.Name)
		}
		return eval.MakeInt(int(v)), nil
	case *model_starlark_pb.Attr_String_:
		return eval.String(value), nil
	default:
		return nil, fmt.Errorf("parameter %#v is not of type bool, int or string", publicAttr.Name)
	}
}
