package starlark

import (
	"errors"
	"fmt"
	"slices"
	"sort"

	pg_label "bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/storage/object"

	"go.starlark.net/starlark"
)

// Subrule corresponds to a Starlark value of a subrule. Subrules are
// effectively simplified rules that cannot be instantiated in the form
// of rule targets. Instead, they can be invoked as part of the
// implementation function of a rule or another subrule.
//
// Subrules effectively act as utility functions that also have label
// attributes bound to them.
type Subrule[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	LateNamedValue
	definition SubruleDefinition[TReference, TMetadata]
}

var (
	_ starlark.Callable                                                   = (*Subrule[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ EncodableValue[object.LocalReference, model_core.ReferenceMetadata] = (*Subrule[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ NamedGlobal                                                         = (*Subrule[object.LocalReference, model_core.ReferenceMetadata])(nil)
)

// NewSubrule returns a Starlark value corresponding to a subrule. Such
// values are typically created using the subrule() function.
func NewSubrule[TReference any, TMetadata model_core.ReferenceMetadata](identifier *pg_label.CanonicalStarlarkIdentifier, definition SubruleDefinition[TReference, TMetadata]) starlark.Value {
	return &Subrule[TReference, TMetadata]{
		LateNamedValue: LateNamedValue{
			Identifier: identifier,
		},
		definition: definition,
	}
}

func (Subrule[TReference, TMetadata]) String() string {
	return "<subrule>"
}

// Type returns the type name of a subrule value.
func (Subrule[TReference, TMetadata]) Type() string {
	return "subrule"
}

// Freeze the subrule. Because subrules are immutable, this method has
// no effect.
func (Subrule[TReference, TMetadata]) Freeze() {}

// Truth returns whether a subrule should evaluate to true or false when
// implicitly converted to a boolean value. Subrules always evaluate to
// true.
func (Subrule[TReference, TMetadata]) Truth() starlark.Bool {
	return starlark.True
}

// Hash a subrule, so that it can be used as a key in a dictionary. For
// subrules, this is not supported.
func (Subrule[TReference, TMetadata]) Hash(thread *starlark.Thread) (uint32, error) {
	return 0, errors.New("subrule cannot be hashed")
}

// Name returns the name of the subrule. This typically corresponds to
// the Starlark identifier of the global variable to which the subrule
// is assigned. If no such assignment is made, a placeholder string is
// returned.
func (sr *Subrule[TReference, TMetadata]) Name() string {
	if sr.Identifier == nil {
		return "subrule"
	}
	return sr.Identifier.GetStarlarkIdentifier().String()
}

// SubruleInvokerKey is the key under which the SubruleInvoker should be
// registered as a local variable in the Starlark thread.
const SubruleInvokerKey = "subrule_invoker"

// SubruleInvoker is a callback type that is invoked when a rule
// implementation calls into a subrule. It is the callback's
// responsibility for actually executing the subrule and returning the
// subrule's return value.
type SubruleInvoker = func(subruleIdentifier pg_label.CanonicalStarlarkIdentifier, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error)

// CallInternal is invoked when a rule implementation calls into a
// subrule.
func (sr *Subrule[TReference, TMetadata]) CallInternal(thread *starlark.Thread, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	subruleInvoker := thread.Local(SubruleInvokerKey)
	if subruleInvoker == nil {
		return nil, errors.New("subrules cannot be invoked from within this context")
	}
	if sr.Identifier == nil {
		return nil, errors.New("subrule does not have a name")
	}
	return subruleInvoker.(SubruleInvoker)(*sr.Identifier, args, kwargs)
}

// EncodeValue encodes a subrule to a Starlark value Protobuf message.
// This allows it to be written to storage and subsequently reloaded
// during target configuration.
func (sr *Subrule[TReference, TMetadata]) EncodeValue(path map[starlark.Value]struct{}, currentIdentifier *pg_label.CanonicalStarlarkIdentifier, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], bool, error) {
	if sr.Identifier == nil {
		return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, false, errors.New("subrule does not have a name")
	}
	if currentIdentifier == nil || *currentIdentifier != *sr.Identifier {
		// Not the canonical identifier under which this subrule
		// is known. Emit a reference.
		return model_core.NewSimplePatchedMessage[TMetadata](
			&model_starlark_pb.Value{
				Kind: &model_starlark_pb.Value_Subrule{
					Subrule: &model_starlark_pb.Subrule{
						Kind: &model_starlark_pb.Subrule_Reference{
							Reference: sr.Identifier.String(),
						},
					},
				},
			},
		), false, nil
	}

	definition, needsCode, err := sr.definition.Encode(path, options)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, false, err
	}
	return model_core.NewPatchedMessage(
		&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_Subrule{
				Subrule: &model_starlark_pb.Subrule{
					Kind: &model_starlark_pb.Subrule_Definition_{
						Definition: definition.Message,
					},
				},
			},
		},
		definition.Patcher,
	), needsCode, nil
}

// SubruleDefinition contains the definition of a subrule.
type SubruleDefinition[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	Encode(path map[starlark.Value]struct{}, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Subrule_Definition, TMetadata], bool, error)
}

