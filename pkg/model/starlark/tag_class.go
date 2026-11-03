package starlark

import (
	pg_label "bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/storage/object"
)

// TagClass is a Starlark value type for module extension tag classes.
// MODULE.bazel files may call use_extension() to declare a dependency
// on a module extension. After calling use_extension(), the module
// extension may be annotated with tags. A tag class describes a kind of
// tag, and defines which attributes may be provided to the tag.
type TagClass[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	TagClassDefinition[TReference, TMetadata]
}

var (
	_ eval.Value                                                          = (*TagClass[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ EncodableValue[object.LocalReference, model_core.ReferenceMetadata] = (*TagClass[object.LocalReference, model_core.ReferenceMetadata])(nil)
)

// NewTagClass returns a Starlark value corresponding to a module
// extension tag class. Such values are normally created by calling
// tag_class().
func NewTagClass[TReference any, TMetadata model_core.ReferenceMetadata](definition TagClassDefinition[TReference, TMetadata]) eval.Value {
	return &TagClass[TReference, TMetadata]{
		TagClassDefinition: definition,
	}
}

// Repr writes the representation of the value.
func (TagClass[TReference, TMetadata]) Repr(_ *eval.Thread, p *eval.Printer) error {
	p.WriteString("<tag_class>")
	return nil
}

// Type returns the name of the type of a module extension tag class
// value.
func (TagClass[TReference, TMetadata]) Type() string {
	return "tag_class"
}

// Truth returns whether a module extension tag class should evaluate to
// true or false when implicitly converted to a Boolean value. Module
// extension tag classes always evaluate to true.
func (TagClass[TReference, TMetadata]) Truth(*eval.Thread) (bool, error) {
	return true, nil
}

// EncodeValue encodes a module extension tag class to a Starlark value
// Protobuf message. This allows it to be written to storage, so that it
// can be reloaded at a later point in time.
func (tc *TagClass[TReference, TMetadata]) EncodeValue(thread *eval.Thread, path []eval.Value, currentIdentifier *pg_label.CanonicalStarlarkIdentifier, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], error) {
	tagClass, err := tc.TagClassDefinition.Encode(thread, path, options)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, err
	}
	return model_core.NewPatchedMessage(
		&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_TagClass{
				TagClass: tagClass.Message,
			},
		},
		tagClass.Patcher,
	), nil
}

// TagClassDefinition contains the definition of a module extension tag
// class, which may either be backed by other Starlark values due to a
// call to tag_class() from within a .bzl file, or be backed by a
// previous encoded definition that has been written to storage.
type TagClassDefinition[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	Encode(thread *eval.Thread, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.TagClass, TMetadata], error)
}

type starlarkTagClassDefinition[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	attrs map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata]
}

// NewStarlarkTagClassDefinition creates a new module extension tag
// class definition, given definitions for the tag class's attributes.
//
// This function is called when tag_class() is invoked from within a
// .bzl file.
func NewStarlarkTagClassDefinition[TReference any, TMetadata model_core.ReferenceMetadata](attrs map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata]) TagClassDefinition[TReference, TMetadata] {
	return &starlarkTagClassDefinition[TReference, TMetadata]{
		attrs: attrs,
	}
}

func (tcd *starlarkTagClassDefinition[TReference, TMetadata]) Encode(thread *eval.Thread, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.TagClass, TMetadata], error) {
	encodedAttrs, err := encodeNamedAttrs(thread, tcd.attrs, path, options)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.TagClass, TMetadata]{}, nil
	}
	return model_core.NewPatchedMessage(
		&model_starlark_pb.TagClass{
			Attrs: encodedAttrs.Message,
		},
		encodedAttrs.Patcher,
	), nil
}

type protoTagClassDefinition[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	message model_core.Message[*model_starlark_pb.TagClass, TReference]
}

// NewProtoTagClassDefinition creates a new module extension tag class
// definition that is backed by a definition that is backed by storage.
//
// This function is invoked when a module extension tag class is stored
// in a global variable, so that it can be reused by other .bzl files.
// This is not very common, but does occur in practice.
func NewProtoTagClassDefinition[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](message model_core.Message[*model_starlark_pb.TagClass, TReference]) TagClassDefinition[TReference, TMetadata] {
	return &protoTagClassDefinition[TReference, TMetadata]{
		message: message,
	}
}

func (tcd *protoTagClassDefinition[TReference, TMetadata]) Encode(thread *eval.Thread, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.TagClass, TMetadata], error) {
	return model_core.Patch(options.ObjectCapturer, tcd.message), nil
}
