package analysis

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"bonanza.build/pkg/evaluation"
	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"

	"go.starlark.net/starlark"
)

type RepositoryRule struct {
	Implementation starlark.Callable
	Attrs          AttrsDict
}

func (c *baseComputer[TReference, TMetadata]) ComputeRepositoryRuleObjectValue(ctx context.Context, key *model_analysis_pb.RepositoryRuleObject_Key, e RepositoryRuleObjectEnvironment[TReference, TMetadata]) (*RepositoryRule, error) {
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
	d, ok := v.RepositoryRule.Kind.(*model_starlark_pb.RepositoryRule_Definition_)
	if !ok {
		return nil, errors.New("global value is not a repository rule definition")
	}

	attrs, err := c.decodeAttrsDict(
		ctx,
		model_core.Nested(repositoryRuleValue, d.Definition.Attrs),
		func(resolvedLabel label.ResolvedLabel) (starlark.Value, error) {
			return model_starlark.NewLabel[TReference, TMetadata](resolvedLabel), nil
		},
	)
	if err != nil {
		return nil, err
	}

	return &RepositoryRule{
		Implementation: model_starlark.NewNamedFunction(model_starlark.NewProtoNamedFunctionDefinition[TReference, TMetadata](
			model_core.Nested(repositoryRuleValue, d.Definition.Implementation),
		)),
		Attrs: attrs,
	}, nil
}

type PublicAttr struct {
	Name     string
	Default  starlark.Value
	AttrType model_starlark.AttrType
}

type AttrsDict struct {
	Public  []PublicAttr
	Private starlark.StringDict
}

func (c *baseComputer[TReference, TMetadata]) decodeAttrsDict(ctx context.Context, encodedAttrs model_core.Message[[]*model_starlark_pb.NamedAttr, TReference], labelCreator func(label.ResolvedLabel) (starlark.Value, error)) (AttrsDict, error) {
	attrsDict := AttrsDict{
		Private: starlark.StringDict{},
	}
	for _, namedAttr := range encodedAttrs.Message {
		attrType, err := model_starlark.DecodeAttrType[TReference, TMetadata](namedAttr.Attr)
		if err != nil {
			return AttrsDict{}, fmt.Errorf("invalid type for attribute %#v: %w", namedAttr.Name, err)
		}

		if strings.HasPrefix(namedAttr.Name, "_") {
			value, err := model_starlark.DecodeValue[TReference, TMetadata](
				model_core.Nested(encodedAttrs, namedAttr.Attr.GetDefault()),
				/* currentIdentifier = */ nil,
				c.getValueDecodingOptions(ctx, labelCreator),
			)
			if err != nil {
				return AttrsDict{}, fmt.Errorf("invalid default value for attribute %#v: %w", namedAttr.Name, err)
			}
			attrsDict.Private[namedAttr.Name] = value
		} else {

			var defaultAttr starlark.Value
			if d := namedAttr.Attr.GetDefault(); d != nil {
				// TODO: Call into attr type to validate
				// the value!
				defaultAttr, err = model_starlark.DecodeValue[TReference, TMetadata](
					model_core.Nested(encodedAttrs, d),
					/* currentIdentifier = */ nil,
					c.getValueDecodingOptions(ctx, labelCreator),
				)
				if err != nil {
					return AttrsDict{}, fmt.Errorf("invalid default value for attribute %#v: %w", namedAttr.Name, err)
				}
			}
			attrsDict.Public = append(attrsDict.Public, PublicAttr{
				Name:     namedAttr.Name,
				Default:  defaultAttr,
				AttrType: attrType,
			})
		}
	}
	return attrsDict, nil
}
