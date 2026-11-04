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
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/starlark/unpack"
	"bonanza.build/pkg/storage/object"
)

type repositoryRule[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	LateNamedValue
	definition RepositoryRuleDefinition[TReference, TMetadata]
}

var (
	_ eval.Callable                                                       = (*repositoryRule[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ EncodableValue[object.LocalReference, model_core.ReferenceMetadata] = (*repositoryRule[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ NamedGlobal                                                         = (*repositoryRule[object.LocalReference, model_core.ReferenceMetadata])(nil)
)

// NewRepositoryRule creates a Starlark repository_rule object. The
// resulting object can be called to register a repository.
func NewRepositoryRule[TReference any, TMetadata model_core.ReferenceMetadata](identifier *pg_label.CanonicalStarlarkIdentifier, definition RepositoryRuleDefinition[TReference, TMetadata]) eval.Value {
	return &repositoryRule[TReference, TMetadata]{
		LateNamedValue: LateNamedValue{
			Identifier: identifier,
		},
		definition: definition,
	}
}

// Repr writes the representation of the value.
func (repositoryRule[TReference, TMetadata]) Repr(_ *eval.Thread, p *eval.Printer) error {
	p.WriteString("<repository_rule>")
	return nil
}

func (repositoryRule[TReference, TMetadata]) Type() string {
	return "repository_rule"
}

func (repositoryRule[TReference, TMetadata]) Truth(*eval.Thread) (bool, error) {
	return true, nil
}

func (rr *repositoryRule[TReference, TMetadata]) Name() string {
	if rr.Identifier == nil {
		return "repository_rule"
	}
	return rr.Identifier.GetStarlarkIdentifier().String()
}

// RepoRegistrarKey is a Starlark thread local variable key that is used
// by repository rule objects to obtain the RepoRegistrar object
// belonging to the module extension that is currently being evaluated.
// If this thread local variable is not set, it is not possible to
// register repositories from within the current context.
const RepoRegistrarKey = "repo_registrar"

func (rr *repositoryRule[TReference, TMetadata]) CallInternal(thread *eval.Thread, args eval.Args) (eval.Value, error) {
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
	values := make([]eval.Value, len(attrs))
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
				unpack.Bind(&values[len(attrNames)], unpacker),
			)
			attrNames = append(attrNames, nameStr)
		}
	}

	var name string
	unpackers = append(
		unpackers,
		"name", unpack.Bind(&name, unpack.Stringer(unpack.ApparentRepo)),
	)

	if err := eval.UnpackArgs(
		thread, rr.Identifier.GetStarlarkIdentifier().String(), args,
		unpackers...,
	); err != nil {
		return nil, err
	}

	valueEncodingOptions := thread.Local(ValueEncodingOptionsKey).(*ValueEncodingOptions[TReference, TMetadata])
	var attrKeys []string
	attrValuesBuilder := NewListBuilder[TReference, TMetadata](valueEncodingOptions)
	for i, attrName := range attrNames {
		if value := values[i]; value != nil {
			encodedValue, err := EncodeValue[TReference, TMetadata](
				thread,
				value,
				/* path = */ nil,
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

	return eval.None, repoRegistrar.registerRepo(
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

func (rr *repositoryRule[TReference, TMetadata]) EncodeValue(thread *eval.Thread, path []eval.Value, currentIdentifier *pg_label.CanonicalStarlarkIdentifier, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], error) {
	if rr.Identifier == nil {
		return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, errors.New("repository_rule does not have a name")
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
		), nil
	}

	definition, err := rr.definition.Encode(thread, path, options)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, err
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
	), nil
}

// RepositoryRuleDefinition can be used to access the properties that
// were provided to the repository rule's constructor function, such as
// its attributes.
type RepositoryRuleDefinition[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	Encode(thread *eval.Thread, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.RepositoryRule_Definition, TMetadata], error)
	GetAttrsCheap(thread *eval.Thread) (map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata], error)
}

type starlarkRepositoryRuleDefinition[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	implementation eval.Function
	attrs          map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata]
}

// NewStarlarkRepositoryRuleDefinition creates a
// RepositoryRuleDefinition object that is backed by Starlark values.
// This is called when a repository rule object is created from within
// Starlark code.
func NewStarlarkRepositoryRuleDefinition[TReference any, TMetadata model_core.ReferenceMetadata](implementation eval.Function, attrs map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata]) RepositoryRuleDefinition[TReference, TMetadata] {
	return &starlarkRepositoryRuleDefinition[TReference, TMetadata]{
		implementation: implementation,
		attrs:          attrs,
	}
}

