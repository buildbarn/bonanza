package starlark

import (
	"errors"

	pg_label "bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/eval"
	"bonanza.build/pkg/storage/object"
)

// decodeFunctionValue decodes values of kinds that are backed by
// Starlark functions, or that are only meaningful in the context of
// rule and module extension definitions. It is called by DecodeValue()
// for all kinds that it does not handle itself.
func decodeFunctionValue[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](thread *eval.Thread, encodedValue model_core.Message[*model_starlark_pb.Value, TReference], currentIdentifier *pg_label.CanonicalStarlarkIdentifier) (eval.Value, error) {
	switch typedValue := encodedValue.Message.GetKind().(type) {
	case *model_starlark_pb.Value_Aspect:
		switch aspectKind := typedValue.Aspect.Kind.(type) {
		case *model_starlark_pb.Aspect_Reference:
			identifier, err := pg_label.NewCanonicalStarlarkIdentifier(aspectKind.Reference)
			if err != nil {
				return nil, err
			}
			return NewAspect[TReference, TMetadata](&identifier, nil), nil
		case *model_starlark_pb.Aspect_Definition_:
			if currentIdentifier == nil {
				return nil, errors.New("encoded aspect does not have a name")
			}
			return NewAspect[TReference, TMetadata](currentIdentifier, aspectKind.Definition), nil
		default:
			return nil, errors.New("encoded aspect does not have a reference or definition")
		}
	case *model_starlark_pb.Value_Attr:
		attrType, err := DecodeAttrType[TReference, TMetadata](model_core.Nested(encodedValue, typedValue.Attr))
		if err != nil {
			return nil, err
		}

		var defaultValue eval.Value
		if d := typedValue.Attr.Default; d != nil {
			// TODO: Should we also canonicalize?
			var err error
			defaultValue, err = DecodeValue[TReference, TMetadata](
				thread,
				model_core.Nested(encodedValue, d),
				nil,
			)
			if err != nil {
				return nil, err
			}
		}
		return NewAttr[TReference, TMetadata](attrType, defaultValue, typedValue.Attr.Configurable), nil
	case *model_starlark_pb.Value_Function:
		return NewNamedFunction(NewProtoNamedFunctionDefinition[TReference, TMetadata](
			model_core.Nested(encodedValue, typedValue.Function),
		)), nil
	case *model_starlark_pb.Value_Provider:
		return DecodeProvider[TReference, TMetadata](model_core.Nested(encodedValue, typedValue.Provider))
	case *model_starlark_pb.Value_Macro:
		switch macroKind := typedValue.Macro.Kind.(type) {
		case *model_starlark_pb.Macro_Reference:
			identifier, err := pg_label.NewCanonicalStarlarkIdentifier(macroKind.Reference)
			if err != nil {
				return nil, err
			}
			return NewMacro(&identifier, NewReloadingMacroDefinition[TReference, TMetadata](identifier)), nil
		case *model_starlark_pb.Macro_Definition_:
			if currentIdentifier == nil {
				return nil, errors.New("encoded macro does not have a name")
			}
			return NewMacro(currentIdentifier, NewProtoMacroDefinition[TReference, TMetadata](
				model_core.Nested(encodedValue, macroKind.Definition),
			)), nil
		default:
			return nil, errors.New("encoded macro does not have a reference or definition")
		}
	case *model_starlark_pb.Value_ModuleExtension:
		return NewModuleExtension(NewProtoModuleExtensionDefinition[TReference, TMetadata](
			model_core.Nested(encodedValue, typedValue.ModuleExtension),
		)), nil
	case *model_starlark_pb.Value_RepositoryRule:
		switch repositoryRuleKind := typedValue.RepositoryRule.Kind.(type) {
		case *model_starlark_pb.RepositoryRule_Reference:
			identifier, err := pg_label.NewCanonicalStarlarkIdentifier(repositoryRuleKind.Reference)
			if err != nil {
				return nil, err
			}
			return NewRepositoryRule[TReference, TMetadata](&identifier, nil), nil
		case *model_starlark_pb.RepositoryRule_Definition_:
			if currentIdentifier == nil {
				return nil, errors.New("encoded repository_rule does not have a name")
			}
			return NewRepositoryRule(currentIdentifier, NewProtoRepositoryRuleDefinition[TReference, TMetadata](
				model_core.Nested(encodedValue, repositoryRuleKind.Definition),
			)), nil
		default:
			return nil, errors.New("encoded repository_rule does not have a reference or definition")
		}
	case *model_starlark_pb.Value_Rule:
		switch ruleKind := typedValue.Rule.Kind.(type) {
		case *model_starlark_pb.Rule_Reference:
			identifier, err := pg_label.NewCanonicalStarlarkIdentifier(ruleKind.Reference)
			if err != nil {
				return nil, err
			}
			return NewRule(&identifier, NewReloadingRuleDefinition[TReference, TMetadata](identifier)), nil
		case *model_starlark_pb.Rule_Definition_:
			if currentIdentifier == nil {
				return nil, errors.New("encoded rule does not have a name")
			}
			return NewRule(currentIdentifier, NewProtoRuleDefinition[TReference, TMetadata](
				model_core.Nested(encodedValue, ruleKind.Definition),
			)), nil
		default:
			return nil, errors.New("encoded rule does not have a reference or definition")
		}
	case *model_starlark_pb.Value_Subrule:
		switch subruleKind := typedValue.Subrule.Kind.(type) {
		case *model_starlark_pb.Subrule_Reference:
			identifier, err := pg_label.NewCanonicalStarlarkIdentifier(subruleKind.Reference)
			if err != nil {
				return nil, err
			}
			return NewSubrule[TReference, TMetadata](&identifier, nil), nil
		case *model_starlark_pb.Subrule_Definition_:
			if currentIdentifier == nil {
				return nil, errors.New("encoded subrule does not have a name")
			}
			return NewSubrule(currentIdentifier, NewProtoSubruleDefinition[TReference, TMetadata]()), nil
		default:
			return nil, errors.New("encoded subrule does not have a reference or definition")
		}
	case *model_starlark_pb.Value_TagClass:
		return NewTagClass(NewProtoTagClassDefinition[TReference, TMetadata](
			model_core.Nested(encodedValue, typedValue.TagClass),
		)), nil
	case *model_starlark_pb.Value_Transition:
		t := NewTransition(
			NewProtoTransitionDefinition[TReference, TMetadata](
				model_core.Nested(encodedValue, typedValue.Transition),
			),
		)
		if currentIdentifier != nil {
			t.AssignIdentifier(*currentIdentifier)
		}
		return t, nil
	default:
		return nil, errors.New("unknown value kind")
	}
}
