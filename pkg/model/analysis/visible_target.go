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
	model_starlark "bonanza.build/pkg/model/starlark"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_core_pb "bonanza.build/pkg/proto/model/core"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
	"bonanza.build/pkg/storage/object"

	"google.golang.org/protobuf/proto"
)

type getValueFromSelectGroupEnvironment[TReference any, TMetadata model_core.ReferenceMetadata] interface {
	model_core.ExistingObjectCapturer[TReference, TMetadata]

	SelectValueGetter[TReference, TMetadata]
}

func getValueFromSelectGroup[TReference object.BasicReference, TMetadata model_core.ReferenceMetadata](
	e getValueFromSelectGroupEnvironment[TReference, TMetadata],
	configurationReference model_core.Message[*model_core_pb.DecodableReference, TReference],
	fromPackage label.CanonicalPackage,
	selectGroup *model_starlark_pb.Select_Group,
	permitNoMatch bool,
) (*model_starlark_pb.Value, error) {
	if conditions := selectGroup.GetConditions(); len(conditions) > 0 {
		conditionIdentifiers := make([]string, 0, len(conditions))
		for _, condition := range conditions {
			conditionIdentifiers = append(conditionIdentifiers, condition.GetConditionIdentifier())
		}
		patchedConfigurationReference := model_core.Patch(e, configurationReference)
		selectValue := e.GetSelectValue(
			model_core.NewPatchedMessage(
				model_analysis_pb.Select_Key_builder{
					ConditionIdentifiers:   conditionIdentifiers,
					ConfigurationReference: patchedConfigurationReference.Message,
					FromPackage:            fromPackage.String(),
				}.Build(),
				patchedConfigurationReference.Patcher,
			),
		)
		if !selectValue.IsSet() {
			return nil, evaluation.ErrMissingDependency
		}
		if conditionIndices := selectValue.Message.GetConditionIndices(); len(conditionIndices) > 0 {
			firstIndex := conditionIndices[0]
			if firstIndex >= uint32(len(conditions)) {
				return nil, fmt.Errorf("condition index %d is out of bounds, as the select group only has %d conditions", firstIndex, len(conditions))
			}
			firstConditionValue := conditions[firstIndex].GetValue()

			// It is valid if multiple conditions match.
			// However, in that case the resulting values
			// must be identical.
			for _, additionalIndex := range conditionIndices {
				if additionalIndex >= uint32(len(conditions)) {
					return nil, fmt.Errorf("condition index %d is out of bounds, as the select group only has %d conditions", additionalIndex, len(conditions))
				}
				if !proto.Equal(firstConditionValue, conditions[additionalIndex].GetValue()) {
					return nil, fmt.Errorf("both conditions %#v and %#v match, but their resulting values differ", conditions[firstIndex].GetConditionIdentifier(), conditions[additionalIndex].GetConditionIdentifier())
				}
			}
			return firstConditionValue, nil
		}
	}

	switch selectGroup.WhichNoMatch() {
	case model_starlark_pb.Select_Group_NoMatchValue_case:
		return selectGroup.GetNoMatchValue(), nil
	case model_starlark_pb.Select_Group_NoMatchError_case:
		if permitNoMatch {
			return nil, nil
		}
		return nil, errors.New(selectGroup.GetNoMatchError())
	case 0:
		if permitNoMatch {
			return nil, nil
		}
		return nil, errors.New("none of the conditions matched, and no default condition or no-match error is specified")
	default:
		return nil, errors.New("select group does not contain a valid no-match behavior")
	}
}

func (c *baseComputer[TReference, TMetadata]) checkVisibility(ctx context.Context, e PackageGroupContainsValueGetter[TReference], valueReaders *model_starlark.ValueReaders[TReference], fromPackage label.CanonicalPackage, toLabel label.CanonicalLabel, toLabelVisibility model_core.Message[*model_starlark_pb.PackageGroup, TReference]) error {
	// Always permit access from within the same package.
	if fromPackage == toLabel.GetCanonicalPackage() {
		return nil
	}

	contains, err := c.packageGroupContains(ctx, e, valueReaders, toLabelVisibility, fromPackage)
	if err != nil {
		return err
	}
	if !contains {
		return fmt.Errorf("target %#v is not visible from package %#v", toLabel.String(), fromPackage.String())
	}
	return nil
}

