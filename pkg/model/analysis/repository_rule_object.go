package analysis

import (
	"context"
	"errors"
	"fmt"
	"strings"

	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
)

// RepositoryRule contains the definition of a repository rule, such as
// its implementation function and the attributes it accepts. Use
// decodeRepositoryRule() to obtain the definition in the form of
// Starlark values.
type RepositoryRule[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	Definition model_core.Message[*model_starlark_pb.RepositoryRule_Definition, TReference]
}

// decodeRepositoryRule decodes the definition of a repository rule, so
// that its implementation function can be invoked.
func (c *baseComputer[TReference, TMetadata]) decodeRepositoryRule(thread *eval.Thread, repositoryRule *RepositoryRule[TReference, TMetadata]) (eval.Value, AttrsDict[TReference, TMetadata], error) {
	definition := repositoryRule.Definition
	attrs, err := c.decodeAttrsDict(thread, model_core.Nested(definition, definition.Message.Attrs))
	if err != nil {
		return nil, AttrsDict[TReference, TMetadata]{}, err
	}
	if definition.Message.Implementation == nil {
		return nil, AttrsDict[TReference, TMetadata]{}, errors.New("repository rule does not have an implementation function")
	}
	return model_starlark.DecodeFunction[TReference, TMetadata](model_core.Nested(definition, definition.Message.Implementation)), attrs, nil
}

func (*baseComputer[TReference, TMetadata]) ComputeRepositoryRuleObjectValue(ctx context.Context, key *model_analysis_pb.RepositoryRuleObject_Key, e RepositoryRuleObjectEnvironment[TReference, TMetadata]) (*RepositoryRule[TReference, TMetadata], error) {
	repositoryRuleValue := e.GetCompiledBzlFileGlobalValue(&model_analysis_pb.CompiledBzlFileGlobal_Key{
		Identifier: key.Identifier,
	})
	if !repositoryRuleValue.IsSet() {
		return nil, evaluation.ErrMissingDependency
	}

	v, ok := repositoryRuleValue.Message.Global.GetKind().(*model_starlark_pb.Value_RepositoryRule)
	if !ok {
		return nil, errors.New("global value is not a repository rule")
	}
	switch kind := v.RepositoryRule.Kind.(type) {
	case *model_starlark_pb.RepositoryRule_Definition_:
		return &RepositoryRule[TReference, TMetadata]{
			Definition: model_core.Nested(repositoryRuleValue, kind.Definition),
		}, nil
	case *model_starlark_pb.RepositoryRule_Reference:
		// When use_repo_rule() is used, the repository rule
		// identifier may still refer to a global that refers to
		// the actual repository rule object.
		repositoryRuleObject, ok := e.GetRepositoryRuleObjectValue(
			&model_analysis_pb.RepositoryRuleObject_Key{
				Identifier: kind.Reference,
			},
		)
		if !ok {
			return nil, evaluation.ErrMissingDependency
		}
		return repositoryRuleObject, nil
	default:
		return nil, errors.New("unknown kind of repository rule")
	}
}

// PublicAttr contains information on a public attribute accepted by a
// repository rule.
type PublicAttr[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	Name     string
	Default  eval.Value
	AttrType model_starlark.AttrType[TReference, TMetadata]
}

// AttrsDict contains the set of attributes a repository rule accepts,
// split up by ones that are public and private.
type AttrsDict[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	Public  []PublicAttr[TReference, TMetadata]
	Private map[string]eval.Value
}

func (*baseComputer[TReference, TMetadata]) decodeAttrsDict(thread *eval.Thread, encodedAttrs model_core.Message[[]*model_starlark_pb.NamedAttr, TReference]) (AttrsDict[TReference, TMetadata], error) {
	attrsDict := AttrsDict[TReference, TMetadata]{
		Private: map[string]eval.Value{},
	}
	for _, namedAttr := range encodedAttrs.Message {
		attrType, err := model_starlark.DecodeAttrType[TReference, TMetadata](model_core.Nested(encodedAttrs, namedAttr.Attr))
		if err != nil {
			return AttrsDict[TReference, TMetadata]{}, fmt.Errorf("invalid type for attribute %#v: %w", namedAttr.Name, err)
		}

		if strings.HasPrefix(namedAttr.Name, "_") {
			value, err := model_starlark.DecodeValue[TReference, TMetadata](
				thread, model_core.Nested(encodedAttrs, namedAttr.Attr.GetDefault()),
				/* currentIdentifier = */ nil,
			)
			if err != nil {
				return AttrsDict[TReference, TMetadata]{}, fmt.Errorf("invalid default value for attribute %#v: %w", namedAttr.Name, err)
			}
			attrsDict.Private[namedAttr.Name] = value
		} else {

			var defaultAttr eval.Value
			if d := namedAttr.Attr.GetDefault(); d != nil {
				// TODO: Call into attr type to validate
				// the value!
				defaultAttr, err = model_starlark.DecodeValue[TReference, TMetadata](
					thread, model_core.Nested(encodedAttrs, d),
					/* currentIdentifier = */ nil,
				)
				if err != nil {
					return AttrsDict[TReference, TMetadata]{}, fmt.Errorf("invalid default value for attribute %#v: %w", namedAttr.Name, err)
				}
			}
			attrsDict.Public = append(attrsDict.Public, PublicAttr[TReference, TMetadata]{
				Name:     namedAttr.Name,
				Default:  defaultAttr,
				AttrType: attrType,
			})
		}
	}
	return attrsDict, nil
}
