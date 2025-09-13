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
	"go.starlark.net/syntax"
)

// SelectGroup contains all of the conditions provided to a single call
// to select(). Each Starlark select() object contains one or more
// groups. Multiple groups occur if select() objects are combined using
// the + or | operator.
type SelectGroup struct {
	conditions   map[pg_label.ResolvedLabel]starlark.Value
	defaultValue starlark.Value
	noMatchError string
}

// NewSelectGroup creates a single group of conditions of a Starlark
// select() object. This function is called exactly once for each
// invocation to select().
func NewSelectGroup(conditions map[pg_label.ResolvedLabel]starlark.Value, defaultValue starlark.Value, noMatchError string) SelectGroup {
	return SelectGroup{
		conditions:   conditions,
		defaultValue: defaultValue,
		noMatchError: noMatchError,
	}
}

// Select is the type of the Starlark value that is returned by the
// select() function. These values can be used to make a value provided
// to a rule attribute configurable by letting the eventual value depend
// on whether conditions are satisfied.
//
// The select() function is not responsible for actually evaluating the
// conditions and selecting the eventual value. Instead, it merely
// records the conditions and their values.
//
// It is possible to combine multiple Starlark select() objects using
// the + or | operator. This also does not cause any conditions to be
// evaluated. Instead, it causes a new select() object to be returned,
// recording all of the conditions and the order in which they are
// supplied.
type Select[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	groups                []SelectGroup
	concatenationOperator syntax.Token
	frozen                bool
}

