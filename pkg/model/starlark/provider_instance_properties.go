package starlark

import (
	"errors"
	"maps"
	"slices"

	pg_label "bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/storage/object"
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
	computedFields map[string]eval.Function
	typeName       string
}

// Encode the instance properties of a provider, so that it can be
// written to storage. This is invoked when either a provider or one of
// its instances needs to be written to storage.
func (pip *ProviderInstanceProperties[TReference, TMetadata]) Encode(thread *eval.Thread, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Provider_InstanceProperties, TMetadata], error) {
	if pip.Identifier == nil {
		return model_core.PatchedMessage[*model_starlark_pb.Provider_InstanceProperties, TMetadata]{}, errors.New("provider does not have a name")
	}

	patcher := model_core.NewReferenceMessagePatcher[TMetadata]()
	computedFields := make([]*model_starlark_pb.Provider_InstanceProperties_ComputedField, 0, len(pip.computedFields))
	for _, name := range slices.Sorted(maps.Keys(pip.computedFields)) {
		function, err := EncodeFunction(thread, pip.computedFields[name], path, options)
		if err != nil {
			patcher.Discard()
			return model_core.PatchedMessage[*model_starlark_pb.Provider_InstanceProperties, TMetadata]{}, err
		}
		computedFields = append(computedFields, &model_starlark_pb.Provider_InstanceProperties_ComputedField{
			Name:     name,
			Function: function.Message,
		})
		patcher.Merge(function.Patcher)
	}

	return model_core.NewPatchedMessage(
		&model_starlark_pb.Provider_InstanceProperties{
			ProviderIdentifier: pip.Identifier.String(),
			DictLike:           pip.dictLike,
			ComputedFields:     computedFields,
			TypeName:           pip.typeName,
		},
		patcher,
	), nil
}

// NewProviderInstanceProperties creates an ProviderInstanceProperties
// object. This is either called via the Starlark provider() function,
// or when a provider or one of its instances is reloaded from storage.
func NewProviderInstanceProperties[TReference any, TMetadata model_core.ReferenceMetadata](identifier *pg_label.CanonicalStarlarkIdentifier, dictLike bool, computedFields map[string]eval.Function, typeName string) *ProviderInstanceProperties[TReference, TMetadata] {
	return &ProviderInstanceProperties[TReference, TMetadata]{
		LateNamedValue: LateNamedValue{
			Identifier: identifier,
		},
		dictLike:       dictLike,
		computedFields: computedFields,
		typeName:       typeName,
	}
}

func decodeProviderInstanceProperties[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](m model_core.Message[*model_starlark_pb.Provider_InstanceProperties, TReference]) (*ProviderInstanceProperties[TReference, TMetadata], error) {
	providerIdentifier, err := pg_label.NewCanonicalStarlarkIdentifier(m.Message.ProviderIdentifier)
	if err != nil {
		return nil, err
	}

	computedFields := make(map[string]eval.Function, len(m.Message.ComputedFields))
	for _, computedField := range m.Message.ComputedFields {
		computedFields[computedField.Name] = DecodeFunction[TReference, TMetadata](model_core.Nested(m, computedField.Function))
	}

	return NewProviderInstanceProperties[TReference, TMetadata](&providerIdentifier, m.Message.DictLike, computedFields, m.Message.TypeName), nil
}
