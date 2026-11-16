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
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/storage/object"
)

// Aspect represents a Starlark aspect object. Aspects allow augmenting
// build dependency graphs with additional information and actions.
type Aspect[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	LateNamedValue
	definition AspectDefinition[TReference, TMetadata]
}

var (
	_ EncodableValue[object.LocalReference, model_core.ReferenceMetadata] = (*Aspect[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ NamedGlobal                                                         = (*Aspect[object.LocalReference, model_core.ReferenceMetadata])(nil)
)

// NewAspect creates a new Starlark aspect object, which is typically
// performed by calling the aspect() constructor function.
func NewAspect[TReference any, TMetadata model_core.ReferenceMetadata](identifier *pg_label.CanonicalStarlarkIdentifier, definition AspectDefinition[TReference, TMetadata]) eval.Value {
	return &Aspect[TReference, TMetadata]{
		LateNamedValue: LateNamedValue{
			Identifier: identifier,
		},
		definition: definition,
	}
}

// Repr writes the representation of the value.
func (Aspect[TReference, TMetadata]) Repr(_ *eval.Thread, p *eval.Printer) error {
	p.WriteString("<aspect>")
	return nil
}

// Type returns the type name of a Starlark aspect object in string
// form.
func (Aspect[TReference, TMetadata]) Type() string {
	return "Aspect"
}

// Truth returns whether a Starlark aspect object is a "truthy" or a
// "falsy". Starlark aspect objects are always "truthy".
func (Aspect[TReference, TMetadata]) Truth(*eval.Thread) (bool, error) {
	return true, nil
}

// EncodeValue encodes a Starlark aspect object as a Protobuf message,
// so that it can be written to storage.
func (a *Aspect[TReference, TMetadata]) EncodeValue(thread *eval.Thread, path []eval.Value, currentIdentifier *pg_label.CanonicalStarlarkIdentifier, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], error) {
	if a.Identifier == nil {
		return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, errors.New("aspect does not have a name")
	}
	if currentIdentifier == nil || *currentIdentifier != *a.Identifier {
		// Not the canonical identifier under which this aspect
		// is known. Emit a reference.
		return model_core.NewSimplePatchedMessage[TMetadata](
			&model_starlark_pb.Value{
				Kind: &model_starlark_pb.Value_Aspect{
					Aspect: &model_starlark_pb.Aspect{
						Kind: &model_starlark_pb.Aspect_Reference{
							Reference: a.Identifier.String(),
						},
					},
				},
			},
		), nil
	}

	definition, err := a.definition.Encode(thread, path, options)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, err
	}
	return model_core.NewPatchedMessage(
		&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_Aspect{
				Aspect: &model_starlark_pb.Aspect{
					Kind: &model_starlark_pb.Aspect_Definition_{
						Definition: definition.Message,
					},
				},
			},
		},
		definition.Patcher,
	), nil
}

// encodeAspectIdentifiers converts a list of aspects to a list of
// aspect identifiers, preserving the original order. Duplicates are
// removed, retaining the first occurrence.
func encodeAspectIdentifiers[TReference any, TMetadata model_core.ReferenceMetadata](aspects []*Aspect[TReference, TMetadata]) ([]string, error) {
	aspectIdentifiers := make([]string, 0, len(aspects))
	seen := make(map[string]struct{}, len(aspects))
	for i, aspect := range aspects {
		if aspect.Identifier == nil {
			return nil, fmt.Errorf("aspect at index %d does not have a name", i)
		}
		aspectIdentifier := aspect.Identifier.String()
		if _, ok := seen[aspectIdentifier]; !ok {
			seen[aspectIdentifier] = struct{}{}
			aspectIdentifiers = append(aspectIdentifiers, aspectIdentifier)
		}
	}
	return aspectIdentifiers, nil
}

