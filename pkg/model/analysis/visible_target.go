package analysis

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/core/btree"
	"bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/storage/object"

	"google.golang.org/protobuf/proto"
)

type getValueFromSelectGroupEnvironment[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	model_core.ExistingObjectCapturer[TReference, TMetadata]

	GetSelectValue(model_core.PatchedMessage[*model_analysis_pb.Select_Key, TMetadata]) model_core.Message[*model_analysis_pb.Select_Value, TReference]
}

func getValueFromSelectGroup[TReference object.BasicReference, TMetadata model_core.WalkableReferenceMetadata](
	e getValueFromSelectGroupEnvironment[TReference, TMetadata],
	configurationReference model_core.Message[*model_core_pb.DecodableReference, TReference],
	fromPackage label.CanonicalPackage,
	selectGroup *model_starlark_pb.Select_Group,
	permitNoMatch bool,
) (*model_starlark_pb.Value, error) {
	if conditions := selectGroup.Conditions; len(conditions) > 0 {
		conditionIdentifiers := make([]string, 0, len(conditions))
		for _, condition := range conditions {
			conditionIdentifiers = append(conditionIdentifiers, condition.ConditionIdentifier)
		}
		patchedConfigurationReference := model_core.Patch(e, configurationReference)
		selectValue := e.GetSelectValue(
			model_core.NewPatchedMessage(
				&model_analysis_pb.Select_Key{
					ConditionIdentifiers:   conditionIdentifiers,
					ConfigurationReference: patchedConfigurationReference.Message,
					FromPackage:            fromPackage.String(),
				},
				patchedConfigurationReference.Patcher,
			),
		)
		if !selectValue.IsSet() {
			return nil, evaluation.ErrMissingDependency
		}
		if len(selectValue.Message.ConditionIndices) > 0 {
			firstIndex := selectValue.Message.ConditionIndices[0]
			if firstIndex >= uint32(len(conditions)) {
				return nil, fmt.Errorf("condition index %d is out of bounds, as the select group only has %d conditions", firstIndex, len(conditions))
			}
			firstConditionValue := conditions[firstIndex].Value

			// It is valid if multiple conditions match.
			// However, in that case the resulting values
			// must be identical.
			for _, additionalIndex := range selectValue.Message.ConditionIndices {
				if additionalIndex >= uint32(len(conditions)) {
					return nil, fmt.Errorf("condition index %d is out of bounds, as the select group only has %d conditions", additionalIndex, len(conditions))
				}
				if !proto.Equal(firstConditionValue, conditions[additionalIndex].Value) {
					return nil, fmt.Errorf("both conditions %#v and %#v match, but their resulting values differ", conditions[firstIndex].ConditionIdentifier, conditions[additionalIndex].ConditionIdentifier)
				}
			}
			return firstConditionValue, nil
		}
	}

	switch noMatch := selectGroup.NoMatch.(type) {
	case *model_starlark_pb.Select_Group_NoMatchValue:
		return noMatch.NoMatchValue, nil
	case *model_starlark_pb.Select_Group_NoMatchError:
		if permitNoMatch {
			return nil, nil
		}
		return nil, errors.New(noMatch.NoMatchError)
	case nil:
		if permitNoMatch {
			return nil, nil
		}
		return nil, errors.New("none of the conditions matched, and no default condition or no-match error is specified")
	default:
		return nil, errors.New("select group does not contain a valid no-match behavior")
	}
}

func checkVisibility[TReference any](e packageGroupContainsEnvironment[TReference], fromPackage label.CanonicalPackage, toLabel label.CanonicalLabel, toLabelVisibility model_core.Message[*model_starlark_pb.PackageGroup, TReference]) error {
	// Always permit access from within the same package.
	if fromPackage == toLabel.GetCanonicalPackage() {
		return nil
	}

	contains, err := packageGroupContains(e, toLabelVisibility, fromPackage)
	if err != nil {
		return err
	}
	if !contains {
		return fmt.Errorf("target %#v is not visible from package %#v", toLabel.String(), fromPackage.String())
	}
	return nil
}

