package starlark

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	pg_label "bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/syntax"
	"bonanza.build/pkg/storage/object"
)

// Provider is a factory for structs having a distinct type. Providers
// are generally returned by rule implementations to share data with
// their dependencies.
type Provider[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	*ProviderInstanceProperties[TReference, TMetadata]
	fields       []string
	initFunction eval.Function
}

var (
	_ EncodableValue[object.LocalReference, model_core.ReferenceMetadata] = (*Provider[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ NamedGlobal                                                         = (*Provider[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ eval.Callable                                                       = (*Provider[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ eval.Comparable                                                     = (*Provider[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ eval.Hashable                                                       = (*Provider[object.LocalReference, model_core.ReferenceMetadata])(nil)
)

// NewProvider creates a new provider. This is typically called via the
// Starlark provider() function, or when a provider is reloaded from
// storage.
func NewProvider[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](instanceProperties *ProviderInstanceProperties[TReference, TMetadata], fields []string, initFunction eval.Function) *Provider[TReference, TMetadata] {
	return &Provider[TReference, TMetadata]{
		ProviderInstanceProperties: instanceProperties,
		fields:                     fields,
		initFunction:               initFunction,
	}
}

// Repr writes the representation of the value.
func (p *Provider[TReference, TMetadata]) Repr(_ *eval.Thread, printer *eval.Printer) error {
	if p.Identifier == nil {
		printer.WriteString("<provider>")
	} else {
		printer.WriteString("<provider " + p.Identifier.String() + ">")
	}
	return nil
}

// Type returns the name of the type of provider objects. This is
// invoked via the Starlark type() function.
func (Provider[TReference, TMetadata]) Type() string {
	return "provider"
}

// Truth returns whether a provider is "truthy" or "falsy" when it is
// implicitly converted to a Boolean value. Providers are always
// "truthy".
func (Provider[TReference, TMetadata]) Truth(*eval.Thread) (bool, error) {
	return true, nil
}

// Hash a provider object, so that it can be placed in a set or used as
// the key of a dict.
func (p *Provider[TReference, TMetadata]) Hash(thread *eval.Thread) (uint32, error) {
	if p.Identifier == nil {
		return 0, errors.New("provider without a name cannot be hashed")
	}
	return eval.Hash(thread, eval.String(p.Identifier.String()))
}

// CompareSameType compares two provider objects by the Starlark
// identifier to which the provider is assigned.
func (p *Provider[TReference, TMetadata]) CompareSameType(thread *eval.Thread, op syntax.Token, other eval.Value, depth int) (bool, error) {
	pOther := other.(*Provider[TReference, TMetadata])
	if p.Identifier == nil || pOther.Identifier == nil {
		return false, errors.New("provider without a name cannot be compared")
	}
	return eval.Compare(thread, op, eval.String(p.Identifier.String()), eval.String(pOther.Identifier.String()))
}

// Name returns the name of the provider callable.
func (Provider[TReference, TMetadata]) Name() string {
	return "provider"
}

// Instantiate a provider, returning an instance in the form of a
// struct. If the provider has a custom init function, the arguments are
// forwarded to the init function. If not, fields in the struct will
// match the keyword arguments.
func (p *Provider[TReference, TMetadata]) Instantiate(thread *eval.Thread, args eval.Args) (*Struct[TReference, TMetadata], error) {
	var fields map[string]any
	if p.initFunction == nil {
		// Trivially constructible provider.
		if len(args.Positional) > 0 {
			return nil, fmt.Errorf("%s: got %d positional arguments, want 0", p.Name(), len(args.Positional))
		}
		fields = make(map[string]any, len(args.Named))
		for _, kwarg := range args.Named {
			field := kwarg.Name
			if len(p.fields) > 0 {
				if _, ok := sort.Find(
					len(p.fields),
					func(i int) int { return strings.Compare(field, p.fields[i]) },
				); !ok {
					return nil, fmt.Errorf("field %#v is not in the allowed set of fields for this provider", field)
				}
			}
			fields[field] = kwarg.Value
		}
	} else {
		// Provider has a custom init function.
		result, err := eval.Call(thread, p.initFunction, args)
		if err != nil {
			return nil, err
		}
		dict, ok := result.(eval.Dict)
		if !ok {
			return nil, fmt.Errorf("init function returned %s, want dict", result.Type())
		}
		fields = map[string]any{}
		var errIter error
		for key, value := range dict.Items(thread, &errIter) {
			keyStr, ok := eval.AsString(key)
			if !ok {
				return nil, fmt.Errorf("init function returned dict containing key of type %s, want string", key.Type())
			}
			fields[keyStr] = value
		}
		if errIter != nil {
			return nil, errIter
		}
	}

	return NewStructFromDict[TReference, TMetadata](p.ProviderInstanceProperties, fields), nil
}

// CallInternal is called when a provider is invoked from within
// Starlark code. It returns a new provider instance.
func (p *Provider[TReference, TMetadata]) CallInternal(thread *eval.Thread, args eval.Args) (eval.Value, error) {
	return p.Instantiate(thread, args)
}

// EncodeValue encodes a provider object to a Starlark value message,
// which allows it to be written to storage. This is necessary when a
// provider is declared in a file in order to create provider instances
// from within another file.
func (p *Provider[TReference, TMetadata]) EncodeValue(thread *eval.Thread, path []eval.Value, currentIdentifier *pg_label.CanonicalStarlarkIdentifier, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], error) {
	instanceProperties, err := p.ProviderInstanceProperties.Encode(thread, path, options)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, err
	}

	provider := &model_starlark_pb.Provider{
		InstanceProperties: instanceProperties.Message,
	}
	patcher := instanceProperties.Patcher

	if p.initFunction != nil {
		initFunction, err := EncodeFunction(thread, p.initFunction, path, options)
		if err != nil {
			patcher.Discard()
			return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, err
		}
		provider.InitFunction = initFunction.Message
		patcher.Merge(initFunction.Patcher)
	}

	return model_core.NewPatchedMessage(
		&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_Provider{
				Provider: provider,
			},
		},
		patcher,
	), nil
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
	var initFunction eval.Function
	if m.Message.InitFunction != nil {
		initFunction = DecodeFunction[TReference, TMetadata](model_core.Nested(m, m.Message.InitFunction))
	}
	return NewProvider[TReference](
		providerInstanceProperties,
		m.Message.Fields,
		initFunction,
	), nil
}
