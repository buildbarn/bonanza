package starlark

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	pg_label "bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/unpack"
	"bonanza.build/pkg/storage/object"

	"go.starlark.net/starlark"
)

type repositoryRule[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	LateNamedValue
	definition RepositoryRuleDefinition[TReference, TMetadata]
}

var (
	_ starlark.Callable                                                   = (*repositoryRule[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ EncodableValue[object.LocalReference, model_core.ReferenceMetadata] = (*repositoryRule[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ NamedGlobal                                                         = (*repositoryRule[object.LocalReference, model_core.ReferenceMetadata])(nil)
)

func NewRepositoryRule[TReference any, TMetadata model_core.ReferenceMetadata](identifier *pg_label.CanonicalStarlarkIdentifier, definition RepositoryRuleDefinition[TReference, TMetadata]) starlark.Value {
	return &repositoryRule[TReference, TMetadata]{
		LateNamedValue: LateNamedValue{
			Identifier: identifier,
		},
		definition: definition,
	}
}

func (rr *repositoryRule[TReference, TMetadata]) String() string {
	return "<repository_rule>"
}

func (rr *repositoryRule[TReference, TMetadata]) Type() string {
	return "repository_rule"
}

func (rr *repositoryRule[TReference, TMetadata]) Freeze() {}

func (rr *repositoryRule[TReference, TMetadata]) Truth() starlark.Bool {
	return starlark.True
}

func (rr *repositoryRule[TReference, TMetadata]) Hash(thread *starlark.Thread) (uint32, error) {
	return 0, errors.New("repository_rule cannot be hashed")
}

func (rr *repositoryRule[TReference, TMetadata]) Name() string {
	if rr.Identifier == nil {
		return "repository_rule"
	}
	return rr.Identifier.GetStarlarkIdentifier().String()
}

const RepoRegistrarKey = "repo_registrar"

func (rr *repositoryRule[TReference, TMetadata]) CallInternal(thread *starlark.Thread, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if rr.Identifier == nil {
		return nil, errors.New("repository rule does not have a name")
	}
	repoRegistrarValue := thread.Local(RepoRegistrarKey)
	if repoRegistrarValue == nil {
		return nil, fmt.Errorf("repository rule cannot be invoked from within this context")
	}
	repoRegistrar := repoRegistrarValue.(*RepoRegistrar[TMetadata])

	attrs, err := rr.definition.GetAttrsCheap(thread)
	if err != nil {
		return nil, err
	}

	var unpackers []any
	attrNames := make([]string, 0, len(attrs))
	values := make([]starlark.Value, len(attrs))
	currentPackage := thread.Local(CanonicalPackageKey).(pg_label.CanonicalPackage)
	for _, name := range slices.SortedFunc(
		maps.Keys(attrs),
		func(a, b pg_label.StarlarkIdentifier) int { return strings.Compare(a.String(), b.String()) },
	) {
		switch nameStr := name.String(); nameStr {
		case "name":
			return nil, fmt.Errorf("repository rule uses attribute with reserved name %#v", nameStr)
		}
		if name.IsPublic() {
			attr := attrs[name]
			nameStr := name.String()
			unpacker := unpack.Canonicalize(attr.attrType.GetCanonicalizer(currentPackage))
			if attr.defaultValue == nil {
				// Attribute is mandatory.
				unpackers = append(unpackers, nameStr)
			} else {
				// Attribute is optional. Bazel allows
				// None to be used to select the default
				// value.
				unpackers = append(unpackers, nameStr+"?")
				unpacker = unpack.IfNotNone(unpacker)
			}
			unpackers = append(
				unpackers,
				unpack.Bind(thread, &values[len(attrNames)], unpacker),
			)
			attrNames = append(attrNames, nameStr)
		}
	}

	var name string
	unpackers = append(
		unpackers,
		"name", unpack.Bind(thread, &name, unpack.Stringer(unpack.ApparentRepo)),
	)

	if err := starlark.UnpackArgs(
		rr.Identifier.GetStarlarkIdentifier().String(), args, kwargs,
		unpackers...,
	); err != nil {
		return nil, err
	}

	valueEncodingOptions := thread.Local(ValueEncodingOptionsKey).(*ValueEncodingOptions[TReference, TMetadata])
	var attrKeys []string
	attrValuesBuilder := NewListBuilder[TReference, TMetadata](valueEncodingOptions)
	for i, attrName := range attrNames {
		if value := values[i]; value != nil {
			encodedValue, _, err := EncodeValue[TReference, TMetadata](
				value,
				/* path = */ map[starlark.Value]struct{}{},
				/* currentIdentifier = */ nil,
				valueEncodingOptions,
			)
			if err != nil {
				return nil, err
			}
			attrKeys = append(attrKeys, attrName)
			if err := attrValuesBuilder.PushChild(model_core.NewPatchedMessage(
				&model_starlark_pb.List_Element{
					Level: &model_starlark_pb.List_Element_Leaf{
						Leaf: encodedValue.Message,
					},
				},
				encodedValue.Patcher,
			)); err != nil {
				return nil, err
			}
		}
	}
	attrValues, err := attrValuesBuilder.FinalizeList()
	if err != nil {
		return nil, err
	}

	return starlark.None, repoRegistrar.registerRepo(
		name,
		model_core.NewPatchedMessage(
			&model_starlark_pb.Repo{
				Name: name,
				Definition: &model_starlark_pb.Repo_Definition{
					RepositoryRuleIdentifier: rr.Identifier.String(),
					AttrValues: &model_starlark_pb.Struct_Fields{
						Keys:   attrKeys,
						Values: attrValues.Message,
					},
				},
			},
			attrValues.Patcher,
		),
	)
}

func (rr *repositoryRule[TReference, TMetadata]) EncodeValue(path map[starlark.Value]struct{}, currentIdentifier *pg_label.CanonicalStarlarkIdentifier, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], bool, error) {
	if rr.Identifier == nil {
		return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, false, errors.New("repository_rule does not have a name")
	}
	if currentIdentifier == nil || *currentIdentifier != *rr.Identifier {
		// Not the canonical identifier under which this
		// repository rule is known. Emit a reference.
		return model_core.NewSimplePatchedMessage[TMetadata](
			&model_starlark_pb.Value{
				Kind: &model_starlark_pb.Value_RepositoryRule{
					RepositoryRule: &model_starlark_pb.RepositoryRule{
						Kind: &model_starlark_pb.RepositoryRule_Reference{
							Reference: rr.Identifier.String(),
						},
					},
				},
			},
		), false, nil
	}

	definition, needsCode, err := rr.definition.Encode(path, options)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, false, err
	}
	return model_core.NewPatchedMessage(
		&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_RepositoryRule{
				RepositoryRule: &model_starlark_pb.RepositoryRule{
					Kind: &model_starlark_pb.RepositoryRule_Definition_{
						Definition: definition.Message,
					},
				},
			},
		},
		definition.Patcher,
	), needsCode, nil
}

type RepositoryRuleDefinition[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	Encode(path map[starlark.Value]struct{}, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.RepositoryRule_Definition, TMetadata], bool, error)
	GetAttrsCheap(thread *starlark.Thread) (map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata], error)
}