// checkAspectAttrs validates that the attrs of an aspect only use the
// subset of features that are permitted for aspects. Public attrs are
// parameters of the aspect, and must be of type bool, int or string.
// Private attrs must have a default value. Label attrs may not use
// user-defined transitions.
func checkAspectAttrs[TReference any, TMetadata model_core.ReferenceMetadata](attrs map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata]) error {
	for _, name := range slices.SortedFunc(
		maps.Keys(attrs),
		func(a, b pg_label.StarlarkIdentifier) int { return strings.Compare(a.String(), b.String()) },
	) {
		nameStr := name.String()
		attr := attrs[name]
		if name.IsPublic() {
			switch attr.attrType.(type) {
			case boolAttrType[TReference, TMetadata], *intAttrType[TReference, TMetadata], *stringAttrType[TReference, TMetadata]:
			default:
				return fmt.Errorf("attr %#v: public aspect attrs must be of type bool, int or string, not %s", nameStr, attr.attrType.Type())
			}
		} else {
			if attr.defaultValue == nil {
				return fmt.Errorf("attr %#v: private aspect attrs must have a default value", nameStr)
			}
			if labelOptions, ok := getAttrLabelOptions(attr.attrType); ok && labelOptions.Cfg.isUserDefinedTransition() {
				return fmt.Errorf("attr %#v: aspect attrs cannot use user-defined transitions", nameStr)
			}
		}
	}
	return nil
}

// AspectDefinition contains the definition of an aspect.
type AspectDefinition[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	Encode(thread *eval.Thread, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Aspect_Definition, TMetadata], error)
}

type starlarkAspectDefinition[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	attrs                   map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata]
	attrAspects             []string
	execGroups              map[string]*ExecGroup[TReference, TMetadata]
	implementation          eval.Function
	requiredProviders       []*model_starlark_pb.ProviderSet
	requiredAspectProviders []*model_starlark_pb.ProviderSet
	provides                []string
	requires                []*Aspect[TReference, TMetadata]
	applyToGeneratingRules  bool
	subrules                []*Subrule[TReference, TMetadata]
	toolchainsAspects       []string
	propagationPredicate    eval.Function
}

// NewStarlarkAspectDefinition creates the definition of an aspect,
// given the parameters that were provided to the aspect() function.
// Provider sets and lists of provider identifiers must already have
// been encoded, as these can be computed eagerly.
func NewStarlarkAspectDefinition[TReference any, TMetadata model_core.ReferenceMetadata](
	attrs map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata],
	attrAspects []string,
	execGroups map[string]*ExecGroup[TReference, TMetadata],
	implementation eval.Function,
	requiredProviders []*model_starlark_pb.ProviderSet,
	requiredAspectProviders []*model_starlark_pb.ProviderSet,
	provides []string,
	requires []*Aspect[TReference, TMetadata],
	applyToGeneratingRules bool,
	subrules []*Subrule[TReference, TMetadata],
	toolchainsAspects []pg_label.ResolvedLabel,
	propagationPredicate eval.Function,
) (AspectDefinition[TReference, TMetadata], error) {
	if err := checkAspectAttrs(attrs); err != nil {
		return nil, err
	}

	// Sort and deduplicate the names of attrs along which the
	// aspect propagates. "*" may not be combined with other names.
	sortedAttrAspects := slices.Clone(attrAspects)
	sort.Strings(sortedAttrAspects)
	sortedAttrAspects = slices.Compact(sortedAttrAspects)
	if slices.Contains(sortedAttrAspects, "*") && len(sortedAttrAspects) > 1 {
		return nil, errors.New("attr_aspects: \"*\" cannot be combined with attr names")
	}
	for _, attrAspect := range sortedAttrAspects {
		if attrAspect != "*" {
			if _, err := pg_label.NewStarlarkIdentifier(attrAspect); err != nil {
				return nil, fmt.Errorf("attr_aspects: invalid attr name %#v: %w", attrAspect, err)
			}
		}
	}

	toolchainsAspectsStrings := make([]string, 0, len(toolchainsAspects))
	for _, toolchainType := range toolchainsAspects {
		toolchainsAspectsStrings = append(toolchainsAspectsStrings, toolchainType.String())
	}
	sort.Strings(toolchainsAspectsStrings)

	return &starlarkAspectDefinition[TReference, TMetadata]{
		attrs:                   attrs,
		attrAspects:             sortedAttrAspects,
		execGroups:              execGroups,
		implementation:          implementation,
		requiredProviders:       requiredProviders,
		requiredAspectProviders: requiredAspectProviders,
		provides:                provides,
		requires:                requires,
		applyToGeneratingRules:  applyToGeneratingRules,
		subrules:                subrules,
		toolchainsAspects:       slices.Compact(toolchainsAspectsStrings),
		propagationPredicate:    propagationPredicate,
	}, nil
}

