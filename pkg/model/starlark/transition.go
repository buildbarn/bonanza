package starlark

import (
	"errors"
	"fmt"

	pg_label "bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/starlark/unpack"
	"bonanza.build/pkg/storage/object"

	"google.golang.org/protobuf/types/known/emptypb"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// Transition is a Starlark value type that corresponds to a predeclared
// or user defined transition. Transitions can be used to mutate a
// configuration, either as part of an incoming or outgoing edge in the
// build graph.
type Transition[TReference any, TMetadata model_core.CloneableReferenceMetadata] struct {
	TransitionDefinition[TReference, TMetadata]
}

var (
	_ starlark.Value                                                               = (*Transition[object.LocalReference, model_core.CloneableReferenceMetadata])(nil)
	_ EncodableValue[object.LocalReference, model_core.CloneableReferenceMetadata] = (*Transition[object.LocalReference, model_core.CloneableReferenceMetadata])(nil)
	_ NamedGlobal                                                                  = (*Transition[object.LocalReference, model_core.CloneableReferenceMetadata])(nil)
)

// NewTransition creates a new Starlark transition value having a given
// definition. This function is typically invoked when exec_transition()
// or transition() is called.
func NewTransition[TReference any, TMetadata model_core.CloneableReferenceMetadata](definition TransitionDefinition[TReference, TMetadata]) *Transition[TReference, TMetadata] {
	return &Transition[TReference, TMetadata]{
		TransitionDefinition: definition,
	}
}

func (Transition[TReference, TMetadata]) String() string {
	return "<transition>"
}

// Type returns a string representation of the type of a transition.
func (Transition[TReference, TMetadata]) Type() string {
	return "transition"
}

// Freeze the definition of the transition. As transitions are
// immutable, this function has no effect.
func (Transition[TReference, TMetadata]) Freeze() {}

// Truth returns whether the transition is a truthy or a falsy.
// Transitions are always truthy.
func (Transition[TReference, TMetadata]) Truth() starlark.Bool {
	return starlark.True
}

// Hash the transition object, so that it may be used as the key in a
// dictionary. This is currently not supported.
func (Transition[TReference, TMetadata]) Hash(thread *starlark.Thread) (uint32, error) {
	return 0, errors.New("transition cannot be hashed")
}

// TransitionDefinition contains the definition of a configuration
// transition. For user defined transitions this may contain all of the
// transition's properties (inputs, outputs, reference to an
// implementation function). For predeclared transitions ("exec",
// "target", config.none(), etc.), the definition may be trivial.
type TransitionDefinition[TReference any, TMetadata model_core.CloneableReferenceMetadata] interface {
	EncodableValue[TReference, TMetadata]
	AssignIdentifier(identifier pg_label.CanonicalStarlarkIdentifier)
	Encode(path map[starlark.Value]struct{}, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Transition, TMetadata], error)
	EncodeUserDefinedTransition(path map[starlark.Value]struct{}, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Transition_UserDefined, TMetadata], error)
}

type protoTransitionDefinition[TReference object.BasicReference, TMetadata model_core.CloneableReferenceMetadata] struct {
	identifier *pg_label.CanonicalStarlarkIdentifier
	definition model_core.Message[*model_starlark_pb.Transition, TReference]
}

// NewProtoTransitionDefinition creates a transition definition that is
// backed by a Protobuf message. These may either refer to a
// user-defined transition using its Starlark identifier, or a
// predeclared transition ("exec", "target", config.none(), etc.).
func NewProtoTransitionDefinition[TReference object.BasicReference, TMetadata model_core.CloneableReferenceMetadata](definition model_core.Message[*model_starlark_pb.Transition, TReference]) TransitionDefinition[TReference, TMetadata] {
	return &protoTransitionDefinition[TReference, TMetadata]{
		definition: definition,
	}
}

func (td *protoTransitionDefinition[TReference, TMetadata]) AssignIdentifier(identifier pg_label.CanonicalStarlarkIdentifier) {
	// Only allow assigning an identifier if this is a definition of
	// a user-defined transition. All other transition types are
	// compact enough that it doesn't make sense to have them named.
	if td.identifier == nil {
		if userDefined, ok := td.definition.Message.Kind.(*model_starlark_pb.Transition_UserDefined_); ok {
			if _, ok := userDefined.UserDefined.Kind.(*model_starlark_pb.Transition_UserDefined_Identifier); !ok {
				td.identifier = &identifier
			}
		}
	}
}

func (td *protoTransitionDefinition[TReference, TMetadata]) Encode(path map[starlark.Value]struct{}, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Transition, TMetadata], error) {
	if td.identifier != nil {
		return model_core.NewSimplePatchedMessage[TMetadata](
			&model_starlark_pb.Transition{
				Kind: &model_starlark_pb.Transition_UserDefined_{
					UserDefined: &model_starlark_pb.Transition_UserDefined{
						Kind: &model_starlark_pb.Transition_UserDefined_Identifier{
							Identifier: td.identifier.String(),
						},
					},
				},
			},
		), nil
	}
	return model_core.Patch(options.ObjectCapturer, td.definition), nil
}

