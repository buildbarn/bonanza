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
	switch encodedValue.Message.WhichKind() {
	case model_starlark_pb.Value_Aspect_case:
		switch encodedValue.Message.GetAspect().WhichKind() {
		case model_starlark_pb.Aspect_Reference_case:
			identifier, err := pg_label.NewCanonicalStarlarkIdentifier(encodedValue.Message.GetAspect().GetReference())
			if err != nil {
				return nil, err
			}
			return NewAspect[TReference, TMetadata](&identifier, nil), nil
		case model_starlark_pb.Aspect_Definition_case:
			if currentIdentifier == nil {
				return nil, errors.New("encoded aspect does not have a name")
			}
			return NewAspect(currentIdentifier, NewProtoAspectDefinition[TReference, TMetadata]()), nil
		default:
			return nil, errors.New("encoded aspect does not have a reference or definition")
		}
	case model_starlark_pb.Value_Attr_case:
		attrType, err := DecodeAttrType[TReference, TMetadata](model_core.Nested(encodedValue, encodedValue.Message.GetAttr()))
		if err != nil {
			return nil, err
		}

		var defaultValue eval.Value
		if d := encodedValue.Message.GetAttr().GetDefault(); d != nil {
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
		return NewAttr[TReference, TMetadata](attrType, defaultValue, encodedValue.Message.GetAttr().GetConfigurable()), nil
	case model_starlark_pb.Value_Function_case:
		return DecodeFunction[TReference, TMetadata](model_core.Nested(encodedValue, encodedValue.Message.GetFunction())), nil
	case model_starlark_pb.Value_Provider_case:
		return DecodeProvider[TReference, TMetadata](model_core.Nested(encodedValue, encodedValue.Message.GetProvider()))
	case model_starlark_pb.Value_Macro_case:
		switch encodedValue.Message.GetMacro().WhichKind() {
		case model_starlark_pb.Macro_Reference_case:
			identifier, err := pg_label.NewCanonicalStarlarkIdentifier(encodedValue.Message.GetMacro().GetReference())
			if err != nil {
				return nil, err
			}
			return NewMacro(&identifier, NewReloadingMacroDefinition[TReference, TMetadata](identifier)), nil
		case model_starlark_pb.Macro_Definition_case:
			if currentIdentifier == nil {
				return nil, errors.New("encoded macro does not have a name")
			}
			return NewMacro(currentIdentifier, NewProtoMacroDefinition[TReference, TMetadata](
				model_core.Nested(encodedValue, encodedValue.Message.GetMacro().GetDefinition()),
			)), nil
		default:
			return nil, errors.New("encoded macro does not have a reference or definition")
		}
	case model_starlark_pb.Value_ModuleExtension_case:
		return NewModuleExtension(NewProtoModuleExtensionDefinition[TReference, TMetadata](
			model_core.Nested(encodedValue, encodedValue.Message.GetModuleExtension()),
		)), nil
	case model_starlark_pb.Value_RepositoryRule_case:
		switch encodedValue.Message.GetRepositoryRule().WhichKind() {
		case model_starlark_pb.RepositoryRule_Reference_case:
			identifier, err := pg_label.NewCanonicalStarlarkIdentifier(encodedValue.Message.GetRepositoryRule().GetReference())
			if err != nil {
				return nil, err
			}
			return NewRepositoryRule(&identifier, NewReloadingRepositoryRuleDefinition[TReference, TMetadata](identifier)), nil
		case model_starlark_pb.RepositoryRule_Definition_case:
			if currentIdentifier == nil {
				return nil, errors.New("encoded repository_rule does not have a name")
			}
			return NewRepositoryRule(currentIdentifier, NewProtoRepositoryRuleDefinition[TReference, TMetadata](
				model_core.Nested(encodedValue, encodedValue.Message.GetRepositoryRule().GetDefinition()),
			)), nil
		default:
			return nil, errors.New("encoded repository_rule does not have a reference or definition")
		}
	case model_starlark_pb.Value_Rule_case:
		switch encodedValue.Message.GetRule().WhichKind() {
		case model_starlark_pb.Rule_Reference_case:
			identifier, err := pg_label.NewCanonicalStarlarkIdentifier(encodedValue.Message.GetRule().GetReference())
			if err != nil {
				return nil, err
			}
			return NewRule(&identifier, NewReloadingRuleDefinition[TReference, TMetadata](identifier)), nil
		case model_starlark_pb.Rule_Definition_case:
			if currentIdentifier == nil {
				return nil, errors.New("encoded rule does not have a name")
			}
			return NewRule(currentIdentifier, NewProtoRuleDefinition[TReference, TMetadata](
				model_core.Nested(encodedValue, encodedValue.Message.GetRule().GetDefinition()),
			)), nil
		default:
			return nil, errors.New("encoded rule does not have a reference or definition")
		}
	case model_starlark_pb.Value_Subrule_case:
		switch encodedValue.Message.GetSubrule().WhichKind() {
		case model_starlark_pb.Subrule_Reference_case:
			identifier, err := pg_label.NewCanonicalStarlarkIdentifier(encodedValue.Message.GetSubrule().GetReference())
			if err != nil {
				return nil, err
			}
			return NewSubrule[TReference, TMetadata](&identifier, nil), nil
		case model_starlark_pb.Subrule_Definition_case:
			if currentIdentifier == nil {
				return nil, errors.New("encoded subrule does not have a name")
			}
			return NewSubrule(currentIdentifier, NewProtoSubruleDefinition[TReference, TMetadata]()), nil
		default:
			return nil, errors.New("encoded subrule does not have a reference or definition")
		}
	case model_starlark_pb.Value_TagClass_case:
		return NewTagClass(NewProtoTagClassDefinition[TReference, TMetadata](
			model_core.Nested(encodedValue, encodedValue.Message.GetTagClass()),
		)), nil
	case model_starlark_pb.Value_Transition_case:
		t := NewTransition(
			NewProtoTransitionDefinition[TReference, TMetadata](
				model_core.Nested(encodedValue, encodedValue.Message.GetTransition()),
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