var (
	_ EncodableValue[object.LocalReference, model_core.ReferenceMetadata] = (*Select[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ HasLabels                                                           = (*Select[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ starlark.HasBinary                                                  = (*Select[object.LocalReference, model_core.ReferenceMetadata])(nil)
	_ starlark.Value                                                      = (*Select[object.LocalReference, model_core.ReferenceMetadata])(nil)
)

// NewSelect returns a new Starlark select() object that contains the
// provided groups of conditions.
func NewSelect[TReference any, TMetadata model_core.ReferenceMetadata](groups []SelectGroup, concatenationOperator syntax.Token) *Select[TReference, TMetadata] {
	return &Select[TReference, TMetadata]{
		groups:                groups,
		concatenationOperator: concatenationOperator,
	}
}

func (Select[TReference, TMetadata]) String() string {
	return "<select>"
}

// Type returns the type name of a Starlark select() object.
func (Select[TReference, TMetadata]) Type() string {
	return "select"
}

// Freeze a Starlark select() object, so that it can no longer be
// mutated.
//
// Even though Starlark select() objects themselves are immutable, they
// may contain conditions whose values are mutable. We therefore need to
// traverse over all condition values and freeze them.
func (s *Select[TReference, TMetadata]) Freeze() {
	if !s.frozen {
		s.frozen = true

		for _, group := range s.groups {
			for _, v := range group.conditions {
				v.Freeze()
			}
			if v := group.defaultValue; v != nil {
				v.Freeze()
			}
		}
	}
}

// Truth returns whether a Starlark select() object should be true or
// false when implicitly converted to a Boolean value. Starlark select()
// objects always evaluate to true.
func (Select[TReference, TMetadata]) Truth() starlark.Bool {
	return starlark.True
}

// Hash the Starlark select() object, so that it can be used as a key in
// a dictionary. For Starlark select() objects, this is not supported.
func (Select[TReference, TMetadata]) Hash(thread *starlark.Thread) (uint32, error) {
	return 0, errors.New("select cannot be hashed")
}

func (s *Select[TReference, TMetadata]) validateConcatenationOperator(op syntax.Token) error {
	if s.concatenationOperator != 0 && op != s.concatenationOperator {
		return fmt.Errorf("cannot perform select %s select %s select", s.concatenationOperator, op)
	}
	return nil
}

// Binary implements concatenation of two Starlark select() objects, or
// concatenation of a Starlark select() object with another value. In
// case of the latter, the value is converted to a select() group having
// only a default condition.
func (s *Select[TReference, TMetadata]) Binary(thread *starlark.Thread, op syntax.Token, y starlark.Value, side starlark.Side) (starlark.Value, error) {
	if op != syntax.PLUS && op != syntax.PIPE {
		return nil, errors.New("select only supports operators + and |")
	}
	if err := s.validateConcatenationOperator(op); err != nil {
		return nil, err
	}
	var newGroups []SelectGroup
	otherFrozen := false
	switch other := y.(type) {
	case *Select[TReference, TMetadata]:
		if err := other.validateConcatenationOperator(op); err != nil {
			return nil, err
		}
		if side == starlark.Left {
			newGroups = append(append([]SelectGroup(nil), s.groups...), other.groups...)
		} else {
			newGroups = append(append([]SelectGroup(nil), other.groups...), s.groups...)
		}
		otherFrozen = other.frozen
	default:
		newGroup := NewSelectGroup(nil, y, "")
		if side == starlark.Left {
			newGroups = append(append([]SelectGroup(nil), s.groups...), newGroup)
		} else {
			newGroups = append([]SelectGroup{newGroup}, s.groups...)
		}
	}
	return &Select[TReference, TMetadata]{
		groups:                newGroups,
		concatenationOperator: op,
		frozen:                s.frozen || otherFrozen,
	}, nil
}

// EncodeGroups encodes the groups contained within the Starlark
// select() object to a list of Protobuf messages.
//
// This method differs from EncodeValue() in that it only returns the
// groups. This is sufficient for cases where the value is known to be a
// select() object, such as during the computation of targets in a
// package.
func (s *Select[TReference, TMetadata]) EncodeGroups(path map[starlark.Value]struct{}, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[[]*model_starlark_pb.Select_Group, TMetadata], bool, error) {
	groups := make([]*model_starlark_pb.Select_Group, 0, len(s.groups))
	patcher := model_core.NewReferenceMessagePatcher[TMetadata]()
	needsCode := false

	for _, group := range s.groups {
		encodedGroup := model_starlark_pb.Select_Group{
			Conditions: make([]*model_starlark_pb.Select_Condition, 0, len(group.conditions)),
		}

		for _, condition := range slices.SortedFunc(
			maps.Keys(group.conditions),
			func(a, b pg_label.ResolvedLabel) int { return strings.Compare(a.String(), b.String()) },
		) {
			value, valueNeedsCode, err := EncodeValue[TReference, TMetadata](group.conditions[condition], path, nil, options)
			if err != nil {
				return model_core.PatchedMessage[[]*model_starlark_pb.Select_Group, TMetadata]{}, false, err
			}

			encodedGroup.Conditions = append(encodedGroup.Conditions, &model_starlark_pb.Select_Condition{
				ConditionIdentifier: condition.String(),
				Value:               value.Message,
			})
			patcher.Merge(value.Patcher)
			needsCode = needsCode || valueNeedsCode
		}

		if group.defaultValue != nil {
			value, valueNeedsCode, err := EncodeValue[TReference, TMetadata](group.defaultValue, path, nil, options)
			if err != nil {
				return model_core.PatchedMessage[[]*model_starlark_pb.Select_Group, TMetadata]{}, false, err
			}
			needsCode = needsCode || valueNeedsCode

			encodedGroup.NoMatch = &model_starlark_pb.Select_Group_NoMatchValue{
				NoMatchValue: value.Message,
			}
			patcher.Merge(value.Patcher)
		} else if group.noMatchError != "" {
			encodedGroup.NoMatch = &model_starlark_pb.Select_Group_NoMatchError{
				NoMatchError: group.noMatchError,
			}
		}

		groups = append(groups, &encodedGroup)
	}

	return model_core.NewPatchedMessage(groups, patcher), needsCode, nil
}

// EncodeValue encodes a Starlar select() object to a Starlark value
// Protobuf message. This allows the object to be written to storage and
// subsequently reloaded.
func (s *Select[TReference, TMetadata]) EncodeValue(path map[starlark.Value]struct{}, currentIdentifier *pg_label.CanonicalStarlarkIdentifier, options *ValueEncodingOptions[TReference, TMetadata]) (model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata], bool, error) {
	groups, needsCode, err := s.EncodeGroups(path, options)
	if err != nil {
		return model_core.PatchedMessage[*model_starlark_pb.Value, TMetadata]{}, false, err
	}

	concatenationOperator := model_starlark_pb.Select_NONE
	switch s.concatenationOperator {
	case syntax.PIPE:
		concatenationOperator = model_starlark_pb.Select_PIPE
	case syntax.PLUS:
		concatenationOperator = model_starlark_pb.Select_PLUS
	}

	return model_core.NewPatchedMessage(
		&model_starlark_pb.Value{
			Kind: &model_starlark_pb.Value_Select{
				Select: &model_starlark_pb.Select{
					Groups:                groups.Message,
					ConcatenationOperator: concatenationOperator,
				},
			},
		},
		groups.Patcher,
	), needsCode, nil
}

// VisitLabels visits all labels contained within the Starlark select()
// object. This includes identifiers of conditions and labels contained
// within condition values.
func (s *Select[TReference, TMetadata]) VisitLabels(thread *starlark.Thread, path map[starlark.Value]struct{}, visitor func(pg_label.ResolvedLabel) error) error {
	for _, sg := range s.groups {
		for conditionIdentifier, conditionValue := range sg.conditions {
			visitor(conditionIdentifier)
			if err := VisitLabels(thread, conditionValue, path, visitor); err != nil {
				return err
			}
		}
		if sg.defaultValue != nil {
			if err := VisitLabels(thread, sg.defaultValue, path, visitor); err != nil {
				return err
			}
		}
	}
	return nil
}

type selectUnpackerInto[TReference any, TMetadata model_core.ReferenceMetadata] struct {
	valueUnpackerInto unpack.Canonicalizer
}

// NewSelectUnpackerInto returns an unpacker for Starlark function
// arguments that is capable of unpacking Starlark select() objects.
//
// If the provided value is not a select() object, it is converted to a
// select() object that only has a single group, having a default
// condition corresponding to the provided value.
func NewSelectUnpackerInto[TReference any, TMetadata model_core.ReferenceMetadata](valueUnpackerInto unpack.Canonicalizer) unpack.UnpackerInto[*Select[TReference, TMetadata]] {
	return &selectUnpackerInto[TReference, TMetadata]{
		valueUnpackerInto: valueUnpackerInto,
	}
}

func (ui *selectUnpackerInto[TReference, TMetadata]) UnpackInto(thread *starlark.Thread, v starlark.Value, dst **Select[TReference, TMetadata]) error {
	switch typedV := v.(type) {
	case *Select[TReference, TMetadata]:
		if actual := typedV.concatenationOperator; actual != 0 {
			if expected := ui.valueUnpackerInto.GetConcatenationOperator(); actual != expected {
				if expected == 0 {
					return errors.New("values of this attribute type cannot be concatenated")
				}
				return fmt.Errorf("values of this attribute type need to be concatenated with %s, not %s", expected, actual)
			}
		}

		canonicalizedGroups := make([]SelectGroup, 0, len(typedV.groups))
		for _, group := range typedV.groups {
			canonicalizedConditions := make(map[pg_label.ResolvedLabel]starlark.Value, len(group.conditions))
			for condition, value := range group.conditions {
				canonicalValue, err := ui.valueUnpackerInto.Canonicalize(thread, value)
				if err != nil {
					return fmt.Errorf("canonicalizing condition %#v: %w", condition.String(), err)
				}
				canonicalizedConditions[condition] = canonicalValue
			}

			var defaultValue starlark.Value
			if group.defaultValue != nil {
				var err error
				defaultValue, err = ui.valueUnpackerInto.Canonicalize(thread, group.defaultValue)
				if err != nil {
					return fmt.Errorf("canonicalizing default value: %w", err)
				}
			}

			canonicalizedGroups = append(canonicalizedGroups, NewSelectGroup(canonicalizedConditions, defaultValue, group.noMatchError))
		}
		*dst = &Select[TReference, TMetadata]{
			groups:                canonicalizedGroups,
			concatenationOperator: typedV.concatenationOperator,
		}
	default:
		canonicalValue, err := ui.valueUnpackerInto.Canonicalize(thread, v)
		if err != nil {
			return err
		}
		*dst = &Select[TReference, TMetadata]{
			groups: []SelectGroup{NewSelectGroup(nil, canonicalValue, "")},
		}
	}
	return nil
}

func (ui *selectUnpackerInto[TReference, TMetadata]) Canonicalize(thread *starlark.Thread, v starlark.Value) (starlark.Value, error) {
	var s *Select[TReference, TMetadata]
	if err := ui.UnpackInto(thread, v, &s); err != nil {
		return nil, err
	}
	return s, nil
}

func (selectUnpackerInto[TReference, TMetadata]) GetConcatenationOperator() syntax.Token {
	return 0
}