func (td *protoTransitionDefinition[TReference, TMetadata]) EncodeUserDefinedTransition(path map[starlark.Value]struct{}, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Transition_UserDefined, TMetadata], error) {
	switch t := td.definition.Message.Kind.(type) {
	case *model_starlark_pb.Transition_Target:
		return model_core.NewSimplePatchedMessage[TMetadata](
			(*model_starlark_pb.Transition_UserDefined)(nil),
		), nil
	case *model_starlark_pb.Transition_UserDefined_:
		if td.identifier != nil {
			return model_core.NewSimplePatchedMessage[TMetadata](
				&model_starlark_pb.Transition_UserDefined{
					Kind: &model_starlark_pb.Transition_UserDefined_Identifier{
						Identifier: td.identifier.String(),
					},
				},
			), nil
		}
		return model_core.Patch(options.ObjectCapturer, model_core.Nested(td.definition, t.UserDefined)), nil
	default:
		return model_core.PatchedMessage[*model_starlark_pb.Transition_UserDefined, TMetadata]{}, errors.New("transition is not a target or user-defined transition")
	}
}

func (td *protoTransitionDefinition[TReference, TMetadata]) EncodeValue(path map[starlark.Value]struct{}, currentIdentifier *pg_label.CanonicalStarlarkIdentifier, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], bool, error) {
	if td.identifier != nil && (currentIdentifier == nil || *currentIdentifier != *td.identifier) {
		return model_core.NewSimplePatchedMessage[TMetadata](
			&model_starlark_pb.Value{
				Kind: &model_starlark_pb.Value_Transition{
					Transition: &model_starlark_pb.Transition{
						Kind: &model_starlark_pb.Transition_UserDefined_{
							UserDefined: &model_starlark_pb.Transition_UserDefined{
								Kind: &model_starlark_pb.Transition_UserDefined_Identifier{
									Identifier: td.identifier.String(),
								},
							},
						},
					},
				},
			},
		), false, nil
	}

	return model_core.BuildPatchedMessage(
		func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_starlark_pb.Value {
			patchedDefinition := model_core.Patch(options.ObjectCapturer, td.definition)
			return &model_starlark_pb.Value{
				Kind: &model_starlark_pb.Value_Transition{
					Transition: patchedDefinition.Merge(patcher),
				},
			}
		},
	), false, nil
}

// Transitions that are used frequently (e.g., "exec", "target",
// config.none()).
var (
	DefaultExecGroupTransition = model_starlark_pb.Transition{
		Kind: &model_starlark_pb.Transition_ExecGroup{
			ExecGroup: "",
		},
	}
	NoneTransition = model_starlark_pb.Transition{
		Kind: &model_starlark_pb.Transition_None{
			None: &emptypb.Empty{},
		},
	}
	TargetTransition = model_starlark_pb.Transition{
		Kind: &model_starlark_pb.Transition_Target{
			Target: &emptypb.Empty{},
		},
	}
	UnconfiguredTransition = model_starlark_pb.Transition{
		Kind: &model_starlark_pb.Transition_Unconfigured{
			Unconfigured: &emptypb.Empty{},
		},
	}
)

type userDefinedTransitionDefinition[TReference any, TMetadata model_core.CloneableReferenceMetadata] struct {
	LateNamedValue

	implementation   NamedFunction[TReference, TMetadata]
	inputs           []string
	outputs          []string
	canonicalPackage pg_label.CanonicalPackage
}

// NewUserDefinedTransitionDefinition creates an object holding the
// properties of a new user defined transition, as normally done by
// exec_transition() or transition().
func NewUserDefinedTransitionDefinition[TReference any, TMetadata model_core.CloneableReferenceMetadata](identifier *pg_label.CanonicalStarlarkIdentifier, implementation NamedFunction[TReference, TMetadata], inputs, outputs []string, canonicalPackage pg_label.CanonicalPackage) TransitionDefinition[TReference, TMetadata] {
	return &userDefinedTransitionDefinition[TReference, TMetadata]{
		LateNamedValue: LateNamedValue{
			Identifier: identifier,
		},
		implementation:   implementation,
		inputs:           inputs,
		outputs:          outputs,
		canonicalPackage: canonicalPackage,
	}
}

