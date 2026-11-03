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

	"github.com/buildbarn/bb-storage/pkg/util"
)

// ConfiguredTargetReference contains the properties of a Starlark Target
// object of a configured target.
//
// As an extension, Bonanza supports creating target references that are
// not configured. This means that properties like the resolved label
// and set of provider values are not always available. This is why they
// are stored in a separate struct, which is only set if targets are
// configured.
type ConfiguredTargetReference[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	label            pg_label.CanonicalLabel
	encodedProviders model_core.Message[[]*model_starlark_pb.Struct, TReference]
	decodedProviders []*Struct[TReference, TMetadata]
}

// NewConfiguredTargetReference creates a ConfiguredTargetReference
// object, which contains the properties of a Starlark Target object of
// a configured target.
func NewConfiguredTargetReference[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](label pg_label.CanonicalLabel, providers model_core.Message[[]*model_starlark_pb.Struct, TReference]) *ConfiguredTargetReference[TReference, TMetadata] {
	return &ConfiguredTargetReference[TReference, TMetadata]{
		label:            label,
		encodedProviders: providers,
		decodedProviders: make([]*Struct[TReference, TMetadata], len(providers.Message)),
	}
}

func (ctr *ConfiguredTargetReference[TReference, TMetadata]) getProviderValue(thread *eval.Thread, providerIdentifier pg_label.CanonicalStarlarkIdentifier) (*Struct[TReference, TMetadata], error) {
	providerIdentifierStr := providerIdentifier.String()
	index, ok := sort.Find(
		len(ctr.encodedProviders.Message),
		func(i int) int {
			return strings.Compare(providerIdentifierStr, ctr.encodedProviders.Message[i].ProviderInstanceProperties.GetProviderIdentifier())
		},
	)
	if !ok {
		return nil, fmt.Errorf("target %#v did not yield provider %#v", ctr.label.String(), providerIdentifierStr)
	}

	strukt := ctr.decodedProviders[index]
	if strukt == nil {
		var err error
		strukt, err = DecodeStruct[TReference, TMetadata](
			thread,
			model_core.Nested(ctr.encodedProviders, ctr.encodedProviders.Message[index]),
		)
		if err != nil {
			return nil, err
		}
		ctr.decodedProviders[index] = strukt
	}
	return strukt, nil
}

// TargetReference is a Starlark value corresponding to the Target type.
// These are the values that rule implementations may access through
// ctx.attr or ctx.split_attr.
type TargetReference[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	originalLabel pg_label.ResolvedLabel
	configured    *ConfiguredTargetReference[TReference, TMetadata]
}

// NewTargetReference creates a new Starlark Target value corresponding
// to a given label, exposing struct instances corresponding to a set of
// providers. This function expects these struct instances to be
// alphabetically sorted by provider identifier.
func NewTargetReference[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](originalLabel pg_label.ResolvedLabel, configured *ConfiguredTargetReference[TReference, TMetadata]) eval.Value {
	return &TargetReference[TReference, TMetadata]{
		originalLabel: originalLabel,
		configured:    configured,
	}
}

