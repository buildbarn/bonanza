package analysis

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
)

// getAspectDefinition looks up the definition of an aspect, given its
// Starlark identifier.
func getAspectDefinition[TReference any](e CompiledBzlFileGlobalValueGetter[TReference], aspectIdentifier label.CanonicalStarlarkIdentifier) (model_core.Message[*model_starlark_pb.Aspect_Definition, TReference], error) {
	aspectIdentifierStr := aspectIdentifier.String()
	aspectValue := e.GetCompiledBzlFileGlobalValue(model_analysis_pb.CompiledBzlFileGlobal_Key_builder{
		Identifier: aspectIdentifierStr,
	}.Build())
	if !aspectValue.IsSet() {
		return model_core.Message[*model_starlark_pb.Aspect_Definition, TReference]{}, evaluation.ErrMissingDependency
	}
	v := aspectValue.Message.GetGlobal().GetAspect()
	if v == nil {
		return model_core.Message[*model_starlark_pb.Aspect_Definition, TReference]{}, fmt.Errorf("%#v is not an aspect", aspectIdentifierStr)
	}
	d := v.GetDefinition()
	if d == nil {
		return model_core.Message[*model_starlark_pb.Aspect_Definition, TReference]{}, fmt.Errorf("%#v is not an aspect definition", aspectIdentifierStr)
	}
	return model_core.Nested(aspectValue, d), nil
}

func (*baseComputer[TReference, TMetadata]) ComputeConfiguredAspectValue(ctx context.Context, key model_core.Message[*model_analysis_pb.ConfiguredAspect_Key, TReference], e ConfiguredAspectEnvironment[TReference, TMetadata]) (PatchedConfiguredAspectValue[TMetadata], error) {
	aspectIdentifier, err := label.NewCanonicalStarlarkIdentifier(key.Message.GetIdentifier())
	if err != nil {
		return PatchedConfiguredAspectValue[TMetadata]{}, fmt.Errorf("invalid aspect identifier: %w", err)
	}
	starlarkValueEncoders, gotStarlarkValueEncoders := getStarlarkValueEncoders(e)
	aspectDefinition, err := getAspectDefinition(e, aspectIdentifier)
	if err != nil {
		return PatchedConfiguredAspectValue[TMetadata]{}, err
	}
	if !gotStarlarkValueEncoders {
		return PatchedConfiguredAspectValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	valueReaders := &starlarkValueEncoders.Readers
	configurationReference := model_core.Nested(key, key.Message.GetConfigurationReference())

	// Resolve the toolchains and execution platforms of all exec
	// groups, and obtain the definitions of all subrules. Labels
	// are resolved relative to the package declaring the aspect.
	aspectPackage := aspectIdentifier.GetCanonicalLabel().GetCanonicalPackage()
	execGroups, execGroupPlatformLabels, execGroupsErr := configureExecGroups(
		ctx,
		e,
		valueReaders,
		aspectPackage,
		aspectDefinition.Message.GetExecGroups(),
		configurationReference,
	)
	subruleDefinitions, subrulesErr := getSubruleDefinitions(e, aspectDefinition.Message.GetSubruleIdentifiers())
	for _, err := range []error{execGroupsErr, subrulesErr} {
		if err != nil && !errors.Is(err, evaluation.ErrMissingDependency) {
			return PatchedConfiguredAspectValue[TMetadata]{}, err
		}
	}
	if execGroupsErr != nil || subrulesErr != nil {
		return PatchedConfiguredAspectValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	return model_core.BuildPatchedMessage(func(patcher *model_core.ReferenceMessagePatcher[TMetadata]) (*model_analysis_pb.ConfiguredAspect_Value, error) {
		missingDependencies := false
		subrules, err := configureSubrules(
			ctx,
			e,
			valueReaders,
			subruleDefinitions,
			configurationReference,
			execGroupPlatformLabels,
			patcher,
		)
		if err != nil {
			if !errors.Is(err, evaluation.ErrMissingDependency) {
				return nil, err
			}
			missingDependencies = true
		}

		// Configure all label attrs of the aspect. As public
		// attrs of aspects cannot be labels, these are always
		// private. Private attrs always use their default value
		// and cannot use user-defined transitions, meaning that
		// the labels contained in them can be resolved here.
		var labelAttrs []*model_analysis_pb.IndexedConfiguredLabelAttr
		for attrIndex, namedAttr := range aspectDefinition.Message.GetAttrs() {
			labelOptions, _, isLabelAttr := getLabelAttrOptions(namedAttr.GetAttr())
			if !isLabelAttr {
				continue
			}
			if labelOptions == nil {
				return nil, fmt.Errorf("attr %#v does not have label options", namedAttr.GetName())
			}
			if !strings.HasPrefix(namedAttr.GetName(), "_") {
				return nil, fmt.Errorf("attr %#v is a public label attr, which aspects cannot have", namedAttr.GetName())
			}
			defaultValue := namedAttr.GetAttr().GetDefault()
			if defaultValue == nil {
				return nil, fmt.Errorf("attr %#v does not have a default value", namedAttr.GetName())
			}
			configuredAttr, err := configureBuiltinTransitionLabelAttr(
				ctx,
				e,
				valueReaders,
				model_core.Nested(aspectDefinition, labelOptions.GetCfg()),
				model_core.Nested(aspectDefinition, defaultValue),
				aspectPackage,
				configurationReference,
				execGroupPlatformLabels,
				patcher,
			)
			if err != nil {
				if errors.Is(err, evaluation.ErrMissingDependency) {
					missingDependencies = true
					continue
				}
				return nil, fmt.Errorf("attr %#v: %w", namedAttr.GetName(), err)
			}
			labelAttrs = append(labelAttrs, model_analysis_pb.IndexedConfiguredLabelAttr_builder{
				AttrIndex:  uint32(attrIndex),
				Configured: configuredAttr,
			}.Build())
		}

		if missingDependencies {
			return nil, evaluation.ErrMissingDependency
		}

		return model_analysis_pb.ConfiguredAspect_Value_builder{
			ExecGroups: execGroups,
			Subrules:   subrules,
			LabelAttrs: labelAttrs,
		}.Build(), nil
	})
}
