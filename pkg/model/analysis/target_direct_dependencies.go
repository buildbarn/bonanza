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
	analysisEncoders, gotAnalysisEncoders := getAnalysisEncoders(e)
	valueReaders, gotValueReaders := getStarlarkValueReaders(e)

	targetLabel, err := label.NewCanonicalLabel(key.GetLabel())
	if err != nil {
		return PatchedTargetDirectDependenciesValue[TMetadata]{}, fmt.Errorf("invalid target label: %w", err)
	}
	target := e.GetTargetValue(model_analysis_pb.Target_Key_builder{
		Label: key.GetLabel(),
	}.Build())
	if !target.IsSet() || !gotValueReaders || !gotAnalysisEncoders {
		return PatchedTargetDirectDependenciesValue[TMetadata]{}, evaluation.ErrMissingDependency
	}
	analysisReaders := &analysisEncoders.Readers
	definition := target.Message.GetDefinition()
	if definition == nil {
		return PatchedTargetDirectDependenciesValue[TMetadata]{}, errors.New("target does not exist")
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
	switch definition.WhichKind() {
	case model_starlark_pb.Target_Definition_Alias_case:
		var attr queryAttr[TReference]
		attr.appendSelectGroup(model_core.Nested(target, definition.GetAlias().GetActual()))
		if err := visitQueryAttrLabels(ctx, valueReaders, &attr, true, addDependency); err != nil {
			return PatchedTargetDirectDependenciesValue[TMetadata]{}, err
		}
	case model_starlark_pb.Target_Definition_LabelSetting_case:
		if l := definition.GetLabelSetting().GetBuildSettingDefault(); l != "" {
			if err := addDependencyString(l); err != nil {
				return PatchedTargetDirectDependenciesValue[TMetadata]{}, err
			}
		}
	case model_starlark_pb.Target_Definition_PackageGroup_case:
		for _, l := range definition.GetPackageGroup().GetIncludePackageGroups() {
			if err := addDependencyString(l); err != nil {
				return PatchedTargetDirectDependenciesValue[TMetadata]{}, err
			}
		}
	case model_starlark_pb.Target_Definition_PredeclaredOutputFileTarget_case:
		// Output files depend on the rule target that
		// generates them.
		ownerTargetName, err := label.NewTargetName(definition.GetPredeclaredOutputFileTarget().GetOwnerTargetName())
		if err != nil {
			return PatchedTargetDirectDependenciesValue[TMetadata]{}, fmt.Errorf("invalid owner target name %#v: %w", definition.GetPredeclaredOutputFileTarget().GetOwnerTargetName(), err)
		}
		dependencies[targetLabel.GetCanonicalPackage().AppendTargetName(ownerTargetName)] = struct{}{}
	case model_starlark_pb.Target_Definition_RuleTarget_case:
		t, err := getQueryRuleTarget(e, targetLabel, model_core.Nested(target, definition.GetRuleTarget()))
		if err != nil {
			return PatchedTargetDirectDependenciesValue[TMetadata]{}, err
		}
		for i := range t.attrs {
			attr := &t.attrs[i]
			if attr.isOutput || (!attr.isExplicit && !key.GetIncludeImplicitDeps()) {
				continue
			}
			if err := visitQueryAttrLabels(ctx, valueReaders, attr, true, addDependency); err != nil {
				return PatchedTargetDirectDependenciesValue[TMetadata]{}, fmt.Errorf("attr %#v: %w", attr.name, err)
			}
		}
	case model_starlark_pb.Target_Definition_SourceFileTarget_case:
		// Source files have no dependencies.
	case model_starlark_pb.Target_Definition_TestSuite_case:
		testSuite := definition.GetTestSuite()
		var errIter error
		for l := range allTestSuiteTests(ctx, analysisReaders, model_core.Nested(target, testSuite.GetTests()), &errIter) {
			if err := addDependencyString(l); err != nil {
				return PatchedTargetDirectDependenciesValue[TMetadata]{}, err
			}
		}
		if errIter != nil {
			return PatchedTargetDirectDependenciesValue[TMetadata]{}, fmt.Errorf("failed to read tests: %w", errIter)
		}
	default:
		return PatchedTargetDirectDependenciesValue[TMetadata]{}, errors.New("unknown target kind")
	}

	targetLabels, err := c.buildTargetLabels(ctx, analysisEncoders, e, dependencies)
	if err != nil {
		return PatchedTargetDirectDependenciesValue[TMetadata]{}, err
	}
	return model_core.NewPatchedMessage(
		model_analysis_pb.TargetDirectDependencies_Value_builder{
			TargetLabels: targetLabels.Message,
		}.Build(),
		targetLabels.Patcher,
	), nil
}

// TargetDirectDependenciesEnvironmentForTesting is used to generate
// mocks for testing ComputeTargetDirectDependenciesValue().
type TargetDirectDependenciesEnvironmentForTesting TargetDirectDependenciesEnvironment[model_core.CreatedObjectTree, model_core.CreatedObjectTree]