type starlarkRepositoryRuleDefinition[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	implementation NamedFunction[TReference, TMetadata]
	attrs          map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata]
}

func NewStarlarkRepositoryRuleDefinition[TReference any, TMetadata model_core.ReferenceMetadata](implementation NamedFunction[TReference, TMetadata], attrs map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata]) RepositoryRuleDefinition[TReference, TMetadata] {
	return &starlarkRepositoryRuleDefinition[TReference, TMetadata]{
		implementation: implementation,
		attrs:          attrs,
	}
}

func (rrd *starlarkRepositoryRuleDefinition[TReference, TMetadata]) Encode(path map[starlark.Value]struct{}, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.RepositoryRule_Definition, TMetadata], bool, error) {
	implementation, implementationNeedsCode, err := rrd.implementation.Encode(path, options)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.RepositoryRule_Definition, TMetadata]{}, false, err
	}

	namedAttrs, namedAttrsNeedCode, err := encodeNamedAttrs(rrd.attrs, path, options)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.RepositoryRule_Definition, TMetadata]{}, false, err
	}

	return model_core.NewPatchedMessage(
		&model_starlark_pb.RepositoryRule_Definition{
			Implementation: implementation.Message,
			Attrs:          namedAttrs.Message,
		},
		namedAttrs.Patcher,
	), implementationNeedsCode || namedAttrsNeedCode, nil
}

func (rrd *starlarkRepositoryRuleDefinition[TReference, TMetadata]) GetAttrsCheap(thread *starlark.Thread) (map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata], error) {
	return rrd.attrs, nil
}

type protoRepositoryRuleDefinition[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	message         model_core.Message[*model_starlark_pb.RepositoryRule_Definition, TReference]
	protoAttrsCache protoAttrsCache[TReference, TMetadata]
}

func NewProtoRepositoryRuleDefinition[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](message model_core.Message[*model_starlark_pb.RepositoryRule_Definition, TReference]) RepositoryRuleDefinition[TReference, TMetadata] {
	return &protoRepositoryRuleDefinition[TReference, TMetadata]{
		message: message,
	}
}

func (rrd *protoRepositoryRuleDefinition[TReference, TMetadata]) Encode(path map[starlark.Value]struct{}, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.RepositoryRule_Definition, TMetadata], bool, error) {
	panic("rule definition was already encoded previously")
}

func (rrd *protoRepositoryRuleDefinition[TReference, TMetadata]) GetAttrsCheap(thread *starlark.Thread) (map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata], error) {
	return rrd.protoAttrsCache.getAttrsCheap(thread, model_core.Nested(rrd.message, rrd.message.Message.Attrs))
}