func (td *userDefinedTransitionDefinition[TReference, TMetadata]) encodeUserDefinedTransition(path map[starlark.Value]struct{}, currentIdentifier *pg_label.CanonicalStarlarkIdentifier, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Transition_UserDefined, TMetadata], bool, error) {
	if td.Identifier != nil && (currentIdentifier == nil || *currentIdentifier != *td.Identifier) {
		// Not the canonical identifier under which this
		// transition is known. Emit a reference.
		return model_core.NewSimplePatchedMessage[TMetadata](
			&model_starlark_pb.Transition_UserDefined{
				Kind: &model_starlark_pb.Transition_UserDefined_Identifier{
					Identifier: td.Identifier.String(),
				},
			},
		), false, nil
	}

	implementation, needsCode, err := td.implementation.Encode(path, options)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Transition_UserDefined, TMetadata]{}, false, err
	}
	return model_core.NewPatchedMessage(
		&model_starlark_pb.Transition_UserDefined{
			Kind: &model_starlark_pb.Transition_UserDefined_Definition_{
				Definition: &model_starlark_pb.Transition_UserDefined_Definition{
					Implementation:   implementation.Message,
					Inputs:           td.inputs,
					Outputs:          td.outputs,
					CanonicalPackage: td.canonicalPackage.String(),
				},
			},
		},
		implementation.Patcher,
	), needsCode, nil
}

func (td *userDefinedTransitionDefinition[TReference, TMetadata]) Encode(path map[starlark.Value]struct{}, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Transition, TMetadata], error) {
	userDefined, _, err := td.encodeUserDefinedTransition(path, nil, options)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Transition, TMetadata]{}, err
	}
	return model_core.BuildPatchedMessage(
		func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_starlark_pb.Transition {
			return &model_starlark_pb.Transition{
				Kind: &model_starlark_pb.Transition_UserDefined_{
					UserDefined: userDefined.Merge(patcher),
				},
			}
		},
	), nil
}

func (td *userDefinedTransitionDefinition[TReference, TMetadata]) EncodeUserDefinedTransition(path map[starlark.Value]struct{}, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Transition_UserDefined, TMetadata], error) {
	userDefined, _, err := td.encodeUserDefinedTransition(path, nil, options)
	return userDefined, err
}

func (td *userDefinedTransitionDefinition[TReference, TMetadata]) EncodeValue(path map[starlark.Value]struct{}, currentIdentifier *pg_label.CanonicalStarlarkIdentifier, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], bool, error) {
	userDefined, needsCode, err := td.encodeUserDefinedTransition(path, currentIdentifier, options)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, false, err
	}
	return model_core.BuildPatchedMessage(
		func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) *model_starlark_pb.Value {
			return &model_starlark_pb.Value{
				Kind: &model_starlark_pb.Value_Transition{
					Transition: &model_starlark_pb.Transition{
						Kind: &model_starlark_pb.Transition_UserDefined_{
							UserDefined: userDefined.Merge(patcher),
						},
					},
				},
			}
		},
	), needsCode, nil
}

type transitionDefinitionUnpackerInto[TReference object.BasicReference, TMetadata model_core.CloneableReferenceMetadata] struct{}

// NewTransitionDefinitionUnpackerInto is capable of unpacking arguments
// to a Starlark function that are expected to refer to a configuration
// transition. These may either be user defined transitions, or strings
// referring to predefined transitions (i.e., "exec" or "target").
func NewTransitionDefinitionUnpackerInto[TReference object.BasicReference, TMetadata model_core.CloneableReferenceMetadata]() unpack.UnpackerInto[TransitionDefinition[TReference, TMetadata]] {
	return transitionDefinitionUnpackerInto[TReference, TMetadata]{}
}

func (transitionDefinitionUnpackerInto[TReference, TMetadata]) UnpackInto(thread *starlark.Thread, v starlark.Value, dst *TransitionDefinition[TReference, TMetadata]) error {
	switch typedV := v.(type) {
	case starlark.String:
		switch typedV {
		case "exec":
			*dst = NewProtoTransitionDefinition[TReference, TMetadata](
				model_core.NewSimpleMessage[TReference](&DefaultExecGroupTransition),
			)
		case "target":
			*dst = NewProtoTransitionDefinition[TReference, TMetadata](
				model_core.NewSimpleMessage[TReference](&TargetTransition),
			)
		default:
			return fmt.Errorf("got %#v, want \"exec\" or \"target\"", typedV)
		}
		return nil
	case *Transition[TReference, TMetadata]:
		*dst = typedV.TransitionDefinition
		return nil
	default:
		return fmt.Errorf("got %s, want transition or str", v.Type())
	}
}

func (ui transitionDefinitionUnpackerInto[TReference, TMetadata]) Canonicalize(thread *starlark.Thread, v starlark.Value) (starlark.Value, error) {
	var td TransitionDefinition[TReference, TMetadata]
	if err := ui.UnpackInto(thread, v, &td); err != nil {
		return nil, err
	}
	return NewTransition[TReference, TMetadata](td), nil
}

func (transitionDefinitionUnpackerInto[TReference, TMetadata]) GetConcatenationOperator() syntax.Token {
	return 0
}
