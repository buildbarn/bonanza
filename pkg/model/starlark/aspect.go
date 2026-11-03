package starlark

import (
	"errors"

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
	definition *model_starlark_pb.Aspect_Definition
}

var (
	_ EncodableValue[object.LocalReference, model_core.ReferenceMetadata] = (*Aspect[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ NamedGlobal                                                         = (*Aspect[object.LocalReference, model_core.ReferenceMetadata])(nil)
)

// NewAspect creates a new Starlark aspect object, which is typically
// performed by calling the aspect() constructor function.
func NewAspect[TReference any, TMetadata model_core.ReferenceMetadata](identifier *pg_label.CanonicalStarlarkIdentifier, definition *model_starlark_pb.Aspect_Definition) eval.Value {
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

	return model_core.NewSimplePatchedMessage[TMetadata](
		&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_Aspect{
				Aspect: &model_starlark_pb.Aspect{
					Kind: &model_starlark_pb.Aspect_Definition_{
						Definition: &model_starlark_pb.Aspect_Definition{},
					},
				},
			},
		},
	), nil
}
