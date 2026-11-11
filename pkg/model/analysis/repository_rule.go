package analysis

import (
	"errors"
	"fmt"
	"strings"

	model_core "bonanza.build/pkg/model/core"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/storage/object"
)

// getRepositoryRuleDefinition obtains the definition of a repository
// rule, given the identifier of the global holding it. An unset message
// is returned if not all dependencies are available yet.
func getRepositoryRuleDefinition[TReference any](e CompiledBzlFileGlobalValueGetter[TReference], identifier string) (model_core.Message[*model_starlark_pb.RepositoryRule_Definition, TReference], error) {
	seenIdentifiers := map[string]struct{}{}
	for {
		if _, ok := seenIdentifiers[identifier]; ok {
			return model_core.Message[*model_starlark_pb.RepositoryRule_Definition, TReference]{}, fmt.Errorf("repository rule %#v refers to itself", identifier)
		}
		seenIdentifiers[identifier] = struct{}{}

		repositoryRuleValue := e.GetCompiledBzlFileGlobalValue(&model_analysis_pb.CompiledBzlFileGlobal_Key{
			Identifier: identifier,
		})
		if !repositoryRuleValue.IsSet() {
			return model_core.Message[*model_starlark_pb.RepositoryRule_Definition, TReference]{}, nil
		}

		v, ok := repositoryRuleValue.Message.Global.GetKind().(*model_starlark_pb.Value_RepositoryRule)
		if !ok {
			return model_core.Message[*model_starlark_pb.RepositoryRule_Definition, TReference]{}, errors.New("global value is not a repository rule")
		}
		switch kind := v.RepositoryRule.Kind.(type) {
		case *model_starlark_pb.RepositoryRule_Definition_:
			return model_core.Nested(repositoryRuleValue, kind.Definition), nil
		case *model_starlark_pb.RepositoryRule_Reference:
			// When use_repo_rule() is used, the repository
			// rule identifier may still refer to a global
			// that refers to the actual repository rule
			// object.
			identifier = kind.Reference
		default:
			return model_core.Message[*model_starlark_pb.RepositoryRule_Definition, TReference]{}, errors.New("unknown kind of repository rule")
		}
	}
}

// decodeRepositoryRule decodes the definition of a repository rule, so
// that its implementation function can be invoked.
func (*baseComputer[TReference, TMetadata]) decodeRepositoryRule(thread *eval.Thread, definition model_core.Message[*model_starlark_pb.RepositoryRule_Definition, TReference]) (eval.Value, AttrsDict[TReference, TMetadata], error) {
	attrs, err := decodeAttrsDict[TReference, TMetadata](thread, model_core.Nested(definition, definition.Message.Attrs))
	if err != nil {
		return nil, AttrsDict[TReference, TMetadata]{}, err
	}
	if definition.Message.Implementation == nil {
		return nil, AttrsDict[TReference, TMetadata]{}, errors.New("repository rule does not have an implementation function")
	}
	return model_starlark.DecodeFunction[TReference, TMetadata](model_core.Nested(definition, definition.Message.Implementation)), attrs, nil
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

func decodeAttrsDict[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](thread *eval.Thread, encodedAttrs model_core.Message[[]*model_starlark_pb.NamedAttr, TReference]) (AttrsDict[TReference, TMetadata], error) {
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