type starlarkSubruleDefinition[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	attrs          map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata]
	implementation NamedFunction[TReference, TMetadata]
	subrules       []*Subrule[TReference, TMetadata]
}

// NewStarlarkSubruleDefinition creates the definition of a subrule,
// given the parameters that were provided to the subrule() function.
func NewStarlarkSubruleDefinition[TReference any, TMetadata model_core.ReferenceMetadata](
	attrs map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata],
	implementation NamedFunction[TReference, TMetadata],
	subrules []*Subrule[TReference, TMetadata],
) SubruleDefinition[TReference, TMetadata] {
	return &starlarkSubruleDefinition[TReference, TMetadata]{
		attrs:          attrs,
		implementation: implementation,
		subrules:       subrules,
	}
}

func (sd *starlarkSubruleDefinition[TReference, TMetadata]) Encode(path map[starlark.Value]struct{}, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Subrule_Definition, TMetadata], bool, error) {
	patcher := model_core.NewReferenceMessagePatcher[TMetadata]()

	implementation, needsCode, err := sd.implementation.Encode(path, options)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Subrule_Definition, TMetadata]{}, false, err
	}
	patcher.Merge(implementation.Patcher)

	namedAttrs, namedAttrsNeedCode, err := encodeNamedAttrs(sd.attrs, path, options)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Subrule_Definition, TMetadata]{}, false, err
	}
	needsCode = needsCode || namedAttrsNeedCode
	patcher.Merge(namedAttrs.Patcher)

	subruleIdentifiers := make([]string, 0, len(sd.subrules))
	for i, subrule := range sd.subrules {
		if subrule.Identifier == nil {
			return model_core.PatchedMessage[*model_starlark_pb.Subrule_Definition, TMetadata]{}, false, fmt.Errorf("subrule at index %d does not have an identifier", i)
		}
		subruleIdentifiers = append(subruleIdentifiers, subrule.Identifier.String())
	}
	sort.Strings(subruleIdentifiers)

	return model_core.NewPatchedMessage(
		&model_starlark_pb.Subrule_Definition{
			Attrs:              namedAttrs.Message,
			Implementation:     implementation.Message,
			SubruleIdentifiers: slices.Compact(subruleIdentifiers),
		},
		patcher,
	), needsCode, nil
}

type protoSubruleDefinition[TReference any, TMetadata model_core.ReferenceMetadata] struct{}

// NewProtoSubruleDefinition contains the definition of a subrule that
// was declared in another .bzl file and has subsequently been written
// to storage.
//
// As subrules are only accessed during target configuration and this is
// not done by directly referencing the Starlark value object, there is no need
// for this type to retain any information. There is also no way for the
// definition of a subrule to be carried over between .bzl files, as such
// indirection is always done by referencing the original identifier of the
// subrule. This type therefore merely acts as a placeholder.
func NewProtoSubruleDefinition[TReference any, TMetadata model_core.ReferenceMetadata]() SubruleDefinition[TReference, TMetadata] {
	return &protoSubruleDefinition[TReference, TMetadata]{}
}

func (sd *protoSubruleDefinition[TReference, TMetadata]) Encode(path map[starlark.Value]struct{}, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Subrule_Definition, TMetadata], bool, error) {
	panic("rule definition was already encoded previously")
}
