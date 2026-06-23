package starlark

import (
	"errors"

	pg_label "bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/storage/object"

	"google.golang.org/protobuf/types/known/emptypb"

	"go.starlark.net/starlark"
)

type macro[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct{}

var _ EncodableValue[object.LocalReference, model_core.ReferenceMetadata] = (*macro[object.LocalReference, model_core.ReferenceMetadata])(nil)

// NewMacro creates a Starlark macro object. These are normally created
// using the macro() function.
func NewMacro[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata]() starlark.Value {
	return macro[TReference, TMetadata]{}
}

func (macro[TReference, TMetadata]) String() string {
	return "<macro>"
}

func (macro[TReference, TMetadata]) Type() string {
	return "macro"
}

func (macro[TReference, TMetadata]) Freeze() {}

func (macro[TReference, TMetadata]) Truth() starlark.Bool {
	return starlark.True
}

func (macro[TReference, TMetadata]) Hash(thread *starlark.Thread) (uint32, error) {
	return 0, errors.New("macro cannot be hashed")
}

func (macro[TReference, TMetadata]) EncodeValue(path map[starlark.Value]struct{}, currentIdentifier *pg_label.CanonicalStarlarkIdentifier, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], bool, error) {
	return model_core.NewSimplePatchedMessage[TMetadata](
		&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_Macro{
				Macro: &emptypb.Empty{},
			},
		},
	), false, nil
}
