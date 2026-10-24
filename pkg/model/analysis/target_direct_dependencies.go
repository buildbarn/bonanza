package analysis

import (
	"context"
	"errors"
	"fmt"

	"bonanza.build/pkg/label"
	model_core "bonanza.build/pkg/model/core"
	"bonanza.build/pkg/model/evaluation"
	model_analysis_pb "bonanza.build/pkg/proto/model/analysis"
	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
)

func (c *baseComputer[TReference, TMetadata]) ComputeTargetDirectDependenciesValue(ctx context.Context, key *model_analysis_pb.TargetDirectDependencies_Key, e TargetDirectDependenciesEnvironment[TReference, TMetadata]) (PatchedTargetDirectDependenciesValue[TMetadata], error) {
	targetLabel, err := label.NewCanonicalLabel(key.Label)
	if err != nil {
		return PatchedTargetDirectDependenciesValue[TMetadata]{}, fmt.Errorf("invalid target label: %w", err)
	}
	target := e.GetTargetValue(&model_analysis_pb.Target_Key{Label: key.Label})
	if !target.IsSet() {
		return PatchedTargetDirectDependenciesValue[TMetadata]{}, evaluation.ErrMissingDependency
	}

	dependencies := labelSet{}
	addDependency := func(l label.ResolvedLabel) error {
		canonicalLabel, err := canonicalizeResolvedQueryLabel(l)
		if err != nil {
			return err
		}
		dependencies[canonicalLabel] = struct{}{}
		return nil
	}
	addDependencyString := func(s string) error {
		canonicalLabel, err := canonicalizeQueryLabel(s)
		if err != nil {
			return err
		}
		dependencies[canonicalLabel] = struct{}{}
		return nil
	}
	switch kind := target.Message.Definition.GetKind().(type) {
	case *model_starlark_pb.Target_Definition_Alias:
		var attr queryAttr[TReference]
		attr.appendSelectGroup(model_core.Nested(target, kind.Alias.Actual))
		if err := c.visitQueryAttrLabels(ctx, &attr, true, addDependency); err != nil {
			return PatchedTargetDirectDependenciesValue[TMetadata]{}, err
		}
	case *model_starlark_pb.Target_Definition_LabelSetting:
		if l := kind.LabelSetting.BuildSettingDefault; l != "" {
			if err := addDependencyString(l); err != nil {
				return PatchedTargetDirectDependenciesValue[TMetadata]{}, err
			}
		}
	case *model_starlark_pb.Target_Definition_PackageGroup:
		for _, l := range kind.PackageGroup.IncludePackageGroups {
			if err := addDependencyString(l); err != nil {
				return PatchedTargetDirectDependenciesValue[TMetadata]{}, err
			}
		}
	case *model_starlark_pb.Target_Definition_PredeclaredOutputFileTarget:
		// Output files depend on the rule target that
		// generates them.
		ownerTargetName, err := label.NewTargetName(kind.PredeclaredOutputFileTarget.OwnerTargetName)
		if err != nil {
			return PatchedTargetDirectDependenciesValue[TMetadata]{}, fmt.Errorf("invalid owner target name %#v: %w", kind.PredeclaredOutputFileTarget.OwnerTargetName, err)
		}
		dependencies[targetLabel.GetCanonicalPackage().AppendTargetName(ownerTargetName)] = struct{}{}
	case *model_starlark_pb.Target_Definition_RuleTarget:
		t, err := getQueryRuleTarget(e, targetLabel, model_core.Nested(target, kind.RuleTarget))
		if err != nil {
			return PatchedTargetDirectDependenciesValue[TMetadata]{}, err
		}
		for i := range t.attrs {
			attr := &t.attrs[i]
			if attr.isOutput || (!attr.isExplicit && !key.IncludeImplicitDeps) {
				continue
			}
			if err := c.visitQueryAttrLabels(ctx, attr, true, addDependency); err != nil {
				return PatchedTargetDirectDependenciesValue[TMetadata]{}, fmt.Errorf("attr %#v: %w", attr.name, err)
			}
		}
	case *model_starlark_pb.Target_Definition_SourceFileTarget:
		// Source files have no dependencies.
	default:
		return PatchedTargetDirectDependenciesValue[TMetadata]{}, errors.New("unknown target kind")
	}

	targetLabels, err := c.buildTargetLabels(ctx, e, dependencies)
	if err != nil {
		return PatchedTargetDirectDependenciesValue[TMetadata]{}, err
	}
	return model_core.NewPatchedMessage(
		&model_analysis_pb.TargetDirectDependencies_Value{
			TargetLabels: targetLabels.Message,
		},
		targetLabels.Patcher,
	), nil
}

// TargetDirectDependenciesEnvironmentForTesting is used to generate
// mocks for testing ComputeTargetDirectDependenciesValue().
type TargetDirectDependenciesEnvironmentForTesting TargetDirectDependenciesEnvironment[model_core.CreatedObjectTree, model_core.CreatedObjectTree]
