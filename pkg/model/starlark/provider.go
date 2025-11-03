package starlark

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	pg_label "bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/storage/object"

	"go.starlark.net/starlark"
)

type ProviderInstanceProperties[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	LateNamedValue
	dictLike       bool
	computedFields map[string]NamedFunction[TReference, TMetadata]
	typeName       string
}

func (pip *ProviderInstanceProperties[TReference, TMetadata]) Encode(path map[starlark.Value]struct{}, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Provider_InstanceProperties, TMetadata], bool, error) {
	if pip.Identifier == nil {
		return model_core.PatchedMessage[*model_starlark_pb.Provider_InstanceProperties, TMetadata]{}, false, errors.New("provider does not have a name")
	}

	patcher := model_core.NewReferenceMessagePatcher[TMetadata]()
	computedFields := make([]*model_starlark_pb.Provider_InstanceProperties_ComputedField, 0, len(pip.computedFields))
	needsCode := false
	for _, name := range slices.Sorted(maps.Keys(pip.computedFields)) {
		function, functionNeedsCode, err := pip.computedFields[name].Encode(path, options)
		if err != nil {
			return model_core.PatchedMessage[*model_starlark_pb.Provider_InstanceProperties, TMetadata]{}, false, err
		}
		computedFields = append(computedFields, &model_starlark_pb.Provider_InstanceProperties_ComputedField{
			Name:     name,
			Function: function.Message,
		})
		patcher.Merge(function.Patcher)
		needsCode = needsCode || functionNeedsCode
	}

	return model_core.NewPatchedMessage(
		&model_starlark_pb.Provider_InstanceProperties{
			ProviderIdentifier: pip.Identifier.String(),
			DictLike:           pip.dictLike,
			ComputedFields:     computedFields,
			TypeName:           pip.typeName,
		},
		patcher,
	), needsCode, nil
}

func NewProviderInstanceProperties[TReference any, TMetadata model_core.ReferenceMetadata](identifier *pg_label.CanonicalStarlarkIdentifier, dictLike bool, computedFields map[string]NamedFunction[TReference, TMetadata], typeName string) *ProviderInstanceProperties[TReference, TMetadata] {
	return &ProviderInstanceProperties[TReference, TMetadata]{
		LateNamedValue: LateNamedValue{
			Identifier: identifier,
		},
		dictLike:       dictLike,
		computedFields: computedFields,
		typeName:       typeName,
	}
}

type Provider[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	*ProviderInstanceProperties[TReference, TMetadata]
	fields       []string
	initFunction *NamedFunction[TReference, TMetadata]
}

var (
	_ EncodableValue[object.LocalReference, model_core.ReferenceMetadata] = (*Provider[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ NamedGlobal                                                         = (*Provider[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ starlark.Callable                                                   = (*Provider[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ starlark.TotallyOrdered                                             = (*Provider[object.LocalReference, model_core.ReferenceMetadata])(nil)
)

func NewProvider[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](instanceProperties *ProviderInstanceProperties[TReference, TMetadata], fields []string, initFunction *NamedFunction[TReference, TMetadata]) *Provider[TReference, TMetadata] {
	return &Provider[TReference, TMetadata]{
		ProviderInstanceProperties: instanceProperties,
		fields:                     fields,
		initFunction:               initFunction,
	}
}

func (Provider[TReference, TMetadata]) String() string {
	return "<provider>"
}

func (Provider[TReference, TMetadata]) Type() string {
	return "provider"
}

func (Provider[TReference, TMetadata]) Freeze() {}

func (Provider[TReference, TMetadata]) Truth() starlark.Bool {
	return starlark.True
}

func (p *Provider[TReference, TMetadata]) Hash(thread *starlark.Thread) (uint32, error) {
	if p.Identifier == nil {
		return 0, errors.New("provider without a name cannot be hashed")
	}
	return starlark.String(p.Identifier.String()).Hash(thread)
}

func (p *Provider[TReference, TMetadata]) Cmp(other starlark.Value, depth int) (int, error) {
	pOther := other.(*Provider[TReference, TMetadata])
	if p.Identifier == nil || pOther.Identifier == nil {
		return 0, errors.New("provider without a name cannot be compared")
	}
	return strings.Compare(p.Identifier.String(), pOther.Identifier.String()), nil
}

func (Provider[TReference, TMetadata]) Name() string {
	return "provider"
}

func (p *Provider[TReference, TMetadata]) Instantiate(thread *starlark.Thread, args starlark.Tuple, kwargs []starlark.Tuple) (*Struct[TReference, TMetadata], error) {
	var fields map[string]any
	if p.initFunction == nil {
		// Trivially constructible provider.
		if len(args) > 0 {
			return nil, fmt.Errorf("%s: got %d positional arguments, want 0", p.Name(), len(args))
		}
		fields = make(map[string]any, len(kwargs))
		for _, kwarg := range kwargs {
			field := string(kwarg[0].(starlark.String))
			if len(p.fields) > 0 {
				if _, ok := sort.Find(
					len(p.fields),
					func(i int) int { return strings.Compare(field, p.fields[i]) },
				); !ok {
					return nil, fmt.Errorf("field %#v is not in the allowed set of fields for this provider", field)
				}
			}
			fields[field] = kwarg[1]
		}
	} else {
		// Provider has a custom init function.
		result, err := starlark.Call(thread, p.initFunction, args, kwargs)
		if err != nil {
			return nil, err
		}
		mapping, ok := result.(starlark.IterableMapping)
		if !ok {
			return nil, fmt.Errorf("init function returned %s, want dict", result.Type())
		}
		fields = map[string]any{}
		for key, value := range starlark.Entries(thread, mapping) {
			keyStr, ok := starlark.AsString(key)
			if !ok {
				return nil, fmt.Errorf("init function returned dict containing key of type %s, want string", key.Type())
			}
			fields[keyStr] = value
		}
	}

	return NewStructFromDict[TReference, TMetadata](p.ProviderInstanceProperties, fields), nil
}

func (p *Provider[TReference, TMetadata]) CallInternal(thread *starlark.Thread, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	return p.Instantiate(thread, args, kwargs)
}

func (p *Provider[TReference, TMetadata]) EncodeValue(path map[starlark.Value]struct{}, currentIdentifier *pg_label.CanonicalStarlarkIdentifier, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], bool, error) {
	instanceProperties, needsCode, err := p.ProviderInstanceProperties.Encode(path, options)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, false, err
	}

	provider := &model_starlark_pb.Provider{
		InstanceProperties: instanceProperties.Message,
	}
	patcher := instanceProperties.Patcher

	if p.initFunction != nil {
		initFunction, initFunctionNeedsCode, err := p.initFunction.Encode(path, options)
		if err != nil {
			return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, false, err
		}
		provider.InitFunction = initFunction.Message
		patcher.Merge(initFunction.Patcher)
		needsCode = needsCode || initFunctionNeedsCode
	}

	return model_core.NewPatchedMessage(
		&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_Provider{
				Provider: provider,
			},
		},
		patcher,
	), needsCode, nil
}
