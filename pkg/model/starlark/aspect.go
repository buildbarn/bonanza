package starlark

import (
	"errors"

	pg_label "bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/storage/object"

	"go.starlark.net/starlark"
)

type Aspect[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	LateNamedValue
	definition *model_starlark_pb.Aspect_Definition
}

var (
	_ EncodableValue[object.LocalReference, model_core.ReferenceMetadata] = (*Aspect[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ NamedGlobal                                                         = (*Aspect[object.LocalReference, model_core.ReferenceMetadata])(nil)
)

func NewAspect[TReference any, TMetadata model_core.ReferenceMetadata](identifier *pg_label.CanonicalStarlarkIdentifier, definition *model_starlark_pb.Aspect_Definition) starlark.Value {
	return &Aspect[TReference, TMetadata]{
		LateNamedValue: LateNamedValue{
			Identifier: identifier,
		},
		definition: definition,
	}
}

func (Aspect[TReference, TMetadata]) String() string {
	return "<aspect>"
}

func (Aspect[TReference, TMetadata]) Type() string {
	return "Aspect"
}

func (Aspect[TReference, TMetadata]) Freeze() {}

func (Aspect[TReference, TMetadata]) Truth() starlark.Bool {
	return starlark.True
}

func (Aspect[TReference, TMetadata]) Hash(thread *starlark.Thread) (uint32, error) {
	return 0, errors.New("aspect cannot be hashed")
}

func (a *Aspect[TReference, TMetadata]) EncodeValue(path map[starlark.Value]struct{}, currentIdentifier *pg_label.CanonicalStarlarkIdentifier, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], bool, error) {
	if a.Identifier == nil {
		return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, false, errors.New("aspect does not have a name")
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
		), false, nil
	}

	needsCode := false
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
	), needsCode, nil
}
