package starlark

import (
	"maps"
	"slices"
	"strings"

	pg_label "bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/storage/object"
)

// ModuleExtensionDefinition contains the actual definition of the
// Starlark module extension object. Its attributes are either backed by
// Starlark value objects, or Protobuf messages in storage.
type ModuleExtensionDefinition[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	EncodeValue(thread *eval.Thread, path []eval.Value, currentIdentifier *pg_label.CanonicalStarlarkIdentifier, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], error)
}

type moduleExtension[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	ModuleExtensionDefinition[TReference, TMetadata]
}

var (
	_ eval.Value                                                          = (*moduleExtension[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ EncodableValue[object.LocalReference, model_core.ReferenceMetadata] = (*moduleExtension[object.LocalReference, model_core.ReferenceMetadata])(nil)
)

// NewModuleExtension creates a Starlark module extension object.
//
// Module extension objects don't have any specific Starlark behavior,
// as they are never invoked directly. They are simply written to
// storage. All subsequent analysis is performed against Protobuf
// encoded versions.
func NewModuleExtension[TReference any, TMetadata model_core.ReferenceMetadata](definition ModuleExtensionDefinition[TReference, TMetadata]) eval.Value {
	return &moduleExtension[TReference, TMetadata]{
		ModuleExtensionDefinition: definition,
	}
}

// Repr writes the representation of the value.
func (moduleExtension[TReference, TMetadata]) Repr(_ *eval.Thread, p *eval.Printer) error {
	p.WriteString("<module_extension>")
	return nil
}

func (moduleExtension[TReference, TMetadata]) Type() string {
	return "module_extension"
}

func (moduleExtension[TReference, TMetadata]) Truth(*eval.Thread) (bool, error) {
	return true, nil
}

type starlarkModuleExtensionDefinition[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	implementation eval.Function
	tagClasses     map[pg_label.StarlarkIdentifier]*TagClass[TReference, TMetadata]
}

// NewStarlarkModuleExtensionDefinition creates a definition of a module
// extension, where all of the attributes are provided in the form of
// Starlark values. This is called when module_extension() is invoked
// inside a .bzl file.
func NewStarlarkModuleExtensionDefinition[TReference any, TMetadata model_core.ReferenceMetadata](implementation eval.Function, tagClasses map[pg_label.StarlarkIdentifier]*TagClass[TReference, TMetadata]) ModuleExtensionDefinition[TReference, TMetadata] {
	return &starlarkModuleExtensionDefinition[TReference, TMetadata]{
		implementation: implementation,
		tagClasses:     tagClasses,
	}
}

func (med *starlarkModuleExtensionDefinition[TReference, TMetadata]) EncodeValue(thread *eval.Thread, path []eval.Value, currentIdentifier *pg_label.CanonicalStarlarkIdentifier, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], error) {
	implementation, err := EncodeFunction(thread, med.implementation, path, options)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, err
	}
	patcher := implementation.Patcher

	tagClasses := make([]*model_starlark_pb.ModuleExtension_NamedTagClass, 0, len(med.tagClasses))
	for _, name := range slices.SortedFunc(
		maps.Keys(med.tagClasses),
		func(a, b pg_label.StarlarkIdentifier) int { return strings.Compare(a.String(), b.String()) },
	) {
		encodedTagClass, err := med.tagClasses[name].Encode(thread, path, options)
		if err != nil {
			patcher.Discard()
			return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, err
		}
		tagClasses = append(tagClasses, &model_starlark_pb.ModuleExtension_NamedTagClass{
			Name:     name.String(),
			TagClass: encodedTagClass.Message,
		})
		patcher.Merge(encodedTagClass.Patcher)
	}

	return model_core.NewPatchedMessage(
		&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_ModuleExtension{
				ModuleExtension: &model_starlark_pb.ModuleExtension{
					Implementation: implementation.Message,
					TagClasses:     tagClasses,
				},
			},
		},
		patcher,
	), nil
}

type protoModuleExtensionDefinition[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	message model_core.Message[*model_starlark_pb.ModuleExtension, TReference]
}

// NewProtoModuleExtensionDefinition creates a definition of a module
// extension that is backed by a Protobuf message that was loaded from
// storage.
func NewProtoModuleExtensionDefinition[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](message model_core.Message[*model_starlark_pb.ModuleExtension, TReference]) ModuleExtensionDefinition[TReference, TMetadata] {
	return &protoModuleExtensionDefinition[TReference, TMetadata]{
		message: message,
	}
}

func (med *protoModuleExtensionDefinition[TReference, TMetadata]) EncodeValue(thread *eval.Thread, path []eval.Value, currentIdentifier *pg_label.CanonicalStarlarkIdentifier, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], error) {
	patchedMessage := model_core.Patch(options.ObjectCapturer, med.message)
	return model_core.NewPatchedMessage(
		&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_ModuleExtension{
				ModuleExtension: patchedMessage.Message,
			},
		},
		patchedMessage.Patcher,
	), nil
}
