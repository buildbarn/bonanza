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

// ProviderInstanceProperties contains the set of properties that affect
// the behavior of a provider instance after it has been constructed.
// Examples of such properties include whether it should behave like a
// dict (OutputGroupInfo), what the output of type(x) should be, and
// whether it should contain additional fields whose values are computed
// dynamically.
type ProviderInstanceProperties[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	LateNamedValue
	dictLike       bool
	computedFields map[string]NamedFunction[TReference, TMetadata]
	typeName       string
}

// Encode the instance properties of a provider, so that it can be
// written to storage. This is invoked when either a provider or one of
// its instances needs to be written to storage.
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

// NewProviderInstanceProperties creates an ProviderInstanceProperties
// object. This is either called via the Starlark provider() function,
// or when a provider or one of its instances is reloaded from storage.
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

// Provider is a factory for structs having a distinct type. Providers
// are generally returned by rule implementations to share data with
// their dependencies.
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

// NewProvider creates a new provider. This is typically called via the
// Starlark provider() function, or when a provider is reloaded from
// storage.
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

// Type returns the name of the type of provider objects. This is
// invoked via the Starlark type() function.
func (Provider[TReference, TMetadata]) Type() string {
	return "provider"
}

// Freeze a provider by making all of its properties immutable. This is
// a no-op, as providers are always frozen.
func (Provider[TReference, TMetadata]) Freeze() {}

// Truth returns whether a provider is "truthy" or "falsy" when it is
// implicitly converted to a Boolean value. Providers are always
// "truthy".
func (Provider[TReference, TMetadata]) Truth() starlark.Bool {
	return starlark.True
}

// Hash a provider object, so that it can be placed in a set or used as
// the key of a dict.
func (p *Provider[TReference, TMetadata]) Hash(thread *starlark.Thread) (uint32, error) {
	if p.Identifier == nil {
		return 0, errors.New("provider without a name cannot be hashed")
	}
	return starlark.String(p.Identifier.String()).Hash(thread)
}

// Cmp compares two provider objects, returning true if both refer to
// the same Starlark identifier to which the provider is assigned.
func (p *Provider[TReference, TMetadata]) Cmp(other starlark.Value, depth int) (int, error) {
	pOther := other.(*Provider[TReference, TMetadata])
	if p.Identifier == nil || pOther.Identifier == nil {
		return 0, errors.New("provider without a name cannot be compared")
	}
	return strings.Compare(p.Identifier.String(), pOther.Identifier.String()), nil
}

// Name returns the name of the provider callable.
func (Provider[TReference, TMetadata]) Name() string {
	return "provider"
}

// Instantiate a provider, returning an instance in the form of a
// struct. If the provider has a custom init function, the arguments are
// forwarded to the init function. If not, fields in the struct will
// match the keyword arguments.
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

// CallInternal is called when a provider is invoked from within
// Starlark code. It returns a new provider instance.
func (p *Provider[TReference, TMetadata]) CallInternal(thread *starlark.Thread, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	return p.Instantiate(thread, args, kwargs)
}

// EncodeValue encodes a provider object to a Starlark value message,
// which allows it to be written to storage. This is necessary when a
// provider is declared in a file in order to create provider instances
// from within another file.
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
