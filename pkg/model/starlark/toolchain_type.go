package starlark

import (
	"fmt"

	pg_label "bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/syntax"
	"bonanza.build/pkg/starlark/unpack"
	"bonanza.build/pkg/storage/object"
)

// ToolchainType corresponds to a Starlark toolchain type object, as
// normally created by the config_common.toolchain_type() function.
// These objects can be used to refer to toolchains, expressing whether
// the dependency on a toolchain is mandatory or optional.
type ToolchainType[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	toolchainType pg_label.ResolvedLabel
	mandatory     bool
}

var (
	_ eval.HasAttrs                                                       = (*ToolchainType[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ eval.Value                                                          = (*ToolchainType[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ EncodableValue[object.LocalReference, model_core.ReferenceMetadata] = (*ToolchainType[object.LocalReference, model_core.ReferenceMetadata])(nil)
)

// NewToolchainType returns a Starlark toolchain type object, as
// normally created by the config_common.toolchain_type() function.
func NewToolchainType[TReference any, TMetadata model_core.ReferenceMetadata](toolchainType pg_label.ResolvedLabel, mandatory bool) *ToolchainType[TReference, TMetadata] {
	return &ToolchainType[TReference, TMetadata]{
		toolchainType: toolchainType,
		mandatory:     mandatory,
	}
}

// Repr writes the representation of the value.
func (ToolchainType[TReference, TMetadata]) Repr(_ *eval.Thread, p *eval.Printer) error {
	p.WriteString("<config_common.toolchain_type>")
	return nil
}

// Type returns the type name of the Starlark toolchain type object.
func (ToolchainType[TReference, TMetadata]) Type() string {
	return "config_common.toolchain_type"
}

// Truth returns whether the Starlark toolchain type object should
// evaluate to true or false when implicitly converted to a Boolean
// value. Toolchain type objects always evaluate to true.
func (ToolchainType[TReference, TMetadata]) Truth(*eval.Thread) (bool, error) {
	return true, nil
}

// Attr can be used to access attributes of the Starlark toolchain type
// object. These attributes correspond to the properties that were used
// to construct the toolchain type object.
func (tt *ToolchainType[TReference, TMetadata]) Attr(thread *eval.Thread, name string) (eval.Value, error) {
	switch name {
	case "mandatory":
		return eval.Bool(tt.mandatory), nil
	case "toolchain_type":
		return NewLabel[TReference, TMetadata](tt.toolchainType), nil
	default:
		return nil, eval.NoSuchAttrError{Value: tt, Name: name}
	}
}

var toolchainTypeAttrNames = []string{
	"mandatory",
	"toolchain_type",
}

// AttrNames returns the names of the attributes of the Starlark
// toolchain type object. These attributes can be used to access the
// properties that were used to construct the toolchain type object.
func (ToolchainType[TReference, TMetadata]) AttrNames(*eval.Thread) ([]string, error) {
	return toolchainTypeAttrNames, nil
}

// Encode the properties of a Starlark toolchain type value in the form
// of a Protobuf message, so that it can be written to storage.
func (tt *ToolchainType[TReference, TMetadata]) Encode() *model_starlark_pb.ToolchainType {
	return &model_starlark_pb.ToolchainType{
		ToolchainType: tt.toolchainType.String(),
		Mandatory:     tt.mandatory,
	}
}

// Merge two toolchain type objects together that have the same
// toolchain type label. This is used when constructing exec group
// objects to filter any toolchain types that are specified redundantly.
func (tt *ToolchainType[TReference, TMetadata]) Merge(other *ToolchainType[TReference, TMetadata]) *ToolchainType[TReference, TMetadata] {
	return &ToolchainType[TReference, TMetadata]{
		toolchainType: tt.toolchainType,
		mandatory:     tt.mandatory || other.mandatory,
	}
}

// EncodeValue encodes the properties of a Starlark toolchain type value
// in the form of a Starlark value Protobuf message, so that it can be
// written to storage. As toolchain types contain relatively little
// information, the resulting message never contains any outgoing
// references.
func (tt *ToolchainType[TReference, TMetadata]) EncodeValue(thread *eval.Thread, path []eval.Value, currentIdentifier *pg_label.CanonicalStarlarkIdentifier, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], error) {
	return model_core.NewSimplePatchedMessage[TMetadata](
		&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_ToolchainType{
				ToolchainType: tt.Encode(),
			},
		},
	), nil
}

type toolchainTypeUnpackerInto[TReference any, TMetadata model_core.ReferenceMetadata] struct{}

// NewToolchainTypeUnpackerInto is capable of unpacking arguments that
// are provided to functions that accept toolchain types.
//
// Toolchain types may either be provided in the form of strings or
// labels, or as values explicitly constructed using the
// toolchain_type() function. When the former is used, toolchain type
// dependencies are assumed to be mandatory.
func NewToolchainTypeUnpackerInto[TReference any, TMetadata model_core.ReferenceMetadata]() unpack.UnpackerInto[*ToolchainType[TReference, TMetadata]] {
	return toolchainTypeUnpackerInto[TReference, TMetadata]{}
}

func (toolchainTypeUnpackerInto[TReference, TMetadata]) UnpackInto(thread *eval.Thread, v eval.Value, dst **ToolchainType[TReference, TMetadata]) error {
	switch typedV := v.(type) {
	case eval.String, Label[TReference, TMetadata]:
		var l pg_label.ResolvedLabel
		currentPackage, err := CurrentFilePackage(thread)
		if err != nil {
			return err
		}
		if err := NewLabelOrStringUnpackerInto[TReference, TMetadata](currentPackage).UnpackInto(thread, v, &l); err != nil {
			return err
		}
		*dst = NewToolchainType[TReference, TMetadata](l, true)
		return nil
	case *ToolchainType[TReference, TMetadata]:
		*dst = typedV
		return nil
	default:
		return fmt.Errorf("got %s, want config_common.toolchain_type, Label or str", v.Type())
	}
}

func (ui toolchainTypeUnpackerInto[TReference, TMetadata]) Canonicalize(thread *eval.Thread, v eval.Value) (eval.Value, error) {
	var tt *ToolchainType[TReference, TMetadata]
	if err := ui.UnpackInto(thread, v, &tt); err != nil {
		return nil, err
	}
	return tt, nil
}

func (toolchainTypeUnpackerInto[TReference, TMetadata]) GetConcatenationOperator() syntax.Token {
	return 0
}

func decodeToolchainType[TReference any, TMetadata model_core.ReferenceMetadata](toolchainType *model_starlark_pb.ToolchainType) (*ToolchainType[TReference, TMetadata], error) {
	toolchainTypeLabel, err := pg_label.NewResolvedLabel(toolchainType.ToolchainType)
	if err != nil {
		return nil, err
	}
	return NewToolchainType[TReference, TMetadata](toolchainTypeLabel, toolchainType.Mandatory), nil
}
