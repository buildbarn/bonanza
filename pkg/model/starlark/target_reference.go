package starlark

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"

	pg_label "bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
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
	decodedProviders []atomic.Pointer[Struct[TReference, TMetadata]]
}

// NewConfiguredTargetReference creates a ConfiguredTargetReference
// object, which contains the properties of a Starlark Target object of
// a configured target.
func NewConfiguredTargetReference[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](label pg_label.CanonicalLabel, providers model_core.Message[[]*model_starlark_pb.Struct, TReference]) *ConfiguredTargetReference[TReference, TMetadata] {
	return &ConfiguredTargetReference[TReference, TMetadata]{
		label:            label,
		encodedProviders: providers,
		decodedProviders: make([]atomic.Pointer[Struct[TReference, TMetadata]], len(providers.Message)),
	}
}

func (ctr *ConfiguredTargetReference[TReference, TMetadata]) getProviderValue(thread *starlark.Thread, providerIdentifier pg_label.CanonicalStarlarkIdentifier) (*Struct[TReference, TMetadata], error) {
	valueDecodingOptions := thread.Local(ValueDecodingOptionsKey)
	if valueDecodingOptions == nil {
		return nil, errors.New("providers cannot be decoded from within this context")
	}

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

	strukt := ctr.decodedProviders[index].Load()
	if strukt == nil {
		var err error
		strukt, err = DecodeStruct[TReference, TMetadata](
			model_core.Nested(ctr.encodedProviders, ctr.encodedProviders.Message[index]),
			valueDecodingOptions.(*ValueDecodingOptions[TReference]),
		)
		if err != nil {
			return nil, err
		}
		ctr.decodedProviders[index].Store(strukt)
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
func NewTargetReference[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](originalLabel pg_label.ResolvedLabel, configured *ConfiguredTargetReference[TReference, TMetadata]) starlark.Value {
	return &TargetReference[TReference, TMetadata]{
		originalLabel: originalLabel,
		configured:    configured,
	}
}

var (
	_ EncodableValue[object.LocalReference, model_core.ReferenceMetadata] = (*TargetReference[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ starlark.Comparable                                                 = (*TargetReference[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ starlark.HasAttrs                                                   = (*TargetReference[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ starlark.Mapping                                                    = (*TargetReference[object.LocalReference, model_core.ReferenceMetadata])(nil)
)

func (tr *TargetReference[TReference, TMetadata]) String() string {
	if tr.configured != nil {
		return fmt.Sprintf("<target %s>", tr.configured.label.String())
	}
	return fmt.Sprintf("<unconfigured target %s>", tr.originalLabel.String())
}

// Type returns the name of the type of a Starlark Target value.
func (TargetReference[TReference, TMetadata]) Type() string {
	return "Target"
}

// Freeze the contents of a Starlark Target value. This function has no
// effect, as a Target value is immutable.
func (TargetReference[TReference, TMetadata]) Freeze() {}

// Truth returns whether the Starlark Target value evaluates to true or
// false when implicitly converted to a Boolean value. Starlark Target
// values always convert to true.
func (TargetReference[TReference, TMetadata]) Truth() starlark.Bool {
	return starlark.True
}

// Hash a Starlark Target value, so that it can be used as the key of a
// dictionary. As we assume that the number of targets having the same
// label, but a different configuration is fairly low, we simply hash
// the target's label.
func (tr *TargetReference[TReference, TMetadata]) Hash(thread *starlark.Thread) (uint32, error) {
	return starlark.String(tr.originalLabel.String()).Hash(thread)
}

func (tr *TargetReference[TReference, TMetadata]) equal(thread *starlark.Thread, other *TargetReference[TReference, TMetadata]) (bool, error) {
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
func (tr *TargetReference[TReference, TMetadata]) CompareSameType(thread *starlark.Thread, op syntax.Token, other starlark.Value, depth int) (bool, error) {
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
func (tr *TargetReference[TReference, TMetadata]) Attr(thread *starlark.Thread, name string) (starlark.Value, error) {
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

	return nil, nil
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
func (tr *TargetReference[TReference, TMetadata]) AttrNames() []string {
	if tr.configured != nil {
		return configuredTargetReferenceAttrNames
	}
	return unconfiguredTargetReferenceAttrNames
}

// Get the value of a given provider from the Starlark Target value.
// This is called when a rule invokes ctx.attr.myattr[MyProviderInfo].
func (tr *TargetReference[TReference, TMetadata]) Get(thread *starlark.Thread, v starlark.Value) (starlark.Value, bool, error) {
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
func (tr *TargetReference[TReference, TMetadata]) EncodeValue(path map[starlark.Value]struct{}, currentIdentifier *pg_label.CanonicalStarlarkIdentifier, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], bool, error) {
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
	}), false, nil
}