// getRuleTargetVisibility returns the package group that controls
// which packages may reference a rule target.
func getRuleTargetVisibility[TReference any](ruleTargetLabel label.CanonicalLabel, ruleTarget model_core.Message[*model_starlark_pb.RuleTarget, TReference]) (model_core.Message[*model_starlark_pb.PackageGroup, TReference], bool, error) {
	inheritableAttrs := ruleTarget.Message.GetInheritableAttrs()
	if inheritableAttrs == nil {
		return model_core.Message[*model_starlark_pb.PackageGroup, TReference]{}, false, fmt.Errorf("rule target %#v has no inheritable attrs", ruleTargetLabel.String())
	}
	return model_core.Nested(ruleTarget, inheritableAttrs.GetVisibility()), true, nil
}

// getTargetVisibility returns the package group that controls which
// packages may reference a target. Aliases and label settings are not
// followed, meaning that the visibility of the target itself is
// returned. If the target is visible to all packages, false is
// returned.
func getTargetVisibility[TReference any](e TargetValueGetter[TReference], targetLabel label.CanonicalLabel, definition model_core.Message[*model_starlark_pb.Target_Definition, TReference]) (model_core.Message[*model_starlark_pb.PackageGroup, TReference], bool, error) {
	switch definition.Message.WhichKind() {
	case model_starlark_pb.Target_Definition_Alias_case:
		return model_core.Nested(definition, definition.Message.GetAlias().GetVisibility()), true, nil
	case model_starlark_pb.Target_Definition_LabelSetting_case:
		return model_core.Nested(definition, definition.Message.GetLabelSetting().GetVisibility()), true, nil
	case model_starlark_pb.Target_Definition_PackageGroup_case:
		// Package groups don't have a visibility of their own.
		// Any target is allowed to reference them.
		return model_core.Message[*model_starlark_pb.PackageGroup, TReference]{}, false, nil
	case model_starlark_pb.Target_Definition_PredeclaredOutputFileTarget_case:
		// The visibility of predeclared output files is
		// controlled by the rule target that owns them.
		ownerTargetNameStr := definition.Message.GetPredeclaredOutputFileTarget().GetOwnerTargetName()
		ownerTargetName, err := label.NewTargetName(ownerTargetNameStr)
		if err != nil {
			return model_core.Message[*model_starlark_pb.PackageGroup, TReference]{}, false, fmt.Errorf("invalid owner target name %#v: %w", ownerTargetNameStr, err)
		}
		ownerLabel := targetLabel.GetCanonicalPackage().AppendTargetName(ownerTargetName)
		ownerTargetValue := e.GetTargetValue(model_analysis_pb.Target_Key_builder{
			Label: ownerLabel.String(),
		}.Build())
		if !ownerTargetValue.IsSet() {
			return model_core.Message[*model_starlark_pb.PackageGroup, TReference]{}, false, evaluation.ErrMissingDependency
		}
		ownerDefinition := ownerTargetValue.Message.GetDefinition()
		if ownerDefinition == nil {
			return model_core.Message[*model_starlark_pb.PackageGroup, TReference]{}, false, fmt.Errorf("owner %#v does not exist", ownerLabel.String())
		}
		ownerRuleTarget := ownerDefinition.GetRuleTarget()
		if ownerRuleTarget == nil {
			return model_core.Message[*model_starlark_pb.PackageGroup, TReference]{}, false, fmt.Errorf("owner %#v is not a rule target", ownerLabel.String())
		}
		return getRuleTargetVisibility(ownerLabel, model_core.Nested(ownerTargetValue, ownerRuleTarget))
	case model_starlark_pb.Target_Definition_RuleTarget_case:
		return getRuleTargetVisibility(targetLabel, model_core.Nested(definition, definition.Message.GetRuleTarget()))
	case model_starlark_pb.Target_Definition_SourceFileTarget_case:
		return model_core.Nested(definition, definition.Message.GetSourceFileTarget().GetVisibility()), true, nil
	case model_starlark_pb.Target_Definition_TestSuite_case:
		return model_core.Nested(definition, definition.Message.GetTestSuite().GetVisibility()), true, nil
	default:
		return model_core.Message[*model_starlark_pb.PackageGroup, TReference]{}, false, errors.New("invalid target type")
	}
}