func checkRuleTargetVisibility[TReference any](e packageGroupContainsEnvironment[TReference], fromPackage label.CanonicalPackage, ruleTargetLabel label.CanonicalLabel, ruleTarget model_core.Message[*model_starlark_pb.RuleTarget, TReference]) error {
	inheritableAttrs := ruleTarget.Message.InheritableAttrs
	if inheritableAttrs == nil {
		return fmt.Errorf("rule target %#v has no inheritable attrs", ruleTargetLabel)
	}
	return checkVisibility(
		e,
		fromPackage,
		ruleTargetLabel,
		model_core.Nested(ruleTarget, inheritableAttrs.Visibility),
	)
}

func (c *baseComputer[TReference, TMetadata]) ComputeVisibleTargetValue(ctx context.Context, key model_core.Message[*model_analysis_pb.VisibleTarget_Key, TReference], e VisibleTargetEnvironment[TReference, TMetadata]) (PatchedVisibleTargetValue[TMetadata], error) {
	fromPackage, err := label.NewCanonicalPackage(key.Message.FromPackage)
	if err != nil {
		return PatchedVisibleTargetValue[TMetadata]{}, fmt.Errorf("invalid from package: %w", err)
	}
	toLabel, err := label.NewCanonicalLabel(key.Message.ToLabel)
	if err != nil {
		return PatchedVisibleTargetValue[TMetadata]{}, fmt.Errorf("invalid to label: %w", err)
	}

	targetValue := e.GetTargetValue(&model_analysis_pb.Target_Key{
		Label: key.Message.ToLabel,
	})
	if !targetValue.IsSet() {
		return PatchedVisibleTargetValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	configurationReference := model_core.Nested(key, key.Message.ConfigurationReference)

	switch definition := targetValue.Message.Definition.GetKind().(type) {
	case *model_starlark_pb.Target_Definition_Alias:
		if err := checkVisibility(
			e,
			fromPackage,
			toLabel,
			model_core.Nested(targetValue, definition.Alias.Visibility),
		); err != nil {
			return PatchedVisibleTargetValue[TMetadata]{}, err
		}

		// If the actual target is a select(), evaluate it.
		actualSelectGroup := definition.Alias.Actual
		if actualSelectGroup == nil {
			return PatchedVisibleTargetValue[TMetadata]{}, errors.New("alias has no actual target")
		}
		actualValue, err := getValueFromSelectGroup(
			e,
			configurationReference,
			toLabel.GetCanonicalPackage(),
			actualSelectGroup,
			key.Message.PermitAliasNoMatch,
		)
		if err != nil {
			return PatchedVisibleTargetValue[TMetadata]{}, err
		}
		if actualValue == nil {
			// None of the conditions match, and the caller
			// is fine with that.
			return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.VisibleTarget_Value{}), nil
		}
		switch actualValueKind := actualValue.Kind.(type) {
		case *model_starlark_pb.Value_Label:
			actualLabel, err := label.NewResolvedLabel(actualValueKind.Label)
			if err != nil {
				return PatchedVisibleTargetValue[TMetadata]{}, fmt.Errorf("invalid label %#v: %w", actualValueKind.Label, err)
			}
			actualCanonicalLabel, err := actualLabel.AsCanonical()
			if err != nil {
				return PatchedVisibleTargetValue[TMetadata]{}, err
			}

			// The actual target may also be an alias.
			patchedConfigurationReference := model_core.Patch(e, configurationReference)
			actualVisibleTargetValue := e.GetVisibleTargetValue(
				model_core.NewPatchedMessage(
					&model_analysis_pb.VisibleTarget_Key{
						FromPackage:            toLabel.GetCanonicalPackage().String(),
						ToLabel:                actualCanonicalLabel.String(),
						PermitAliasNoMatch:     key.Message.PermitAliasNoMatch,
						StopAtLabelSetting:     key.Message.StopAtLabelSetting,
						ConfigurationReference: patchedConfigurationReference.Message,
					},
					patchedConfigurationReference.Patcher,
				),
			)
			if !actualVisibleTargetValue.IsSet() {
				return PatchedVisibleTargetValue[TMetadata]{}, evaluation.ErrMissingDependency
			}
			return model_core.NewSimplePatchedMessage[TMetadata](actualVisibleTargetValue.Message), nil
		case *model_starlark_pb.Value_None:
			// This implementation allows alias(actual = None).
			// This extension is necessary to support
			// configuration_field("coverage", "output_generator")
			// which may return None if --collect_code_coverage
			// is not enabled.
			return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.VisibleTarget_Value{}), nil
		default:
			return PatchedVisibleTargetValue[TMetadata]{}, errors.New("actual target of alias is not a label")
		}
	case *model_starlark_pb.Target_Definition_LabelSetting:
		if key.Message.StopAtLabelSetting {
			// We are applying a transition and want to
			// resolve the label of a label_setting().
			return model_core.NewSimplePatchedMessage[TMetadata](
				&model_analysis_pb.VisibleTarget_Value{
					Label: toLabel.String(),
				},
			), nil
		}

		// Determine if there is an override in place for this
		// label setting.
		toLabelStr := toLabel.String()
		override, err := btree.Find(
			ctx,
			c.buildSettingOverrideReader,
			getBuildSettingOverridesFromReference(model_core.Nested(key, key.Message.ConfigurationReference)),
			func(entry model_core.Message[*model_analysis_pb.BuildSettingOverride, TReference]) (int, *model_core_pb.DecodableReference) {
				switch level := entry.Message.Level.(type) {
				case *model_analysis_pb.BuildSettingOverride_Leaf_:
					return strings.Compare(toLabelStr, level.Leaf.Label), nil
				case *model_analysis_pb.BuildSettingOverride_Parent_:
					return strings.Compare(toLabelStr, level.Parent.FirstLabel), level.Parent.Reference
				default:
					return 0, nil
				}
			},
		)
		if err != nil {
			return PatchedVisibleTargetValue[TMetadata]{}, err
		}

		var nextFromPackage string
		var nextToLabel string
		if override.IsSet() {
			// An override is in place. Use the label value
			// associated with the override. Disable
			// visibility checking, as the user is free to
			// specify a target that is not visible from the
			// label setting's perspective.
			leaf, ok := override.Message.Level.(*model_analysis_pb.BuildSettingOverride_Leaf_)
			if !ok {
				return PatchedVisibleTargetValue[TMetadata]{}, errors.New("build setting override is not a valid leaf")
			}
			value := leaf.Leaf.Value
			if listValue, ok := value.GetKind().(*model_starlark_pb.Value_List); ok {
				elements := listValue.List.Elements
				if len(elements) != 1 {
					return PatchedVisibleTargetValue[TMetadata]{}, errors.New("build setting override value is not a single element list")
				}
				listLeaf, ok := elements[0].Level.(*model_starlark_pb.List_Element_Leaf)
				if !ok {
					return PatchedVisibleTargetValue[TMetadata]{}, errors.New("build setting override value is not a list")
				}
				value = listLeaf.Leaf
			}
			switch labelValue := value.GetKind().(type) {
			case *model_starlark_pb.Value_Label:
				overrideLabel, err := label.NewResolvedLabel(labelValue.Label)
				if err != nil {
					return PatchedVisibleTargetValue[TMetadata]{}, fmt.Errorf("invalid build setting override label value %#v: %w", labelValue.Label, err)
				}
				canonicalOverrideLabel, err := overrideLabel.AsCanonical()
				if err != nil {
					return PatchedVisibleTargetValue[TMetadata]{}, err
				}
				nextFromPackage = canonicalOverrideLabel.GetCanonicalPackage().String()
				nextToLabel = overrideLabel.String()
			case *model_starlark_pb.Value_None:
				return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.VisibleTarget_Value{}), nil
			default:
				return PatchedVisibleTargetValue[TMetadata]{}, errors.New("build setting override value is not a label")
			}
		} else {
			// Use the default target associated with the
			// label setting. Validate that the default
			// target is visible from the label setting.
			nextFromPackage = toLabel.GetCanonicalPackage().String()
			nextToLabel = definition.LabelSetting.BuildSettingDefault
			if nextToLabel == "" {
				// Label setting defaults to None.
				return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.VisibleTarget_Value{}), nil
			}
		}

		patchedConfigurationReference := model_core.Patch(e, configurationReference)
		actualVisibleTargetValue := e.GetVisibleTargetValue(
			model_core.NewPatchedMessage(
				&model_analysis_pb.VisibleTarget_Key{
					FromPackage:            nextFromPackage,
					ToLabel:                nextToLabel,
					PermitAliasNoMatch:     key.Message.PermitAliasNoMatch,
					ConfigurationReference: patchedConfigurationReference.Message,
				},
				patchedConfigurationReference.Patcher,
			),
		)
		if !actualVisibleTargetValue.IsSet() {
			return PatchedVisibleTargetValue[TMetadata]{}, evaluation.ErrMissingDependency
		}
		return model_core.NewSimplePatchedMessage[TMetadata](actualVisibleTargetValue.Message), nil
	case *model_starlark_pb.Target_Definition_PackageGroup:
		// Package groups don't have a visibility of their own.
		// Any target is allowed to reference them.
		return model_core.NewSimplePatchedMessage[TMetadata](
			&model_analysis_pb.VisibleTarget_Value{
				Label: toLabel.String(),
			},
		), nil
	case *model_starlark_pb.Target_Definition_PredeclaredOutputFileTarget:
		// The visibility of predeclared output files is
		// controlled by the rule target that owns them.
		ownerTargetNameStr := definition.PredeclaredOutputFileTarget.OwnerTargetName
		ownerTargetName, err := label.NewTargetName(ownerTargetNameStr)
		if err != nil {
			return PatchedVisibleTargetValue[TMetadata]{}, fmt.Errorf("invalid owner target name %#v: %w", ownerTargetNameStr, err)
		}

		ownerLabel := toLabel.GetCanonicalPackage().AppendTargetName(ownerTargetName)
		ownerLabelStr := ownerLabel.String()
		ownerTargetValue := e.GetTargetValue(&model_analysis_pb.Target_Key{
			Label: ownerLabelStr,
		})
		if !ownerTargetValue.IsSet() {
			return PatchedVisibleTargetValue[TMetadata]{}, evaluation.ErrMissingDependency
		}
		ruleDefinition, ok := ownerTargetValue.Message.Definition.GetKind().(*model_starlark_pb.Target_Definition_RuleTarget)
		if !ok {
			return PatchedVisibleTargetValue[TMetadata]{}, fmt.Errorf("owner %#v is not a rule target", ownerLabelStr)
		}
		if err := checkRuleTargetVisibility(
			e,
			fromPackage,
			ownerLabel,
			model_core.Nested(ownerTargetValue, ruleDefinition.RuleTarget),
		); err != nil {
			return PatchedVisibleTargetValue[TMetadata]{}, err
		}

		// Found the definitive target.
		return model_core.NewSimplePatchedMessage[TMetadata](
			&model_analysis_pb.VisibleTarget_Value{
				Label: toLabel.String(),
			},
		), nil
	case *model_starlark_pb.Target_Definition_RuleTarget:
		if err := checkRuleTargetVisibility(
			e,
			fromPackage,
			toLabel,
			model_core.Nested(targetValue, definition.RuleTarget),
		); err != nil {
			return PatchedVisibleTargetValue[TMetadata]{}, err
		}

		// Found the definitive target.
		return model_core.NewSimplePatchedMessage[TMetadata](
			&model_analysis_pb.VisibleTarget_Value{
				Label: toLabel.String(),
			},
		), nil
	case *model_starlark_pb.Target_Definition_SourceFileTarget:
		if err := checkVisibility(
			e,
			fromPackage,
			toLabel,
			model_core.Nested(targetValue, definition.SourceFileTarget.Visibility),
		); err != nil {
			return PatchedVisibleTargetValue[TMetadata]{}, err
		}

		// Found the definitive target.
		return model_core.NewSimplePatchedMessage[TMetadata](
			&model_analysis_pb.VisibleTarget_Value{
				Label: toLabel.String(),
			},
		), nil
	default:
		return PatchedVisibleTargetValue[TMetadata]{}, errors.New("invalid target type")
	}
}