var (
	_ EncodableValue[object.LocalReference, model_core.ReferenceMetadata] = (*TargetReference[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ eval.Comparable                                                     = (*TargetReference[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ eval.HasAttrs                                                       = (*TargetReference[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ eval.Mapping                                                        = (*TargetReference[object.LocalReference, model_core.ReferenceMetadata])(nil)
)

// Repr writes the representation of a Starlark Target value.
func (tr *TargetReference[TReference, TMetadata]) Repr(_ *eval.Thread, p *eval.Printer) error {
	if tr.configured != nil {
		p.WriteString(fmt.Sprintf("<target %s>", tr.configured.label.String()))
	} else {
		p.WriteString(fmt.Sprintf("<unconfigured target %s>", tr.originalLabel.String()))
	}
	return nil
}

// Type returns the name of the type of a Starlark Target value.
func (TargetReference[TReference, TMetadata]) Type() string {
	return "Target"
}

// Truth returns whether the Starlark Target value evaluates to true or
// false when implicitly converted to a Boolean value. Starlark Target
// values always convert to true.
func (TargetReference[TReference, TMetadata]) Truth(*eval.Thread) (bool, error) {
	return true, nil
}

// Hash a Starlark Target value, so that it can be used as the key of a
// dictionary. As we assume that the number of targets having the same
// label, but a different configuration is fairly low, we simply hash
// the target's label.
func (tr *TargetReference[TReference, TMetadata]) Hash(thread *eval.Thread) (uint32, error) {
	return eval.Hash(thread, eval.String(tr.originalLabel.String()))
}

func (tr *TargetReference[TReference, TMetadata]) equal(thread *eval.Thread, other *TargetReference[TReference, TMetadata]) (bool, error) {
	if tr != other {
		if tr.originalLabel != other.originalLabel {
			return false, nil
		}
		if (tr.configured != nil) != (other.configured != nil) {
			return false, nil
		}
		if tr.configured != nil {
			if tr.configured.label != other.configured.label {
				return false, nil
			}
			if !model_core.MessagesEqualList(tr.configured.encodedProviders, other.configured.encodedProviders) {
				return false, nil
			}
		}
	}
	return true, nil
}

// CompareSameType can be used to compare Starlark Target values for
// equality.
func (tr *TargetReference[TReference, TMetadata]) CompareSameType(thread *eval.Thread, op syntax.Token, other eval.Value, depth int) (bool, error) {
	switch op {
	case syntax.EQL:
		return tr.equal(thread, other.(*TargetReference[TReference, TMetadata]))
	case syntax.NEQ:
		equals, err := tr.equal(thread, other.(*TargetReference[TReference, TMetadata]))
		return !equals, err
	default:
		return false, errors.New("target references cannot be compared for inequality")
	}
}

var defaultInfoProviderIdentifier = util.Must(pg_label.NewCanonicalStarlarkIdentifier("@@builtins_core+//:exports.bzl%DefaultInfo"))

// Attr returns the value of an attribute of a Starlark Target value.
//
// The only attribute provided by the Target value itself is "label",
// which returns the label of the configured target. The other
// attributes are merely forwarded to the DefaultInfo provider. This
// allows these commonly used fields to be accessed with less
// indirection.
func (tr *TargetReference[TReference, TMetadata]) Attr(thread *eval.Thread, name string) (eval.Value, error) {
	switch name {
	case "original_label":
		// Bonanza specific extension.
		return NewLabel[TReference, TMetadata](tr.originalLabel), nil
	}

	if ctr := tr.configured; ctr != nil {
		switch name {
		case "data_runfiles", "default_runfiles", "files", "files_to_run":
			// Fields provided by DefaultInfo can be accessed directly.
			defaultInfoProviderValue, err := ctr.getProviderValue(thread, defaultInfoProviderIdentifier)
			if err != nil {
				return nil, err
			}
			return defaultInfoProviderValue.Attr(thread, name)
		case "label":
			return NewLabel[TReference, TMetadata](ctr.label.AsResolved()), nil
		}
	}

	return nil, eval.NoSuchAttrError{Value: tr, Name: name}
}

var unconfiguredTargetReferenceAttrNames = []string{
	"original_label",
}

var configuredTargetReferenceAttrNames = []string{
	"data_runfiles",
	"default_runfiles",
	"files",
	"files_to_run",
	"label",
	"original_label",
}

// AttrNames returns the attribute names of a Starlark Target value.
func (tr *TargetReference[TReference, TMetadata]) AttrNames(*eval.Thread) ([]string, error) {
	if tr.configured != nil {
		return configuredTargetReferenceAttrNames, nil
	}
	return unconfiguredTargetReferenceAttrNames, nil
}

// Get the value of a given provider from the Starlark Target value.
// This is called when a rule invokes ctx.attr.myattr[MyProviderInfo].
func (tr *TargetReference[TReference, TMetadata]) Get(thread *eval.Thread, v eval.Value) (eval.Value, bool, error) {
	provider, ok := v.(*Provider[TReference, TMetadata])
	if !ok {
		return nil, false, errors.New("keys have to be of type provider")
	}
	providerIdentifier := provider.Identifier
	if providerIdentifier == nil {
		return nil, false, errors.New("provider does not have a name")
	}

	ctr := tr.configured
	if ctr == nil {
		return nil, false, errors.New("target is not configured")
	}
	providerValue, err := ctr.getProviderValue(thread, *providerIdentifier)
	if err != nil {
		return nil, false, err
	}
	return providerValue, true, nil
}

// EncodeValue encodes a Starlark Target value to a Protobuf message, so
// that it can be written to storage and restored at a later point in
// time.
func (tr *TargetReference[TReference, TMetadata]) EncodeValue(thread *eval.Thread, path []eval.Value, currentIdentifier *pg_label.CanonicalStarlarkIdentifier, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], error) {
	return model_core.MustBuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_starlark_pb.Value {
		m := &model_starlark_pb.TargetReference{
			OriginalLabel: tr.originalLabel.String(),
		}
		if ctr := tr.configured; ctr != nil {
			m.Configured = &model_starlark_pb.TargetReference_Configured{
				Label: ctr.label.String(),
				Providers: model_core.PatchList(
					options.ObjectCapturer,
					ctr.encodedProviders,
				).Merge(patcher),
			}
		}
		return &model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_TargetReference{
				TargetReference: m,
			},
		}
	}), nil
}

func decodeTargetReference[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](encodedValue model_core.Message[*model_starlark_pb.Value, TReference], targetReference *model_starlark_pb.TargetReference) (eval.Value, error) {
	originalLabel, err := pg_label.NewResolvedLabel(targetReference.OriginalLabel)
	if err != nil {
		return nil, fmt.Errorf("invalid original label %#v: %w", targetReference.OriginalLabel, err)
	}
	var configuredTargetReference *ConfiguredTargetReference[TReference, TMetadata]
	if configured := targetReference.Configured; configured != nil {
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
}