func (c *baseComputer[TReference, TMetadata]) ComputeVisibleTargetValue(ctx context.Context, key model_core.Message[*model_analysis_pb.VisibleTarget_Key, TReference], e VisibleTargetEnvironment[TReference, TMetadata]) (PatchedVisibleTargetValue[TMetadata], error) {
	analysisReaders, gotAnalysisReaders := getAnalysisReaders(e)
	valueReaders, gotValueReaders := getStarlarkValueReaders(e)

	fromPackage, err := label.NewCanonicalPackage(key.Message.GetFromPackage())
	if err != nil {
		return PatchedVisibleTargetValue[TMetadata]{}, fmt.Errorf("invalid from package: %w", err)
	}
	toLabel, err := label.NewCanonicalLabel(key.Message.GetToLabel())
	if err != nil {
		return PatchedVisibleTargetValue[TMetadata]{}, fmt.Errorf("invalid to label: %w", err)
	}

	targetValue := e.GetTargetValue(model_analysis_pb.Target_Key_builder{
		Label: key.Message.GetToLabel(),
	}.Build())
	if !targetValue.IsSet() || !gotAnalysisReaders || !gotValueReaders {
		return PatchedVisibleTargetValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	targetValueDefinition := targetValue.Message.GetDefinition()
	if targetValueDefinition == nil {
		return PatchedVisibleTargetValue[TMetadata]{}, fmt.Errorf("target %#v does not exist", key.Message.GetToLabel())
	}

	configurationReference := model_core.Nested(key, key.Message.GetConfigurationReference())

	switch targetValueDefinition.WhichKind() {
	case model_starlark_pb.Target_Definition_Alias_case:
		if err := c.checkVisibility(
			ctx,
			e,
			valueReaders,
			fromPackage,
			toLabel,
			model_core.Nested(targetValue, targetValueDefinition.GetAlias().GetVisibility()),
		); err != nil {
			return PatchedVisibleTargetValue[TMetadata]{}, err
		}

		// If the actual target is a select(), evaluate it.
		actualSelectGroup := targetValueDefinition.GetAlias().GetActual()
		if actualSelectGroup == nil {
			return PatchedVisibleTargetValue[TMetadata]{}, errors.New("alias has no actual target")
		}
		actualValue, err := getValueFromSelectGroup(
			e,
			configurationReference,
			toLabel.GetCanonicalPackage(),
			actualSelectGroup,
			key.Message.GetPermitAliasNoMatch(),
		)
		if err != nil {
			return PatchedVisibleTargetValue[TMetadata]{}, err
		}
		if actualValue == nil {
			// None of the conditions match, and the caller
			// is fine with that.
			return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.VisibleTarget_Value{}), nil
		}
		switch actualValue.WhichKind() {
		case model_starlark_pb.Value_Label_case:
			actualLabel, err := label.NewResolvedLabel(actualValue.GetLabel())
			if err != nil {
				return PatchedVisibleTargetValue[TMetadata]{}, fmt.Errorf("invalid label %#v: %w", actualValue.GetLabel(), err)
			}
			actualCanonicalLabel, err := actualLabel.AsCanonical()
			if err != nil {
				return PatchedVisibleTargetValue[TMetadata]{}, err
			}

			// The actual target may also be an alias.
			patchedConfigurationReference := model_core.Patch(e, configurationReference)
			actualVisibleTargetValue := e.GetVisibleTargetValue(
				model_core.NewPatchedMessage(
					model_analysis_pb.VisibleTarget_Key_builder{
						FromPackage:            toLabel.GetCanonicalPackage().String(),
						ToLabel:                actualCanonicalLabel.String(),
						PermitAliasNoMatch:     key.Message.GetPermitAliasNoMatch(),
						StopAtLabelSetting:     key.Message.GetStopAtLabelSetting(),
						ConfigurationReference: patchedConfigurationReference.Message,
					}.Build(),
					patchedConfigurationReference.Patcher,
				),
			)
			if !actualVisibleTargetValue.IsSet() {
				return PatchedVisibleTargetValue[TMetadata]{}, evaluation.ErrMissingDependency
			}
			return model_core.NewSimplePatchedMessage[TMetadata](actualVisibleTargetValue.Message), nil
		case model_starlark_pb.Value_None_case:
			// This implementation allows alias(actual = None).
			// This extension is necessary to support
			// configuration_field("coverage", "output_generator")
			// which may return None if --collect_code_coverage
			// is not enabled.
			return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.VisibleTarget_Value{}), nil
		default:
			return PatchedVisibleTargetValue[TMetadata]{}, errors.New("actual target of alias is not a label")
		}
	case model_starlark_pb.Target_Definition_LabelSetting_case:
		// The label setting itself needs to be visible.
		if err := c.checkVisibility(
			ctx,
			e,
			valueReaders,
			fromPackage,
			toLabel,
			model_core.Nested(targetValue, targetValueDefinition.GetLabelSetting().GetVisibility()),
		); err != nil {
			return PatchedVisibleTargetValue[TMetadata]{}, err
		}

		if key.Message.GetStopAtLabelSetting() {
			// We are applying a transition and want to
			// resolve the label of a label_setting().
			return model_core.NewSimplePatchedMessage[TMetadata](model_analysis_pb.VisibleTarget_Value_builder{
				Label: toLabel.String(),
			}.Build()), nil
		}

		// Determine if there is an override in place for this
		// label setting.
		toLabelStr := toLabel.String()
		override, err := btree.Find(
			ctx,
			analysisReaders.BuildSettingOverride,
			getBuildSettingOverridesFromReference(model_core.Nested(key, key.Message.GetConfigurationReference())),
			func(entry model_core.Message[*model_analysis_pb.BuildSettingOverride, TReference]) (int, *model_core_pb.DecodableReference) {
				switch entry.Message.WhichLevel() {
				case model_analysis_pb.BuildSettingOverride_Leaf_case:
					return strings.Compare(toLabelStr, entry.Message.GetLeaf().GetLabel()), nil
				case model_analysis_pb.BuildSettingOverride_Parent_case:
					return strings.Compare(toLabelStr, entry.Message.GetParent().GetFirstLabel()), entry.Message.GetParent().GetReference()
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
			leaf := override.Message.GetLeaf()
			if leaf == nil {
				return PatchedVisibleTargetValue[TMetadata]{}, errors.New("build setting override is not a valid leaf")
			}
			value := leaf.GetValue()
			if listValue := value.GetList(); listValue != nil {
				elements := listValue.GetElements()
				if len(elements) != 1 {
					return PatchedVisibleTargetValue[TMetadata]{}, errors.New("build setting override value is not a single element list")
				}
				listLeaf := elements[0].GetLeaf()
				if listLeaf == nil {
					return PatchedVisibleTargetValue[TMetadata]{}, errors.New("build setting override value is not a list")
				}
				value = listLeaf
			}
			switch value.WhichKind() {
			case model_starlark_pb.Value_Label_case:
				overrideLabel, err := label.NewResolvedLabel(value.GetLabel())
				if err != nil {
					return PatchedVisibleTargetValue[TMetadata]{}, fmt.Errorf("invalid build setting override label value %#v: %w", value.GetLabel(), err)
				}
				canonicalOverrideLabel, err := overrideLabel.AsCanonical()
				if err != nil {
					return PatchedVisibleTargetValue[TMetadata]{}, err
				}
				nextFromPackage = canonicalOverrideLabel.GetCanonicalPackage().String()
				nextToLabel = overrideLabel.String()
			case model_starlark_pb.Value_None_case:
				return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.VisibleTarget_Value{}), nil
			default:
				return PatchedVisibleTargetValue[TMetadata]{}, errors.New("build setting override value is not a label")
			}
		} else {
			// Use the default target associated with the
			// label setting. Validate that the default
			// target is visible from the label setting.
			nextFromPackage = toLabel.GetCanonicalPackage().String()
			nextToLabel = targetValueDefinition.GetLabelSetting().GetBuildSettingDefault()
			if nextToLabel == "" {
				// Label setting defaults to None.
				return model_core.NewSimplePatchedMessage[TMetadata](&model_analysis_pb.VisibleTarget_Value{}), nil
			}
		}

		patchedConfigurationReference := model_core.Patch(e, configurationReference)
		actualVisibleTargetValue := e.GetVisibleTargetValue(
			model_core.NewPatchedMessage(
				model_analysis_pb.VisibleTarget_Key_builder{
					FromPackage:            nextFromPackage,
					ToLabel:                nextToLabel,
					PermitAliasNoMatch:     key.Message.GetPermitAliasNoMatch(),
					ConfigurationReference: patchedConfigurationReference.Message,
				}.Build(),
				patchedConfigurationReference.Patcher,
			),
		)
		if !actualVisibleTargetValue.IsSet() {
			return PatchedVisibleTargetValue[TMetadata]{}, evaluation.ErrMissingDependency
		}
		return model_core.NewSimplePatchedMessage[TMetadata](actualVisibleTargetValue.Message), nil
	default:
		// Found the definitive target.
		visibility, restricted, err := getTargetVisibility(e, toLabel, model_core.Nested(targetValue, targetValueDefinition))
		if err != nil {
			return PatchedVisibleTargetValue[TMetadata]{}, err
		}
		if restricted {
			if err := c.checkVisibility(ctx, e, valueReaders, fromPackage, toLabel, visibility); err != nil {
				return PatchedVisibleTargetValue[TMetadata]{}, err
			}
		}
		return model_core.NewSimplePatchedMessage[TMetadata](model_analysis_pb.VisibleTarget_Value_builder{
			Label: toLabel.String(),
		}.Build()), nil
	}
}