func (rrd *starlarkRepositoryRuleDefinition[TReference, TMetadata]) Encode(thread *eval.Thread, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.RepositoryRule_Definition, TMetadata], error) {
	implementation, err := EncodeFunction(thread, rrd.implementation, path, options)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.RepositoryRule_Definition, TMetadata]{}, err
	}

	namedAttrs, err := encodeNamedAttrs(thread, rrd.attrs, path, options)
	if err != nil {
		implementation.Discard()
		return model_core.PatchedMessage[*model_starlark_pb.RepositoryRule_Definition, TMetadata]{}, err
	}

	patcher := implementation.Patcher
	patcher.Merge(namedAttrs.Patcher)
	return model_core.NewPatchedMessage(
		&model_starlark_pb.RepositoryRule_Definition{
			Implementation: implementation.Message,
			Attrs:          namedAttrs.Message,
		},
		patcher,
	), nil
}

func (rrd *starlarkRepositoryRuleDefinition[TReference, TMetadata]) GetAttrsCheap(thread *eval.Thread) (map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata], error) {
	return rrd.attrs, nil
}

type protoRepositoryRuleDefinition[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	message         model_core.Message[*model_starlark_pb.RepositoryRule_Definition, TReference]
	protoAttrsCache protoAttrsCache[TReference, TMetadata]
}

// NewProtoRepositoryRuleDefinition creates a RepositoryRuleDefinition
// object that is backed by a repository rule definition in the form of
// a Protobuf message. This is called when a repository rule object is
// reloaded from storage.
func NewProtoRepositoryRuleDefinition[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](message model_core.Message[*model_starlark_pb.RepositoryRule_Definition, TReference]) RepositoryRuleDefinition[TReference, TMetadata] {
	return &protoRepositoryRuleDefinition[TReference, TMetadata]{
		message: message,
	}
}

func (*protoRepositoryRuleDefinition[TReference, TMetadata]) Encode(thread *eval.Thread, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.RepositoryRule_Definition, TMetadata], error) {
	panic("rule definition was already encoded previously")
}

func (rrd *protoRepositoryRuleDefinition[TReference, TMetadata]) GetAttrsCheap(thread *eval.Thread) (map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata], error) {
	return rrd.protoAttrsCache.getAttrsCheap(thread, model_core.Nested(rrd.message, rrd.message.Message.Attrs))
}

type reloadingRepositoryRuleDefinition[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata] struct {
	identifier pg_label.CanonicalStarlarkIdentifier
	base       RepositoryRuleDefinition[TReference, TMetadata]
}

// NewReloadingRepositoryRuleDefinition creates a repository rule
// definition that is obtained by resolving the global variable to
// which the repository rule is assigned. This is used when repository
// rules are reloaded from storage by reference, e.g. because they were
// assigned to multiple globals.
func NewReloadingRepositoryRuleDefinition[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](identifier pg_label.CanonicalStarlarkIdentifier) RepositoryRuleDefinition[TReference, TMetadata] {
	return &reloadingRepositoryRuleDefinition[TReference, TMetadata]{
		identifier: identifier,
	}
}

func (*reloadingRepositoryRuleDefinition[TReference, TMetadata]) Encode(thread *eval.Thread, path []eval.Value, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.RepositoryRule_Definition, TMetadata], error) {
	panic("repository rule definition was already encoded previously")
}

// getBase obtains the actual definition of the repository rule. As
// Starlark values are never shared between threads, the definition
// can be cached without making dependencies unstable.
func (rrd *reloadingRepositoryRuleDefinition[TReference, TMetadata]) getBase(thread *eval.Thread) (RepositoryRuleDefinition[TReference, TMetadata], error) {
	if rrd.base == nil {
		globalResolver, ok := thread.Local(GlobalResolverKey).(GlobalResolver[TReference])
		if !ok {
			return nil, fmt.Errorf("repository rule %#v cannot be resolved from within this context", rrd.identifier.String())
		}
		value, err := globalResolver(rrd.identifier)
		if err != nil {
			return nil, err
		}
		valueKind, ok := value.Message.Kind.(*model_starlark_pb.Value_RepositoryRule)
		if !ok {
			return nil, fmt.Errorf("identifier %#v is not a repository rule", rrd.identifier.String())
		}
		definition, ok := valueKind.RepositoryRule.Kind.(*model_starlark_pb.RepositoryRule_Definition_)
		if !ok {
			return nil, fmt.Errorf("repository rule %#v does not have a definition", rrd.identifier.String())
		}
		rrd.base = NewProtoRepositoryRuleDefinition[TReference, TMetadata](model_core.Nested(value, definition.Definition))
	}
	return rrd.base, nil
}

func (rrd *reloadingRepositoryRuleDefinition[TReference, TMetadata]) GetAttrsCheap(thread *eval.Thread) (map[pg_label.StarlarkIdentifier]*Attr[TReference, TMetadata], error) {
	base, err := rrd.getBase(thread)
	if err != nil {
		return nil, err
	}
	return base.GetAttrsCheap(thread)
}