func (ad *starlarkAspectDefinition[TReference, TMetadata]) Encode(thread *eval.Thread, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Aspect_Definition, TMetadata], error) {
	patcher := model_core.NewReferenceMessagePatcher[TMetadata]()

	// Release all references that have been added to the patcher if
	// encoding fails.
	success := false
	defer func() {
		if !success {
			patcher.Discard()
		}
	}()

	namedAttrs, err := encodeNamedAttrs(thread, ad.attrs, path, options)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Aspect_Definition, TMetadata]{}, err
	}
	namedAttrsMessage := namedAttrs.Merge(patcher)

	execGroups := make([]*model_starlark_pb.NamedExecGroup, 0, len(ad.execGroups))
	for _, name := range slices.Sorted(maps.Keys(ad.execGroups)) {
		execGroups = append(execGroups, &model_starlark_pb.NamedExecGroup{
			Name:      name,
			ExecGroup: ad.execGroups[name].Encode(),
		})
	}

	implementation, err := EncodeFunction(thread, ad.implementation, path, options)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Aspect_Definition, TMetadata]{}, err
	}
	implementationMessage := implementation.Merge(patcher)

	requires, err := encodeAspectIdentifiers(ad.requires)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Aspect_Definition, TMetadata]{}, fmt.Errorf("requires: %w", err)
	}

	subruleIdentifiers := make([]string, 0, len(ad.subrules))
	for i, subrule := range ad.subrules {
		if subrule.Identifier == nil {
			return model_core.PatchedMessage[*model_starlark_pb.Aspect_Definition, TMetadata]{}, fmt.Errorf("subrule at index %d does not have an identifier", i)
		}
		subruleIdentifiers = append(subruleIdentifiers, subrule.Identifier.String())
	}
	sort.Strings(subruleIdentifiers)

	var propagationPredicateMessage *model_starlark_pb.Function
	if ad.propagationPredicate != nil {
		propagationPredicate, err := EncodeFunction(thread, ad.propagationPredicate, path, options)
		if err != nil {
			return model_core.PatchedMessage[*model_starlark_pb.Aspect_Definition, TMetadata]{}, err
		}
		propagationPredicateMessage = propagationPredicate.Merge(patcher)
	}

	success = true
	return model_core.NewPatchedMessage(
		&model_starlark_pb.Aspect_Definition{
			Attrs:                   namedAttrsMessage,
			AttrAspects:             ad.attrAspects,
			ExecGroups:              execGroups,
			Implementation:          implementationMessage,
			RequiredProviders:       ad.requiredProviders,
			RequiredAspectProviders: ad.requiredAspectProviders,
			Provides:                ad.provides,
			Requires:                requires,
			ApplyToGeneratingRules:  ad.applyToGeneratingRules,
			SubruleIdentifiers:      slices.Compact(subruleIdentifiers),
			ToolchainsAspects:       ad.toolchainsAspects,
			PropagationPredicate:    propagationPredicateMessage,
		},
		patcher,
	), nil
}

type protoAspectDefinition[TReference any, TMetadata model_core.ReferenceMetadata] struct{}

// NewProtoAspectDefinition contains the definition of an aspect that
// was declared in another .bzl file and has subsequently been written
// to storage.
//
// As aspects are only referenced by identifier, both by rules and other
// aspects, there is no need for this type to retain any information.
// This type therefore merely acts as a placeholder.
func NewProtoAspectDefinition[TReference any, TMetadata model_core.ReferenceMetadata]() AspectDefinition[TReference, TMetadata] {
	return &protoAspectDefinition[TReference, TMetadata]{}
}

func (protoAspectDefinition[TReference, TMetadata]) Encode(thread *eval.Thread, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Aspect_Definition, TMetadata], error) {
	panic("aspect definition was already encoded previously")
}
